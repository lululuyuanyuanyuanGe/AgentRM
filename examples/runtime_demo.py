"""Run after starting the Go control plane with `make run`."""

from agentrm_runtime import AgentRMClient, ResourceBounds, ResourceEstimator
from agentrm_runtime.models import GIB


client = AgentRMClient()
estimator = ResourceEstimator()
bounds = ResourceBounds(
    min_cpu_milli=500,
    max_cpu_milli=8_000,
    min_memory_bytes=512 * 1024 * 1024,
    max_memory_bytes=8 * GIB,
)

client.create_session(
    session_id="demo-session",
    min_cpu_milli=bounds.min_cpu_milli,
    min_memory_bytes=bounds.min_memory_bytes,
    max_cpu_milli=bounds.max_cpu_milli,
    max_memory_bytes=bounds.max_memory_bytes,
    priority=2,
)

estimate = estimator.estimate("cmake --build . -j8", bounds=bounds)
client.update_state("demo-session", "RUNNING_TOOL", priority=2)
client.request_resources("demo-session", estimate, generation=1, priority=2)
print(client.get_session("demo-session"))

