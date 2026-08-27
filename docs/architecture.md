# AgentRM MLFQ CPU Scheduler 架构

## 1. 问题定义

一个 Kubernetes（容器编排系统）节点上可能同时运行多个 Coding Agent Sandbox：

- 几十毫秒到几百毫秒的搜索、格式化和轻量 Tool；
- 数秒的单元测试；
- 数十秒到数分钟的 Compile；
- 更长时间的 Benchmark。

CPU（Central Processing Unit，中央处理器）不满载时，这些任务可以同时使用空闲核心；CPU 满载时，如果所有 Sandbox 具有相同调度权重，长 Compile 或 Benchmark 会显著增加短 Tool 的完成时间。

AgentRM 的目标不是限制长任务，而是在 contention 下让短任务优先完成，同时保证长任务持续推进：

```text
No contention:
Linux uses every idle CPU cycle

Contention:
Q0 gets a larger relative share than Q1
Q1 gets a larger relative share than Q2
```

## 2. 非目标

AgentRM 不做：

- 根据命令、模型或 Agent hint 预测任务时长；
- 修改 hard CPU quota；
- 为每个 Sandbox 预留固定 CPU；
- 重启 Pod 来改变资源；
- 替代 Linux Completely Fair Scheduler（完全公平调度器）；
- 调度内存、存储或图形处理器。

## 3. 总体架构

```text
Kubernetes Agent Sandbox / Tool Runtime
                │
       generic job start/finish
       (no semantic classification)
                │
                ▼
┌──────────────────────────────────────────────────────┐
│                 AgentRM Node Daemon                  │
│                                                      │
│  Job Store ──► MLFQ Engine ──► Q0 / Q1 / Q2        │
│      ▲                ▲                 │             │
│      │                │                 ▼             │
│ lifecycle       cpu.stat usage     desired weight     │
│      │                │                 │             │
│      └──────── cgroup v2 Client ◄───────┘             │
└──────────────────────────────────────────────────────┘
                         │
                         ▼
                 Linux cpu.weight
```

Node Daemon 以 Kubernetes DaemonSet 运行，每个节点独立调度本节点的 Tool Job。当前接口通过通用 Job lifecycle 注册 cgroup；后续可由 Agent Sandbox controller、executor hook 或 cgroup discovery adapter 自动产生这些事件。该事件只表达“Job 开始/结束和对应 cgroup”，不包含任务语义或预计时长。

## 4. MLFQ 状态

MLFQ（Multi-Level Feedback Queue，多级反馈队列）默认参数：

| Queue | `cpu.weight` | Service quantum | 定位 |
|---|---:|---:|---|
| Q0 | 10000 | 250 ms | 所有新 Job、短 Tool |
| Q1 | 3000 | 2 s | 中等长度测试或构建 |
| Q2 | 500 | 无上限 | 长 Compile、Benchmark |

所有新 Job 无条件进入 Q0。系统不提前判断它是 grep、test 还是 compile。

状态迁移：

```text
new job
   │
   ▼
  Q0 ── consume Q0 quantum ──► Q1 ── consume Q1 quantum ──► Q2
   ▲                            ▲                            │
   │                            └──────── Aging ─────────────┘
   └──────────── Aging / global Priority Boost ─────────────┘
```

每次迁移都会重置当前 queue 的 service counter，并立即映射新的 `cpu.weight`。

## 5. 使用实际 CPU Service

Daemon 周期读取 cgroup v2 `cpu.stat`：

```text
usage_usec 1234567
user_usec 1000000
system_usec 234567
```

对同一 Job 连续两次观测：

```text
service_delta = current_usage_usec - previous_usage_usec
```

只有实际获得的 CPU 时间计入 quantum。一个 Q0 Job 即使等待输入十秒，只要只消耗 10 ms CPU，就仍然留在 Q0；一个持续占用 CPU 的 Job 才会快速降到 Q1、Q2。这避免了 wall-clock quantum 对阻塞型任务的误判。

若 cgroup 重建导致计数器变小，Daemon 将其视为 counter reset：更新基线但不制造虚假的巨大 service delta。

## 6. Aging 与 Priority Boost

长任务不能永久停留在最低相对权重：

- Q1 停留达到 `q1-aging`，提升到 Q0；
- Q2 停留达到 `q2-aging`，提升到 Q1；
- 每隔 `boost-interval`，所有运行中的 Q1/Q2 Job 回到 Q0。

默认值分别为 5 秒、15 秒和 30 秒。Quantum exhaustion 在普通 tick 中优先于 Aging，避免正在持续消耗 CPU 的 Job 因为仅仅停留时间较长而立即反向提权；全局 Boost 优先级最高。

## 7. Work-conserving 的原因

AgentRM 只写 `cpu.weight`，从不写 `cpu.max`。

`cpu.weight` 是相对 share：只有多个 runnable cgroup 同时争用 CPU 时才影响时间分配。如果 Q0 Job 当前阻塞或不存在，Q2 Job 仍然可以使用所有剩余 CPU。因此：

- 高优先级短任务能够在 contention 下获得更大 share；
- 低优先级长任务始终有正权重并持续推进；
- 没有 contention 时不会人为制造空闲 CPU；
- 不需要修改 Pod resource limit 或重启 Pod。

测试会创建 `cpu.max` 哨兵文件并验证权重调整后内容完全不变。

## 8. Node Daemon Tick

每个 tick：

1. 判断是否触发 global Priority Boost；
2. 枚举所有 `RUNNING` Job；
3. 读取每个 cgroup 的 `cpu.stat`；
4. 累计当前 queue 的 CPU service；
5. 计算 quantum demotion、Aging promotion 或 Boost；
6. 持久化 Job 状态；
7. 读取实际 `cpu.weight`；
8. 只在实际值与目标值不一致时写入，修复外部漂移。

单个 Job 失败会记录到 tick report，不会阻塞同节点其他 Job。写权重失败时，调度状态仍保留，下一次 tick 会再次发现实际权重不一致并重试。

## 9. 并发与幂等

- Register、Finish 和 Tick 在单个 Daemon 内串行执行，避免同一 cgroup 的竞争写入。
- 相同 Job 的相同注册请求幂等返回。
- 一个 cgroup 同时只能有一个运行中 Job。
- 已完成 Job 的 Finish 重试幂等返回。
- Job Store 是状态源；`cpu.weight` 是被持续 reconcile 的执行状态。

当前 `MemoryJobStore` 在进程重启后丢失。生产版本应接入本地持久化数据库，并在 Daemon 启动时从 Kubernetes Pod/cgroup 状态重建运行中 Job。

## 10. Kubernetes 部署

`deployments/kubernetes/daemonset.yaml` 将宿主机 `/sys/fs/cgroup` 挂载到容器 `/host-cgroup`。修改 cgroup controller 文件通常需要特权权限，因此示例使用 privileged container；生产环境应根据运行时和内核能力收紧为最小 Linux capability 与文件权限。

关键前提：

- 节点使用 cgroup v2；
- 每个被独立调度的 Tool Job 有独立可写 cgroup；
- cgroup 已启用 CPU controller；
- Daemon 能可靠关联 Job、Sandbox 与相对 cgroup path。

## 11. 安全边界

Node Daemon 能修改宿主机 cgroup 权重，属于高权限组件：

- Job lifecycle 接口不能直接暴露公网；
- `cgroup_path` 必须限制在配置根目录内；
- 生产接口需要节点身份认证与授权；
- DaemonSet 应限制可调度命名空间和 Pod；
- 日志不得包含令牌或 Sandbox 内存内容；
- 本设计不读取进程内存，也不创建 checkpoint。

## 12. 当前限制

- Job Store 尚未持久化；
- Kubernetes Agent Sandbox watcher 尚未实现，当前使用通用 lifecycle API；
- 尚未采集节点级 utilization 与短任务延迟指标；
- 默认权重与 quantum 需要通过真实 Coding Agent trace 校准；
- 一个 Tool Job 必须对应可独立加权的 cgroup。
