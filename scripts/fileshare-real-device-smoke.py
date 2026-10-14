#!/usr/bin/env python3
"""Real-device RelayProxy file-sharing smoke test.

Run on the CLIENT Agent's machine while both Agents are connected to the same
real Relay Server. The host exposes a pre-created, approved shared file.
The expected digest must be computed independently on the HOST machine.
This script never sends the expected digest or the local Web token to Relay.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import stat
import sys
import time
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode, urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener

PATHS = ("p2p_quic", "direct_quic", "direct_tls", "relay_quic", "relay_tls")


class RejectRedirect(HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, msg, headers, newurl):
        raise RuntimeError("refusing management API redirect")


def validate_base_url(raw: str) -> str:
    result = urlsplit(raw.rstrip("/"))
    if result.scheme not in ("http", "https") or not result.hostname:
        raise ValueError("management URL must be an absolute HTTP(S) URL")
    if result.username or result.password or result.path not in ("", "/") or result.query or result.fragment:
        raise ValueError("management URL must not contain credentials, paths or query strings")
    if result.scheme == "http" and result.hostname.lower() not in ("127.0.0.1", "::1", "localhost"):
        raise ValueError("refusing to send management Web token over remote cleartext HTTP")
    return raw.rstrip("/")


class AgentAPI:
    def __init__(self, base: str, token: str):
        self.base = validate_base_url(base)
        self.opener = build_opener(RejectRedirect())
        self.token = token

    def call(self, method: str, path: str, payload: dict | None = None, query: dict | None = None):
        url = self.base + path
        if query:
            url += "?" + urlencode(query)
        headers = {"Accept": "application/json"}
        if self.token:
            headers["Authorization"] = "Bearer " + self.token
        body = None
        if payload is not None:
            body = json.dumps(payload, separators=(",", ":")).encode("utf-8")
            headers["Content-Type"] = "application/json"
        request = Request(url, data=body, method=method, headers=headers)
        try:
            with self.opener.open(request, timeout=15) as response:
                return json.load(response)
        except HTTPError as exc:
            message = exc.read(300).decode("utf-8", "replace").strip()
            raise RuntimeError("Agent management API returned HTTP %d: %s" % (exc.code, message)) from exc
        except URLError as exc:
            raise RuntimeError("Agent management API is unreachable: %s" % exc.reason) from exc


def hash_published_file(raw: str) -> tuple[str, int]:
    root = (Path.home() / "Downloads" / "RelayProxy").resolve()
    target = Path(raw)
    if not target.is_absolute():
        raise RuntimeError("Agent returned a non-absolute download destination")
    metadata = target.lstat()
    if not stat.S_ISREG(metadata.st_mode):
        raise RuntimeError("download destination is not a regular file")
    resolved = target.resolve(strict=True)
    if not resolved.is_relative_to(root):
        raise RuntimeError("download destination escaped the dedicated RelayProxy folder")
    digest = hashlib.sha256()
    size = 0
    with resolved.open("rb") as file:
        while True:
            chunk = file.read(1024 * 1024)
            if not chunk:
                break
            digest.update(chunk)
            size += len(chunk)
    return digest.hexdigest(), size


def run(args: argparse.Namespace) -> dict:
    started = time.monotonic()
    api = AgentAPI(args.agent_url, os.getenv("RELAYPROXY_WEB_TOKEN", ""))
    if args.expected_sha256.lower() != args.expected_sha256 or len(args.expected_sha256) != 64:
        raise ValueError("expected SHA-256 must contain 64 lowercase hex digits")
    try:
        bytes.fromhex(args.expected_sha256)
    except ValueError as exc:
        raise ValueError("invalid expected SHA-256 digest") from exc

    peers = api.call("GET", "/api/file-shares/peers")
    if not any(item.get("deviceId") == args.host_device_id for item in peers):
        raise RuntimeError("host is not online or current files.client/files.host approval is missing")
    shares = api.call("GET", "/api/file-shares/remote", query={"deviceId": args.host_device_id})
    if not any(item.get("id") == args.share_id for item in shares):
        raise RuntimeError("the host did not expose the expected share to this device")
    relative_path = args.remote_file.strip("/")
    if not relative_path or relative_path in (".", "..") or "\\" in relative_path or ".." in relative_path.split("/"):
        raise ValueError("remote file must be a safe share-relative path")
    directory, _, filename = relative_path.rpartition("/")
    entries = api.call("GET", "/api/file-shares/entries", query={
        "deviceId": args.host_device_id, "shareId": args.share_id, "path": directory or ".",
    })
    entry = next((x for x in entries if x.get("name") == filename and not x.get("isDir")), None)
    if entry is None:
        raise RuntimeError("remote file was not found under this approved share")
    expected_size = int(entry["size"])
    if expected_size <= 0 and args.pause_after_bytes:
        raise ValueError("pause/resume requires a nonempty remote file")

    task = api.call("POST", "/api/file-transfers", {
        "deviceId": args.host_device_id, "shareId": args.share_id, "path": relative_path,
    })
    task_id = task.get("id")
    if not isinstance(task_id, str) or not task_id:
        raise RuntimeError("Agent did not create a valid transfer task")
    print("传输任务: %s，远端大小: %d bytes" % (task_id, expected_size), flush=True)
    deadline = time.monotonic() + args.timeout
    last_progress = 0.0
    paused_once = False
    resume_requested = False
    while time.monotonic() < deadline:
        tasks = api.call("GET", "/api/file-transfers")
        task = next((x for x in tasks if x.get("id") == task_id), None)
        if task is None:
            raise RuntimeError("transfer task disappeared before completion")
        state = task.get("state", "")
        done = int(task.get("done", 0))
        speed = float(task.get("speed", 0))
        now = time.monotonic()
        if now - last_progress >= 1:
            print("状态=%s 已传输=%d/%d 速率=%.1f KiB/s 路径=%s" %
                  (state, done, expected_size, speed / 1024, task.get("transportPath", "pending")), flush=True)
            last_progress = now

        if args.pause_after_bytes and not paused_once and state == "running" and done >= args.pause_after_bytes:
            api.call("POST", "/api/file-transfers/%s/cancel" % task_id)
            paused_once = True
        if state == "paused" and paused_once and not resume_requested:
            api.call("POST", "/api/file-transfers/%s/resume" % task_id)
            resume_requested = True
        if state == "failed":
            raise RuntimeError("transfer failed: %s" % task.get("error", "unknown"))
        if state == "cancelled":
            raise RuntimeError("transfer was cancelled unexpectedly")
        if state == "completed":
            if args.pause_after_bytes and not resume_requested:
                raise RuntimeError("file finished before controlled pause/resume was verified")
            path = task.get("transportPath", "")
            if path not in PATHS:
                raise RuntimeError("completed transfer lacks a recognized actual transport path")
            if args.require_path != "any" and path != args.require_path:
                raise RuntimeError("unexpected transfer path: got %s, required %s" % (path, args.require_path))
            digest, size = hash_published_file(task.get("destination", ""))
            if digest != args.expected_sha256:
                raise RuntimeError("real-device SHA-256 mismatch: got %s, expected %s" %
                                   (digest, args.expected_sha256))
            if size != expected_size or int(task.get("done", -1)) != size:
                raise RuntimeError("download size/progress does not match the host's directory listing")
            return {
                "status": "passed",
                "task_id": task_id,
                "host_device_id": args.host_device_id,
                "share_id": args.share_id,
                "remote_file": relative_path,
                "bytes": size,
                "sha256": digest,
                "transport_path": path,
                "pause_resume_verified": resume_requested,
                "elapsed_seconds": round(time.monotonic() - started, 3),
                "measured_mib_per_second": round(size / max(0.001, time.monotonic() - started) / (1024 * 1024), 3),
            }
        time.sleep(args.poll_interval)
    raise TimeoutError("file transfer did not finish within %.1f seconds" % args.timeout)


def main() -> int:
    parser = argparse.ArgumentParser(description="Verify file sharing between two actual RelayProxy devices.")
    parser.add_argument("--agent-url", default="http://127.0.0.1:21290",
                        help="Client Agent's local Web API base URL")
    parser.add_argument("--host-device-id", required=True)
    parser.add_argument("--share-id", required=True)
    parser.add_argument("--remote-file", required=True, help="Share-relative file path")
    parser.add_argument("--expected-sha256", required=True,
                        help="Compute independently on the HOST Agent's disk")
    parser.add_argument("--require-path", choices=("any",) + PATHS, default="any")
    parser.add_argument("--pause-after-bytes", type=int, default=0,
                        help="Prove actual pause/resume on a sufficiently large file")
    parser.add_argument("--timeout", type=float, default=240)
    parser.add_argument("--poll-interval", type=float, default=0.25)
    parser.add_argument("--report", help="Optional JSON result path; token is never recorded")
    args = parser.parse_args()
    if args.timeout <= 0 or args.poll_interval <= 0 or args.pause_after_bytes < 0:
        parser.error("timeout/poll-interval must be positive and pause-after-bytes cannot be negative")
    result = None
    try:
        result = run(args)
        print(json.dumps(result, ensure_ascii=False, indent=2))
        return 0
    except (RuntimeError, ValueError, TimeoutError, OSError, TypeError, KeyError) as exc:
        result = {"status": "failed", "reason": str(exc)}
        print("实网验收失败: %s" % exc, file=sys.stderr)
        return 1
    finally:
        if args.report and result is not None:
            output = Path(args.report)
            output.parent.mkdir(parents=True, exist_ok=True)
            output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    sys.exit(main())
