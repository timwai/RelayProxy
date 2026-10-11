#!/usr/bin/env python3
"""Native Windows/macOS production CLI Agent/Server old/new authentication matrix.

This builds real production main packages and runs eight signed/approved
loopback connection combinations. It is NOT a signed installer or GUI test.
All credentials and SQLite data exist only inside a disposable directory.
"""
import os
import argparse
import functools
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import shutil
import socket
import subprocess
import sys
import tempfile
import threading
import time


def run(args, cwd=None, timeout=300):
    subprocess.run([str(p) for p in args], cwd=cwd, timeout=timeout, check=True)


def get_available_port():
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def available(host, port):
    try:
        with socket.create_connection((host, port), timeout=0.2):
            return True
    except OSError:
        return False


def kill_process(process):
    if process is None:
        return
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=4)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()


def wait_for_approval(agent_log, server, agent, timeout=25):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if agent_log.exists() and "[Agent] Device approved. SessionID:" in agent_log.read_text(
                encoding="utf-8", errors="replace"):
            return server.poll() is None and agent.poll() is None
        if server.poll() is not None or agent.poll() is not None:
            return False
        time.sleep(0.10)
    return False


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--packaged-new", type=Path, help="unpacked distro platform directory with real Agent/Server binaries")
    args = parser.parse_args()
    if sys.platform not in {"darwin", "win32"}:
        print("This native runner is for macOS and Windows; Linux uses ci-brutal-full-binary-compat.sh",
              file=sys.stderr)
        return 2

    repo = Path(__file__).resolve().parent.parent
    suffix = ".exe" if os.name == "nt" else ""
    base = subprocess.check_output(
        ["git", "merge-base", "origin/main", "HEAD"], cwd=repo, text=True).strip()
    with tempfile.TemporaryDirectory(prefix="relay-full-binary-compat-") as temp_name:
        temp = Path(temp_name)
        baseline = temp / "baseline"
        added = False
        try:
            run(["git", "worktree", "add", "--detach", str(baseline), base], cwd=repo)
            added = True
            (baseline / "scripts" / "compat_seed").mkdir(parents=True, exist_ok=True)
            shutil.copyfile(repo / "scripts" / "compat_seed" / "main.go",
                            baseline / "scripts" / "compat_seed" / "main.go")
            binaries = {}
            for name, root in (("old", baseline), ("new", repo)):
                for kind, package in (("server", "./cmd/relay-server"),
                                      ("agent", "./cmd/relay-agent"),
                                      ("seed", "./scripts/compat_seed")):
                    target = temp / f"{name}-{kind}{suffix}"
                    if name == "new" and kind in ("agent", "server") and args.packaged_new:
                        packaged = args.packaged_new / f"relay-{kind}{suffix}"
                        if not packaged.is_file() or packaged.stat().st_size == 0:
                            raise RuntimeError(f"missing packaged new binary: {packaged}")
                        shutil.copy2(packaged, target)
                        if os.name != "nt":
                            target.chmod(0o755)
                    else:
                        run(["go", "build", "-trimpath", "-o", str(target), package], cwd=root, timeout=480)
                    binaries[name, kind] = target
                output = subprocess.check_output(
                    [str(binaries[name, "agent"]), "--version"], text=True, timeout=10)
                if "relay-agent" not in output:
                    raise RuntimeError(f"{name} Agent version probe failed")

            for server_version in ("old", "new"):
                for agent_version in ("old", "new"):
                    for transport in ("quic_only", "tcp_only"):
                        case = temp / f"{server_version}-{agent_version}-{transport}"
                        agent_dir = case / "agent"
                        agent_dir.mkdir(parents=True)
                        ports = set()
                        while len(ports) < 5:
                            ports.add(get_available_port())
                        quic, tcp, admin, socks, origin_port = sorted(ports)
                        database = case / "lab.db"
                        brutal_mbps = 4 if (server_version, agent_version, transport) == (
                            "new", "new", "quic_only") else 0
                        run([binaries[server_version, "seed"], "-db", database,
                             "-agent-dir", agent_dir, "-quic-port", quic,
                             "-tcp-port", tcp, "-socks-port", socks,
                             "-brutal-mbps", brutal_mbps, "-mode", transport,
                             "-cert-dir", case],
                            timeout=30)
                        config = case / "server.yaml"
                        config.write_text(f"""
server:
  tls_enabled: true
  quic:
    listen: "127.0.0.1:{quic}"
  tls:
    listen: "127.0.0.1:{tcp}"
  admin:
    listen: "127.0.0.1:{admin}"
    tls_enabled: false
  cert_file: "{(case / 'localhost.crt').as_posix()}"
  key_file: "{(case / 'localhost.key').as_posix()}"
database:
  driver: sqlite
  dsn: "{database.as_posix()}"
tunnel:
  heartbeat_sec: 5
  max_connections: 64
  max_connections_per_device: 16
p2p:
  enabled: false
direct:
  enabled: false
exit:
  enabled: true
  allow_internet: true
  allow_private_network: true
  allow_loopback: true
  upstream:
    mode: direct
relay_acl:
  allow_internet: true
  allow_private_network: true
  allow_loopback: true
logging:
  level: info
""", encoding="utf-8")
                        server = agent = None
                        with (case / "server.log").open("w", encoding="utf-8") as server_log, \
                             (case / "agent.log").open("w", encoding="utf-8") as agent_log:
                            try:
                                server = subprocess.Popen(
                                    [str(binaries[server_version, "server"]), "-config", str(config)],
                                    cwd=case, stdout=server_log, stderr=subprocess.STDOUT)
                                deadline = time.monotonic() + 20
                                while time.monotonic() < deadline and not available("127.0.0.1", admin):
                                    if server.poll() is not None:
                                        break
                                    time.sleep(0.1)
                                else:
                                    if server.poll() is not None:
                                        raise RuntimeError("server died after listener startup")
                                if not available("127.0.0.1", admin):
                                    raise RuntimeError("real Server admin listener failed to start")
                                environment = dict(os.environ, HOME=str(agent_dir))
                                agent = subprocess.Popen([
                                    str(binaries[agent_version, "agent"]), "--no-gui", "--no-web",
                                    "--config", str(agent_dir / "relay-agent.yaml")],
                                    cwd=agent_dir, env=environment,
                                    stdout=agent_log, stderr=subprocess.STDOUT)
                                agent_log.flush()
                                if not wait_for_approval(case / "agent.log", server, agent):
                                    raise RuntimeError("real Agent failed signed identity approval")
                                origin_dir = case / "origin"
                                origin_dir.mkdir(exist_ok=True)
                                fixture = (f"real-proxy-payload-{server_version}-{agent_version}-"
                                           f"{transport}\n").encode()
                                (origin_dir / "fixture.bin").write_bytes(fixture)
                                handler = functools.partial(SimpleHTTPRequestHandler,
                                                            directory=str(origin_dir))
                                origin_server = ThreadingHTTPServer(("127.0.0.1", origin_port), handler)
                                thread = threading.Thread(target=origin_server.serve_forever, daemon=True)
                                thread.start()
                                try:
                                    downloaded = case / "download.bin"
                                    proxy_url = f"socks5h://127.0.0.1:{socks}"
                                    endpoint = f"http://127.0.0.1:{origin_port}/fixture.bin"
                                    success = False
                                    for retry in range(15):
                                        probe = subprocess.run([
                                            "curl", "--disable", "--noproxy", "",
                                            "--proxy", proxy_url, "--fail", "--silent",
                                            "--show-error", "--connect-timeout", "2",
                                            "--max-time", "6", "--output", str(downloaded), endpoint
                                        ], capture_output=True, text=True, timeout=10, check=False)
                                        if probe.returncode == 0 and downloaded.exists() and downloaded.read_bytes() == fixture:
                                            success = True
                                            break
                                        time.sleep(.2)
                                    if not success:
                                        raise RuntimeError(f"real tunneled HTTP failed: {probe.stderr[:300]}")
                                finally:
                                    origin_server.shutdown()
                                    origin_server.server_close()
                                    thread.join(timeout=5)
                                if brutal_mbps:
                                    logs = [
                                        (case / "agent.log", "Relay QUIC upload switched to Brutal"),
                                        (case / "server.log", "Relay QUIC download switched to Brutal"),
                                    ]
                                    for log_file, expected in logs:
                                        if expected not in log_file.read_text(encoding="utf-8", errors="replace"):
                                            raise RuntimeError(f"new/new QUIC failed to install Brutal: {expected}")
                                    print("[native-full-bin] new/new QUIC Brutal installed in both directions at 4Mbps")
                                print(f"[native-full-bin] real SOCKS->Server Exit payload passed os={sys.platform} "
                                      f"server={server_version} agent={agent_version} transport={transport}")
                                print(f"[native-full-bin] PASS os={sys.platform} "
                                      f"server={server_version} agent={agent_version} "
                                      f"transport={transport}", flush=True)
                            except Exception:
                                server_log.flush()
                                agent_log.flush()
                                for file in (case / "server.log", case / "agent.log"):
                                    print(f"--- {file.name} ---\n" +
                                          file.read_text(encoding="utf-8", errors="replace")[-5000:],
                                          file=sys.stderr)
                                raise
                            finally:
                                kill_process(agent)
                                kill_process(server)
            print(f"[native-full-bin] PASS all 8 production entrypoint pairings on {sys.platform}")
            print("[native-full-bin] NOT signed installers, real WAN proxy traffic or GUI upgrade QA")
        finally:
            if added:
                subprocess.run(["git", "worktree", "remove", "--force", str(baseline)],
                               cwd=repo, check=False, timeout=30)
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, subprocess.SubprocessError, RuntimeError) as exc:
        print(f"native real-binary compatibility failed: {exc}", file=sys.stderr)
        sys.exit(1)
