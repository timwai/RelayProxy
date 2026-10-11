"""Test that kernel netem fairness analysis uses only overlapping samples."""
import tempfile
import unittest
from pathlib import Path
from analyze_brutal_netem import analyze, extract_intervals, main


def sample(scenario, mode, ms, bytes_recv, rate=4.0, rtt=42.0):
    return (f"brutal_loss_test.go:301: overlap_sample scenario={scenario} mode={mode} "
            f"wall_unix_ms={ms} interval_goodput_mbps={rate} "
            f"sender_smoothed_rtt_ms={rtt} received_bytes={bytes_recv}\n")


class OverlapAnalysisTests(unittest.TestCase):
    def test_overlap_excludes_different_completion_windows(self):
        lines = []
        for ms in (0, 250, 500, 750, 1000, 1250):
            lines.append(sample("under-bottleneck-8mbit", "bbr", ms, ms * 1000, rate=4.0, rtt=50))
        for ms in (250, 500, 750, 1000):
            lines.append(sample("under-bottleneck-8mbit", "brutal", ms, ms * 2000, rate=7.0, rtt=100))
        rows, problems = analyze(lines, "1", minimum_overlap_ms=500)
        self.assertEqual([], problems)
        self.assertEqual(1, len(rows))
        self.assertEqual("0.750", rows[0]["overlap_seconds"])
        self.assertEqual("4.0000", rows[0]["bbr_overlap_mbps"])
        self.assertEqual("7.0000", rows[0]["brutal_overlap_mbps"])
        self.assertGreater(float(rows[0]["jain_coactive_rate_index"]), 0.8)

    def test_missing_competitor_is_rejected(self):
        rows, problems = analyze([sample("overcommit-16mbit", "bbr", n, n) for n in (0, 250, 500)], "5")
        self.assertEqual([], rows)
        self.assertTrue(any("missing BBR or Brutal" in text for text in problems), problems)

    def test_insufficient_overlap_is_rejected(self):
        lines = [sample("overcommit-16mbit", mode, n, n, 6.0)
                 for mode in ("bbr", "brutal") for n in (0, 250, 500)]
        _, problems = analyze(lines, "0", minimum_overlap_ms=750)
        self.assertTrue(any("only 250ms" in p for p in problems), problems)

    def test_nonmonotonic_receiver_bytes_is_rejected(self):
        lines = [sample("overcommit-16mbit", "brutal", ms, n)
                 for ms, n in [(0, 10), (250, 20), (500, 10)]]
        with self.assertRaisesRegex(ValueError, "counter went backwards"):
            extract_intervals(lines)

    def test_creates_evidence_csv(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / "netem.log"
            rows = [sample("overcommit-16mbit", mode, ms, ms * 1000)
                    for mode in ("bbr", "brutal") for ms in (0, 250, 500, 750, 1000, 1250)]
            path.write_text("".join(rows), encoding="utf-8")
            out = Path(folder) / "metrics.csv"
            self.assertEqual(0, main(["--input", str(path), "--output", str(out),
                                      "--loss-label", "0"]))
            self.assertIn("jain_coactive_rate_index", out.read_text(encoding="utf-8"))


if __name__ == "__main__":
    unittest.main()
