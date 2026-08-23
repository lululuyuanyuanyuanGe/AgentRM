# AgentRM HTTP API（Hypertext Transfer Protocol Application Programming Interface）

当前接口版本为 `v1`，默认监听 `http://127.0.0.1:8080`。请求和响应均使用 JSON（JavaScript Object Notation）。

## 约定

- CPU（Central Processing Unit，中央处理器）统一使用 millicore，`1000` 表示一个核。
- 内存统一使用字节。
- `generation` 必须单调递增；相同 generation 仅允许提交相同绝对目标。
- `priority`：`0` 为后台，`1` 为普通，`2` 为交互。
- 服务端拒绝未知 JSON 字段，请求体上限为 1 MiB（Mebibyte）。

## 健康与集群

### `GET /healthz`

返回服务存活状态。

### `GET /v1/cluster`

返回总容量、已分配容量、空闲容量和等待请求数。

## Session

### `POST /v1/sessions`

创建 Session，并立即为其分配 minimum resource。

```json
{
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
}
```

### `GET /v1/sessions`

列出所有 Session。

### `GET /v1/sessions/{session_id}`

读取一个 Session 的 authoritative state。

### `PATCH /v1/sessions/{session_id}/state`

更新 Agent 语义状态。

```json
{
  "session_state": "RUNNING_TOOL",
  "task_priority": 2
}
```

可用状态：`CREATING`、`READY`、`RUNNING_TOOL`、`WAITING_LLM`、`WAITING_USER`、`BACKGROUND`、`WAITING_RESOURCE`、`SUSPENDING`、`SUSPENDED`、`RESUMING`、`FINISHED`、`FAILED`。

### `PUT /v1/sessions/{session_id}/metrics`

写入轻量资源采样结果。内存稳定起点用于判断是否允许缩容。

```json
{
  "actual_cpu_milli": 730,
  "memory_working_set_bytes": 1610612736,
  "memory_stable_since": "2026-08-25T08:00:00Z"
}
```

### `POST /v1/sessions/{session_id}/resources`

提交绝对资源目标，而不是增量。

```json
{
  "desired_resource": {
    "cpu_milli": 8000,
    "memory_bytes": 6442450944
  },
  "generation": 3,
  "priority": 2
}
```

成功后返回 `202 Accepted`。后台 reconcile loop 会异步处理请求；读取 Session 的 `allocated_resource` 和 `applied_generation` 判断是否完整落地。只有所有维度都达到目标后才推进 `applied_generation`；不安全的内存缩容会延迟重试。

### `POST /v1/sessions/{session_id}/suspend`

挂起处于可挂起状态的 Session。当前内存后端生成模拟 checkpoint reference；生产后端应替换为真实持久化引用。

### `POST /v1/sessions/{session_id}/resume`

从 checkpoint 恢复，并重新分配 minimum resource。

### `POST /v1/sessions/{session_id}/finish`

结束 Session，删除后端 Sandbox 并释放资源。

## 调度调试

### `POST /v1/scheduler/run-once`

手动处理一个合并后的资源请求。服务已自带 reconcile loop，此接口主要用于测试与观察调度计划。

## 错误格式

```json
{
  "error": "stale request generation"
}
```

常见状态码：

- `400 Bad Request`：参数、状态或 generation 不合法。
- `404 Not Found`：Session 不存在。
- `409 Conflict`：Session 已存在，或相同 generation 对应不同目标。
- `503 Service Unavailable`：集群无法满足新建或恢复所需的 minimum resource。
