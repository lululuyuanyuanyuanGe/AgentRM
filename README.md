# AgentRM

AgentRM 是一个运行在 Kubernetes Agent Sandbox 之上的 Sandbox CPU（Central Processing Unit，中央处理器）反馈调度器。它将一个 Agent Sandbox backing Pod 视为一个长期存在的调度实体，通过 eBPF（extended Berkeley Packet Filter，扩展伯克利包过滤器）在 Linux 内核中按 cgroup 统计实际 CPU Service，并用 MLFQ（Multi-Level Feedback Queue，多级反馈队列）策略把 Q0、Q1、Q2 映射到 cgroup v2（control groups version 2，控制组第二版）的 `cpu.weight`。

AgentRM 不预测命令时长，不解析 Tool，不接收 Agent 语义，也不修改 `cpu.max`：

> CPU 空闲时让 Linux 使用全部算力；CPU 发生竞争时，让尚未耗尽高优先级 Credit 的 Sandbox 获得更大的相对 share。

## 项目状态

当前版本是可构建、可部署的系统原型，已经包含：

- 基于官方 `sigs.k8s.io/agent-sandbox/api/v1beta1` 类型识别 Sandbox backing Pod；
- 每节点 Kubernetes Pod informer，只监听调度到本节点的 Pod；
- systemd 与 cgroupfs 两种 kubelet cgroup 路径解析；
- Pod cgroup 及其 container leaf cgroup 的 ID 聚合；
- `sched_switch` tracepoint eBPF CPU Service 记账；
- BPF Map Credit 状态和 BPF Ring Buffer 阈值事件；
- Go Node Daemon 中的 Q0 → Q1 → Q2 策略和低频 Priority Boost；
- `cpu.weight` 写入、漂移隔离和过期事件过滤；
- Linux/amd64 与 Linux/arm64 镜像构建；
- 单元测试、竞态检测和 Kubernetes DaemonSet。

尚未完成真实多节点集群的 workload benchmark、状态持久化和生产权限收敛。详见[当前限制](#当前限制)。

## 为什么调度 Sandbox，而不是 Tool

旧设计把每个 Tool Call 当作一个新 Job：

```text
Tool start → Q0 → Tool finish
next Tool → fresh Q0
```

这种模型允许一个 Session 通过连续执行 `grep`、`git diff`、`git status` 等轻量命令反复刷新 Q0 Credit。新模型把 Credit 绑定到整个 Agent Session：

```text
1 Agent Session
≈ 1 Agent Sandbox
≈ 1 backing Pod
≈ 1 AgentRM scheduling entity
```

Tool 切换、命令变化和普通 Pod update 都不会重置队列或 Credit。只有新的 backing Pod 被发现，或者低频全局 Priority Boost 发生时，实体才重新进入 Q0。

例如 Q0 Credit 为 4 CPU-s：

```text
grep       0.1 CPU-s
git diff   0.2 CPU-s
pytest     2.0 CPU-s
compile    1.7 CPU-s
--------------------
累计       4.0 CPU-s → Q0 降级到 Q1
```

AgentRM 从数据模型、HTTP（Hypertext Transfer Protocol，超文本传输协议）接口和控制器中删除了 Tool lifecycle、任务分类、LLM（Large Language Model，大语言模型）Hint 与 CPU Demand Prediction。

## 与 Kubernetes Agent Sandbox 的关系

[Kubernetes Apps Special Interest Group（SIG，特别兴趣小组）Agent Sandbox](https://github.com/kubernetes-sigs/agent-sandbox) 管理 Sandbox 的声明式生命周期、隔离运行环境、稳定身份、Pod、网络和持久卷。AgentRM 不 fork 或重新实现它，而是依赖其公开 API（Application Programming Interface，应用程序编程接口）和 backing Pod ownership contract：

```text
Agent
  │
  ▼
Kubernetes Agent Sandbox controller
  │ creates / owns
  ▼
Sandbox CR（Custom Resource，自定义资源）
  │
  ▼
backing Pod ──────► AgentRM Pod informer
                         │
                         ▼
                 cgroup discovery
                         │
                         ▼
                 eBPF accounting
                         │
                         ▼
                 MLFQ cpu.weight
```

当前固定对接官方核心资源：

```yaml
apiVersion: agents.x-k8s.io/v1beta1
kind: Sandbox
```

Agent Sandbox controller 给 backing Pod 设置指向 Sandbox 的 controller owner reference。AgentRM 只接纳满足这个 owner reference 的 Pod；普通 Deployment、Job 或用户伪造的无归属 Pod不会进入调度器。

职责边界：

| 组件 | 职责 |
|---|---|
| Agent Sandbox | Sandbox 创建、暂停、恢复、Pod、网络、工作区与隔离运行时 |
| AgentRM watcher | 找到本节点属于 `agents.x-k8s.io/v1beta1` Sandbox 的 backing Pod |
| eBPF | 高频 Observe、Account、Threshold Detect |
| Go Node Daemon | MLFQ Policy、Demotion、Boost、错误重试 |
| cgroup v2 | 执行相对 CPU 权重 |
| Linux Scheduler | 真正决定 runnable thread 获得哪个 CPU 时间片 |

## 完整数据路径

```text
Kubernetes API Server
        │ Pod list/watch, fieldSelector=spec.nodeName=<this-node>
        ▼
AgentRM Sandbox Pod Watcher
        │ Sandbox owner UID（Unique Identifier，唯一标识符）+ Pod UID
        ▼
cgroup Resolver
        │
        ├─ systemd: kubepods-<qos>-pod<uid>.slice
        └─ cgroupfs: kubepods/<qos>/pod<uid>
        │
        ├─ Pod cgroup ID
        └─ descendant container cgroup IDs
        ▼
BPF membership Map
container leaf cgroup ID ──► Sandbox Pod cgroup ID
        │
        ▼
sched_switch eBPF program
        │ per-Sandbox used_cpu_ns += runtime_delta_ns
        ▼
Credit threshold crossed
        │ one Ring Buffer event per generation
        ▼
AgentRM Go Controller
        │ Q0 → Q1 → Q2
        ▼
write /sys/fs/cgroup/<pod>/cpu.weight
        │
        ▼
Linux Scheduler allocates relative CPU share under contention
```

Node Daemon 是 DaemonSet：每个节点只管理本节点 Sandbox，降级路径不依赖中心控制器往返。

## CPU Service Credit

AgentRM 使用实际 CPU Service，而不是 wall-clock time：

```text
CPU Service = 所有 Sandbox 线程实际占用 CPU 的运行时间总和
```

默认参数：

| Queue | `cpu.weight` | CPU Service Credit | 行为 |
|---|---:|---:|---|
| Q0 | 1000 | 4 CPU-s | 新 Sandbox，高相对优先级 |
| Q1 | 300 | 20 CPU-s | Q0 Credit 耗尽后 |
| Q2 | 100 | 无上限 | 长任务持续推进 |

4 CPU-s 的含义与并行度自然相关：

| 实际并行 CPU 使用 | 大约多久耗尽 Q0 |
|---:|---:|
| 1 Core | 4 s |
| 2 Core | 2 s |
| 4 Core | 1 s |
| 8 Core | 0.5 s |
| 16 Core | 0.25 s |

一个等待模型响应、网络或磁盘输入的 Sandbox 几乎不消耗 CPU Service，因此不会因为等待时间长而被降级。一个高并发 Compile 会更快用完 Credit，不需要额外识别命令或估算核心数。

## Pod-level MLFQ

状态机只有 CPU Credit 驱动的降级和低频定时 Boost：

```text
new backing Pod
       │
       ▼
 Q0 / 4 CPU-s
       │ budget event
       ▼
 Q1 / 20 CPU-s
       │ budget event
       ▼
 Q2 / no lower queue

periodic global boost:
Q1 or Q2 ───────────────► Q0
```

默认每 60 秒执行一次全局 Boost。Boost 由 Go Timer 处理；高频 runtime accounting 留在内核，避免用户态每 100 ms 遍历全部 Pod 和读取 `cpu.stat`。

### 为什么 Tool 切换不会刷新 Q0

调度状态以 Pod UID 保存。informer 的重复 Add/Update 只刷新 container cgroup membership，不重新创建调度实体。测试专门覆盖了：

- 普通 Pod update 后仍保持原来的 Q1/Q2；
- container restart 引入新的 leaf cgroup ID 时只更新映射；
- Priority Boost 后迟到的旧 Ring Buffer 事件不会再次降级。

## eBPF 记账设计

eBPF 程序位于 [`bpf/agentrm.bpf.c`](bpf/agentrm.bpf.c)，挂载到 `tracepoint/sched/sched_switch`。

### Map

`entities`：

```text
Sandbox Pod cgroup ID → {
    used_ns,
    budget_ns,
    queue_level,
    generation,
    reported
}
```

`memberships`：

```text
container leaf cgroup ID → Sandbox Pod cgroup ID
```

`last_switch_ns` 是 per-CPU Array Map，用于计算每个逻辑 CPU 两次 `sched_switch` 之间的 runtime delta。

### 为什么需要 membership Map

Sandbox 的权重写在 Pod cgroup，但真实 `gcc`、`pytest` 或 `node` 线程通常位于容器子 cgroup。`bpf_get_current_cgroup_id()` 返回当前线程的 leaf cgroup ID，不能直接拿它查 Pod-level Credit。

AgentRM 因此在发现 Pod 时枚举 Pod cgroup 下的所有子 cgroup并建立反向映射。容器重启出现新的 leaf cgroup 后，Pod update 会更新 membership，而不会清零 `used_ns`。

### Threshold 与 Ring Buffer

每次 runtime 累计使用 64 位原子加法。当 `used_ns` 首次跨过 `budget_ns` 时，`reported` 通过 64 位 compare-and-swap 保证同一 generation 只发送一个事件：

```text
{
    cgroup_id,
    used_ns,
    budget_ns,
    timestamp_ns,
    queue_level,
    generation
}
```

如果 Ring Buffer 已满，程序把 `reported` 恢复为 0，下一次 context switch 会重试。eBPF 只负责检测，不在内核里实现完整 MLFQ，也不写 `cpu.weight`。

### Generation 防止过期事件

每次 Demotion 或 Boost 都递增 generation，并用新的 Credit 配置覆盖 BPF Map。Go Controller 只处理 queue 和 generation 同时匹配当前 Session 状态的事件；旧事件直接丢弃。

## 为什么是 work-conserving

AgentRM 只写 `cpu.weight`，代码没有写 `cpu.max` 的接口。

`cpu.weight` 是相同父层级 runnable cgroup 之间的相对 share，而不是核数或硬限制。例如 Q0/Q1/Q2 同时满载且处于可比较的兄弟层级时，初始权重大致为：

```text
1000 : 300 : 100
```

如果只有一个 Q2 Sandbox runnable，即使它的 weight 是 100，它仍可以使用所有未被其他工作负载使用的 CPU。因此：

- contention 时短期新 Session 获得更大 share；
- 长 Session 始终保持正权重并持续推进；
- CPU 空闲时不会因为 AgentRM 的优先级策略产生人为 idle；
- 不需要修改 Pod spec 或重启 Pod。

已有 Kubernetes CPU limit 仍会通过 `cpu.max` 形成硬上限。要获得完整 work-conserving 行为，不应给 Sandbox 配置过紧的 CPU limit。

## cgroup 层级注意事项

权重只在相同调度层级可直接比较。生产集群需要保证被比较的 Sandbox Pod 位于兼容的 Kubernetes QoS（Quality of Service，服务质量）层级；Guaranteed、Burstable 与 BestEffort Pod 可能先在不同父 slice 上竞争。

AgentRM 的 resolver：

- 同时支持 systemd 和 cgroupfs kubelet 驱动；
- 以 Pod UID 匹配目录，不依赖容器 ID 字符串格式；
- 要求目标目录存在 `cpu.stat` 和 `cpu.weight`；
- 多个候选同时存在时拒绝猜测并等待重试；
- 返回目录 inode 作为 cgroup ID，并枚举所有 descendant IDs。

## 仓库结构

```text
AgentRM/
├── bpf/
│   └── agentrm.bpf.c             # sched_switch accounting + Ring Buffer
├── cmd/agentrm/
│   └── main.go                   # Node Daemon 入口
├── internal/
│   ├── accounting/               # eBPF loader、Map、Ring Buffer、测试内存实现
│   ├── api/                      # 只读观测 API
│   ├── cgroup/                   # cpu.weight client 与 Pod cgroup resolver
│   ├── discovery/                # Agent Sandbox backing Pod informer
│   ├── mlfq/                     # Pod-level Credit policy
│   ├── model/                    # Sandbox scheduling entity
│   ├── scheduler/                # 事件编排、Demotion、Boost、重试
│   └── store/                    # 当前进程内状态表
├── deployments/kubernetes/
│   └── daemonset.yaml            # ServiceAccount、RBAC（Role-Based Access Control，基于角色的访问控制）、DaemonSet
├── docs/
│   ├── api.md
│   └── architecture.md
├── Dockerfile                    # Go + eBPF 多阶段构建
└── Makefile
```

## 环境要求

运行环境：

- Linux 内核支持 BPF Ring Buffer，建议 5.8 或更高；
- 统一 cgroup v2 hierarchy；
- CPU controller 已启用并且 Pod cgroup 的 `cpu.weight` 可写；
- 存在 `sched:sched_switch` tracepoint；
- 已安装 Kubernetes Agent Sandbox v0.5.x，API 为 `agents.x-k8s.io/v1beta1`；
- DaemonSet 能读取 host tracefs、加载 eBPF 并写 host cgroup；
- amd64 或 arm64 节点。

构建环境：

- Go 1.26；
- Docker，或者 Linux 上的 clang、libbpf headers 和 Linux UAPI（User-space API，用户空间应用程序接口）headers；
- Kubernetes 命令行工具用于部署。

macOS 可以运行 Go 单元测试和内存集成测试，但无法直接加载 Linux eBPF 程序或验证真实 `cpu.weight` 调度。

## 快速开始

### 1. 安装 Kubernetes Agent Sandbox

以下示例固定使用 `v0.5.3`，与当前 Go 依赖一致：

```bash
export AGENT_SANDBOX_VERSION=v0.5.3

kubectl apply -f \
  https://github.com/kubernetes-sigs/agent-sandbox/releases/download/${AGENT_SANDBOX_VERSION}/sandbox.yaml
```

如需 `SandboxClaim`、`SandboxTemplate` 和 `SandboxWarmPool`，再安装 extensions manifest。以[官方发布页](https://github.com/kubernetes-sigs/agent-sandbox/releases)为准。

### 2. 构建并发布 AgentRM 镜像

```bash
docker build --platform linux/amd64 -t ghcr.io/<owner>/agentrm:<tag> .
docker push ghcr.io/<owner>/agentrm:<tag>
```

arm64 节点使用：

```bash
docker build --platform linux/arm64 -t ghcr.io/<owner>/agentrm:<tag> .
```

修改 `deployments/kubernetes/daemonset.yaml` 中的镜像地址后部署：

```bash
kubectl apply -f deployments/kubernetes/daemonset.yaml
kubectl rollout status daemonset/agentrm -n agent-sandbox-system
```

DaemonSet 会通过 Downward API 自动取得当前 `NODE_NAME`，并用 field selector 只监听本节点 Pod。

### 3. 创建 Sandbox

```yaml
apiVersion: agents.x-k8s.io/v1beta1
kind: Sandbox
metadata:
  name: build-session-a
  namespace: default
spec:
  podTemplate:
    spec:
      containers:
        - name: workspace
          image: ubuntu:24.04
          command: ["sleep", "infinity"]
```

```bash
kubectl apply -f sandbox.yaml
kubectl wait --for=condition=Ready sandbox/build-session-a --timeout=120s
```

当 backing Pod 进入 Running 后，本节点 AgentRM 会自动：

1. 验证 Sandbox owner reference；
2. 解析 Pod cgroup；
3. 写入 Q0 `cpu.weight=1000`；
4. 将 Pod 和 container cgroup IDs 注册到 BPF Map；
5. 等待 Credit threshold event。

不需要调用 `/v1/jobs`，也不需要 Agent Runtime 发送 Tool Start/Finish。

### 4. 查看调度状态

```bash
kubectl port-forward -n agent-sandbox-system daemonset/agentrm 8080:8080

curl http://127.0.0.1:8080/v1/config
curl http://127.0.0.1:8080/v1/scheduler
curl http://127.0.0.1:8080/v1/sandboxes
```

API 只用于观测，不允许调用者指定 cgroup path 或伪造 Sandbox 生命周期。

## 配置

| Flag | 默认值 | 说明 |
|---|---:|---|
| `--listen` | `:8080` | 观测 HTTP 地址 |
| `--node-name` | `$NODE_NAME` | 当前 Kubernetes 节点 |
| `--kubeconfig` | 空 | 空值使用 in-cluster credentials |
| `--cgroup-root` | `/sys/fs/cgroup` | host cgroup v2 根目录 |
| `--bpf-object` | `/usr/lib/agentrm/agentrm.bpf.o` | 编译后的 eBPF ELF（Executable and Linkable Format，可执行与可链接格式）对象 |
| `--q0-weight` | `1000` | Q0 `cpu.weight` |
| `--q1-weight` | `300` | Q1 `cpu.weight` |
| `--q2-weight` | `100` | Q2 `cpu.weight` |
| `--q0-budget` | `4s` | Q0 CPU Service Credit |
| `--q1-budget` | `20s` | Q1 CPU Service Credit |
| `--boost-interval` | `60s` | 全局 Priority Boost 周期 |

`time.Duration` 在这里表示累计 CPU runtime。例如 `4s` 是 4 CPU-seconds，不是 Sandbox 经过 4 秒墙钟时间。

## 本地开发

```bash
make test
make vet
make build
```

在 Linux 上单独编译 eBPF：

```bash
make bpf
```

完整镜像构建：

```bash
make docker-build
```

测试覆盖：

- Q0/Q1 Service Credit 和 Q2 无上限；
- Priority Boost；
- Tool/Pod update 不刷新 Session Credit；
- container restart membership refresh；
- stale generation event；
- systemd/cgroupfs 路径；
- `cpu.weight` 范围与 `cpu.max` 不变；
- 官方 Agent Sandbox v1beta1 owner reference；
- informer list/watch；
- Go/C Ring Buffer ABI；
- 并发访问竞态检测。

CI（Continuous Integration，持续集成）还会在 Ubuntu 上编译 eBPF 程序并构建 Node Daemon。

## 安全模型

AgentRM 是节点高权限组件。当前示例使用 privileged root container 是为了兼容不同内核的 eBPF load/attach 和 cgroup 写权限，不代表最终生产权限边界。

已实现的限制：

- 只监听当前节点 Pod；
- 只接纳官方 Sandbox controller owner reference；
- 不接受外部 cgroup path；
- resolver 只返回配置 root 下的相对路径；
- `cpu.weight` 仅允许 `1..10000`；
- 不读取 Sandbox 文件、环境变量、命令或进程内存；
- 不向 Sandbox Pod注入 Kubernetes ServiceAccount token；
- 观测 API 不提供调度写操作。

生产环境还应：

- 用 seccomp、AppArmor/SELinux 和最小 capabilities 替代通用 privileged；
- 通过 NetworkPolicy 限制观测 API；
- 限制可接纳的 namespace；
- 对 eBPF attach、Map 容量和 Ring Buffer 丢事件建立告警；
- 验证所用 RuntimeClass、kubelet cgroup driver 和内核版本。

## 当前限制

- `MemorySandboxStore` 未持久化。Daemon 重启后 informer 会重建实体，但现有 Session Credit 会从 Q0 开始；
- cgroup resolver 当前在 Pod 事件和重试时扫描 host cgroup tree，尚未接入 CRI（Container Runtime Interface，容器运行时接口）以直接解析路径；
- backing Pod 重建会被视为新的调度实体；普通 Tool 切换和 container restart 不会；
- 尚未在真实大规模集群验证默认 `1000/300/100` 权重和 `4/20 CPU-s` Credit；
- 尚未提供端到端 benchmark，不能宣称具体 P50/P99（50/99 百分位）延迟改善；
- 不管理 memory、storage、GPU（Graphics Processing Unit，图形处理器）、Suspend 或 Resume；
- 不修改 Kubernetes requests/limits，也不会绕过已有 `cpu.max`；
- 不同 QoS 父层级会影响权重比例；
- Ring Buffer Map 默认 1 MiB，membership Map 默认最多 32768 个 leaf cgroup；
- DaemonSet 权限仍偏大，需要按目标发行版收敛。

## 下一步

- 用 SQLite 或 pinned BPF Map 持久化 Session generation 与 Credit；
- 使用 CRI status / cgroup path 信息替代全树扫描；
- 增加真实 Agent Sandbox + containerd + cgroup v2 端到端测试；
- 建立 Q0 latency、Q2 progress、Ring Buffer pressure 与 demotion rate 指标；
- 对比 baseline、polling MLFQ 和 eBPF event-driven MLFQ；
- 根据真实 trace 校准 Credit、weight 和 Boost；
- 收敛 BPF、tracepoint 与 cgroup 写入权限。

更严格的状态机、并发顺序和失败恢复说明见 [`docs/architecture.md`](docs/architecture.md)，观测接口见 [`docs/api.md`](docs/api.md)。
