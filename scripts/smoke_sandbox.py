#!/usr/bin/env python3
"""Verify ephemeral Tailnet admission and persistent paired device identity across restarts."""

import argparse
import http.client
import json
import pathlib
import subprocess
import tempfile
import time
import uuid

from smoke_local import MCP, address
from smoke_peer import verify_peer


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=pathlib.Path)
    parser.add_argument("--auth-key-file", required=True, type=pathlib.Path)
    parser.add_argument("--control-url", default="https://controlplane.tailscale.com")
    parser.add_argument("--peer", required=True, action="append", metavar="NAME:OS")
    parser.add_argument("--peer-password-file", required=True, action="append", metavar="NAME=PATH",
                        help="Private connection-password file for each peer; used for the first xmesh pairing")
    parser.add_argument("--observer-gateway", help="Existing loopback Gateway used for incoming calls and offline observation")
    parser.add_argument("--observer-token-file", type=pathlib.Path, help="The observer Gateway's default token file, <state-dir>/pats/default.token")
    parser.add_argument("--rounds", type=int, choices=range(1, 4), default=2)
    options = parser.parse_args()
    binary, key = options.binary.resolve(), options.auth_key_file.resolve(strict=True)
    peers = [entry.rsplit(":", 1) for entry in options.peer]
    assert all(len(peer) == 2 and peer[1] in ("darwin", "linux", "windows") for peer in peers), peers
    passwords = {}
    for item in options.peer_password_file:
        peer, separator, filename = item.partition("=")
        assert separator and peer and filename and peer not in passwords, "Use one NAME=PATH password file per peer"
        passwords[peer] = pathlib.Path(filename).resolve(strict=True)
    assert set(passwords) == {peer[0] for peer in peers}, "Provide a password file for every peer"
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
            "schema_version": 2,
            "node": {"name": name, "roles": ["agent", "gateway"]},
            "mesh": {"lan": {"enabled": False}, "tailnet": {"enabled": True, "control_url": options.control_url,
                     "authkey_file": str(key), "ephemeral": True}},
            "gateway": {"listen": gateway},
            "agent": {"default_cwd": str(workspace), "cwd_policy": {"allow": [str(workspace)]},
                      "authz": {"tag:peer": {"tools": ["*"], "backends": ["*"]}, "tag:device": {"tools": ["*"], "backends": ["*"]}}}}))
        for number in range(options.rounds):
            client = None
            with (root / "runtime.log").open("w+") as log:
                process = subprocess.Popen([str(binary), "serve", "--config", str(config),
                    "--state-dir", str(state)], stdout=log, stderr=log)
                try:
                    deadline = time.monotonic() + 60
                    while True:
                        try:
                            gateway_token = (state / "pats" / "default.token").read_text().strip()
                            client = MCP(gateway, gateway_token)
                            result = client.request("tools/call", {"name": "local/sys.info", "arguments": {}})
                            if result.get("isError") and (result.get("structuredContent") or {}).get("error") == "not_found":
                                client.close()
                                client = None
                                assert time.monotonic() < deadline, result
                                time.sleep(0.1)
                                continue
                            assert not result.get("isError"), result
                            info = result["structuredContent"]
                            break
                        except (OSError, http.client.HTTPException):
                            if process.poll() is not None or time.monotonic() >= deadline:
                                log.seek(0)
                                raise RuntimeError(log.read())
                            time.sleep(0.1)
                    assert info["network"]["mode"] == "tailnet", info["network"]
                    identity = info["network"]["node_id"]
                    assert identity.startswith("dev-"), "Missing persistent xmesh device identity"
                    assert not identities or identity == identities[0], "Network restart changed the paired device identity"
                    identities.append(identity)
                    if number == 0:
                        for peer, _ in peers:
                            deadline = time.monotonic() + 45
                            while True:
                                found = subprocess.run([str(binary), "control", "pair", "discover", "--state-dir", str(state)], capture_output=True, text=True, timeout=10)
                                assert found.returncode == 0, found.stderr
                                candidate = next((item for item in json.loads(found.stdout)["devices"] if item["name"] == peer and item.get("path") == "tailnet"), None)
                                if candidate:
                                    break
                                assert time.monotonic() < deadline, "Paired peer not discovered: " + peer
                                time.sleep(0.25)
                            paired = subprocess.run([str(binary), "control", "pair", "connect", "--state-dir", str(state), "--path", "tailnet", "--address", candidate["address"], "--fingerprint", candidate["fingerprint"], "--password-file", str(passwords[peer])], capture_output=True, text=True, timeout=15)
                            assert paired.returncode == 0, paired.stderr
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
                observer = MCP(options.observer_gateway, observer_token)
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
                      "checks": ["stable-device-identity", "workspace-cleanup"]}))


if __name__ == "__main__":
    main()
