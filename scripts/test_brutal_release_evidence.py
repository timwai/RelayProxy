import tempfile
import unittest
from pathlib import Path

from verify_brutal_release_evidence import verify


class GateTests(unittest.TestCase):
    def test_no_input_fails_and_never_self_approves_release(self):
        with tempfile.TemporaryDirectory() as tmp:
            source = Path(tmp) / "ab.csv"
            source.write_text("scenario,loss_label\n", encoding="utf-8")
            verdict = verify(source, {}, {}, {})
            self.assertFalse(verdict["machine_checks_passed"])
            self.assertTrue(verdict["human_real_wan_provenance_and_release_signoff_required"])
            self.assertTrue(any("missing A/B condition" in e for e in verdict["failures"]))

    def test_nonfinite_metric_is_rejected(self):
        from verify_brutal_release_evidence import number
        with self.assertRaises(ValueError):
            number(float("nan"), "p95")
        with self.assertRaises(ValueError):
            number(-1, "goodput")


if __name__ == "__main__":
    unittest.main()
