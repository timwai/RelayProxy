#!/usr/bin/env python3
"""Check real scripts/build.sh release archives; never claim signed installer QA.

This inspects actual distribution archives, verifies checksums, executable
payloads, WinDivert runtime, macOS App layout and fail-closed BBR defaults.
It does not validate signatures, installed drivers, OS GUI, or upgrade state.
"""
import argparse
import hashlib
import json
from pathlib import Path, PurePosixPath
import re
import sys
import tarfile
import zipfile

PLATFORMS = ("linux-amd64", "linux-arm64", "darwin-amd64",
             "darwin-arm64", "windows-amd64", "windows-arm64")


def safe_name(name):
    candidate = PurePosixPath(name)
    return bool(name and not name.startswith("/") and "\\" not in name
                and all(part not in {"", ".", ".."} for part in candidate.parts))


def validate_agent_config(config):
    # Preserve the existing legacy default behavior: both directions BBR.
    # A default policy can only change after explicit Issue #198 approval.
    value = config.decode("utf-8")
    for key in ("up_mbps", "down_mbps"):
        if not re.search(r"(?m)^\s*" + key + r":\s*0(?:\s|#|$)", value):
            raise ValueError(f"release Agent default {key} is not 0 (BBR)")
    return True


def expected_paths(platform):
    binaries = ("relay-agent.exe", "relay-server.exe") if platform.startswith("windows") else (
        "relay-agent", "relay-server")
    required = {f"{platform}/{name}" for name in binaries}
    required.add(f"{platform}/configs/relay-agent.yaml")
    required.add(f"{platform}/configs/relay-server.yaml")
    required.add(f"{platform}/brand/logo.png")
    if platform.startswith("windows"):
        required.update({f"{platform}/relay-agent-gui.exe"})
        if platform == "windows-amd64":
            required.update(f"{platform}/windivert/{name}" for name in (
                "WinDivert.dll", "WinDivert64.sys", "LICENSE", "VERSION"))
    if platform.startswith("darwin"):
        required.update({f"{platform}/RelayProxy.app/Contents/Info.plist",
                         f"{platform}/RelayProxy.app/Contents/MacOS/RelayProxy"})
    return required


def read_tar(file, expected):
    with tarfile.open(file, "r:gz") as archive:
        index = {}
        for member in archive:
            if not safe_name(member.name):
                raise ValueError(f"unsafe archive name {member.name!r}")
            if member.issym() or member.islnk():
                raise ValueError(f"symbolic/hard link in distribution: {member.name}")
            if member.isfile():
                if member.name in index:
                    raise ValueError(f"duplicate archive entry {member.name}")
                index[member.name] = member
        missing = expected - set(index)
        if missing:
            raise ValueError(f"{file.name} missing {sorted(missing)}")
        for name in expected:
            if index[name].size <= 0:
                raise ValueError(f"empty distribution payload {name}")
        config = archive.extractfile(index[next(x for x in expected
                                                 if x.endswith("/configs/relay-agent.yaml"))]).read()
        validate_agent_config(config)
        return len(index)


def read_zip(file, expected):
    with zipfile.ZipFile(file) as archive:
        broken = archive.testzip()
        if broken:
            raise ValueError(f"{file.name} corrupted member {broken}")
        entries = {}
        for entry in archive.infolist():
            if not safe_name(entry.filename):
                raise ValueError(f"unsafe archive name {entry.filename!r}")
            if not entry.is_dir():
                if entry.filename in entries:
                    raise ValueError(f"duplicate ZIP member {entry.filename}")
                entries[entry.filename] = entry
        missing = expected - set(entries)
        if missing:
            raise ValueError(f"{file.name} missing {sorted(missing)}")
        if any(entries[name].file_size <= 0 for name in expected):
            raise ValueError(f"empty payload in {file.name}")
        validate_agent_config(archive.read(next(name for name in expected
                                                if name.endswith("/configs/relay-agent.yaml"))))
        return len(entries)


def verify_checksums(dist):
    manifest = dist / "SHA256SUMS.txt"
    if not manifest.is_file():
        raise ValueError("build.sh did not produce SHA256SUMS.txt")
    checked = 0
    for entry in manifest.read_text(encoding="utf-8").splitlines():
        match = re.fullmatch(r"([0-9a-fA-F]{64})\s+\*?(.+)", entry)
        if match is None:
            raise ValueError(f"invalid SHA256SUMS line: {entry!r}")
        expected, filename = match.groups()
        if not safe_name(filename.lstrip("./")):
            raise ValueError(f"unsafe checksum path {filename}")
        path = (dist / filename).resolve()
        if not path.is_relative_to(dist.resolve()) or not path.is_file():
            raise ValueError(f"missing/unsafe checksum member {filename}")
        digest = hashlib.sha256()
        with path.open("rb") as stream:
            for chunk in iter(lambda: stream.read(1 << 20), b""):
                digest.update(chunk)
        if digest.hexdigest() != expected.lower():
            raise ValueError(f"SHA256 mismatch in {filename}")
        checked += 1
    if checked < 12:
        raise ValueError(f"too few checksummed distribution payloads: {checked}")
    return checked


def inspect(dist):
    result = {"archive_content_integrity_passed": False,
              "signatures_and_installed_upgrade_verified": False,
              "real_wan_acceptance_verified": False,
              "platforms": {}, "warnings": [
                  "Unsigned CI packaging is not a signed platform release",
                  "WinDivert/macOS NetworkExtension install, desktop GUI and live migration were not tested",
                  "No Hysteria2 physical WAN or loaded RTT measurements",
              ]}
    result["checksums_verified"] = verify_checksums(dist)
    for platform in PLATFORMS:
        archive = dist / f"RelayProxy-{platform}.tar.gz"
        if not archive.is_file():
            raise ValueError(f"missing platform full archive {archive.name}")
        required = expected_paths(platform)
        inspected = read_tar(archive, required)
        result["platforms"][platform] = {"complete_tar": True, "files_checked": len(required),
                                         "archived_files": inspected}
        if platform.startswith("windows"):
            full_zip = dist / f"RelayProxy-{platform}.zip"
            if not full_zip.is_file():
                raise ValueError(f"missing full Windows ZIP {full_zip.name}")
            result["platforms"][platform]["zip_files"] = read_zip(full_zip, required)
    agent_zip = dist / "RelayProxy-agent-windows-amd64.zip"
    if not agent_zip.is_file():
        raise ValueError("missing independently packaged x64 Agent+WinDivert ZIP")
    with zipfile.ZipFile(agent_zip) as archive:
        if archive.testzip() is not None:
            raise ValueError("Windows Agent ZIP checksum failure")
        needed = ("relay-agent.exe", "relay-agent-gui.exe",
                  "windivert/WinDivert.dll", "windivert/WinDivert64.sys",
                  "configs/relay-agent.yaml")
        names = set(archive.namelist())
        for path in needed:
            if f"windows-amd64/{path}" not in names:
                raise ValueError(f"Windows Agent ZIP missing {path}")
        validate_agent_config(archive.read("windows-amd64/configs/relay-agent.yaml"))
    result["archive_content_integrity_passed"] = True
    return result


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dist", type=Path, required=True)
    parser.add_argument("--report", type=Path, required=True)
    args = parser.parse_args(argv)
    args.report.parent.mkdir(parents=True, exist_ok=True)
    try:
        report = inspect(args.dist)
    except (OSError, UnicodeError, ValueError, tarfile.TarError, zipfile.BadZipFile) as exc:
        report = {"archive_content_integrity_passed": False,
                  "signatures_and_installed_upgrade_verified": False,
                  "real_wan_acceptance_verified": False,
                  "error": str(exc)}
    args.report.write_text(json.dumps(report, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    print(json.dumps(report, indent=2, ensure_ascii=False))
    return 0 if report["archive_content_integrity_passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
