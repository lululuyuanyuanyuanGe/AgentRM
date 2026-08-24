# AgentRM 架构与一致性设计

## 1. 控制面边界

AgentRM 只负责资源意图、调度决策和 Sandbox 操作编排，不重新实现容器运行时、操作系统隔离或 checkpoint 格式。

```text
Agent Runtime
  ├─ Session event
  ├─ Resource request
  └─ Metrics sample
          │
          ▼
HTTP API → Controller → Coalescing Queue
                        │
                        ▼
                 Session Store
                        │
                        ▼
                   Scheduler
                    │       │
                    │       └─ Victim selection
                    ▼
               Sandbox Backend
```

`internal/backend.SandboxBackend` 是唯一执行层边界。当前 `MemoryBackend` 用于本地开发和确定性测试；Kubernetes（容器编排系统）适配器需要实现 resize、suspend、resume 和 delete 四个操作。

## 2. 关键不变量

### 2.1 资源边界

对任何非挂起 Session：

```text
minimum <= desired <= maximum
minimum <= allocated <= maximum
```

挂起或结束时 `allocated = 0`。

### 2.2 集群容量

每个调度计划都保持：

```text
sum(allocated) <= cluster capacity
```

Controller 用互斥区串行执行会改变总资源的操作，避免并发计划基于同一份旧快照而过量分配。

### 2.3 请求幂等

请求表达绝对目标：

```text
desired_cpu = 8000 millicores
```

而不是 `cpu += 4000`。同一 Session 只有最新 generation 保留在 pending map 中；堆里的旧节点在出队时惰性丢弃。

### 2.4 Stale completion

Session 同时保存：

- `generation`：控制面当前希望达到的版本。
- `applied_generation`：最近成功执行的目标版本。

后续接入异步 Kubernetes resize completion event 时，只能在 event generation 不小于当前 generation 时更新 applied state。

## 3. 调度算法

对目标 Session 的一次请求：

1. 将目标裁剪到 Session 的 minimum 和 maximum。
2. 对 CPU（Central Processing Unit，中央处理器）安全缩容；内存缩容需要先通过稳定性检查。
3. 使用集群当前空闲容量。
4. 若仍有缺口，构建 victim list。
5. 依次回收 borrowed resource，且只回收本次缺口所需数量。
6. 若所有 Session 均到 minimum 后仍不足，目标进入 `WAITING_RESOURCE`，请求带短暂 backoff 重新入队，避免无法满足的高优先请求阻塞后续释放请求。

Victim 排序键：

```text
(reclaim_class ASC, borrowed_cpu DESC, last_active_at ASC, session_id ASC)
```

请求只会从 reclaim class 比自身更低的 Session 回收资源，避免等待或后台 Session 为恢复旧的 desired allocation 反向抢占正在运行的交互任务。被回收 Session 会保留 desired state，并在未来出现 free capacity 后自动尝试恢复 borrowed resource。

Reclaim class：

| Class | Session 类型 | 含义 |
|---:|---|---|
| 0 | 长时间空闲且可挂起 | 最优先回收 |
| 1 | 等待用户、等待模型、等待资源、Ready | 无工具正在执行 |
| 2 | Background | 后台任务 |
| 3 | 普通 Active | 普通活跃任务 |
| 4 | Interactive Active | 最后回收 |

## 4. 内存保护

CPU 分配不足通常只会降低速度，内存压缩过度可能触发 OOM（Out of Memory，内存耗尽）。因此内存可回收下界为：

```text
max(session.minimum_memory, working_set × headroom)
```

默认 headroom 为 `125%`，working set 必须稳定至少两分钟。缺少指标或稳定时间不足时，不允许内存缩容。

## 5. Suspend/Resume 状态机

```text
READY / WAITING / BACKGROUND
            │ suspend
            ▼
       SUSPENDING
            │ checkpoint persisted
            ▼
        SUSPENDED
            │ resume + minimum allocation
            ▼
         RESUMING
            │ backend ready
            ▼
           READY
```

生产级 Full-state Suspend 后端应依次执行：阻止新工具、冻结进程、刷写文件系统、创建进程与内存 checkpoint、持久化元数据、删除 Pod。恢复后必须通过 hook 重建外部连接；TCP（Transmission Control Protocol，传输控制协议）连接、数据库连接和设备状态不属于透明恢复保证。

## 6. 故障语义

- Victim resize 失败：停止当前计划并重入目标请求。
- Target resize 失败：已回收资源保持释放，目标请求重入队；这会牺牲瞬时利用率，但不会超配。
- Suspend 失败：Session 状态恢复到挂起前状态。
- Resume 失败：Session 返回 `SUSPENDED`。
- Controller 重启：当前内存版不持久化；接入数据库后应从 Session Store 与 Kubernetes actual state 做 reconciliation。

## 7. Kubernetes 适配要求

生产适配器至少需要：

1. 将 `ResizeOperation.Target` 转换为 Pod resize subresource 的 requests/limits。
2. 用 operation generation 标注或关联异步完成事件。
3. 监听 Pod Ready、Failed、Deleted 和 resize 状态。
4. 调用 Kubelet Checkpoint API（Application Programming Interface）与 CRI（Container Runtime Interface，容器运行时接口）兼容运行时。
5. 将 checkpoint 加密存储，记录 image digest、runtime version、filesystem snapshot、resource spec 和 generation。
6. 恢复前执行镜像、内核、运行时与 checkpoint 格式兼容性检查。
