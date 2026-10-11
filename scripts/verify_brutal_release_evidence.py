#!/usr/bin/env python3
"""Machine-check completeness of Issue #198 WAN evidence (never auto-approve release).

Requires 3 loss conditions x 2 transfer directions x 3 proxies x >=5 runs,
loaded ICMP RTT for each, and >=5 co-active 60s BBR+Brutal experiments
for each loss condition. Policies and real-link evidence are operator-supplied.
No assertion here can prove the WAN path or the provenance of its impairment.
"""
import argparse
import csv
import json
import math
import re
from pathlib import Path
import sys

from summarize_brutal_acceptance import summarize

LOSS = ("0", "1", "5")
DIRECTIONS = ("download", "upload")
VARIANTS = ("bbr", "brutal", "hysteria2")
MANIFEST_FIELDS = (
    "client_region", "server_region", "client_hardware", "server_hardware",
    "network_path", "relayproxy_commit", "relayproxy_agent_sha256",
    "relayproxy_server_sha256", "hysteria2_version", "hysteria2_sha256",
    "origin_fixture_sha256", "rtt_probe_target",
)
POLICY_FIELDS = ("min_jain", "min_bbr_retention", "max_loaded_icmp_p95_ms",
                 "max_loaded_over_idle_p50_ratio", "min_duration_s")


def number(value, label):
    try:
        result = float(value)
    except (ValueError, TypeError):
        raise ValueError(f"{label}: invalid number") from None
    if not math.isfinite(result) or result <= 0:
        raise ValueError(f"{label}: value must be finite and positive")
    return result


def verify(ab_csv, coexistence_paths, manifest, policy):
    errors = []
    for field in MANIFEST_FIELDS:
        if not isinstance(manifest.get(field), str) or not manifest[field].strip():
            errors.append(f"manifest missing {field}")
    for field in ("relayproxy_agent_sha256", "relayproxy_server_sha256",
                  "hysteria2_sha256", "origin_fixture_sha256"):
        if not re.fullmatch(r"[0-9a-fA-F]{64}", str(manifest.get(field, ""))):
            errors.append(f"manifest {field} must be a real SHA256 hex digest")
    if not re.fullmatch(r"[0-9a-fA-F]{40}", str(manifest.get("relayproxy_commit", ""))):
        errors.append("manifest relayproxy_commit must be the full 40-digit git SHA")
    for field in MANIFEST_FIELDS:
        value = str(manifest.get(field, "")).strip().lower()
        if value.startswith(("todo", "change_me", "example", "replace_me")):
            errors.append(f"manifest {field} still contains placeholder text")
    loss_evidence = manifest.get("loss_evidence", {})
    if not isinstance(loss_evidence, dict):
        errors.append("manifest loss_evidence must be an object")
        loss_evidence = {}
    for loss in LOSS:
        evidence = loss_evidence.get(loss, {})
        if not isinstance(evidence, dict) or not evidence.get("impairment_log"):
            errors.append(f"manifest missing impairment log reference for loss={loss}")
        elif not Path(evidence["impairment_log"]).is_file():
            errors.append(f"loss={loss} impairment log does not exist")
    params = {}
    for field in POLICY_FIELDS:
        try:
            params[field] = number(policy.get(field), field)
        except ValueError as exc:
            errors.append(str(exc))
    if params.get("min_jain", 1) > 1 or params.get("min_bbr_retention", 1) > 1:
        errors.append("fairness/retention thresholds cannot exceed 1")
    # A policy may tighten the acceptance gate, never weaken its minimum 60s run.
    if 0 < params.get("min_duration_s", 60) < 60:
        errors.append("min_duration_s cannot be below the mandatory 60-second baseline")
    summary, problems = summarize(ab_csv, min_runs=5)
    errors.extend(problems)
    scenarios = {r["scenario"] for r in summary}
    if len(scenarios) != 1:
        errors.append("A/B trials have inconsistent scenario identifiers across loss and direction")
    groups = {(r["loss_label"], r["direction"], r["variant"]): r for r in summary}
    for loss in LOSS:
        for direction in DIRECTIONS:
            for variant in VARIANTS:
                if (loss, direction, variant) not in groups:
                    errors.append(f"missing A/B condition loss={loss} direction={direction} variant={variant}")
    try:
        with Path(ab_csv).open(newline="", encoding="utf-8") as source:
            original = list(csv.DictReader(source))
    except (OSError, UnicodeError) as exc:
        errors.append(f"cannot read A/B CSV: {exc}")
        original = []
    for row in original:
        label = f"{row.get('loss_label')}/{row.get('direction')}/{row.get('variant')}/round={row.get('round')}"
        try:
            if number(row.get("loaded_icmp_samples"), label + " ping samples") < 5:
                errors.append(f"{label}: fewer than five loaded ICMP responses")
            rtt = number(row.get("loaded_icmp_p95_ms"), label + " loaded p95")
            if rtt > params.get("max_loaded_icmp_p95_ms", float("inf")):
                errors.append(f"{label}: ICMP p95 {rtt:.2f}ms exceeds policy")
            if row.get("direction") == "download" and row.get("checksum_ok", "").lower() not in ("true", "1"):
                errors.append(f"{label}: download checksum not verified")
        except ValueError as exc:
            errors.append(str(exc))

    for loss in LOSS:
        path = coexistence_paths.get(loss)
        if not path:
            errors.append(f"missing sustained coexistence CSV loss={loss}")
            continue
        try:
            with Path(path).open(newline="", encoding="utf-8") as source:
                coexist = list(csv.DictReader(source))
        except (OSError, UnicodeError) as exc:
            errors.append(f"loss={loss}: cannot open coexistence CSV: {exc}")
            continue
        if len(coexist) < 5:
            errors.append(f"loss={loss}: at least five sustained coexistence rounds required")
        seen = set()
        for entry in coexist:
            label = f"coexistence loss={loss} round={entry.get('round')}"
            try:
                if entry.get("round") in seen:
                    errors.append(f"{label}: duplicate round")
                seen.add(entry.get("round"))
                if (entry.get("loss_label") != loss or
                        entry.get("scenario") not in scenarios or
                        entry.get("valid", "").lower() not in ("true", "1")):
                    errors.append(f"{label}: invalid run, differing scenario or wrong loss label")
                    continue
                duration = number(entry.get("duration_s"), label + " duration")
                jain = number(entry.get("jain_coactive_index"), label + " Jain")
                bbr = number(entry.get("bbr_coactive_mbps"), label + " BBR throughput")
                number(entry.get("brutal_coactive_mbps"), label + " Brutal throughput")
                idle = number(entry.get("idle_icmp_p50_ms"), label + " idle RTT")
                loaded = number(entry.get("loaded_icmp_p95_ms"), label + " loaded p95")
                loaded_p50 = number(entry.get("loaded_icmp_p50_ms"), label + " loaded p50")
                if duration < max(60, params.get("min_duration_s", 60)):
                    errors.append(f"{label}: insufficient load duration")
                if jain < params.get("min_jain", 1):
                    errors.append(f"{label}: Jain={jain:.3f} below policy")
                if loaded > params.get("max_loaded_icmp_p95_ms", float("inf")):
                    errors.append(f"{label}: loaded RTT exceeds policy")
                if loaded_p50 / idle > params.get("max_loaded_over_idle_p50_ratio", float("inf")):
                    errors.append(f"{label}: loaded RTT inflation exceeds policy")
                alone = groups.get((loss, "download", "bbr"))
                if alone:
                    alone_mbps = number(alone["median_goodput_mbps"], "BBR standalone median")
                    if bbr / alone_mbps < params.get("min_bbr_retention", 1):
                        errors.append(f"{label}: BBR retains only {100*bbr/alone_mbps:.1f}% of solo rate")
                if number(entry.get("loaded_icmp_samples"), label + " loaded samples") < 5:
                    errors.append(f"{label}: insufficient loaded RTT samples")
            except ValueError as exc:
                errors.append(str(exc))
        if seen != {str(k) for k in range(1, len(coexist) + 1)}:
            errors.append(f"loss={loss}: coexistence rounds are not contiguous starting at 1")
    return {"machine_checks_passed": not errors,
            "human_real_wan_provenance_and_release_signoff_required": True,
            "ab_conditions_present": len(groups),
            "coexistence_csv_present": sorted(coexistence_paths),
            "failures": errors,
            "warning": "No external WAN path, actual loss or installer authenticity is proven by CSV alone."}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--ab-csv", type=Path, required=True)
    parser.add_argument("--coexistence", type=str, action="append", required=True,
                        help="0=path/to/coexistence-summary.csv (repeat for 0,1,5)")
    parser.add_argument("--manifest", type=Path, required=True)
    parser.add_argument("--policy", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args(argv)
    paths = {}
    for value in args.coexistence:
        if "=" not in value:
            parser.error("coexistence must use loss=path")
        loss, path = value.split("=", 1)
        if loss not in LOSS or loss in paths or not path:
            parser.error("provide unique 0=, 1=, and 5= coexistence paths")
        paths[loss] = path
    try:
        manifest = json.loads(args.manifest.read_text(encoding="utf-8"))
        policy = json.loads(args.policy.read_text(encoding="utf-8"))
        verdict = verify(args.ab_csv, paths, manifest, policy)
    except (OSError, UnicodeError, ValueError, TypeError) as exc:
        verdict = {"machine_checks_passed": False,
                   "human_real_wan_provenance_and_release_signoff_required": True,
                   "failures": [str(exc)]}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(verdict, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    for failure in verdict["failures"]:
        print("ACCEPTANCE EVIDENCE INVALID:", failure, file=sys.stderr)
    print(f"Machine evidence checks passed={verdict['machine_checks_passed']}. "
          f"Operator real-WAN and release approval are STILL required.")
    return 0 if verdict["machine_checks_passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
