"""Offline API fixture tests. Passing these does not certify physical devices."""
from __future__ import annotations

import argparse
from hashlib import sha256
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import json
from pathlib import Path
import tempfile
import threading
import unittest
from unittest import mock

SCRIPT = Path(__file__).with_name("fileshare-real-device-smoke.py")
SPEC = importlib.util.spec_from_file_location("file_smoke", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
smoke = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(smoke)


class SmokeScriptTests(unittest.TestCase):
    def test_local_web_token_cannot_be_sent_over_remote_cleartext(self):
        self.assertEqual(smoke.validate_base_url("http://127.0.0.1:21290"), "http://127.0.0.1:21290")
        self.assertEqual(smoke.validate_base_url("https://agent.example:8443"), "https://agent.example:8443")
        for forbidden in ("http://example.com", "http://192.168.0.3:21290",
                          "http://user:pass@127.0.0.1:21290", "https://agent.example/api",
                          "https://agent.example/?token=secret"):
            with self.subTest(url=forbidden), self.assertRaises(ValueError):
                smoke.validate_base_url(forbidden)

    def test_download_hash_rejects_symlink_and_escape(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            root = home / "Downloads" / "RelayProxy"
            root.mkdir(parents=True)
            good = root / "good.bin"
            good.write_bytes(b"sha256-fixture")
            outside = home / "outside.bin"
            outside.write_bytes(b"private")
            symlink = root / "unsafe.bin"
            symlink.symlink_to(outside)
            with mock.patch.object(Path, "home", return_value=home):
                self.assertEqual(smoke.hash_published_file(str(good)),
                                 (sha256(b"sha256-fixture").hexdigest(), len(b"sha256-fixture")))
                with self.assertRaises(RuntimeError):
                    smoke.hash_published_file(str(symlink))
                with self.assertRaises(RuntimeError):
                    smoke.hash_published_file(str(outside))

    def test_authentic_api_flow_with_loopback_fixture(self):
        payload = b"a locally published test file"
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            root = home / "Downloads" / "RelayProxy"
            root.mkdir(parents=True)
            published = root / "file-task123.bin"
            published.write_bytes(payload)

            class Handler(BaseHTTPRequestHandler):
                def do_GET(self):
                    if self.path == "/api/file-shares/peers":
                        response = [{"deviceId": "host"}]
                    elif self.path.startswith("/api/file-shares/remote?"):
                        response = [{"id": "share"}]
                    elif self.path.startswith("/api/file-shares/entries?"):
                        response = [{"name": "file.bin", "isDir": False, "size": len(payload)}]
                    elif self.path == "/api/file-transfers":
                        response = [{"id": "task123", "state": "completed",
                                     "direction": "download", "done": len(payload),
                                     "total": len(payload),
                                     "transportPath": "direct_quic", "destination": str(published)}]
                    else:
                        self.send_error(404)
                        return
                    self.respond(response)

                def do_POST(self):
                    if self.path != "/api/file-transfers":
                        self.send_error(404)
                        return
                    length = int(self.headers.get("Content-Length", "0"))
                    body = json.loads(self.rfile.read(length))
                    if body != {"deviceId": "host", "shareId": "share", "path": "file.bin"}:
                        self.send_error(400)
                        return
                    self.respond({"id": "task123"})

                def respond(self, value):
                    output = json.dumps(value).encode("utf-8")
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", str(len(output)))
                    self.end_headers()
                    self.wfile.write(output)

                def log_message(self, format, *args):
                    pass

            server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            try:
                args = argparse.Namespace(agent_url="http://127.0.0.1:%d" % server.server_port,
                                          host_device_id="host", share_id="share",
                                          remote_file="file.bin", expected_sha256=sha256(payload).hexdigest(),
                                          require_path="direct_quic", pause_after_bytes=0,
                                          timeout=5, poll_interval=0.05)
                with mock.patch.object(Path, "home", return_value=home):
                    result = smoke.run(args)
                self.assertEqual(result["status"], "passed")
                self.assertEqual(result["transport_path"], "direct_quic")
                self.assertEqual(result["bytes"], len(payload))
            finally:
                server.shutdown()
                server.server_close()
                thread.join(timeout=2)


if __name__ == "__main__":
    unittest.main()
