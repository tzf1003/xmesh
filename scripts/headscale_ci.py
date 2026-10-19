#!/usr/bin/env python3
"""Run a throwaway Headscale on loopback and verify xmesh tailnet peering against it.

The sequence follows docs/validation/2026-10-15-headscale.md, on one host:

  1. start Headscale (SQLite, loopback embedded DERP/STUN) and create a tagged ephemeral key;
  2. start two xmesh nodes (agent + gateway) that join with that key through tsnet (no TUN);
  3. run scripts/smoke_peer.py in both directions;
  4. run Test_TailnetPeerConnectivity from a third node against one of them (one retry: the first
     HTTP dial after a fresh join occasionally stalls until the 45 s test deadline);
  5. with --ingress, start a third node hs-i that has the ingress role behind a loopback "tunnel"
     listener and run scripts/oauth_client.py against it (Gate 4 protocol path, devices hs-a and
     hs-b). The ingress is a separate node because a tag:ingress node may only reach Agents with
     an OAuth client identity (identity.FromPeer), so it cannot take part in the plain peer check.

The default run uses same-host direct paths. --force-derp disables UDP in the
throwaway clients, routes via the loopback embedded DERP and checks reported
peer connection state. Multi-host NAT and external relay outages remain part
of the real-environment validation notes.
"""

# conformance: CON-ARCH-08
import argparse
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import time
import urllib.request
from types import SimpleNamespace

from smoke_local import MCP, address, stop_runtime, wait_for_default_token, wait_for_gateway
from smoke_peer import verify_peer
from accept_computer import verify_computer
from accept_net import Endpoint, Session
from oauth_client import obtain_tokens

HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parent
CONTROL_PORT = 18090


def sh(*command, **options):
    done = subprocess.run([str(part) for part in command], capture_output=True, text=True, **options)
    if done.returncode:
        raise AssertionError("%s exited %d\n%s%s" % (pathlib.Path(str(command[0])).name, done.returncode, done.stdout, done.stderr))
    return done.stdout


def headscale_config(work, port):
    # Headscale refuses an empty DERP map, and a map whose only entry cannot be reached makes every
    # node's netcheck fail and rebind its UDP socket every few seconds, which can swallow a fresh
    # WireGuard handshake. The embedded DERP server therefore runs on loopback with its STUN
    # listener, so netchecks succeed. The nodes share the host and find each other directly.
    stun = int(address().rsplit(":", 1)[1])
    (work / "policy.json").write_text(json.dumps({
        "tagOwners": {"tag:device": ["xmesh@"], "tag:peer": ["xmesh@"], "tag:ingress": ["xmesh@"]},
        # Whole-range prefixes rather than "*": a wildcard is expanded per node, so a node that joins
        # later (the probe) can have its first TCP packets dropped by peers whose filter has not caught
        # up yet, while disco pings, which the filter does not see, already succeed.
        "acls": [{"action": "accept", "src": ["100.64.0.0/10", "fd7a:115c:a1e0::/48"],
                  "dst": ["100.64.0.0/10:*", "fd7a:115c:a1e0::/48:*"]}],
    }))
    (work / "headscale.yaml").write_text("\n".join([
        f"server_url: http://127.0.0.1:{port}",
        f"listen_addr: 127.0.0.1:{port}",
        f"metrics_listen_addr: 127.0.0.1:{port + 1000}",
        f"grpc_listen_addr: 127.0.0.1:{port + 1001}",
        "grpc_allow_insecure: true",
        f"noise:\n  private_key_path: {work / 'noise.key'}",
        "prefixes:\n  v4: 100.64.0.0/10\n  v6: fd7a:115c:a1e0::/48\n  allocation: sequential",
        f"database:\n  type: sqlite\n  sqlite:\n    path: {work / 'db.sqlite'}",
        "derp:\n  server:\n    enabled: true\n    region_id: 999\n    region_code: xmesh\n    region_name: xmesh-test\n"
        f"    stun_listen_addr: 127.0.0.1:{stun}\n    private_key_path: {work / 'derp.key'}\n    ipv4: 127.0.0.1\n"
        "    automatically_add_embedded_derp_region: true\n  urls: []\n  paths: []\n  auto_update_enabled: false",
        "dns:\n  magic_dns: false\n  base_domain: xmesh.test\n  override_local_dns: false\n  nameservers:\n    global: []",
        f"policy:\n  mode: file\n  path: {work / 'policy.json'}",
        f"unix_socket: {work / 'headscale.sock'}",
        "unix_socket_permission: \"0770\"",
        "log:\n  level: info",
        "",
    ]))
    return work / "headscale.yaml"


def xmesh_config(name, key, port, control):
    grant = {"tools": ["*"], "backends": ["*"]}
    return {"node": {"name": name, "roles": ["agent", "gateway"]},
            "mesh": {"mode": "tailnet", "control_url": control, "authkey_file": str(key), "ephemeral": True},
            "gateway": {"listen": f"127.0.0.1:{port}"},
            "agent": {"authz": {"tag:device": grant, "tag:ingress": grant}}}


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--headscale", required=True, type=pathlib.Path)
    parser.add_argument("--binary", required=True, type=pathlib.Path)
    parser.add_argument("--expected-os", default={"linux": "linux", "darwin": "darwin"}.get(sys.platform, "linux"))
    parser.add_argument("--skip-go-test", action="store_true")
    parser.add_argument("--ingress", action="store_true", help="also run the OAuth protocol check through an ingress on hs-a")
    parser.add_argument("--computer-helper", type=pathlib.Path, help="also verify native desktop images and finite control through peer and OAuth routes")
    parser.add_argument("--force-derp", action="store_true", help="disable UDP and verify desktop calls through the local embedded DERP")
    options = parser.parse_args()
    headscale, binary = options.headscale.resolve(), options.binary.resolve()
    control = f"http://127.0.0.1:{CONTROL_PORT}"
    processes, logs = [], []
    child_env = dict(os.environ)
    if options.force_derp:
        # Test-only debug knobs in the pinned tsnet SDK. HTTP DERP remains on
        # loopback; peer payloads still traverse the encrypted WireGuard layer.
        child_env.update(TS_DEBUG_ALWAYS_USE_DERP="true", TS_DEBUG_USE_DERP_HTTP="true")

    def spawn(command, name):
        log = open(work / f"{name}.log", "w+")
        logs.append(log)
        process = subprocess.Popen([str(part) for part in command], stdout=log, stderr=log, env=child_env)
        processes.append(process)
        return process, log

    with tempfile.TemporaryDirectory(prefix="xmesh-headscale-") as temporary:
        work = pathlib.Path(temporary).resolve()
        try:
            probe_test = work / "mesh.test"
            if not options.skip_go_test:
                # Compile first: building the test binary takes a minute on a CI runner and would starve
                # the running nodes if done after they have started.
                sh("go", "test", "-c", "-o", probe_test, "./internal/mesh", cwd=ROOT)
            config = headscale_config(work, CONTROL_PORT)
            hs, hs_log = spawn([headscale, "-c", config, "serve"], "headscale")
            deadline = time.monotonic() + 60
            while True:
                try:
                    urllib.request.urlopen(control + "/health", timeout=2).read()
                    break
                except OSError:
                    assert hs.poll() is None, (hs_log.seek(0), hs_log.read())[1]
                    assert time.monotonic() < deadline, "Headscale did not become healthy"
                    time.sleep(0.2)
            cli = [headscale, "-c", config, "-o", "json"]
            user = json.loads(sh(*cli, "users", "create", "xmesh"))
            key = json.loads(sh(*cli, "preauthkeys", "create", "--user", user["id"], "--reusable", "--ephemeral",
                                "--tags", "tag:device", "--expiration", "1h"))["key"]
            key_file = work / "authkey"
            key_file.write_text(key)
            key_file.chmod(0o600)
            ingress_key = json.loads(sh(*cli, "preauthkeys", "create", "--user", user["id"], "--reusable", "--ephemeral",
                                        "--tags", "tag:device,tag:ingress", "--expiration", "1h"))["key"]
            ingress_key_file = work / "ingress-authkey"
            ingress_key_file.write_text(ingress_key)
            ingress_key_file.chmod(0o600)
            issuer, tunnel = "https://mcp.xmesh.test", address()
            password_file = work / "oauth-password"
            password_file.write_text("correct-horse-battery-" + os.urandom(8).hex())
            password_file.chmod(0o600)

            ports = {name: int(address().rsplit(":", 1)[1]) for name in ("hs-a", "hs-b", "hs-i")[:3 if options.ingress else 2]}
            nodes = {}
            desktop_endpoint = None
            desktop_pat = None
            if options.computer_helper:
                desktop_pat = os.urandom(32).hex()
                desktop_pat_file = work / "computer-pat"
                desktop_pat_file.touch(mode=0o600)
                desktop_pat_file.write_text(desktop_pat)
                peer_pat = os.urandom(32).hex()
                peer_pat_file = work / "peer-pat"
                peer_pat_file.touch(mode=0o600)
                peer_pat_file.write_text(peer_pat)
                desktop_state = work / "computer"
                host, host_log = spawn([binary, "computer", "host", "--state-dir", desktop_state,
                                        "--helper", options.computer_helper.resolve()], "computer-host")
                deadline = time.monotonic() + 15
                while True:
                    assert host.poll() is None, "desktop host exited before readiness"
                    status_probe = subprocess.run([binary, "computer", "status", "--state-dir", desktop_state],
                                                  capture_output=True, text=True, timeout=5)
                    if status_probe.returncode == 0 and json.loads(status_probe.stdout).get("running"):
                        desktop_endpoint = json.loads(pathlib.Path(host_log.name).read_text().splitlines()[0])["endpoint"]
                        break
                    assert time.monotonic() < deadline, "desktop host did not become ready"
                    time.sleep(.1)
            for name, port in ports.items():
                node = work / name
                (node / "workspace").mkdir(parents=True)
                cfg = xmesh_config(name, key_file, port, control)
                cfg["agent"].update({"default_cwd": str(node / "workspace"),
                                     "cwd_policy": {"allow": [str(node / "workspace")]}})
                if name == "hs-b" and desktop_endpoint:
                    cfg["agent"]["computer"] = {"enabled": True, "endpoint": desktop_endpoint}
                if name == "hs-a" and desktop_endpoint:
                    cfg["gateway"]["pats"] = {
                        "peer-test": {"token_file": str(peer_pat_file), "scopes": ["devices:read", "sys:*", "exec:*", "pty:*", "fs:read:*", "fs:write:*"]},
                        "computer-test": {"token_file": str(desktop_pat_file), "scopes": ["devices:read", "computer:control:hs-b"]}}
                command = [binary, "serve", "--config", node / "config.json", "--state-dir", node / "state"]
                if name == "hs-i":
                    cfg["node"]["roles"].append("ingress")
                    cfg["mesh"]["authkey_file"] = str(ingress_key_file)
                    cfg["ingress"] = {"listen": "tunnel", "oauth": {"issuer": issuer, "clients": {
                        "codex": {"scopes": ["devices:read", "exec:*"]},
                        **({"computer-test": {"scopes": ["devices:read", "computer:control:hs-b"]}} if desktop_endpoint else {})}}}
                    command += ["--ingress", "--tunnel-listen", tunnel, "--oauth-password-file", password_file]
                (node / "config.json").write_text(json.dumps(cfg))
                process, log = spawn(command, name)
                nodes[name] = (process, log, f"127.0.0.1:{port}")
            tokens = {name: peer_pat if name == "hs-a" and desktop_endpoint else wait_for_default_token(work / name / "state") for name in nodes}
            clients = {name: wait_for_gateway(process, gateway, log, tokens[name], seconds=90)
                       for name, (process, log, gateway) in nodes.items()}
            results = []
            for source, target in (("hs-a", "hs-b"), ("hs-b", "hs-a")):
                results.append(verify_peer(nodes[source][2], target, options.expected_os, token=tokens[source]))
            desktop_checks = []
            if desktop_endpoint:
                desktop_client = MCP(nodes["hs-a"][2], desktop_pat)
                try:
                    desktop_checks.append({"route": "peer", **verify_computer(desktop_client, "hs-b")})
                finally:
                    desktop_client.close()
            for client in clients.values():
                client.close()

            probe = None
            if not options.skip_go_test:
                probe_dir = work / "probe"
                (probe_dir / "workspace").mkdir(parents=True)
                cfg = xmesh_config("hs-probe", key_file, 0, control)
                cfg["node"]["roles"] = ["agent"]
                cfg.pop("gateway")
                cfg["agent"].update({"default_cwd": str(probe_dir / "workspace"),
                                     "cwd_policy": {"allow": [str(probe_dir / "workspace")]}})
                (probe_dir / "config.json").write_text(json.dumps(cfg))
                env = dict(child_env, XMESH_TEST_TAILNET_CONFIG=str(probe_dir / "config.json"),
                           XMESH_TEST_TAILNET_STATE=str(probe_dir / "state"), XMESH_TEST_TAILNET_PEER="hs-b",
                           XMESH_TEST_TAILNET_VERBOSE="1")
                for attempt in (1, 2):
                    probe = subprocess.run([probe_test, "-test.run", "^Test_TailnetPeerConnectivity$",
                                            "-test.v", "-test.count=1", "-test.timeout=120s"], cwd=ROOT / "internal" / "mesh",
                                           env=env, capture_output=True, text=True)
                    sys.stderr.write(f"--- go test attempt {attempt}\n{probe.stdout}{probe.stderr}")
                    if probe.returncode == 0 and "--- PASS: Test_TailnetPeerConnectivity" in probe.stdout:
                        break
                else:
                    raise AssertionError("tailnet probe failed twice")
            oauth = None
            if options.ingress:
                oauth = json.loads(sh(sys.executable, HERE / "oauth_client.py", "--base", tunnel, "--issuer", issuer,
                                      "--password-file", password_file, "--preregistered", "codex",
                                      "--device", "hs-a", "--device", "hs-b"))
                if desktop_endpoint:
                    endpoint = Endpoint(issuer + "=" + tunnel)
                    access = obtain_tokens(endpoint, "computer-test", "devices:read computer:control:hs-b", password_file.read_text())
                    session = Session(endpoint, access["access_token"])
                    try:
                        desktop_checks.append({"route": "oauth-ingress", **verify_computer(SimpleNamespace(request=session.rpc), "hs-b")})
                    finally:
                        session.close()
            if options.force_derp:
                deadline = time.monotonic() + 10
                while True:
                    peers = json.loads(sh(binary, "control", "peers", "--state-dir", work / "hs-a" / "state"))["peers"]
                    target = next((peer for peer in peers if peer["name"] == "hs-b"), {})
                    if target.get("connection") == "derp" and target.get("derp_region"):
                        break
                    assert time.monotonic() < deadline, {"expected": "derp", "peer_state": target}
                    time.sleep(.1)
            print(json.dumps({"result": "PASS", "peer_checks": results, "tailnet_probe": probe is not None,
                              "oauth": oauth, "path": "derp" if options.force_derp else "direct",
                              **({"computer": desktop_checks} if desktop_checks else {}),
                              "checks": ["join", "peer-tools-both-ways"] + (["tailnet-connectivity"] if probe else [])
                              + (["oauth-protocol"] if oauth else [])}))
        except BaseException:
            for log in logs:
                log.seek(0)
                sys.stderr.write(f"--- {pathlib.Path(log.name).name}\n{log.read()[-4000:]}\n")
            raise
        finally:
            for process in reversed(processes):
                if process.poll() is None:
                    try:
                        stop_runtime(process)
                    except AssertionError as error:
                        print("shutdown:", error, file=sys.stderr)


if __name__ == "__main__":
    main()
