# AgentRM

面向长生命周期 Coding Agent 的弹性 Sandbox 资源管理系统。

AgentRM 利用 Agent Runtime 在工具执行前能够提前表达资源需求这一特点，在固定集群容量内为多个 Session 动态分配、借用和回收 CPU（Central Processing Unit，中央处理器）与 RAM（Random Access Memory，随机存取存储器），并为长期空闲 Sandbox 提供 Full-state Suspend/Resume 编排边界。

> 项目状态：早期可运行原型。核心请求合并、Session Table、弹性 CPU 调度、保守内存回收、挂起/恢复状态机、HTTP API（Hypertext Transfer Protocol Application Programming Interface）和 Python Runtime 已实现并有自动化测试；Kubernetes（容器编排系统）执行适配器、持久化存储和真实 checkpoint 后端仍在路线图中。

## 目录

- [为什么需要 AgentRM](#为什么需要-agentrm)
- [目标与边界](#目标与边界)
- [当前已实现能力](#当前已实现能力)
- [系统架构](#系统架构)
- [核心设计](#核心设计)
- [快速开始](#快速开始)
- [完整调用示例](#完整调用示例)
- [Python Runtime](#python-runtime)
- [HTTP API](#http-api)
- [项目结构](#项目结构)
- [测试](#测试)
- [生产化接入](#生产化接入)
- [实验与指标](#实验与指标)
- [安全](#安全)
- [路线图](#路线图)

## 为什么需要 AgentRM

一个 Coding Agent Session 可能持续几十分钟甚至数小时，但每个阶段的资源需求差异很大：

```text
Session A：正在等待模型响应
实际 CPU 使用接近 0
Sandbox 固定占用 4 CPU

Session B：正在执行 C++ 编译
希望使用 8 CPU
Sandbox 固定只有 2 CPU
```

固定分配同时造成两种浪费：A 占着不用，B 需要却拿不到。AgentRM 将一个 Session 的资源拆成：

```text
Guaranteed Resource（minimum）
+
Borrowed Resource（allocated - minimum）
```

当集群有空闲时，活跃 Session 可以临时借入资源；资源紧张时，优先从空闲、等待和后台 Session 收回 borrowed resource，再分配给当前真正需要资源的任务。

目标是在相同集群容量下：

- 支持更多并发 Agent Session；
- 提高集群 CPU 利用率；
- 降低每个任务消耗的 CPU-core-seconds；
- 不显著恶化 P95（95th Percentile，第 95 百分位）资源等待时间与工具启动延迟。

## 目标与边界

AgentRM 聚焦三个问题：

1. **Resource Request**：Agent 下一步需要多少资源？
2. **Allocation/Reclamation**：资源不足时，从谁那里回收多少？
3. **Suspend/Resume**：Session 长时间空闲时，如何释放运行 Pod 的资源并恢复？

AgentRM 不重新实现：

- Sandbox 隔离；
- Container Runtime；
- Kubernetes Scheduler；
- CRIU（Checkpoint/Restore In Userspace，用户空间检查点与恢复）格式；
- 文件系统快照机制。

生产目标是接入 Kubernetes SIG（Special Interest Group，特别兴趣小组）Agent Sandbox、Pod in-place resize、Kubelet Checkpoint API 和 CRI（Container Runtime Interface，容器运行时接口）兼容运行时。AgentRM 自身负责控制状态、请求队列、受害者选择、资源操作编排与故障协调。

## 当前已实现能力

| 模块 | 状态 | 说明 |
|---|---|---|
| Resource Model | 已实现 | CPU 使用 millicore，内存使用字节；minimum/maximum 强校验 |
| Session Table | 已实现 | 维护 desired、allocated、metrics、state、generation 和 checkpoint reference |
| Coalescing Queue | 已实现 | 每个 Session 只保留最新 generation；优先级队列惰性丢弃旧节点 |
| Elastic Scheduler | 已实现 | 空闲分配、borrowed resource、victim 排序、部分回收、资源等待 |
| Memory Guard | 已实现 | working set、稳定窗口、125% 默认安全余量 |
| Suspend/Resume | 已实现状态机 | 内存后端提供可测试的模拟 checkpoint；尚未连接真实 CRIU |
| Go Control Plane | 已实现 | REST（Representational State Transfer，表述性状态转移）风格接口与后台 reconcile loop |
| Python Runtime | 已实现 | 静态命令识别、并行度提取、历史 P95、语义 hint、无第三方依赖客户端 |
| Kubernetes Backend | 未实现 | 已通过 `SandboxBackend` 接口隔离，等待集群环境接入 |
| Persistent Store | 未实现 | 当前 Session 和 Sandbox 状态在进程内存中 |
| Authentication | 未实现 | 当前接口仅适合本地与受信网络开发环境 |

## 系统架构

```text
                          Coding Agent
                               │
                     Tool / Task Decision
                               │
                               ▼
                      Resource Estimator
                 static + history + optional hint
                               │
                               ▼
                       Resource Request
                    absolute target + generation
                               │
                               ▼
┌──────────────────────────────────────────────────────────────┐
│                     AgentRM Control Plane                    │
│                                                              │
│  HTTP API ──► Coalescing Queue ──► Controller               │
│                     │                  │                       │
│                     ▼                  ▼                       │
│                Session Table ◄── Scheduler                    │
│                                      │                        │
│                         free capacity + victim selection      │
└──────────────────────────────────────┼────────────────────────┘
                                       │
                                       ▼
                             SandboxBackend interface
                                 │             │
                           MemoryBackend   Kubernetes adapter
                           current MVP       planned
```

MVP（Minimum Viable Product，最小可行产品）默认使用 `MemoryBackend`，因此克隆仓库后不需要集群即可完整观察调度行为。

## 核心设计

### 1. 绝对资源目标

请求使用：

```json
{
  "desired_resource": {
    "cpu_milli": 8000,
    "memory_bytes": 6442450944
  },
  "generation": 103
}
```

而不是：

```text
+4000 CPU
```

绝对目标天然幂等；网络重试不会重复加资源，乱序完成也能通过 generation 判断是否过期。

### 2. Request Coalescing

同一 Session 快速产生：

```text
generation 101 → 2000m CPU
generation 102 → 8000m CPU
generation 103 → 4000m CPU
```

队列只保留 generation 103。实现由两部分组成：

- `pending map`：指向每个 Session 的最新请求；
- `priority heap`：按交互优先级和等待时间选出下一个请求。

旧 heap node 无需同步删除，出队时发现它已不再是 pending map 中的权威节点，直接丢弃。

### 3. Session Table

Session Table 是控制面的 authoritative state，核心字段包括：

```text
minimum / maximum
desired / allocated / borrowed
actual_cpu / memory_working_set
session_state / task_priority
generation / applied_generation
last_active_at / pod_state
checkpoint_reference
```

Queue 表示“发生了什么请求”，Session Table 表示“系统现在相信什么”。

### 4. 分配与回收

一次调度按以下顺序执行：

1. 将 desired resource 裁剪到 Session 的 minimum/maximum；
2. 如果请求是安全缩容，先释放目标自己的资源；
3. 使用集群 free capacity；
4. 仍有缺口时构建 victim list；
5. 依次回收 victim 的 borrowed resource；
6. 只回收当前目标所需数量，不把 victim 一次降到底；
7. 若所有 Session 都到 minimum 后仍不够，目标进入 `WAITING_RESOURCE`，带短暂 backoff 重新排队，避免高优先等待请求一直占住队首；
8. 请求只从 reclaim class 比自身更低的 Session 回收，防止后台或等待 Session 反向抢占交互任务；被回收 Session 保留 desired state，未来有空闲容量时自动尝试恢复。

示例：

```text
Target A：current 5000m → desired 8000m，缺 3000m
Victim B：minimum 2000m，current 8000m，可回收 6000m

结果：
B：8000m → 5000m
A：5000m → 8000m
```

### 5. Victim Selection

Reclaim class 数字越小，越优先被回收：

| Class | 状态 | 说明 |
|---:|---|---|
| 0 | Long-idle 且可挂起 | 长期不活跃 |
| 1 | `WAITING_USER` / `WAITING_LLM` / `WAITING_RESOURCE` / `READY` | 没有正在执行的工具 |
| 2 | `BACKGROUND` | 后台任务 |
| 3 | 普通 `RUNNING_TOOL` | 正常活跃任务 |
| 4 | 交互式 `RUNNING_TOOL` | 最后回收 |

同一 class 内：

1. borrowed CPU 越多越先回收；
2. borrowed CPU 相同则越久未活跃越先回收；
3. 再用 Session ID（Identifier，标识符）保证结果确定。

### 6. CPU 与内存区别

CPU 不足通常意味着任务变慢；内存过度缩容可能导致 OOM（Out of Memory，内存耗尽）。因此：

- CPU borrowed resource 可以较频繁调整；
- 内存必须存在 working set 指标；
- working set 必须稳定达到配置窗口；
- 缩容后仍需保留默认 `25%` headroom；
- 没有可靠指标时拒绝缩内存。

内存可回收下界：

```text
max(minimum_memory, memory_working_set × 1.25)
```

### 7. Suspend/Resume

当前内存后端实现状态机与资源释放语义：

```text
WAITING / READY / BACKGROUND
          │
          ▼
      SUSPENDING
          │
          ▼
       SUSPENDED (allocated = 0)
          │
          ▼
        RESUMING
          │
          ▼
          READY (allocated = minimum)
```

生产 Full-state Suspend 路径应保存：

- Process tree；
- memory pages；
- persistent workspace；
- image digest 与 runtime version；
- resource spec 与 generation；
- filesystem snapshot 与 checkpoint reference。

外部 TCP（Transmission Control Protocol，传输控制协议）连接、数据库连接和设备状态不保证透明恢复，应由 post-resume hook 重建。

## 快速开始

### 环境要求

- Go 1.23 或更高版本；
- Python 3.9 或更高版本；
- `make`，可选。

### 1. 克隆

```bash
git clone https://github.com/lululuyuanyuanyuanGe/AgentRM.git
cd AgentRM
```

### 2. 运行测试

```bash
make test
```

等价命令：

```bash
GOCACHE="$PWD/.gocache" go test -race ./...
cd runtime/python
PYTHONPATH=. python3 -m unittest discover -s tests -v
```

### 3. 启动控制面

```bash
make run
```

默认配置：

```text
listen                :8080
cluster CPU           16000 millicores
cluster memory        32768 MiB
reconcile interval    1s
backend               in-memory
```

自定义容量：

```bash
GOCACHE="$PWD/.gocache" go run ./cmd/agentrm \
  -listen :9090 \
  -capacity-cpu-milli 32000 \
  -capacity-memory-mib 65536 \
  -reconcile-interval 500ms
```

健康检查：

```bash
curl http://127.0.0.1:8080/healthz
```

## 完整调用示例

### 1. 创建交互 Session

```bash
curl -X POST http://127.0.0.1:8080/v1/sessions \
  -H 'Content-Type: application/json' \
  -d '{
    "session_id": "session-a",
    "min_resource": {
      "cpu_milli": 1000,
      "memory_bytes": 1073741824
    },
    "max_resource": {
      "cpu_milli": 8000,
      "memory_bytes": 8589934592
    },
    "task_priority": 2
  }'
```

### 2. 工具执行前申请资源

```bash
curl -X PATCH http://127.0.0.1:8080/v1/sessions/session-a/state \
  -H 'Content-Type: application/json' \
  -d '{"session_state":"RUNNING_TOOL","task_priority":2}'

curl -X POST http://127.0.0.1:8080/v1/sessions/session-a/resources \
  -H 'Content-Type: application/json' \
  -d '{
    "desired_resource": {
      "cpu_milli": 8000,
      "memory_bytes": 4294967296
    },
    "generation": 1,
    "priority": 2
  }'
```

后台 reconcile loop 会处理请求。检查结果：

```bash
curl http://127.0.0.1:8080/v1/sessions/session-a
curl http://127.0.0.1:8080/v1/cluster
```

### 3. 工具结束后归还借用资源

```bash
curl -X PATCH http://127.0.0.1:8080/v1/sessions/session-a/state \
  -H 'Content-Type: application/json' \
  -d '{"session_state":"WAITING_LLM","task_priority":1}'

curl -X POST http://127.0.0.1:8080/v1/sessions/session-a/resources \
  -H 'Content-Type: application/json' \
  -d '{
    "desired_resource": {
      "cpu_milli": 1000,
      "memory_bytes": 1073741824
    },
    "generation": 2,
    "priority": 1
  }'
```

### 4. 挂起与恢复

```bash
curl -X POST http://127.0.0.1:8080/v1/sessions/session-a/suspend \
  -H 'Content-Type: application/json' \
  -d '{}'

curl -X POST http://127.0.0.1:8080/v1/sessions/session-a/resume \
  -H 'Content-Type: application/json' \
  -d '{}'
```

## Python Runtime

Python 包位于 `runtime/python`，不依赖第三方库。

### Resource Estimator

估算优先级：

```text
命令中的显式并行度
>
历史 P95 profile
>
命令 / 工具静态分类
>
Agent semantic hint
>
默认 profile
```

当前可识别：

- Light：`rg`、`grep`、`cat`、`git diff`、`git status`；
- Input/Output bound：`git clone`、`pip install`、`npm install`；
- CPU heavy：`gcc`、`clang`、`make`、`ninja`、`cmake --build`、`cargo build`；
- Test：`pytest`、`go test`、`cargo test`、`ctest`；
- Memory heavy：`ld`、`javac`、`rustc`。

并行参数支持 `-j16`、`-j 16`、`--jobs=16`、`--jobs 16`、`pytest -n 16`。

示例：

```python
from agentrm_runtime import ResourceBounds, ResourceEstimator

estimator = ResourceEstimator()
estimate = estimator.estimate(
    "cmake --build . -j16",
    bounds=ResourceBounds(
        min_cpu_milli=500,
        max_cpu_milli=32000,
        min_memory_bytes=512 * 1024 * 1024,
        max_memory_bytes=32 * 1024 * 1024 * 1024,
    ),
)

print(estimate)
```

### Runtime Client

```python
from agentrm_runtime import AgentRMClient, ResourceEstimator

client = AgentRMClient("http://127.0.0.1:8080")
estimator = ResourceEstimator()

client.create_session(
    session_id="agent-42",
    min_cpu_milli=1000,
    min_memory_bytes=1024**3,
    max_cpu_milli=8000,
    max_memory_bytes=8 * 1024**3,
    priority=2,
)

estimate = estimator.estimate("pytest -n 8")
client.update_state("agent-42", "RUNNING_TOOL", priority=2)
client.request_resources("agent-42", estimate, generation=1, priority=2)
```

运行仓库示例：

```bash
cd runtime/python
PYTHONPATH=. python3 ../../examples/runtime_demo.py
```

## HTTP API

完整字段和错误码见 [docs/api.md](docs/api.md)。

| Method | Path | 用途 |
|---|---|---|
| `GET` | `/healthz` | 健康检查 |
| `GET` | `/v1/cluster` | 集群容量快照 |
| `POST` | `/v1/sessions` | 创建 Session |
| `GET` | `/v1/sessions` | 列出 Session |
| `GET` | `/v1/sessions/{id}` | 获取 Session |
| `PATCH` | `/v1/sessions/{id}/state` | 更新 Agent 语义状态 |
| `PUT` | `/v1/sessions/{id}/metrics` | 更新资源指标 |
| `POST` | `/v1/sessions/{id}/resources` | 提交绝对资源目标 |
| `POST` | `/v1/sessions/{id}/suspend` | 挂起 Sandbox |
| `POST` | `/v1/sessions/{id}/resume` | 恢复 Sandbox |
| `POST` | `/v1/sessions/{id}/finish` | 结束 Session |
| `POST` | `/v1/scheduler/run-once` | 手动执行一个调度周期 |

## 项目结构

```text
AgentRM/
├── cmd/agentrm/                 # Go 控制面入口
├── internal/
│   ├── api/                     # HTTP handlers
│   ├── backend/                 # SandboxBackend 与 MemoryBackend
│   ├── controller/              # 状态变更与资源操作编排
│   ├── model/                   # Session、Request、Resource 模型
│   ├── queue/                   # 合并式优先级队列
│   ├── scheduler/               # 分配、victim selection、内存保护
│   └── store/                   # SessionStore 与内存实现
├── runtime/python/
│   ├── agentrm_runtime/         # estimator 与客户端
│   └── tests/
├── examples/                    # Runtime 调用示例
├── docs/
│   ├── api.md
│   └── architecture.md
├── Dockerfile
├── Makefile
└── README.md
```

更详细的一致性、不变量与故障语义见 [docs/architecture.md](docs/architecture.md)。

## 测试

Go 测试覆盖：

- 同一 Session 的请求合并与 stale generation；
- 交互优先级和等待时间排序；
- free capacity 分配；
- victim class 选择；
- 只回收实际缺口；
- 所有 Session 到 minimum 后进入等待；
- 内存稳定窗口与 headroom；
- Controller 资源申请、回收、挂起和恢复；
- HTTP Session 生命周期。

Python 测试覆盖：

- 轻量命令分类；
- 显式并行度优先；
- 历史 P95 覆盖静态 profile；
- semantic hint fallback；
- minimum/maximum clamp。

运行带 race detector 的全部测试：

```bash
make test
```

## 生产化接入

### 1. Kubernetes Backend

实现 `internal/backend.SandboxBackend`：

```go
type SandboxBackend interface {
    Resize(context.Context, ResizeOperation) error
    Suspend(context.Context, string, int64) (string, error)
    Resume(context.Context, string, string, model.Resources, int64) error
    Delete(context.Context, string) error
}
```

推荐职责：

- `Resize`：写入 Pod resize subresource，并关联 request generation；
- `Suspend`：阻止新工具、冻结 guest、触发 Kubelet checkpoint、持久化 workspace 与元数据、删除 Pod；
- `Resume`：兼容性检查、分配节点、恢复 workspace、恢复进程和内存、运行 post-resume hook；
- `Delete`：结束 Sandbox 并清理受控资源。

### 2. Persistent Session Store

当前 `MemorySessionStore` 应替换为 PostgreSQL（对象关系数据库）或其他持久化存储。Controller 启动时需要执行：

```text
desired state from Session Store
vs
actual state from Kubernetes watch/list
→ reconciliation
```

Event queue 不能成为唯一状态源。

### 3. Event 与 Metrics

状态变化适合 watch/event：

- Pod Ready / Failed / Deleted；
- resize started / completed / failed；
- checkpoint completed / failed；
- suspend / resume completed；
- Agent state event。

连续资源使用适合轻量周期采样：

- cgroup v2（control groups version 2，控制组第二版）；
- metrics-server；
- Prometheus。

### 4. 可观测性

生产版计划输出：

- resource request wait duration；
- resize latency 与 failure count；
- reclaimed/borrowed CPU；
- memory shrink rejected reason；
- checkpoint latency、size 与 released memory；
- Session state transition；
- OpenTelemetry（开放遥测）trace。

## 实验与指标

建议对比三组：

1. **Fixed Allocation**：每个 Sandbox 固定资源；
2. **Request Only**：有空闲就扩，没有就等待，不回收；
3. **AgentRM**：请求合并、弹性借用、优先级回收、Suspend。

Workload 应混合：

- Light tool：`grep`、`git`、`cat`；
- CPU heavy：`gcc`、`cmake`、`ninja`；
- Input/Output heavy：`git clone`、包安装；
- Test：`pytest`；
- Long idle：`WAITING_USER`；
- Interactive、background 和 long-running Agent。

四个核心指标：

```text
Concurrent Agent Sessions              ↑
Cluster CPU Utilization                 ↑
CPU-core-seconds / Task                 ↓
P95 Resource Wait / Tool Start Delay    不明显恶化
```

Suspend 单独记录：

- checkpoint latency；
- resume latency；
- checkpoint size；
- released RAM。

## 安全

真实 checkpoint archive 可能包含进程内存中的 token、password、API key 和其他敏感数据，生产实现必须至少具备：

- encryption at rest；
- 按 Session 隔离的访问控制；
- 仅限 controller service account 的最小权限；
- checkpoint TTL（Time To Live，生存时间）清理；
- 审计日志；
- 恢复前完整性和兼容性校验；
- HTTP API 鉴权、授权与传输加密。

当前内存版本未实现鉴权，不应直接暴露到公网。

## 路线图

### Phase 1 — Basic Runtime

- [x] Session model 与内存 Session Store
- [x] Sandbox backend interface
- [x] 本地可运行 MemoryBackend
- [x] HTTP control plane

### Phase 2 — Resource Request

- [x] 静态 Resource Estimator
- [x] 显式并行度解析
- [x] Historical P95 profile
- [x] Request coalescing
- [x] Generation 幂等语义

### Phase 3 — Elastic CPU Scheduler

- [x] Minimum/maximum resource
- [x] Borrowed resource
- [x] Free capacity allocation
- [x] Victim selection
- [x] Priority reclamation
- [ ] Kubernetes in-place resize adapter

### Phase 4 — Memory Control

- [x] Memory working set model
- [x] Stable window 与 headroom
- [ ] OOM event feedback
- [ ] 多采样窗口与自适应 headroom

### Phase 5 — Deep Suspend

- [x] Suspend/Resume 控制状态机
- [x] Checkpoint backend contract
- [ ] Kubelet Checkpoint API adapter
- [ ] CRIU-compatible restore
- [ ] Persistent workspace snapshot
- [ ] Post-resume hook

### Phase 6 — Recovery and Production

- [ ] PostgreSQL / Redis state adapter
- [ ] Kubernetes watch/informer
- [ ] Controller restart reconciliation
- [ ] Metrics、trace 与 benchmark suite
- [ ] Authentication、authorization 与 multi-tenancy

---

AgentRM 的核心判断很简单：**暂时不用的资源应该让给现在真正需要资源的 Agent；长期不用的 Sandbox 应该被完整挂起，而不是继续占用计算容量。**
