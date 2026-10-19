#!/usr/bin/env python3
"""Join a short-lived sandbox to a tailnet and verify peer calls across restarts."""

import argparse
import http.client
import json
import pathlib
import subprocess
import tempfile
import time
import uuid

from smoke_local import MCP, address, wait_for_default_token
from smoke_peer import verify_peer


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=pathlib.Path)
    parser.add_argument("--auth-key-file", required=True, type=pathlib.Path)
    parser.add_argument("--control-url", default="https://controlplane.tailscale.com")
    parser.add_argument("--peer", required=True, action="append", metavar="NAME:OS")
    parser.add_argument("--observer-gateway", help="Existing loopback Gateway used for incoming calls and offline observation")
    parser.add_argument("--observer-token-file", type=pathlib.Path, help="The observer Gateway token_file returned by xmesh control pats list")
    parser.add_argument("--rounds", type=int, choices=range(1, 4), default=2)
    options = parser.parse_args()
    binary, key = options.binary.resolve(), options.auth_key_file.resolve(strict=True)
    peers = [entry.rsplit(":", 1) for entry in options.peer]
    assert all(len(peer) == 2 and peer[1] in ("darwin", "linux") for peer in peers), peers
    if options.observer_gateway:
        assert options.observer_gateway.startswith("127.0.0.1:"), "Observer must use loopback"
        assert options.observer_token_file, "The observer needs its token file"
        observer_token = options.observer_token_file.read_text().strip()
    identities, checks = [], []
    name = "xmesh-sandbox-" + uuid.uuid4().hex[:12]
    with tempfile.TemporaryDirectory(prefix="xmesh-sandbox-") as temporary:
        root = pathlib.Path(temporary).resolve()
        workspace = root / "workspace"
        workspace.mkdir()
        gateway = address()
        config = root / "config.json"
        state = root / "state"
        config.write_text(json.dumps({
            "node": {"name": name, "roles": ["agent", "gateway"]},
            "mesh": {"mode": "tailnet", "control_url": options.control_url,
                     "authkey_file": str(key), "ephemeral": True},
            "gateway": {"listen": gateway},
            "agent": {"default_cwd": str(workspace), "cwd_policy": {"allow": [str(workspace)]},
                      "authz": {"tag:device": {"tools": ["*"], "backends": ["*"]}}}}))
        for number in range(options.rounds):
            client = None
            with (root / "runtime.log").open("w+") as log:
                process = subprocess.Popen([str(binary), "serve", "--config", str(config),
                    "--state-dir", str(state)], stdout=log, stderr=log)
                try:
                    gateway_token = wait_for_default_token(binary, state, seconds=60)
                    deadline = time.monotonic() + 60
                    while True:
                        try:
                            client = MCP(gateway, gateway_token)
                            info = client.call("local/sys.info")
                            break
                        except (OSError, http.client.HTTPException):
                            if process.poll() is not None or time.monotonic() >= deadline:
                                log.seek(0)
                                raise RuntimeError(log.read())
                            time.sleep(0.1)
                    assert info["network"]["mode"] == "tailnet", info["network"]
                    identity = info["network"]["node_id"]
                    assert identity and identity not in identities, "Restart reused ephemeral node identity"
                    identities.append(identity)
                    for peer, system in peers:
                        checks.append(verify_peer(gateway, peer, system, token=gateway_token))
                    if options.observer_gateway:
                        checks.append(verify_peer(options.observer_gateway, name, info["os"], token=observer_token))
                finally:
                    try:
                        if client:
                            client.close()
                    finally:
                        process.terminate()
                        try:
                            process.wait(timeout=20)
                        except subprocess.TimeoutExpired:
                            process.kill()
                            process.wait()
                            raise AssertionError("ephemeral runtime shutdown exceeded 20s")
                        if process.returncode != 0:
                            log.seek(0)
                            raise AssertionError((process.returncode, log.read()))
                assert not list((state / "network").rglob("tailscaled.state*")), "Node credentials persisted"
            if options.observer_gateway:
                observer = MCP(options.observer_gateway)
                try:
                    deadline = time.monotonic() + 45
                    while True:
                        rows = observer.call("devices.list")["devices"]
                        if not any(row["id"] == name and row["online"] for row in rows):
                            break
                        assert time.monotonic() < deadline, "Exited sandbox is still online"
                        time.sleep(0.5)
                finally:
                    observer.close()
            print(json.dumps({"round": number + 1, "node": name, "node_id": identities[-1],
                              "result": "PASS", "checks": ["ephemeral-join", "peer-tools", "shutdown", "memory-state"]}), flush=True)
    print(json.dumps({"result": "PASS", "rounds": len(identities), "peer_checks": checks,
                      "checks": ["fresh-identity", "workspace-cleanup"]}))


if __name__ == "__main__":
    main()
