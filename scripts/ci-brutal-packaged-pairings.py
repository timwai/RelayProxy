#!/usr/bin/env python3
"""Validate actual distribution archive and run old/new signed Relay pairings.

The new Server/Agent come from scripts/build.sh archives, NOT go build.
The pre-Brutal baseline is built from the merge-base worktree.
This is still unsigned portable-package smoke QA, not a GUI/driver install.
"""
import argparse
import os
import platform
from pathlib import Path, PurePosixPath
import subprocess
import sys
import tarfile
import tempfile
import zipfile


def archive_name():
    if sys.platform == "win32":
        return "RelayProxy-windows-amd64.zip", "windows-amd64"
    if sys.platform == "darwin":
        arch = "arm64" if platform.machine().lower() in ("arm64", "aarch64") else "amd64"
        return f"RelayProxy-darwin-{arch}.tar.gz", f"darwin-{arch}"
    if sys.platform.startswith("linux"):
        return "RelayProxy-linux-amd64.tar.gz", "linux-amd64"
    raise ValueError("unsupported test runner")


def unpack_distributed_archive(archive, destination, platform_dir):
    if archive.name.endswith(".zip"):
        with zipfile.ZipFile(archive) as zipped:
            if zipped.testzip() is not None:
                raise ValueError("distribution ZIP integrity check failed")
            for member in zipped.infolist():
                path = PurePosixPath(member.filename)
                if (path.is_absolute() or ".." in path.parts or
                        not path.parts or path.parts[0] != platform_dir):
                    raise ValueError(f"unsafe Windows ZIP entry: {member.filename}")
            zipped.extractall(destination)
    else:
        with tarfile.open(archive, "r:gz") as zipped:
            for member in zipped:
                path = PurePosixPath(member.name)
                if (path.is_absolute() or ".." in path.parts or
                        not path.parts or path.parts[0] != platform_dir):
                    raise ValueError(f"unsafe Unix archive entry: {member.name}")
                if member.issym() or member.islnk():
                    raise ValueError(f"links not permitted in archive: {member.name}")
            zipped.extractall(destination, filter="data")
    result = destination / platform_dir
    suffix = ".exe" if sys.platform == "win32" else ""
    for filename in (f"relay-agent{suffix}", f"relay-server{suffix}"):
        target = result / filename
        if not target.is_file() or not target.stat().st_size:
            raise ValueError(f"release payload missing {filename}")
        if os.name != "nt":
            target.chmod(target.stat().st_mode | 0o111)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--artifacts", type=Path, required=True)
    args = parser.parse_args()
    name, platform_dir = archive_name()
    matches = list(args.artifacts.rglob(name))
    if len(matches) != 1:
        raise ValueError(f"expected exactly one packaged archive {name}; got {matches}")
    repo = Path(__file__).resolve().parent.parent
    with tempfile.TemporaryDirectory(prefix="relay-packaged-interop-") as temp:
        extracted = unpack_distributed_archive(matches[0], Path(temp), platform_dir)
        if sys.platform.startswith("linux"):
            env = dict(os.environ, RELAYPROXY_PACKAGED_NEW_DIR=str(extracted))
            command = ["bash", "scripts/ci-brutal-full-binary-compat.sh"]
        else:
            env = os.environ.copy()
            command = [sys.executable, "scripts/ci-brutal-native-binary-compat.py",
                       "--packaged-new", str(extracted)]
        print(f"[packaged-interop] EXECUTE old/new against real {name}", flush=True)
        subprocess.run(command, cwd=repo, env=env, check=True, timeout=1500)
        print(f"[packaged-interop] PASS archive={name}; signed-auth and actual SOCKS payload "
              f"8 pairings. NOT installer/GUI/driver QA", flush=True)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, subprocess.SubprocessError, tarfile.TarError,
            zipfile.BadZipFile) as exc:
        print(f"[packaged-interop] FAIL: {exc}", file=sys.stderr)
        sys.exit(1)
