# AgentRM HTTP API（Hypertext Transfer Protocol Application Programming Interface）

当前接口版本为 `v1`，默认监听 `:8080`，请求和响应使用 JSON（JavaScript Object Notation）。接口只承载通用 Tool Job 生命周期与观测，不接收任务类型、预计时长、CPU（Central Processing Unit，中央处理器）需求或 Agent 语义优先级。

## 约定

- `cgroup_path` 必须是相对于 `--cgroup-root` 的路径，不能是绝对路径或包含 `..`。
- `cpu_usage_usec` 和 `service_in_level_usec` 单位为微秒。
- `cpu_weight` 必须位于 cgroup v2 规定的 `1..10000` 范围。
- 一个 cgroup 同时只能关联一个运行中的 Job。
- Job 完成后，Daemon 将其 `cpu.weight` 恢复为 `idle_weight`。
- 请求体上限为 1 MiB（Mebibyte），未知字段与多个 JSON 对象会被拒绝。

## 健康检查

### `GET /healthz`

```json
{
  "status": "ok"
}
```

## 调度配置

### `GET /v1/config`

```json
{
  "queues": {
    "Q0": {"weight": 10000, "quantum_usec": 250000},
    "Q1": {"weight": 3000, "quantum_usec": 2000000},
    "Q2": {"weight": 500}
  },
  "q1_aging_millis": 5000,
  "q2_aging_millis": 15000,
  "boost_millis": 30000,
  "idle_weight": 100
}
```

## 调度快照

### `GET /v1/scheduler`

返回各队列和终态 Job 数量。

```json
{
  "running": 3,
  "finished": 12,
  "failed": 1,
  "q0": 1,
  "q1": 1,
  "q2": 1
}
```

## 注册 Tool Job

### `POST /v1/jobs`

当一个新的 Tool Job 对应 cgroup 已经创建后，向 Node Daemon 注册。注册不包含任何工作负载预测；所有 Job 无条件进入 Q0。

```json
{
  "job_id": "tool-01J6Z8",
  "sandbox_id": "sandbox-42",
  "cgroup_path": "kubepods.slice/pod-uid/tool-01J6Z8"
}
```

成功返回 `201 Created`：

```json
{
  "job_id": "tool-01J6Z8",
  "sandbox_id": "sandbox-42",
  "cgroup_path": "kubepods.slice/pod-uid/tool-01J6Z8",
  "state": "RUNNING",
  "queue": "Q0",
  "cpu_weight": 10000,
  "cpu_usage_usec": 931,
  "service_in_level_usec": 0,
  "demotions": 0,
  "promotions": 0,
  "started_at": "2026-08-27T10:00:00Z",
  "level_entered_at": "2026-08-27T10:00:00Z",
  "last_observed_at": "2026-08-27T10:00:00Z"
}
```

相同 `job_id`、`sandbox_id` 和 `cgroup_path` 的运行中 Job 重试注册是幂等的。不同 Job 复用仍在运行的 cgroup 返回 `409 Conflict`。

## 查询 Job

### `GET /v1/jobs`

返回全部 Job，包括已结束记录。

### `GET /v1/jobs/{job_id}`

返回单个 Job。`queue`、`cpu_weight`、`service_in_level_usec`、`demotions` 和 `promotions` 可用于调试调度行为。

## 结束 Job

### `POST /v1/jobs/{job_id}/finish`

正常结束：

```json
{}
```

显式标记失败：

```json
{
  "state": "FAILED"
}
```

只接受 `FINISHED` 或 `FAILED`。操作成功后 cgroup 恢复 idle weight；重复结束是幂等的。

## 手动 Tick

### `POST /v1/tick`

立即执行一次采样与调度周期，主要用于测试和诊断。正常运行时由 `--sample-interval` 驱动后台 tick。

```json
{
  "observed_at": "2026-08-27T10:00:01Z",
  "priority_boost": false,
  "jobs": [
    {
      "job_id": "tool-01J6Z8",
      "weight_applied": true,
      "evaluation": {
        "previous_queue": "Q0",
        "reason": "quantum_exhausted",
        "service_delta_usec": 250000,
        "level_changed": true,
        "weight_changed": true,
        "job": {}
      }
    }
  ]
}
```

单个 cgroup 读取或写入失败不会阻止其他 Job 调度；错误会出现在对应 `jobs[].error` 中，并在下一次 tick 重试。

## 常见状态码

- `400 Bad Request`：字段、状态、路径或请求体不合法。
- `404 Not Found`：Job 不存在。
- `409 Conflict`：Job 标识冲突，或 cgroup 已被其他运行中 Job 占用。
