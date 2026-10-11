"""Fast tests for real-WAN coexistence result integrity."""
import unittest
from bench_brutal_coexistence import parse_ping, validate_rates, tcp_proxy_command


class CoexistenceTests(unittest.TestCase):
    def test_ping_parsing(self):
        self.assertEqual(21.42, parse_ping("64 bytes: icmp_seq=1 ttl=55 time=21.42 ms"))
        self.assertIsNone(parse_ping("Request timeout for icmp_seq 1"))

    def test_overlap_rates_include_stalls(self):
        rows = []
        for idx in range(60):
            rows.append({
                "phase": "load", "unix_ms": str(1000 + idx * 1000),
                "bbr_interval_mbps": 0 if idx % 2 else 4,
                "brutal_interval_mbps": 9,
            })
        measures, error = validate_rates(rows)
        self.assertFalse(error)
        self.assertEqual(2, measures[0])
        self.assertEqual(9, measures[1])
        self.assertLess(measures[2], 0.75)

    def test_short_overlap_rejected_even_with_many_samples(self):
        rows = [{"phase": "load", "unix_ms": str(i * 1000),
                 "bbr_interval_mbps": 4, "brutal_interval_mbps": 4}
                for i in range(20)]
        rates, reason = validate_rates(rows)
        self.assertIsNone(rates)
        self.assertIn("sampling span", reason)

    def test_incomplete_overlap_rejected(self):
        self.assertIsNone(validate_rates([{
            "phase": "load", "unix_ms": "5000",
            "bbr_interval_mbps": 1, "brutal_interval_mbps": 3
        }])[0])

    def test_curl_forces_proxy_and_disables_redirects(self):
        command = tcp_proxy_command("socks5h://127.0.0.1:1080", "https://example.test/huge", "/tmp/transfer")
        self.assertEqual("", command[command.index("--noproxy") + 1])
        self.assertNotIn("--location", command)
        self.assertIn("--disable", command)


if __name__ == "__main__":
    unittest.main()
