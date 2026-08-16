import re
import shlex
from enum import Enum
from typing import Dict, Optional, Tuple

from .models import GIB, MIB, HistoricalProfile, ResourceBounds, ResourceEstimate


class WorkloadClass(str, Enum):
    LIGHT = "LIGHT"
    IO_BOUND = "IO_BOUND"
    CPU_HEAVY = "CPU_HEAVY"
    TEST = "TEST"
    MEMORY_HEAVY = "MEMORY_HEAVY"
    DEFAULT = "DEFAULT"


class ResourceHint(str, Enum):
    LIGHT = "LIGHT"
    HEAVY = "HEAVY"


STATIC_PROFILES: Dict[WorkloadClass, Tuple[int, int]] = {
    WorkloadClass.LIGHT: (250, 256 * MIB),
    WorkloadClass.IO_BOUND: (1_000, 1 * GIB),
    WorkloadClass.CPU_HEAVY: (4_000, 3 * GIB),
    WorkloadClass.TEST: (2_000, 2 * GIB),
    WorkloadClass.MEMORY_HEAVY: (2_000, 6 * GIB),
    WorkloadClass.DEFAULT: (1_000, 1 * GIB),
}

LIGHT_COMMANDS = {
    "cat",
    "find",
    "git diff",
    "git status",
    "grep",
    "head",
    "ls",
    "pwd",
    "read_file",
    "rg",
    "sed",
    "tail",
}
IO_COMMANDS = {
    "cargo fetch",
    "git clone",
    "go mod download",
    "npm ci",
    "npm install",
    "pip install",
    "pnpm install",
    "yarn install",
}
CPU_COMMANDS = {
    "cargo build",
    "clang",
    "cmake --build",
    "g++",
    "gcc",
    "go build",
    "make",
    "ninja",
}
TEST_COMMANDS = {
    "cargo test",
    "ctest",
    "go test",
    "npm test",
    "pytest",
}
MEMORY_COMMANDS = {"javac", "ld", "link", "rustc"}


class ResourceEstimator:
    """Estimate a tool's resource target without another model call."""

    def estimate(
        self,
        command: str,
        bounds: ResourceBounds = ResourceBounds(),
        history: Optional[HistoricalProfile] = None,
        hint: Optional[ResourceHint] = None,
    ) -> ResourceEstimate:
        normalized = self._normalize(command)
        workload = self.classify(normalized)
        cpu_milli, memory_bytes = STATIC_PROFILES[workload]
        reasons = ["static profile: {}".format(workload.value)]
        confidence = 0.60 if workload is not WorkloadClass.DEFAULT else 0.35

        if workload is WorkloadClass.DEFAULT and hint is not None:
            if hint is ResourceHint.LIGHT:
                workload = WorkloadClass.LIGHT
            else:
                workload = WorkloadClass.CPU_HEAVY
            cpu_milli, memory_bytes = STATIC_PROFILES[workload]
            reasons = ["semantic hint: {}".format(hint.value)]
            confidence = 0.45

        if history is not None and history.samples >= 3:
            cpu_milli = history.cpu_p95_milli
            memory_bytes = history.memory_p95_bytes
            reasons.append("historical P95 from {} samples".format(history.samples))
            confidence = max(confidence, min(0.95, 0.65 + history.samples / 100.0))

        parallelism = self._parallelism(normalized)
        if parallelism is not None:
            cpu_milli = parallelism * 1_000
            memory_bytes = max(memory_bytes, parallelism * 512 * MIB)
            reasons.append("explicit parallelism: {}".format(parallelism))
            confidence = max(confidence, 0.90)

        cpu_milli = min(max(cpu_milli, bounds.min_cpu_milli), bounds.max_cpu_milli)
        memory_bytes = min(
            max(memory_bytes, bounds.min_memory_bytes), bounds.max_memory_bytes
        )
        return ResourceEstimate(
            cpu_milli=cpu_milli,
            memory_bytes=memory_bytes,
            workload_class=workload.value,
            confidence=round(confidence, 2),
            rationale="; ".join(reasons),
        )

    def classify(self, command: str) -> WorkloadClass:
        normalized = self._normalize(command)
        for prefix in LIGHT_COMMANDS:
            if self._matches(normalized, prefix):
                return WorkloadClass.LIGHT
        for prefix in IO_COMMANDS:
            if self._matches(normalized, prefix):
                return WorkloadClass.IO_BOUND
        for prefix in TEST_COMMANDS:
            if self._matches(normalized, prefix):
                return WorkloadClass.TEST
        for prefix in CPU_COMMANDS:
            if self._matches(normalized, prefix):
                return WorkloadClass.CPU_HEAVY
        for prefix in MEMORY_COMMANDS:
            if self._matches(normalized, prefix):
                return WorkloadClass.MEMORY_HEAVY
        return WorkloadClass.DEFAULT

    @staticmethod
    def _normalize(command: str) -> str:
        if not command or not command.strip():
            return ""
        try:
            tokens = shlex.split(command)
        except ValueError:
            tokens = command.split()
        return " ".join(tokens).strip().lower()

    @staticmethod
    def _matches(command: str, prefix: str) -> bool:
        return command == prefix or command.startswith(prefix + " ")

    @staticmethod
    def _parallelism(command: str) -> Optional[int]:
        patterns = (
            r"(?:^|\s)-j\s*(\d+)(?:\s|$)",
            r"(?:^|\s)--jobs(?:=|\s+)(\d+)(?:\s|$)",
            r"(?:^|\s)(?:-n|--numprocesses)\s*(\d+)(?:\s|$)",
        )
        for pattern in patterns:
            match = re.search(pattern, command)
            if match:
                return max(1, int(match.group(1)))
        return None

