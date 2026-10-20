import importlib.util
import pathlib
import unittest

spec = importlib.util.spec_from_file_location("benchmark_native", pathlib.Path(__file__).with_name("benchmark-native.py"))
benchmark = importlib.util.module_from_spec(spec)
spec.loader.exec_module(benchmark)

class BenchmarkAcceptanceTests(unittest.TestCase):
    def test_invalid_envelope_and_nonzero_exit_fail_acceptance(self):
        base = {"wallMs": 1, "cpuMs": 1, "peakBytes": 1, "exitCode": 0, "envelopeOK": True}
        rows = [base, dict(base, envelopeOK=False), dict(base, exitCode=7)]
        self.assertEqual(benchmark.summary(rows)["failures"], 2)

    def test_p95_uses_nearest_rank(self):
        rows = [{"wallMs": n, "cpuMs": 1, "peakBytes": 1, "exitCode": 0, "envelopeOK": True} for n in range(1, 101)]
        self.assertEqual(benchmark.summary(rows)["p95Ms"], 95)

if __name__ == "__main__":
    unittest.main()
