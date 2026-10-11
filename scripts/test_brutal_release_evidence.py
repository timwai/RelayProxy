import csv
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


    def test_full_matrix_uses_loaded_p50_not_p95_for_rtt_ratio(self):
        from verify_brutal_release_evidence import MANIFEST_FIELDS
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            ab = root / "ab.csv"
            rows = []
            for loss in ("0", "1", "5"):
                for direction in ("download", "upload"):
                    for variant, port in (("bbr", 1080), ("brutal", 1081), ("hysteria2", 1082)):
                        for i in range(1, 6):
                            rows.append({
                                "scenario": "wan-scenario", "loss_label": loss,
                                "direction": direction, "variant": variant, "round": i,
                                "success": "true", "bytes": 1000000,
                                "elapsed_s": 1, "goodput_mbps": 8,
                                "checksum_ok": "true" if direction == "download" else "",
                                "url": "https://origin.test/" + direction,
                                "proxy": f"socks5h://127.0.0.1:{port}",
                                "loaded_icmp_p95_ms": 150,
                                "loaded_icmp_samples": 12,
                            })
            with ab.open("w", newline="", encoding="utf-8") as stream:
                writer = csv.DictWriter(stream, fieldnames=list(rows[0]))
                writer.writeheader()
                writer.writerows(rows)
            manifest = {name: "test-real-wan" for name in MANIFEST_FIELDS}
            for name in ("relayproxy_agent_sha256", "relayproxy_server_sha256",
                         "hysteria2_sha256", "origin_fixture_sha256"):
                manifest[name] = "a" * 64
            manifest["relayproxy_commit"] = "a" * 40
            manifest["loss_evidence"] = {}
            coexistence = {}
            for loss in ("0", "1", "5"):
                evidence = root / f"loss-{loss}.txt"
                evidence.write_text("independent operator capture\\n", encoding="utf-8")
                manifest["loss_evidence"][loss] = {"impairment_log": str(evidence)}
                csv_path = root / f"coexist-{loss}.csv"
                coexistence[loss] = str(csv_path)
                data = [{
                    "scenario": "wan-scenario", "loss_label": loss,
                    "round": str(i), "valid": "true", "duration_s": 60,
                    "jain_coactive_index": 1, "bbr_coactive_mbps": 5,
                    "brutal_coactive_mbps": 5, "idle_icmp_p50_ms": 10,
                    "loaded_icmp_p50_ms": 20, "loaded_icmp_p95_ms": 150,
                    "loaded_icmp_samples": 15,
                } for i in range(1, 6)]
                with csv_path.open("w", newline="", encoding="utf-8") as stream:
                    writer = csv.DictWriter(stream, fieldnames=list(data[0]))
                    writer.writeheader()
                    writer.writerows(data)
            policy = {
                "min_jain": 0.85, "min_bbr_retention": 0.5,
                "max_loaded_icmp_p95_ms": 180,
                "max_loaded_over_idle_p50_ratio": 3,
                "min_duration_s": 60,
            }
            accepted = verify(ab, coexistence, manifest, policy)
            self.assertTrue(accepted["machine_checks_passed"], accepted["failures"])
            # Operator-supplied thresholds must never reduce the fixed 60s floor.
            weak_policy = dict(policy, min_duration_s=1)
            weak = verify(ab, coexistence, manifest, weak_policy)
            self.assertFalse(weak["machine_checks_passed"])
            self.assertTrue(any("mandatory 60-second" in e for e in weak["failures"]))
            self.assertTrue(accepted["human_real_wan_provenance_and_release_signoff_required"])
            # A p95/idle-p50 ratio of 15 is NOT the p50 inflation ratio.
            # Increasing the real loaded p50 to 40ms must fail a 3x cap.
            path = Path(coexistence["1"])
            raw = path.read_text(encoding="utf-8").replace(
                "wan-scenario,1,1,true,60,1,5,5,10,20,150,15",
                "wan-scenario,1,1,true,60,1,5,5,10,40,150,15")
            path.write_text(raw, encoding="utf-8")
            rejected = verify(ab, coexistence, manifest, policy)
            self.assertFalse(rejected["machine_checks_passed"])
            self.assertTrue(any("inflation" in x for x in rejected["failures"]))


if __name__ == "__main__":
    unittest.main()
