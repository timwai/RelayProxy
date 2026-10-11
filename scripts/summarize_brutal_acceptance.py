#!/usr/bin/env python3
"""Summarize Issue #198 proxy throughput CSV and reject incomplete A/B runs.

Statistics are of HTTP(S) *application goodput* as reported by curl.
These checks do NOT verify a real 0/1/5% network loss, RTT, QUIC loss,
CPU, or BBR coexistence fairness. They are NOT release approval.
"""
import argparse
import csv
import math
import statistics
import sys
from collections import defaultdict
from pathlib import Path

VARIANTS = {"bbr", "brutal", "hysteria2"}
REQUIRED_COLUMNS = {
    "scenario", "loss_label", "direction", "variant", "round", "success",
    "goodput_mbps", "bytes", "elapsed_s", "checksum_ok",
}
SUMMARY_FIELDS = [
    "scenario", "loss_label", "direction", "variant", "runs",
    "median_goodput_mbps", "p10_goodput_mbps", "p90_goodput_mbps",
    "relative_to_hysteria2_pct",
]


def percentile(values, fraction):
    ordered = sorted(values)
    pos = (len(ordered) - 1) * fraction
    low = math.floor(pos)
    high = math.ceil(pos)
    return ordered[low] + (ordered[high] - ordered[low]) * (pos - low)


def summarize(file_path, min_runs=5):
    problems = []
    groups = defaultdict(lambda: defaultdict(dict))
    with Path(file_path).open(newline="", encoding="utf-8") as source:
        reader = csv.DictReader(source)
        missing = REQUIRED_COLUMNS - set(reader.fieldnames or [])
        if missing:
            return [], [f"CSV missing columns: {sorted(missing)}"]
        for number, row in enumerate(reader, 2):
            prefix = f"line {number}"
            try:
                key = (row["scenario"], row["loss_label"], row["direction"])
                name = row["variant"]
                round_number = int(row["round"])
                if not key[0] or key[1] not in {"0", "1", "5"} or key[2] not in {"download", "upload"}:
                    raise ValueError("unknown scenario, loss condition, or direction")
                if name not in VARIANTS:
                    raise ValueError(f"unknown variant {name!r}")
                if round_number < 1:
                    raise ValueError("round must be positive")
                if round_number in groups[key][name]:
                    raise ValueError(f"duplicate scenario/loss/direction/variant/round {key}/{name}/{round_number}")
                if row["success"].strip().lower() not in {"true", "1"}:
                    raise ValueError(f"failed transfer: {row.get('error', 'unknown error')}")
                if row["checksum_ok"].strip().lower() == "false":
                    raise ValueError("download checksum failed")
                goodput = float(row["goodput_mbps"])
                sent_bytes = float(row["bytes"])
                elapsed = float(row["elapsed_s"])
                if not all(math.isfinite(x) and x > 0 for x in (goodput, sent_bytes, elapsed)):
                    raise ValueError("nonpositive or invalid transfer metrics")
                groups[key][name][round_number] = goodput
            except (ValueError, TypeError, KeyError) as exc:
                problems.append(f"{prefix}: {exc}")

    summary = []
    if not groups:
        problems.append("no successful transfers")
    for key, variants in sorted(groups.items()):
        absent = VARIANTS - set(variants)
        if absent:
            problems.append(f"{key}: missing variants {sorted(absent)}")
        rounds = {variant: set(values) for variant, values in variants.items()}
        if len({frozenset(v) for v in rounds.values()}) > 1:
            problems.append(f"{key}: variants have nonmatching round numbers")
        baseline = variants.get("hysteria2", {})
        baseline_median = statistics.median(baseline.values()) if baseline else None
        for variant, values in sorted(variants.items()):
            if len(values) < min_runs:
                problems.append(f"{key}/{variant}: {len(values)} successful runs, require >= {min_runs}")
            speeds = list(values.values())
            median = statistics.median(speeds)
            summary.append(dict(zip(SUMMARY_FIELDS, [
                *key, variant, len(speeds),
                f"{median:.4f}", f"{percentile(speeds, .1):.4f}",
                f"{percentile(speeds, .9):.4f}",
                f"{(median / baseline_median - 1) * 100:.2f}" if baseline_median else "",
            ])))
    return summary, problems


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", type=Path, required=True, help="sampler raw CSV")
    parser.add_argument("--output", type=Path, help="optional summary CSV")
    parser.add_argument("--min-runs", type=int, default=5)
    args = parser.parse_args(argv)
    if args.min_runs < 1:
        parser.error("--min-runs must be positive")
    try:
        summary, problems = summarize(args.input, args.min_runs)
    except (OSError, UnicodeError) as exc:
        print(f"cannot read sampler CSV: {exc}", file=sys.stderr)
        return 1
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        with args.output.open("w", newline="", encoding="utf-8") as target:
            writer = csv.DictWriter(target, fieldnames=SUMMARY_FIELDS)
            writer.writeheader()
            writer.writerows(summary)
    for entry in summary:
        print(f"{entry['scenario']} loss={entry['loss_label']}% direction={entry['direction']} "
              f"variant={entry['variant']} n={entry['runs']} median_Mbps={entry['median_goodput_mbps']} "
              f"p10/p90={entry['p10_goodput_mbps']}/{entry['p90_goodput_mbps']} "
              f"vs_hysteria2={entry['relative_to_hysteria2_pct']}%")
    for problem in problems:
        print(f"INVALID A/B DATA: {problem}", file=sys.stderr)
    if not problems:
        print("A/B sampler CSV is structurally complete. This is NOT WAN performance acceptance.")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
