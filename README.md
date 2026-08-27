# AgentRM

面向多 Coding Agent Sandbox 的 MLFQ-inspired、work-conserving CPU Scheduler。

AgentRM 解决的是一个非常具体的问题：当 Kubernetes（容器编排系统）节点 CPU（Central Processing Unit，中央处理器）满载时，几十毫秒的搜索与格式化、几秒的测试任务，会和持续数十秒甚至数分钟的 Compile / Benchmark 竞争，导致短任务被长任务明显拖慢。

AgentRM 不预测任务时长，也不要求 Agent 提供“这是轻任务还是重任务”的语义。所有新 Tool Job 一律进入 Q0；Node Daemon 读取 cgroup v2（control groups version 2，控制组第二版）的 `cpu.stat`，根据 Job 实际消耗的 CPU service 执行 Q0 → Q1 → Q2 反馈式降权，再将队列等级映射到 `cpu.weight`。

它只改变 contention 下的相对 CPU share，不修改 hard quota，不写 `cpu.max`，也不重启 Pod。高优先级任务没有运行时，低优先级任务仍然可以使用全部剩余 CPU。

> 项目状态：可运行原型。MLFQ（Multi-Level Feedback Queue，多级反馈队列）状态机、实际 CPU service 计量、Aging、Priority Boost、cgroup v2 文件适配、Node Daemon、HTTP API（Hypertext Transfer Protocol Application Programming Interface）、自动化测试和 Kubernetes DaemonSet 已实现。Kubernetes Agent Sandbox watcher、持久化状态和真实 workload benchmark 仍在路线图中。

## 目录

- [为什么要做这个项目](#为什么要做这个项目)
- [一句话设计](#一句话设计)
- [核心目标](#核心目标)
- [非目标](#非目标)
- [系统架构](#系统架构)
- [MLFQ 如何工作](#mlfq-如何工作)
- [为什么只使用 cpu.weight](#为什么只使用-cpuweight)
- [为什么不需要 Agent 语义](#为什么不需要-agent-语义)
- [快速开始](#快速开始)
- [接口示例](#接口示例)
- [Kubernetes 部署](#kubernetes-部署)
- [配置](#配置)
- [项目结构](#项目结构)
- [测试](#测试)
- [实验设计](#实验设计)
- [安全与生产注意事项](#安全与生产注意事项)
- [当前限制](#当前限制)
- [路线图](#路线图)

## 为什么要做这个项目

假设同一个节点上正在运行三个 Sandbox：

```text
Sandbox A
Tool: rg / git diff / formatter
预计只需要几十到几百毫秒 CPU 时间

Sandbox B
Tool: unit test
预计需要几秒 CPU 时间

Sandbox C
Tool: C++ compile / benchmark
可能持续几十秒或更久
```

CPU 空闲时，三者可以同时运行，不需要任何控制。问题只在 CPU 满载后出现：如果三个 Sandbox 的相对权重完全相同，长时间运行的 Compile / Benchmark 会持续和短 Tool 平分 CPU，短任务的完成时间明显上升，用户感知到 Agent “卡住了”。

我们希望同时满足：

```text
短任务优先完成
长任务持续推进
空闲 CPU 不浪费
不需要提前预测任务时长
不需要重启 Pod
```

## 一句话设计

```text
所有新 Job 进入 Q0
        │
        ▼
读取 cpu.stat 的实际 usage_usec
        │
        ▼
耗尽当前 service quantum 才逐级降权
        │
        ▼
Q0 / Q1 / Q2 映射到 cpu.weight
        │
        ▼
Linux 只在 contention 下按相对权重分配 CPU
```

## 核心目标

### 1. 短任务优先完成

新 Tool Job 不论命令是什么，都先获得 Q0 的高相对权重。真正很短的任务通常会在 Q0 quantum 内完成，不会被长 Compile 拖到同一优先级。

### 2. 长任务持续推进

长任务耗尽 Q0、Q1 quantum 后进入 Q2，但 Q2 权重始终大于零；它只是 contention 下得到较小 share，不会被暂停。

### 3. Work-conserving

如果 Q0/Q1 Job 阻塞或不存在，Q2 Job 可以使用全部空闲 CPU。AgentRM 不通过 quota 人为制造空闲。

### 4. 基于行为反馈

队列迁移依据真实 `usage_usec`，而不是命令名、Agent hint、模型分类或预计执行时间。

### 5. 不侵入 Sandbox 生命周期

调整 `cpu.weight` 不需要修改 Pod spec、不触发 resize、不重建容器。

## 非目标

AgentRM 当前不负责：

- 预测 `gcc`、`pytest` 或任意命令的执行时间；
- 让 Agent 声明 Light / Heavy priority；
- 分配固定 CPU core 数量；
- 修改 CPU limit 或 `cpu.max`；
- 调度内存、存储、网络或图形处理器；
- Suspend / Resume Sandbox；
- 替代 Linux 内核调度器；
- 重新实现 Kubernetes Scheduler。

## 系统架构

```text
          Kubernetes Agent Sandbox / Tool Runtime
                            │
              generic Job start / finish
              job_id + sandbox_id + cgroup_path
              no command classification or duration hint
                            │
                            ▼
┌─────────────────────────────────────────────────────────────┐
│                    AgentRM Node Daemon                      │
│                                                             │
│  HTTP API ──► Job Store ──► MLFQ Engine                    │
│                                │                             │
│                     Q0 / Q1 / Q2 state                       │
│                                │                             │
│  cgroup Client ◄── desired cpu.weight                       │
│       │                                                      │
│       └──────────── cpu.stat usage_usec ───────────────►     │
└───────────────────────────┬─────────────────────────────────┘
                            │
                            ▼
                   Linux cgroup v2 CPU controller
```

Node Daemon 以 DaemonSet 方式部署，每个节点独立管理本节点的 Sandbox/Tool cgroup。调度决策不跨节点传播，避免为一次 100 ms 的 Tool Job 引入中心控制面往返。

### 组件职责

| 组件 | 职责 |
|---|---|
| `model.ToolJob` | 保存 Job、Sandbox、cgroup、队列、权重和 service counter |
| `mlfq.Engine` | quantum 降级、Aging 提升、全局 Priority Boost |
| `cgroup.FSClient` | 只读 `cpu.stat`、读写 `cpu.weight` |
| `daemon.Daemon` | 注册、完成、周期采样、状态持久化和权重 reconcile |
| `store.JobStore` | Job authoritative state；当前为内存实现 |
| `api.Server` | 通用 Job lifecycle 与诊断接口 |

## MLFQ 如何工作

### 默认队列

| Queue | `cpu.weight` | CPU service quantum | 典型结果 |
|---|---:|---:|---|
| Q0 | 10000 | 250 ms | 短 Tool 通常在这里完成 |
| Q1 | 3000 | 2 s | 测试、中型构建继续运行 |
| Q2 | 500 | 无上限 | Compile / Benchmark 持续推进 |

权重范围来自 cgroup v2：`1..10000`。这些值不是核数，也不是百分比；它们是同一调度层级下 runnable cgroup 之间的相对 share。

### 新 Job

所有 Job 都执行同一逻辑：

```text
register
  ↓
read initial cpu.stat usage_usec
  ↓
queue = Q0
service_in_level = 0
cpu.weight = 10000
```

没有命令白名单，没有静态规则，也没有大语言模型调用。

### 实际 CPU service

每个 tick 读取：

```text
usage_usec 1250000
user_usec 1000000
system_usec 250000
```

使用相邻观测的差值：

```text
service_delta = current_usage_usec - previous_usage_usec
service_in_level += service_delta
```

这和 wall-clock time 不同。例如一个 Job 在 Q0 等待输入十秒，但只运行了 5 ms，它只消耗 5 ms quantum，仍然保留 Q0。只有实际持续占用 CPU 的任务才会降级。

### Quantum Demotion

```text
Q0 service >= 250 ms
    → Q1, weight 3000

Q1 service >= 2 s
    → Q2, weight 500

Q2
    → 不再降级，继续运行
```

每次进入新队列后，`service_in_level_usec` 归零。

### Aging

低队列中的 Job 会定期提升：

```text
Q2 wait >= 15 s → Q1
Q1 wait >= 5 s  → Q0
```

Aging 一次只提升一级，使长任务周期性获得更高 share，同时避免直接长期占据 Q0。

### Priority Boost

默认每 30 秒触发一次全局 Boost：

```text
all RUNNING Q1/Q2 jobs → Q0
```

这提供了更强的 starvation protection，也能修正长期运行中累积的队列偏差。

### CPU 计数器重置

如果 cgroup 重建导致 `usage_usec` 小于上次观测，Daemon 将其识别为 counter reset：

- 本次 service delta 记为零；
- 更新新的计数基线；
- 不产生无符号整数下溢；
- 不因为重建误降级 Job。

## 为什么只使用 cpu.weight

`cpu.weight` 和 hard quota 的语义完全不同。

### `cpu.weight`

- 只在多个 runnable cgroup 竞争 CPU 时影响相对 share；
- 没有其他 runnable cgroup 时，可以使用空闲 CPU；
- 修改不需要重启容器；
- 适合 work-conserving priority。

### `cpu.max`

- 是带 period 的硬上限；
- 即使节点有空闲 CPU，也可能触发 throttling；
- 容易造成空闲 CPU 浪费；
- 不是本项目的执行机制。

AgentRM 的 cgroup Client 接口根本没有写 `cpu.max` 的方法。测试会创建 `cpu.max` 哨兵文件，调整权重后再次读取并确认内容未变化。

### 相对 share 示例

如果 Q0 和 Q2 cgroup 在相同父级下同时 runnable：

```text
Q0 weight = 10000
Q2 weight =   500

relative ratio ≈ 20 : 1
```

这不是严格的延迟或吞吐保证。实际结果还取决于 CPU 数量、任务并行度、父级 cgroup、其他系统进程以及 Linux 调度细节。

### Kubernetes CPU limit 注意事项

AgentRM 自己不写 `cpu.max`，但 Kubernetes 现有 CPU limit 仍可能映射成 hard quota。如果 Sandbox Pod 配置了很紧的 CPU limit，低优先级 Job 即使节点有空闲 CPU，也无法突破该 limit。

要获得完整 work-conserving 行为，应避免为这类 Sandbox 设置过紧的 hard CPU limit，或确保上层 quota 足够宽松。CPU request 可以用于 Kubernetes placement，但 request、weight 和 quota 的具体映射必须结合集群运行时验证。

### cgroup 层级注意事项

`cpu.weight` 在层级结构中生效。要让多个 Sandbox 的权重可比较：

- 应在合适的兄弟 cgroup 层级修改权重；
- `cgroup_path` 应指向真正代表该 Sandbox/Tool 调度实体的 cgroup；
- 如果 Tool cgroup 位于不同 Pod 父级下，需要确认 Pod 父级权重不会覆盖预期比例；
- 最简单的模型是每个 Sandbox 同时只有一个活跃 Tool，并直接调整 Sandbox Pod cgroup。

## 为什么不需要 Agent 语义

旧式方案可能要求 Agent 提交：

```text
resource_hint = LIGHT
desired_cpu = 8
predicted_duration = 30s
```

本设计全部删除。Daemon 只需要三个通用标识：

```text
job_id
sandbox_id
cgroup_path
```

Job start/finish 可以来自 Agent Sandbox controller、executor lifecycle hook、sidecar 或后续 cgroup discovery adapter。它们只表示生命周期，不表达任务语义。

因此：

- 错误预测不会让长任务长期占据高优先级；
- 新工具和未知命令自动获得短任务机会；
- 系统能适应同一命令在不同仓库中的不同行为；
- 调度依据可直接从内核观测和复现。

## 快速开始

### 环境要求

- Go 1.23 或更高版本；
- Linux cgroup v2；
- 对目标 `cpu.weight` 的写权限；
- `make`，可选。

macOS 可以运行全部单元测试和内存 cgroup 集成测试，但不能运行真实 Linux cgroup 调度。

### 克隆

```bash
git clone https://github.com/lululuyuanyuanyuanGe/AgentRM.git
cd AgentRM
```

### 运行验证

```bash
make test
make vet
make build
```

### 启动 Node Daemon

Linux 节点：

```bash
sudo ./bin/agentrm \
  --listen=:8080 \
  --cgroup-root=/sys/fs/cgroup \
  --sample-interval=100ms
```

健康检查：

```bash
curl http://127.0.0.1:8080/healthz
```

查看队列配置：

```bash
curl http://127.0.0.1:8080/v1/config
```

## 接口示例

完整接口见 [docs/api.md](docs/api.md)。

### 注册 Job

假设相对于 `/sys/fs/cgroup` 的目标路径为 `kubepods.slice/pod-uid/tool-job-a`：

```bash
curl -X POST http://127.0.0.1:8080/v1/jobs \
  -H 'Content-Type: application/json' \
  -d '{
    "job_id": "job-a",
    "sandbox_id": "sandbox-a",
    "cgroup_path": "kubepods.slice/pod-uid/tool-job-a"
  }'
```

Daemon 会读取当前 `usage_usec` 作为基线，并写入 Q0 weight。

### 查询 Job

```bash
curl http://127.0.0.1:8080/v1/jobs/job-a
```

### 查看队列分布

```bash
curl http://127.0.0.1:8080/v1/scheduler
```

### 结束 Job

```bash
curl -X POST http://127.0.0.1:8080/v1/jobs/job-a/finish \
  -H 'Content-Type: application/json' \
  -d '{}'
```

失败任务：

```bash
curl -X POST http://127.0.0.1:8080/v1/jobs/job-a/finish \
  -H 'Content-Type: application/json' \
  -d '{"state":"FAILED"}'
```

## Kubernetes 部署

示例清单位于 [deployments/kubernetes/daemonset.yaml](deployments/kubernetes/daemonset.yaml)。

```bash
kubectl apply -f deployments/kubernetes/daemonset.yaml
```

DaemonSet：

- 每个节点运行一个 AgentRM；
- 将宿主机 `/sys/fs/cgroup` 挂载为 `/host-cgroup`；
- 以 `--cgroup-root=/host-cgroup` 启动；
- 默认每 100 ms 采样；
- 当前示例使用 privileged container 获取 cgroup 写权限。

生产环境必须根据容器运行时、内核与安全策略缩小权限。示例镜像地址为 `ghcr.io/lululuyuanyuanyuanGe/agentrm:latest`，需要先构建并发布对应镜像。

### Kubernetes Agent Sandbox 集成点

当前实现提供通用 lifecycle API。推荐集成路径：

```text
Tool cgroup created
        │
        ▼
Agent Sandbox controller / executor hook
        │ POST /v1/jobs
        ▼
AgentRM Node Daemon
        │
        ▼
Tool exits
        │ POST /v1/jobs/{id}/finish
        ▼
restore idle weight
```

未来 discovery adapter 可以监听 Kubernetes Pod 和运行时 cgroup 事件，自动完成注册与结束，无需 Agent 代码参与。

## 配置

| Flag | 默认值 | 说明 |
|---|---:|---|
| `--listen` | `:8080` | HTTP 监听地址 |
| `--cgroup-root` | `/sys/fs/cgroup` | cgroup v2 根目录 |
| `--sample-interval` | `100ms` | `cpu.stat` 采样周期 |
| `--q0-weight` | `10000` | Q0 相对权重 |
| `--q1-weight` | `3000` | Q1 相对权重 |
| `--q2-weight` | `500` | Q2 相对权重 |
| `--q0-quantum` | `250ms` | Q0 实际 CPU service quantum |
| `--q1-quantum` | `2s` | Q1 实际 CPU service quantum |
| `--q1-aging` | `5s` | Q1 Aging 阈值 |
| `--q2-aging` | `15s` | Q2 Aging 阈值 |
| `--boost-interval` | `30s` | 全局 Priority Boost 周期 |
| `--idle-weight` | `100` | Job 完成后的恢复权重 |

配置校验：

```text
1 <= weight <= 10000
Q0 weight > Q1 weight > Q2 weight
Q0 / Q1 quantum > 0
Aging / Boost interval > 0
sample interval > 0
```

默认参数是原型初始值，需要通过真实 Coding Agent trace 调优，不能直接视为所有集群的最优配置。

## 项目结构

```text
AgentRM/
├── cmd/agentrm/                 # Node Daemon 入口与 flags
├── internal/
│   ├── api/                     # Job lifecycle 与诊断接口
│   ├── cgroup/                  # cpu.stat / cpu.weight 客户端
│   ├── daemon/                  # 注册、完成与周期 reconcile
│   ├── mlfq/                    # quantum、Aging、Priority Boost
│   ├── model/                   # ToolJob 与 Q0/Q1/Q2 状态
│   └── store/                   # JobStore 与内存实现
├── deployments/kubernetes/      # DaemonSet 示例
├── docs/
│   ├── api.md
│   └── architecture.md
├── Dockerfile
├── Makefile
└── README.md
```

详细算法、不变量与 cgroup 层级说明见 [docs/architecture.md](docs/architecture.md)。

## 测试

运行全部测试：

```bash
make test
```

测试启用 Go race detector，覆盖：

### MLFQ Engine

- 新 Job 固定进入 Q0；
- 使用 CPU service 而不是 wall time；
- Q0 quantum exhaustion 进入 Q1；
- Q1 quantum exhaustion 进入 Q2；
- Q2 不继续降级；
- Q1 / Q2 Aging；
- global Priority Boost；
- `usage_usec` counter reset。

### cgroup Client

- 完整解析 `cpu.stat`；
- 缺少 `usage_usec` 时失败；
- 读写 `cpu.weight`；
- 权重范围校验；
- 拒绝绝对路径与 `..` escape；
- 验证修改权重后 `cpu.max` 完全不变。

### Node Daemon

- 注册 Job 时设置 Q0 weight；
- 重试注册幂等；
- 一个 cgroup 只能有一个运行 Job；
- 长 Job 逐级降到 Q2；
- 新短 Job 保持 Q0；
- Q0 / Q2 权重同时存在；
- Job 完成后恢复 idle weight。

### HTTP API

- Job 注册、采样、降级、查询与结束；
- 配置和队列快照；
- JSON 请求约束与错误映射。

静态检查与构建：

```bash
make vet
make build
```

## 实验设计

这个项目最终必须证明的不是“队列能迁移”，而是短任务尾延迟真的下降，并且 CPU 不被浪费。

### Baseline A：Equal Weight

```text
所有 Sandbox cpu.weight 相同
```

### Baseline B：Hard Quota Priority

```text
低优先级任务设置较低 cpu.max
```

它可能改善短任务，但会在高优先级空闲时浪费 CPU。

### AgentRM

```text
Q0 / Q1 / Q2 feedback
+
cpu.weight only
+
Aging / Priority Boost
```

### Workload

在 CPU 满载节点混合：

- 50–300 ms 搜索、格式化和小 Tool；
- 1–10 s 单元测试；
- 30–180 s Compile；
- 5 min Benchmark；
- 不同到达率与并发 Sandbox 数量。

### 核心指标

```text
P50 / P95 / P99 short-job completion time     ↓
test completion time                          ↓ or stable
long-job throughput                           remains acceptable
node CPU utilization                          stays near saturated
idle CPU caused by policy                     ≈ 0
queue promotions / demotions                  explainable
```

### 公平性指标

- Q2 Job 在持续短任务到达时仍获得 CPU service；
- 最大 starvation interval；
- Priority Boost 前后的 service share；
- 不同权重和 quantum 对长任务 slowdown 的影响。

## 安全与生产注意事项

Node Daemon 能修改宿主机 cgroup controller 文件，是高权限组件。

生产要求：

- lifecycle API 只允许节点内受信组件访问；
- 增加节点身份认证与授权；
- 严格校验 `cgroup_path`；
- 限制允许管理的 Pod、namespace 和 cgroup subtree；
- 根据环境替换 privileged container 为最小权限；
- 记录 queue transition 与 weight write 审计日志；
- 对频繁失败的 cgroup 做退避与告警；
- 不接受任意主机文件路径；
- 不把 HTTP 端口直接暴露公网。

当前路径校验拒绝绝对路径、`.` 和 `..` escape；真实 cgroup root 也必须在启动时存在且为目录。

## 当前限制

- `MemoryJobStore` 在 Daemon 重启后丢失；
- Kubernetes Agent Sandbox watcher 尚未实现；
- 当前通过 lifecycle API 注册 Job cgroup；
- 尚未自动发现已存在的运行中 Job；
- 尚未提供 Prometheus 指标和 OpenTelemetry（开放遥测）trace；
- 尚未用真实多 Sandbox trace 校准默认参数；
- 权重效果受 cgroup hierarchy 和现有 Kubernetes CPU limit 影响；
- 同一个被调度实体必须有可独立写权重的 cgroup。

## 路线图

### Phase 1 — MLFQ Core

- [x] Tool Job model
- [x] Q0 / Q1 / Q2
- [x] CPU service quantum
- [x] Demotion
- [x] Aging
- [x] Global Priority Boost
- [x] Counter reset handling

### Phase 2 — Node Daemon

- [x] `cpu.stat` reader
- [x] `cpu.weight` reconciler
- [x] Generic Job lifecycle API
- [x] Per-job failure isolation
- [x] Kubernetes DaemonSet example
- [ ] Persistent local Job Store
- [ ] Restart reconciliation

### Phase 3 — Agent Sandbox Integration

- [ ] Kubernetes Agent Sandbox watcher
- [ ] Tool cgroup discovery adapter
- [ ] Pod / Sandbox identity resolver
- [ ] Automatic stale Job cleanup
- [ ] Multi-container Sandbox handling

### Phase 4 — Observability

- [ ] Queue size and transition metrics
- [ ] Per-queue CPU service
- [ ] Short-job latency histogram
- [ ] Weight write failure metrics
- [ ] Node contention and utilization metrics

### Phase 5 — Evaluation

- [ ] Reproducible mixed workload generator
- [ ] Equal-weight baseline
- [ ] Hard-quota baseline
- [ ] P95 / P99 short-job latency report
- [ ] Long-job slowdown and starvation analysis
- [ ] Weight / quantum tuning guide

---

AgentRM 的核心原则是：**不猜任务会运行多久，只观察它已经消耗了多少 CPU；不限制低优先级任务能跑多少，只在真正竞争时改变谁先获得更多 CPU。**
