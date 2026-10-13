import tempfile
import unittest
from pathlib import Path
from recommend_brutal_target import candidates_from_summary, main


def complete_summary():
    values = []
    for loss in ("0", "1", "5"):
        for direction in ("upload", "download"):
            for variant in ("bbr", "brutal", "hysteria2"):
                values.append({
                    "scenario": "controlled-wan-a", "loss_label": loss,
                    "direction": direction, "variant": variant, "runs": 5,
                    "p10_goodput_mbps": "12" if direction == "download" else "8",
                })
    return values


class RecommendBrutalTargetTests(unittest.TestCase):
    def test_conservative_candidate_never_auto_enabled(self):
        rates = candidates_from_summary(complete_summary(), 0.60)
        self.assertEqual(4, rates["proposed_agent_transport_bandwidth"]["up_mbps"])
        self.assertEqual(7, rates["proposed_agent_transport_bandwidth"]["down_mbps"])
        self.assertFalse(rates["auto_apply_allowed"])
        self.assertFalse(rates["brutal_default_eligible"])

    def test_missing_real_loss_condition_is_rejected(self):
        rows = [r for r in complete_summary() if r["loss_label"] != "5"]
        with self.assertRaisesRegex(ValueError, "missing verified"):
            candidates_from_summary(rows)

    def test_mixed_site_data_is_never_silently_combined(self):
        rows = complete_summary()
        rows[0]["scenario"] = "different-site"
        with self.assertRaisesRegex(ValueError, "select one scenario"):
            candidates_from_summary(rows)

    def test_headroom_must_be_conservative(self):
        for x in (0, -0.1, 0.8, 10):
            with self.subTest(x=x), self.assertRaises(ValueError):
                candidates_from_summary(complete_summary(), x)

    def test_insufficient_bandwidth_chooses_bbr(self):
        rows = complete_summary()
        for row in rows:
            if row["direction"] == "upload" and row["variant"] == "bbr":
                row["p10_goodput_mbps"] = "1.3"
        result = candidates_from_summary(rows)
        self.assertEqual(0, result["proposed_agent_transport_bandwidth"]["up_mbps"])
        self.assertIn("stay on BBR", result["directional_evidence"]["upload"]["reason"])

    def test_no_user_evidence_never_produces_valid_recommendation(self):
        with tempfile.TemporaryDirectory() as dirname:
            output = Path(dirname) / "proposal.json"
            missing = Path(dirname) / "missing.csv"
            self.assertNotEqual(0, main(["--input", str(missing), "--output", str(output)]))
            self.assertFalse(output.exists())


if __name__ == "__main__":
    unittest.main()
