#!/usr/bin/env python3
"""Estimate co-active throughput from per-250ms kernel netem QUIC logs.

Jain index and rates cover ONLY the overlapping observed intervals, not
completion-time rates. RTT fields are samples of QUIC smoothed sender RTT,
not one-way or end-to-end p50/p95 latency. Lab results cannot certify WAN.
"""
import argparse
import csv
import math
import re
import statistics
import sys
from collections import defaultdict
from pathlib import Path

PATTERN = re.compile(
    r"overlap_sample scenario=(\S+) mode=(bbr|brutal) "
    r"wall_unix_ms=(\d+) interval_goodput_mbps=([0-9.]+) "
    r"sender_smoothed_rtt_ms=([0-9.]+) received_bytes=(\d+)"
)
FIELDS = [
    "loss_label", "scenario", "overlap_seconds", "overlap_intervals",
    "bbr_overlap_mbps", "brutal_overlap_mbps", "jain_coactive_rate_index",
    "bbr_quic_smoothed_rtt_sample_p50_ms", "bbr_quic_smoothed_rtt_sample_p95_ms",
    "brutal_quic_smoothed_rtt_sample_p50_ms",
    "brutal_quic_smoothed_rtt_sample_p95_ms",
]


def percentile(values, ratio):
    values = sorted(values)
    position = (len(values) - 1) * ratio
    low = math.floor(position)
    high = math.ceil(position)
    return values[low] + (values[high] - values[low]) * (position - low)


def extract_intervals(lines):
    points = defaultdict(lambda: defaultdict(list))
    for line in lines:
        match = PATTERN.search(line)
        if match is None:
            continue
        scenario, mode, timestamp, speed, rtt, received = match.groups()
        sample = (int(timestamp), float(speed), float(rtt), int(received))
        if not (math.isfinite(sample[1]) and math.isfinite(sample[2])
                and sample[1] >= 0 and sample[2] >= 0):
            raise ValueError("invalid non-finite rate/RTT sample")
        points[scenario][mode].append(sample)
    intervals = defaultdict(dict)
    for scenario, modes in points.items():
        for mode, samples in modes.items():
            samples.sort(key=lambda entry: entry[0])
            result = []
            for previous, current in zip(samples, samples[1:]):
                if current[0] <= previous[0]:
                    raise ValueError(f"{scenario}/{mode}: non-monotonic telemetry timestamps")
                if current[3] < previous[3]:
                    raise ValueError(f"{scenario}/{mode}: receiver byte counter went backwards")
                result.append((previous[0], current[0], current[1], current[2]))
            intervals[scenario][mode] = result
    return intervals


def analyze(lines, loss_label, minimum_overlap_ms=750):
    problems = []
    summary = []
    groups = extract_intervals(lines)
    if not groups:
        problems.append("no overlap_sample telemetry found")
    for scenario in sorted(groups):
        modes = groups[scenario]
        a, b = modes.get("bbr", []), modes.get("brutal", [])
        if not a or not b:
            problems.append(f"{scenario}: missing BBR or Brutal time-series intervals")
            continue
        i = j = 0
        overlap_ms = 0
        events = 0
        bits = [0.0, 0.0]
        rtts = [[], []]
        while i < len(a) and j < len(b):
            start = max(a[i][0], b[j][0])
            end = min(a[i][1], b[j][1])
            if end > start:
                seconds = (end - start) / 1000
                overlap_ms += end - start
                events += 1
                bits[0] += a[i][2] * seconds
                bits[1] += b[j][2] * seconds
                rtts[0].append(a[i][3])
                rtts[1].append(b[j][3])
            if a[i][1] <= b[j][1]:
                i += 1
            else:
                j += 1
        if overlap_ms < minimum_overlap_ms:
            problems.append(f"{scenario}: only {overlap_ms}ms co-active overlap, need {minimum_overlap_ms}ms")
            continue
        duration = overlap_ms / 1000
        rates = [n / duration for n in bits]
        denominator = 2 * sum(x * x for x in rates)
        jain = sum(rates) ** 2 / denominator if denominator else 0
        summary.append(dict(zip(FIELDS, [
            loss_label, scenario, f"{duration:.3f}", events,
            f"{rates[0]:.4f}", f"{rates[1]:.4f}", f"{jain:.4f}",
            f"{percentile(rtts[0], 0.5):.3f}", f"{percentile(rtts[0], 0.95):.3f}",
            f"{percentile(rtts[1], 0.5):.3f}", f"{percentile(rtts[1], 0.95):.3f}",
        ])))
    return summary, problems


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--loss-label", required=True, choices=["0", "1", "5"])
    parser.add_argument("--min-overlap-ms", type=int, default=750)
    args = parser.parse_args(argv)
    if args.min_overlap_ms < 250:
        parser.error("--min-overlap-ms must be >= 250")
    try:
        with args.input.open(encoding="utf-8") as stream:
            rows, problems = analyze(stream, args.loss_label, args.min_overlap_ms)
    except (OSError, UnicodeError, ValueError) as exc:
        print(f"invalid netem log: {exc}", file=sys.stderr)
        return 1
    args.output.parent.mkdir(parents=True, exist_ok=True)
    with args.output.open("w", encoding="utf-8", newline="") as out:
        writer = csv.DictWriter(out, fieldnames=FIELDS)
        writer.writeheader()
        writer.writerows(rows)
    for row in rows:
        print(f"coactive scenario={row['scenario']} loss_configured={row['loss_label']}% "
              f"seconds={row['overlap_seconds']} bbr_mbps={row['bbr_overlap_mbps']} "
              f"brutal_mbps={row['brutal_overlap_mbps']} jain={row['jain_coactive_rate_index']} "
              f"(lab estimate, NOT WAN fairness)")
    for problem in problems:
        print(f"INVALID OVERLAP: {problem}", file=sys.stderr)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
