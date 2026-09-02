# AgentRM 架构与一致性模型

## 1. 目标

AgentRM 是 Kubernetes Agent Sandbox 的节点级 CPU（Central Processing Unit，中央处理器）反馈调度层。每个 Sandbox backing Pod 是一个调度实体；eBPF（extended Berkeley Packet Filter，扩展伯克利包过滤器）按 cgroup 统计实际 CPU Service，Go Node Daemon 执行 MLFQ（Multi-Level Feedback Queue，多级反馈队列）策略，cgroup v2（control groups version 2，控制组第二版）`cpu.weight` 执行相对优先级。

系统只解决同一节点多个 Agent Session 在 CPU contention 下的相对 share：

```text
no contention    → 所有 runnable Sandbox 使用空闲 CPU
contention       → Q0 share > Q1 share > Q2 share
long-running Q2  → 保持正权重并持续运行
```

非目标包括 Tool 分类、命令预测、hard quota、memory 调度、跨节点 placement、Pod resize、Suspend/Resume 和替代 Linux Scheduler。

## 2. 调度实体

实体身份来自 Kubernetes：

```text
Sandbox namespace/name/UID（Unique Identifier，唯一标识符）
        │ owner reference
        ▼
backing Pod name/UID/nodeName
        │ cgroup resolver
        ▼
Pod cgroup path/inode + descendant inode set
```

Pod UID 是当前进程内 Store 的主键，Pod cgroup inode 是 BPF Map 与 Ring Buffer 的实体 ID。Sandbox UID 用于验证并保留上层 Session 身份。

一个普通 Pod update 不创建新实体。container cgroup 集合变化时只更新 membership Map，不改变 queue、generation 或 accumulated Credit。

## 3. Agent Sandbox 接入

`discovery.Watcher` 使用 Kubernetes `client-go` informer：

```text
resource: core/v1 Pods
namespace: all
field selector: spec.nodeName=<NODE_NAME>
resync: disabled
```

Pod 必须存在满足以下条件的 controller owner reference：

```text
apiVersion = agents.x-k8s.io/v1beta1
kind       = Sandbox
UID        != empty
```

该版本常量直接来自 `sigs.k8s.io/agent-sandbox/api/v1beta1`，避免在 AgentRM 中复制 Custom Resource Definition（自定义资源定义）类型。

事件规则：

- Running：尝试解析 cgroup 并接纳；
- Pending：不接纳，等待后续 Pod update；
- Succeeded/Failed：移除；
- deletion timestamp 或 Delete event：移除；
- cgroup 尚未出现：保存为 pending，每秒重试；
- 已接纳 Pod再次 Update：重新枚举 descendant cgroup IDs，但保留 Session Credit。

## 4. cgroup 解析

`FSResolver` 在 host cgroup root 下匹配 Pod UID，支持：

```text
systemd:
kubepods.slice/
  kubepods-burstable.slice/
    kubepods-burstable-pod<uid_with_underscores>.slice

cgroupfs:
kubepods/
  burstable/
    pod<uid-with-hyphens>
```

候选必须同时包含 `cpu.stat` 与 `cpu.weight`。匹配完成后：

1. 用 `stat(2)` 的 inode 得到 Pod cgroup ID；
2. 递归枚举 Pod 目录下所有 cgroup inode；
3. 生成相对于配置 root 的安全路径；
4. 多候选时返回错误，不通过目录顺序猜测。

Linux `bpf_get_current_cgroup_id()` 观察到的是运行线程所在 leaf cgroup，而 `cpu.weight` 写在 Pod cgroup。因此必须维护：

```text
memberships[leaf_cgroup_id] = pod_cgroup_id
entities[pod_cgroup_id]     = credit state
```

## 5. 内核记账

### 5.1 挂载点

程序挂载到：

```text
tracepoint/sched/sched_switch
```

每个逻辑 CPU 在 `last_switch_ns` per-CPU Map 中保存上一次切换时间。下一次切换发生时：

```text
delta_ns = bpf_ktime_get_ns() - last_switch_ns[cpu]
leaf_id  = bpf_get_current_cgroup_id()
pod_id   = memberships[leaf_id]
entities[pod_id].used_ns += delta_ns
```

只有被 AgentRM 注册的 leaf cgroup 才会命中 membership；节点上其他进程只进行两次 Map lookup，不产生事件或状态。

### 5.2 实体状态

```c
struct entity_state {
    u64 used_ns;
    u64 budget_ns;
    u32 level;
    u32 generation;
    u64 reported;
};
```

Q2 的 `budget_ns=0`，表示不再产生降级事件。`used_ns` 和 `reported` 使用 64 位原子操作，支持同一 Pod 多个线程同时在不同 CPU 上运行。

### 5.3 Threshold event

跨过预算时发送固定 40-byte Ring Buffer ABI（Application Binary Interface，应用二进制接口）：

```c
struct threshold_event {
    u64 cgroup_id;
    u64 used_ns;
    u64 budget_ns;
    u64 timestamp_ns;
    u32 level;
    u32 generation;
};
```

`reported` 从 0 原子切换为 1，保证同一 generation 最多存在一个已提交 threshold event。Ring Buffer reserve 失败时恢复为 0，以允许后续重试。

## 6. 用户态状态机

默认配置：

| Level | Weight | Budget |
|---|---:|---:|
| Q0 | 1000 | 4,000,000,000 ns |
| Q1 | 300 | 20,000,000,000 ns |
| Q2 | 100 | 0（无限） |

Demotion 事务：

```text
1. 按 cgroup ID 查找 Store entity
2. 比较 event.level 和 event.generation
3. 验证 used_ns >= current budget_ns
4. 计算 next entity，generation + 1
5. 写 next cpu.weight
6. 覆盖 BPF entity Credit，used_ns 回到 0
7. 保存 userspace entity
```

第 5 步失败时不改变 Store；event 放入 pending queue 定期重试。第 6 步失败时尝试恢复旧 `cpu.weight`，同一 event 继续重试。

### 6.1 Priority Boost

Boost 每 60 秒由 Go Timer 触发：

```text
Q1/Q2 → Q0
generation + 1
used_ns = 0
budget_ns = Q0 budget
cpu.weight = Q0 weight
```

Boost 和 Demotion 使用同一个 Controller mutex 串行执行。旧 generation 的 Ring Buffer event 即使已经到达用户态 channel，也不能改变新状态。

### 6.2 删除

Pod删除时：

1. 从 Store 删除实体；
2. 删除 BPF `entities` entry；
3. 删除该实体所有 `memberships` entries；
4. 如果 cgroup 仍存在，将 weight 恢复为 Q2 weight；
5. 后续迟到事件因 Store 不存在而忽略。

## 7. 并发模型

内核并发：

- `last_switch_ns` 是 per-CPU，无跨 CPU 写冲突；
- `used_ns` 是跨 CPU 64 位 atomic add；
- `reported` 是 64 位 compare-and-swap；
- userspace 覆盖 entity state 开启下一 generation。

用户态并发：

- Pod event、threshold event、retry 与 Boost 在一个 Run loop 接收；
- 对外测试入口也使用同一 Controller mutex；
- Store 自身使用 read/write mutex，允许 HTTP 只读快照；
- `cpu.weight` 与 BPF configuration 更新由 Controller 串行化。

## 8. 失败与恢复

| 失败 | 行为 |
|---|---|
| Pod已 Running，cgroup 尚未创建 | pending Pod 每秒重试 |
| cgroup 路径存在多个候选 | 不接纳，等待旧目录消失 |
| 初始 BPF Configure 失败 | 不写 Q0 weight，不创建 Store entity |
| 初始 weight 写失败 | 删除刚注册的 BPF entity，重试 Pod |
| Demotion weight 写失败 | Store/BPF generation 不变，重试 event |
| Demotion BPF Configure 失败 | 尝试恢复旧 weight，重试 event |
| Ring Buffer 已满 | 内核清除 reported，下一 switch 重试 |
| 旧 generation event | 忽略 |
| Pod已删除后事件到达 | 忽略 |
| container restart | 更新 membership，不刷新 Credit |
| Daemon restart | informer 重建实体；当前版本 Credit 从 Q0 重新开始 |

## 9. work-conserving 不变量

AgentRM 的 cgroup Client 只暴露：

```text
ReadCPUStat
ReadWeight
WriteWeight
```

没有写 `cpu.max` 的方法。`FSClient` 测试使用 `cpu.max` sentinel，执行 `WriteWeight` 前后内容必须一致。

`cpu.weight` 只影响竞争时相对 share；没有竞争者时不会阻止 Q2 使用空闲 CPU。这个性质来自 Linux cgroup CPU controller，不由 AgentRM 在用户态模拟。

## 10. 权限与信任边界

Node Daemon 当前需要：

- Kubernetes Pods 的 `get/list/watch` RBAC（Role-Based Access Control，基于角色的访问控制）；
- 读取 host tracefs；
- 加载并 attach eBPF；
- 读写 host cgroup `cpu.weight`；
- root 或等价最小 capabilities。

Sandbox Pod是不可信工作负载，不应获得 AgentRM ServiceAccount 或 host mounts。AgentRM 不读取 Sandbox 命令、文件、环境变量或内存。

## 11. 容量边界

默认 Map 容量：

| Map | 类型 | 最大 entries / bytes |
|---|---|---:|
| `entities` | Hash | 8192 Sandbox Pods |
| `memberships` | Hash | 32768 cgroups |
| `last_switch_ns` | Per-CPU Array | 1 per CPU |
| `events` | Ring Buffer | 1 MiB |

一个 Sandbox 有 Pod cgroup和多个容器/运行时子 cgroup，因此 membership 容量通常先成为约束。生产部署应根据单节点 Sandbox 与 sidecar 上限调整。

## 12. 尚未解决

- userspace Store 和 generation 持久化；
- BPF Map pinning 与 daemon 升级连续性；
- Container Runtime Interface（容器运行时接口）直接 cgroup resolution；
- QoS（Quality of Service，服务质量）跨父层级的权重归一；
- 真实 kernel verifier、containerd、gVisor/Kata 与 Agent Sandbox 端到端矩阵；
- Prometheus metrics 和 trace-based 参数校准；
- privileged 权限收敛。
