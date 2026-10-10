#!/usr/bin/env python3
"""Collect repeatable proxy goodput CSV for RelayProxy Issue #198.

This samples transfers only. Loss must be configured on the real test link;
curl connect/first-byte timings are NOT network RTT measurements.
"""
import argparse
import csv
import hashlib
import random
import subprocess
import sys
import tempfile
from datetime import datetime, timezone
from pathlib import Path
from urllib.parse import urlsplit

FIELDS = [
    "timestamp_utc", "scenario", "loss_label", "direction", "round", "variant",
    "proxy", "url", "success", "http_code", "elapsed_s", "bytes",
    "goodput_mbps", "curl_speed_mbps", "curl_connect_s", "curl_ttfb_s",
    "checksum_ok", "error",
]


def parse_variant(value):
    if "=" not in value:
        raise argparse.ArgumentTypeError("expected variant=socks5h://host:port")
    name, url = value.split("=", 1)
    parsed = urlsplit(url)
    if (not name or parsed.scheme not in {"socks5h", "socks5", "http", "https"}
            or not parsed.hostname or not parsed.port):
        raise argparse.ArgumentTypeError("proxy must use socks5h, socks5, http or https with host:port")
    return name, url


def sample(args, proxy, destination):
    command = [
        "curl", "--disable", "--noproxy", "", "--fail", "--location", "--silent", "--show-error",
        "--connect-timeout", "15", "--max-time", str(args.timeout),
        "--proxy", proxy, "--output", str(destination),
        "--write-out",
        "%{http_code}\t%{time_total}\t%{size_download}\t%{size_upload}"
        "\t%{speed_download}\t%{speed_upload}\t%{time_connect}\t%{time_starttransfer}",
    ]
    if args.direction == "upload":
        command += ["--upload-file", str(args.upload_file)]
    command.append(args.url)
    try:
        result = subprocess.run(command, capture_output=True, text=True,
                                timeout=args.timeout + 15, check=False)
    except (OSError, subprocess.TimeoutExpired) as exc:
        return {"success": False, "error": str(exc)}
    if result.returncode != 0:
        return {"success": False, "error": (result.stderr or f"curl exit {result.returncode}").strip()[:500]}
    try:
        code, elapsed, downloaded, uploaded, downrate, uprate, connected, ttfb = result.stdout.strip().split("\t")
        elapsed = float(elapsed)
        size = float(uploaded if args.direction == "upload" else downloaded)
        speed = float(uprate if args.direction == "upload" else downrate)
        if elapsed <= 0 or size <= 0:
            raise ValueError("empty transfer or zero elapsed time")
        checksum_ok = ""
        if args.expected_sha256:
            digest = hashlib.sha256()
            with destination.open("rb") as source:
                for chunk in iter(lambda: source.read(1 << 20), b""):
                    digest.update(chunk)
            checksum_ok = str(digest.hexdigest().lower() == args.expected_sha256.lower())
            if checksum_ok != "True":
                raise ValueError("download SHA256 mismatch")
        return {
            "success": True, "http_code": code,
            "elapsed_s": f"{elapsed:.6f}", "bytes": int(size),
            "goodput_mbps": f"{size * 8 / elapsed / 1e6:.5f}",
            "curl_speed_mbps": f"{speed * 8 / 1e6:.5f}",
            "curl_connect_s": connected, "curl_ttfb_s": ttfb,
            "checksum_ok": checksum_ok,
        }
    except (ValueError, TypeError) as exc:
        return {"success": False, "error": str(exc)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--variant", action="append", type=parse_variant, required=True,
                        help="repeat for bbr=, brutal=, hysteria2= with their own proxy URLs")
    parser.add_argument("--url", required=True, help="same static origin for all variants")
    parser.add_argument("--scenario", required=True, help="identifier for test environment")
    parser.add_argument("--loss-label", required=True, choices=["0", "1", "5"],
                        help="DECLARED loss percent; configure externally")
    parser.add_argument("--direction", choices=["download", "upload"], default="download")
    parser.add_argument("--upload-file", type=Path, help="for PUT upload tests")
    parser.add_argument("--expected-sha256", help="expected download fixture SHA256")
    parser.add_argument("--runs", type=int, default=5)
    parser.add_argument("--timeout", type=int, default=120)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()

    variants = dict(args.variant)
    if len(args.variant) != 3 or set(variants) != {"bbr", "brutal", "hysteria2"}:
        parser.error("specify exactly one of each: bbr, brutal, hysteria2")
    if args.runs < 1 or args.timeout < 5:
        parser.error("runs must be >= 1 and timeout >= 5 seconds")
    if args.direction == "upload" and (not args.upload_file or not args.upload_file.is_file()):
        parser.error("upload requires --upload-file and an HTTP server accepting PUT")
    if args.direction == "upload" and args.expected_sha256:
        parser.error("checksum is only available for downloads")
    if urlsplit(args.url).scheme not in {"http", "https"}:
        parser.error("--url must be an HTTP(S) URL")

    args.output.parent.mkdir(parents=True, exist_ok=True)
    new_file = not args.output.exists() or args.output.stat().st_size == 0
    failures = 0
    with args.output.open("a", encoding="utf-8", newline="") as output:
        writer = csv.DictWriter(output, fieldnames=FIELDS)
        if new_file:
            writer.writeheader()
        for round_number in range(1, args.runs + 1):
            order = list(variants)
            random.Random(round_number).shuffle(order)
            for name in order:
                with tempfile.TemporaryDirectory(prefix="brutal-bench-") as directory:
                    result = sample(args, variants[name], Path(directory) / "transfer")
                row = {
                    "timestamp_utc": datetime.now(timezone.utc).isoformat(),
                    "scenario": args.scenario, "loss_label": args.loss_label,
                    "direction": args.direction, "round": round_number,
                    "variant": name, "proxy": variants[name],
                    "url": args.url, "success": result.get("success", False),
                }
                row.update(result)
                writer.writerow(row)
                output.flush()
                failures += not result.get("success", False)
                print(f"round={round_number} variant={name} success={row['success']} "
                      f"goodput_mbps={row.get('goodput_mbps', '-')} error={row.get('error', '')}")
    if failures:
        print(f"{failures} transfers failed: see {args.output}", file=sys.stderr)
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
