#!/usr/bin/env python3
"""Conservative, evidence-based directional Brutal *candidate* rates.

Only reads already collected Issue #198 three-way WAN A/B CSV. BBR's
lower-tail throughput is NOT the available link capacity and MUST NOT be
used to auto-enable Brutal without site-specific approval and fairness
validation. The output is a planning artifact, never a live configuration.
"""
import argparse
import json
import math
from pathlib import Path
import sys

from summarize_brutal_acceptance import summarize

LOSSES = ("0", "1", "5")
DIRECTIONS = ("upload", "download")


def candidates_from_summary(summary, fraction=0.60, scenario=None):
    if not 0 < fraction <= 0.75:
        raise ValueError("headroom factor must be greater than zero and at most 0.75")
    if not summary:
        raise ValueError("empty WAN A/B summary")
    scenarios = {row["scenario"] for row in summary}
    if scenario is None:
        if len(scenarios) != 1:
            raise ValueError("select one scenario; never mix different WAN paths")
        scenario = next(iter(scenarios))
    if scenario not in scenarios:
        raise ValueError("requested WAN scenario not present")
    rows = [row for row in summary if row["scenario"] == scenario]
    expected = {(loss, direction, variant)
                for loss in LOSSES for direction in DIRECTIONS
                for variant in ("bbr", "brutal", "hysteria2")}
    provided = {(row["loss_label"], row["direction"], row["variant"]) for row in rows}
    if expected - provided:
        raise ValueError(f"missing verified BBR/Brutal/Hysteria2 A/B conditions: {sorted(expected - provided)}")

    rates = {}
    for direction in DIRECTIONS:
        bbr_tail = []
        for loss in LOSSES:
            group = [row for row in rows if row["loss_label"] == loss
                     and row["direction"] == direction and row["variant"] == "bbr"]
            if len(group) != 1 or int(group[0]["runs"]) < 5:
                raise ValueError(f"missing five-run BBR evidence for {direction}/{loss}%")
            p10 = float(group[0]["p10_goodput_mbps"])
            if not math.isfinite(p10) or p10 <= 0:
                raise ValueError("invalid lower-tail BBR throughput")
            bbr_tail.append(p10)
        conservative = fraction * min(bbr_tail)
        # The user-facing config requires whole Mbps. Rounding down rather
        # than up ensures the estimate never exceeds its own headroom policy.
        rounded = math.floor(conservative)
        rates[direction] = {
            "candidate_mbps": max(0, rounded),
            "bbr_worst_loss_p10_goodput_mbps": round(min(bbr_tail), 4),
            "headroom_fraction": fraction,
            "usable_for_default_on": False,
            "reason": ("below 1 Mbps; stay on BBR" if rounded < 1 else
                       "candidate only; actual link capacity, other tenants, "
                       "loaded RTT and fairness still require approval"),
        }
    return {
        "scenario": scenario,
        "proposed_agent_transport_bandwidth": {
            "up_mbps": rates["upload"]["candidate_mbps"],
            "down_mbps": rates["download"]["candidate_mbps"],
        },
        "directional_evidence": rates,
        "auto_apply_allowed": False,
        "brutal_default_eligible": False,
        "warnings": [
            "BBR observed application goodput is not measured physical path capacity",
            "Insufficient data on competing BBR flows or other traffic",
            "Must run sustained coactive fairness and loaded RTT acceptance",
            "Do not write these values to production Agent or Server configuration automatically",
        ],
    }


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--input", type=Path, required=True, help="original three-way WAN A/B CSV")
    p.add_argument("--output", type=Path, required=True, help="candidate JSON, never a runtime config")
    p.add_argument("--scenario", help="physical WAN experiment ID (required for multi-site CSV)")
    p.add_argument("--headroom", type=float, default=0.60,
                   help="max share of worst-case lower-tail BBR throughput, default 0.60")
    args = p.parse_args(argv)
    try:
        summary, problems = summarize(args.input, min_runs=5)
        if problems:
            raise ValueError("invalid A/B evidence: " + "; ".join(problems[:6]))
        value = candidates_from_summary(summary, args.headroom, args.scenario)
    except (OSError, UnicodeError, ValueError, KeyError, TypeError) as exc:
        print(f"Cannot recommend Brutal rate: {exc}", file=sys.stderr)
        return 1
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    for name, data in value["directional_evidence"].items():
        print(f"{name} candidate={data['candidate_mbps']} Mbps "
              f"BBR p10_floor={data['bbr_worst_loss_p10_goodput_mbps']} Mbps "
              f"(NOT a production default decision)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
