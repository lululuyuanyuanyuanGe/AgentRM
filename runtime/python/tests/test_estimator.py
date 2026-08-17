import unittest

from agentrm_runtime.estimator import ResourceEstimator, ResourceHint, WorkloadClass
from agentrm_runtime.models import GIB, HistoricalProfile, ResourceBounds


class ResourceEstimatorTest(unittest.TestCase):
    def setUp(self) -> None:
        self.estimator = ResourceEstimator()

    def test_light_command(self) -> None:
        estimate = self.estimator.estimate("rg --files")
        self.assertEqual(estimate.workload_class, WorkloadClass.LIGHT.value)
        self.assertEqual(estimate.cpu_milli, 250)

    def test_explicit_parallelism_has_highest_priority(self) -> None:
        estimate = self.estimator.estimate(
            "cmake --build . -j16",
            bounds=ResourceBounds(max_cpu_milli=32_000, max_memory_bytes=32 * GIB),
            history=HistoricalProfile(cpu_p95_milli=3_000, memory_p95_bytes=2 * GIB, samples=20),
        )
        self.assertEqual(estimate.cpu_milli, 16_000)
        self.assertEqual(estimate.memory_bytes, 8 * GIB)
        self.assertGreaterEqual(estimate.confidence, 0.9)

    def test_history_overrides_static_profile(self) -> None:
        estimate = self.estimator.estimate(
            "cargo build",
            history=HistoricalProfile(cpu_p95_milli=6_400, memory_p95_bytes=5 * GIB, samples=10),
        )
        self.assertEqual(estimate.cpu_milli, 6_400)
        self.assertEqual(estimate.memory_bytes, 5 * GIB)

    def test_hint_is_used_only_for_unknown_command(self) -> None:
        estimate = self.estimator.estimate("custom-tool", hint=ResourceHint.HEAVY)
        self.assertEqual(estimate.workload_class, WorkloadClass.CPU_HEAVY.value)

        known = self.estimator.estimate("cat README.md", hint=ResourceHint.HEAVY)
        self.assertEqual(known.workload_class, WorkloadClass.LIGHT.value)

    def test_bounds_are_enforced(self) -> None:
        estimate = self.estimator.estimate(
            "make -j64",
            bounds=ResourceBounds(max_cpu_milli=8_000, max_memory_bytes=4 * GIB),
        )
        self.assertEqual(estimate.cpu_milli, 8_000)
        self.assertEqual(estimate.memory_bytes, 4 * GIB)


if __name__ == "__main__":
    unittest.main()
