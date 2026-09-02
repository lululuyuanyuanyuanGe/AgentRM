# AgentRM 观测 API（Application Programming Interface，应用程序编程接口）

AgentRM 暴露只读 HTTP（Hypertext Transfer Protocol，超文本传输协议）API，默认监听 `:8080`。

API 不负责注册 Tool、Sandbox 或 cgroup。所有调度实体必须来自 Kubernetes Agent Sandbox backing Pod informer，从而避免外部调用者提交任意 host cgroup path。

## `GET /healthz`

进程健康检查：

```json
{
  "status": "ok"
}
```

当前 health endpoint 只表示进程 HTTP handler 可响应，不等价于 Kubernetes informer 已同步或 eBPF Ring Buffer 没有丢事件。生产版应增加独立 readiness 状态。

## `GET /v1/config`

返回生效的队列配置：

```json
{
  "accounting": "ebpf_sched_switch",
  "boost_interval_seconds": 60,
  "queues": {
    "Q0": {
      "weight": 1000,
      "budget_ns": 4000000000
    },
    "Q1": {
      "weight": 300,
      "budget_ns": 20000000000
    },
    "Q2": {
      "weight": 100
    }
  },
  "scheduling_unit": "agent_sandbox_pod"
}
```

`budget_ns` 是累计 CPU（Central Processing Unit，中央处理器）Service，不是 wall-clock duration。Q2 没有有限 Credit，因此省略该字段。

## `GET /v1/scheduler`

返回本节点当前调度实体计数：

```json
{
  "sandboxes": 8,
  "q0": 2,
  "q1": 3,
  "q2": 3
}
```

## `GET /v1/sandboxes`

返回本节点当前 Sandbox scheduling entities：

```json
{
  "sandboxes": [
    {
      "namespace": "agents",
      "sandbox_name": "build-session-a",
      "sandbox_uid": "0934c230-9bd3-4b25-b438-779e64fb7ff4",
      "pod_name": "build-session-a",
      "pod_uid": "4f98b24d-27e1-4f42-a257-2d756d92c19b",
      "node_name": "worker-a",
      "cgroup_path": "kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod4f98b24d_27e1_4f42_a257_2d756d92c19b.slice",
      "cgroup_id": 90210,
      "member_cgroup_ids": [90210, 90211],
      "queue": "Q1",
      "cpu_weight": 300,
      "budget_ns": 20000000000,
      "accounted_ns": 4000123456,
      "generation": 2,
      "demotions": 1,
      "promotions": 0,
      "started_at": "2026-09-02T01:00:00Z",
      "level_entered_at": "2026-09-02T01:00:02Z",
      "last_event_at": "2026-09-02T01:00:02Z"
    }
  ]
}
```

字段说明：

| 字段 | 含义 |
|---|---|
| `sandbox_uid` | Agent Sandbox Custom Resource 的 UID（Unique Identifier，唯一标识符） |
| `pod_uid` | 当前 backing Pod UID，也是 userspace Store key |
| `cgroup_id` | Pod cgroup inode / BPF entity key |
| `member_cgroup_ids` | Pod 和 container descendant cgroup IDs |
| `budget_ns` | 当前 level Credit；Q2 为 0 |
| `accounted_ns` | 已完成 level 中通过 threshold event 确认的 CPU Service |
| `generation` | Demotion/Boost 后递增，用于过滤旧事件 |

Level 内的实时 `used_ns` 保存在 BPF Map，当前观测 API 不定期读取该高频状态。`accounted_ns` 只包含已通过 threshold event 完成的 level，不是实时累计值。

## 已删除接口

以下旧 Tool lifecycle 接口不再存在：

```text
POST /v1/jobs
POST /v1/jobs/{id}/finish
POST /v1/tick
```

请求这些路径返回 `404 Not Found`。Agent Runtime 不需要与 AgentRM 建立 Tool-level 集成。

## 安全建议

- 不要把端口直接暴露到公网；
- 使用 ClusterIP、port-forward 或受认证的内部 observability proxy；
- 使用 NetworkPolicy 限制调用方；
- cgroup path 与 inode 属于节点实现信息，不应作为租户级稳定 API；
- 当前 JSON（JavaScript Object Notation）响应没有分页，大规模节点需要增加限制。
