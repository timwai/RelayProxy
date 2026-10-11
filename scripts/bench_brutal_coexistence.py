#!/usr/bin/env python3
"""Operator-run, Linux, WAN coexistence measurement for Issue #198.

Two independent SOCKS/HTTP proxy downloads contend for a real bottleneck.
Periodic output-file deltas approximate application receive rate. ICMP echo
RTT to the server is measured independently during each trial. Do not run
this on CI loopback and describe it as a WAN measurement.
"""
import argparse
import csv
import json
import math
import os
from pathlib import Path
import re
import statistics
import subprocess
import sys
import tempfile
import time
from datetime import datetime, timezone

from bench_brutal_acceptance import parse_variant

RATE_COLUMNS = ["scenario", "loss_label", "round", "phase", "unix_ms",
                "bbr_received_bytes", "brutal_received_bytes",
                "bbr_interval_mbps", "brutal_interval_mbps",
                "rtt_icmp_ms", "rtt_success"]
SUMMARY_COLUMNS = ["scenario", "loss_label", "round", "duration_s",
                   "bbr_coactive_mbps", "brutal_coactive_mbps",
                   "jain_coactive_index", "idle_icmp_p50_ms",
                   "loaded_icmp_p50_ms", "loaded_icmp_p95_ms",
                   "idle_icmp_samples", "loaded_icmp_samples",
                   "bbr_bytes", "brutal_bytes", "valid", "error"]


def percentile(values, p):
    ordered = sorted(values)
    pos = (len(ordered) - 1) * p
    lower = math.floor(pos)
    upper = math.ceil(pos)
    return ordered[lower] + (ordered[upper] - ordered[lower]) * (pos - lower)


def parse_ping(output):
    match = re.search(r"(?:time|时间)[=<]([0-9]+(?:\.[0-9]+)?)\s*ms", output)
    return float(match.group(1)) if match else None


def ping_once(host, timeout=3):
    try:
        result = subprocess.run(["ping", "-n", "-c", "1", "-W", "1", host],
                                capture_output=True, text=True, timeout=timeout, check=False)
        if result.returncode:
            return None
        return parse_ping(result.stdout)
    except (OSError, subprocess.TimeoutExpired):
        return None


def tcp_proxy_command(proxy, url, file):
    # No curlrc, redirect following, proxy bypass, transfer compression or
    # shell interpolation. The URL must be a stable large, incompressible file.
    return ["curl", "--disable", "--noproxy", "", "--http1.1",
            "--fail", "--silent", "--show-error", "--max-time", "3600",
            "--proxy", proxy, "--dump-header", str(file) + ".headers",
            "--output", str(file), url]


def check_http_headers(path):
    try:
        text = Path(str(path) + ".headers").read_text(encoding="iso-8859-1")
    except OSError:
        return False
    codes = re.findall(r"(?m)^HTTP/\S+\s+([0-9]{3})", text)
    # SOCKS proxies typically emit a single response; HTTP CONNECT may
    # produce an additional 200 response before the origin status.
    return bool(codes and 200 <= int(codes[-1]) < 300)


def file_bytes(path):
    try:
        return path.stat().st_size
    except FileNotFoundError:
        return 0


def validate_rates(rows, min_samples=10):
    active = [r for r in rows if r["phase"] == "load"]
    valid = [r for r in active if float(r["bbr_interval_mbps"]) >= 0
             and float(r["brutal_interval_mbps"]) >= 0]
    if len(valid) < min_samples:
        return None, "insufficient simultaneous transfer measurements"
    # Include zero-progress intervals, since stalls must count as starvation.
    seconds = (int(valid[-1]["unix_ms"]) - int(valid[0]["unix_ms"])) / 1000
    if seconds <= 0:
        return None, "no sustained overlapping time span"
    if seconds < 55:
        return None, "insufficient sustained overlapping sampling span (<55s)"
    speeds = []
    for key in ("bbr_interval_mbps", "brutal_interval_mbps"):
        speeds.append(statistics.mean(float(r[key]) for r in valid))
    denom = 2 * sum(x*x for x in speeds)
    return (speeds[0], speeds[1], (sum(speeds)**2 / denom if denom else 0)), ""


def trial(args, number):
    rows = []
    idle = []
    loaded = []
    for _ in range(args.idle_pings):
        measure = ping_once(args.ping_host)
        if measure is not None:
            idle.append(measure)
        time.sleep(args.interval)
    if len(idle) < args.min_pings:
        return rows, {"round": number, "valid": False,
                      "error": f"insufficient idle RTT samples ({len(idle)})"}

    with tempfile.TemporaryDirectory(prefix="relay-wan-") as directory:
        paths = {name: Path(directory) / (name + ".bin") for name in ("bbr", "brutal")}
        processes = {}
        start_at = time.monotonic()
        previous_bytes = {"bbr": 0, "brutal": 0}
        previous_at = start_at
        premature = ""
        try:
            for name, url in (("bbr", args.bbr_proxy), ("brutal", args.brutal_proxy)):
                processes[name] = subprocess.Popen(
                    tcp_proxy_command(url, args.url, paths[name]),
                    stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
            # A scheduled interval, not the completion times of individual
            # transfers. Every tick records *both* receives.
            for tick in range(1, math.ceil(args.duration / args.interval) + 1):
                due = start_at + tick * args.interval
                time.sleep(max(0, due - time.monotonic()))
                now = time.monotonic()
                elapsed = now - previous_at
                current = {name: file_bytes(paths[name]) for name in processes}
                if any(p.poll() is not None for p in processes.values()):
                    premature = "at least one curl transfer completed/failed before full test duration"
                    break
                rtt = ping_once(args.ping_host)
                if rtt is not None:
                    loaded.append(rtt)
                rows.append({
                    "scenario": args.scenario, "loss_label": args.loss_label,
                    "round": number, "phase": "load",
                    "unix_ms": int(time.time() * 1000),
                    "bbr_received_bytes": current["bbr"],
                    "brutal_received_bytes": current["brutal"],
                    "bbr_interval_mbps": max(0, current["bbr"] - previous_bytes["bbr"]) * 8 / max(elapsed, 1e-6) / 1e6,
                    "brutal_interval_mbps": max(0, current["brutal"] - previous_bytes["brutal"]) * 8 / max(elapsed, 1e-6) / 1e6,
                    "rtt_icmp_ms": "" if rtt is None else rtt,
                    "rtt_success": rtt is not None,
                })
                previous_bytes, previous_at = current, now
            for process in processes.values():
                if process.poll() is None:
                    process.terminate()
            for name, process in processes.items():
                try:
                    _, stderr = process.communicate(timeout=4)
                except subprocess.TimeoutExpired:
                    process.kill()
                    _, stderr = process.communicate()
                # A SIGTERM at the configured stop point is expected. A
                # premature process exit is considered invalid regardless.
            vals, invalid_rate = validate_rates(rows)
            observed = {name: file_bytes(path) for name, path in paths.items()}
            errors = [x for x in (premature, invalid_rate) if x]
            if any(observed[name] < args.min_bytes for name in observed):
                errors.append("one flow transferred fewer than the minimum required bytes")
            if any(not check_http_headers(path) for path in paths.values()):
                errors.append("origin did not return HTTP 2xx for both flows")
            if len(loaded) < args.min_pings:
                errors.append(f"insufficient loaded RTT samples ({len(loaded)})")
            if time.monotonic() - start_at < args.duration * 0.95:
                errors.append("trial ended before requested full duration")
            rates = vals or (0, 0, 0)
            return rows, {
                "round": number, "duration_s": args.duration,
                "bbr_coactive_mbps": f"{rates[0]:.3f}",
                "brutal_coactive_mbps": f"{rates[1]:.3f}",
                "jain_coactive_index": f"{rates[2]:.4f}",
                "idle_icmp_p50_ms": f"{statistics.median(idle):.3f}",
                "loaded_icmp_p50_ms": f"{statistics.median(loaded):.3f}" if loaded else "",
                "loaded_icmp_p95_ms": f"{percentile(loaded,.95):.3f}" if loaded else "",
                "idle_icmp_samples": len(idle), "loaded_icmp_samples": len(loaded),
                "bbr_bytes": observed["bbr"], "brutal_bytes": observed["brutal"],
                "valid": not errors, "error": "; ".join(errors),
            }
        finally:
            for process in processes.values():
                if process.poll() is None:
                    process.kill()
                    process.wait()


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bbr-proxy", required=True)
    parser.add_argument("--brutal-proxy", required=True)
    parser.add_argument("--url", required=True)
    parser.add_argument("--ping-host", required=True, help="ICMP-reachable endpoint on the same physical bottleneck")
    parser.add_argument("--scenario", required=True)
    parser.add_argument("--loss-label", choices=("0", "1", "5"), required=True)
    parser.add_argument("--duration", type=int, default=60)
    parser.add_argument("--rounds", type=int, default=5)
    parser.add_argument("--interval", type=float, default=1.0)
    parser.add_argument("--idle-pings", type=int, default=8)
    parser.add_argument("--min-pings", type=int, default=5)
    parser.add_argument("--min-bytes", type=int, default=1_000_000)
    parser.add_argument("--output-dir", type=Path, required=True)
    args = parser.parse_args(argv)
    for name in ("bbr", "brutal"):
        value = getattr(args, name + "_proxy")
        try:
            actual, parsed = parse_variant(name + "=" + value)
            if actual != name:
                raise ValueError(name)
        except argparse.ArgumentTypeError as exc:
            parser.error(str(exc))
    if args.duration < 60 or args.rounds < 5 or not 0.25 <= args.interval <= 5:
        parser.error("require >=60s duration, >=5 rounds and 0.25-5s sample interval")
    if args.idle_pings < args.min_pings or args.min_pings < 3:
        parser.error("require >=3 ping samples in each phase")
    if not args.url.startswith(("https://", "http://")):
        parser.error("--url must be HTTP(S), with a large static uncompressed fixture")
    args.output_dir.mkdir(parents=True, exist_ok=True)
    summary = []
    invalid = False
    with (args.output_dir / "coexistence-timeseries.csv").open("w", newline="", encoding="utf-8") as data:
        writer = csv.DictWriter(data, fieldnames=RATE_COLUMNS)
        writer.writeheader()
        for round_number in range(1, args.rounds + 1):
            records, item = trial(args, round_number)
            writer.writerows(records)
            data.flush()
            summary.append(dict(item, scenario=args.scenario, loss_label=args.loss_label))
            invalid |= not item["valid"]
            print(f"coexistence round={round_number} valid={item['valid']} "
                  f"bbr={item.get('bbr_coactive_mbps')} brutal={item.get('brutal_coactive_mbps')} "
                  f"loaded_icmp_p95_ms={item.get('loaded_icmp_p95_ms')} error={item['error']}", flush=True)
    with (args.output_dir / "coexistence-summary.csv").open("w", newline="", encoding="utf-8") as report:
        writer = csv.DictWriter(report, fieldnames=SUMMARY_COLUMNS)
        writer.writeheader()
        writer.writerows(summary)
    (args.output_dir / "coexistence-metadata.json").write_text(json.dumps({
        "utc": datetime.now(timezone.utc).isoformat(), "scenario": args.scenario,
        "loss_label": args.loss_label, "duration_s": args.duration,
        "interval_s": args.interval, "rounds": args.rounds,
        "ping_host": args.ping_host, "url": args.url,
        "method": "two competing curl HTTP downloads and independent ICMP RTT",
        "warning": "operator-declared loss; no automatic impairment, no WAN proof unless deployed on actual WAN",
    }, indent=2) + "\n")
    return 1 if invalid else 0


if __name__ == "__main__":
    sys.exit(main())
