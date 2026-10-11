"""Offline validation for the Issue #198 A/B data processing utilities."""
import csv
import hashlib
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

from bench_brutal_acceptance import parse_variant, sample
from summarize_brutal_acceptance import summarize, main, REQUIRED_COLUMNS


class DataSummaryTests(unittest.TestCase):
    def make_csv(self, folder, rounds=5, skip=None, fail=None):
        path = Path(folder) / "result.csv"
        fieldnames = sorted(REQUIRED_COLUMNS | {"error"})
        with path.open("w", newline="", encoding="utf-8") as f:
            writer = csv.DictWriter(f, fieldnames=fieldnames)
            writer.writeheader()
            for r in range(1, rounds + 1):
                for variant in ("bbr", "brutal", "hysteria2"):
                    if (r, variant) == skip:
                        continue
                    writer.writerow({
                        "scenario": "fixed-link-50ms", "loss_label": "1",
                        "direction": "download", "variant": variant, "round": r,
                        "success": str((r, variant) != fail),
                        "goodput_mbps": str({"bbr": 80, "brutal": 100, "hysteria2": 110}[variant] + r),
                        "bytes": 67108864, "elapsed_s": 6, "checksum_ok": "True",
                        "error": "curl failed" if (r, variant) == fail else "",
                    })
        return path

    def test_complete_balanced_matrix(self):
        with tempfile.TemporaryDirectory() as folder:
            rows, problems = summarize(self.make_csv(folder))
            self.assertEqual([], problems)
            self.assertEqual(3, len(rows))
            brutal = next(row for row in rows if row["variant"] == "brutal")
            self.assertEqual(5, brutal["runs"])
            self.assertEqual("103.0000", brutal["median_goodput_mbps"])
            self.assertLess(float(brutal["relative_to_hysteria2_pct"]), 0)
            self.assertEqual(0, main(["--input", str(Path(folder) / "result.csv"),
                                      "--output", str(Path(folder) / "summary.csv")]))
            self.assertTrue((Path(folder) / "summary.csv").is_file())

    def test_missing_round_rejected(self):
        with tempfile.TemporaryDirectory() as folder:
            _, problems = summarize(self.make_csv(folder, skip=(3, "brutal")))
            self.assertTrue(any("nonmatching" in p for p in problems), problems)
            self.assertTrue(any("require >= 5" in p for p in problems), problems)

    def test_failed_transfer_rejected(self):
        with tempfile.TemporaryDirectory() as folder:
            _, problems = summarize(self.make_csv(folder, fail=(2, "hysteria2")))
            self.assertTrue(any("failed transfer" in p for p in problems), problems)

    def test_duplicate_round_rejected(self):
        with tempfile.TemporaryDirectory() as folder:
            path = self.make_csv(folder)
            with path.open(newline="", encoding="utf-8") as source:
                reader = csv.DictReader(source)
                names, rows = reader.fieldnames, list(reader)
            with path.open("a", newline="", encoding="utf-8") as out:
                writer = csv.DictWriter(out, fieldnames=names)
                writer.writerow(rows[0])
            _, problems = summarize(path)
            self.assertTrue(any("duplicate scenario" in p for p in problems), problems)

    def test_malformed_metrics_rejected(self):
        with tempfile.TemporaryDirectory() as folder:
            path = self.make_csv(folder)
            with path.open(newline="", encoding="utf-8") as source:
                reader = csv.DictReader(source)
                names, rows = reader.fieldnames, list(reader)
            rows[0]["goodput_mbps"] = "NaN"
            with path.open("w", newline="", encoding="utf-8") as target:
                writer = csv.DictWriter(target, fieldnames=names)
                writer.writeheader()
                writer.writerows(rows)
            _, problems = summarize(path)
            self.assertTrue(any("invalid transfer metrics" in p for p in problems), problems)


class SamplerTests(unittest.TestCase):
    def test_proxy_requires_explicit_scheme(self):
        with self.assertRaises(Exception):
            parse_variant("brutal=127.0.0.1:1080")
        self.assertEqual(("brutal", "socks5h://127.0.0.1:1080"),
                         parse_variant("brutal=socks5h://127.0.0.1:1080"))

    def test_proxy_is_forced_despite_environment(self):
        args = SimpleNamespace(direction="download", url="http://example.test/file",
                               timeout=10, expected_sha256=hashlib.sha256(b"payload").hexdigest())
        with tempfile.TemporaryDirectory() as folder:
            dest = Path(folder) / "download"
            def fake_run(cmd, **kwargs):
                self.assertIn("--noproxy", cmd)
                self.assertEqual("", cmd[cmd.index("--noproxy") + 1])
                self.assertEqual("socks5h://127.0.0.1:1080", cmd[cmd.index("--proxy") + 1])
                dest.write_bytes(b"payload")
                return SimpleNamespace(returncode=0, stdout="200\t1.0\t7\t0\t7\t0\t0.01\t0.03",
                                       stderr="")
            with mock.patch("bench_brutal_acceptance.subprocess.run", side_effect=fake_run):
                result = sample(args, "socks5h://127.0.0.1:1080", dest)
            self.assertTrue(result["success"], result)
            self.assertEqual("True", result["checksum_ok"])


if __name__ == "__main__":
    unittest.main()
