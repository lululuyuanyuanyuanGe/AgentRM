from dataclasses import dataclass


MIB = 1024 * 1024
GIB = 1024 * MIB


@dataclass(frozen=True)
class ResourceBounds:
    min_cpu_milli: int = 250
    max_cpu_milli: int = 8_000
    min_memory_bytes: int = 256 * MIB
    max_memory_bytes: int = 8 * GIB

    def __post_init__(self) -> None:
        if min(
            self.min_cpu_milli,
            self.max_cpu_milli,
            self.min_memory_bytes,
            self.max_memory_bytes,
        ) < 0:
            raise ValueError("resource bounds must be non-negative")
        if self.min_cpu_milli > self.max_cpu_milli:
            raise ValueError("minimum CPU exceeds maximum CPU")
        if self.min_memory_bytes > self.max_memory_bytes:
            raise ValueError("minimum memory exceeds maximum memory")


@dataclass(frozen=True)
class HistoricalProfile:
    cpu_p95_milli: int
    memory_p95_bytes: int
    samples: int


@dataclass(frozen=True)
class ResourceEstimate:
    cpu_milli: int
    memory_bytes: int
    workload_class: str
    confidence: float
    rationale: str

    def as_api_resource(self) -> dict:
        return {
            "cpu_milli": self.cpu_milli,
            "memory_bytes": self.memory_bytes,
        }

