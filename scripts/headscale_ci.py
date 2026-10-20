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

Only the direct path is covered: all nodes share the host, so they find each other directly even though
the embedded DERP server exists (it is there so that netchecks succeed and magicsock does not keep rebinding).
Proving the DERP fallback (CON-NET-05) still needs the multi-host run in the validation notes.
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
import ssl

from smoke_local import MCP, address, stop_runtime, wait_for_default_token, wait_for_gateway
from smoke_peer import verify_peer

HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parent
CONTROL_PORT = 18090


def sh(*command, **options):
    done = subprocess.run([str(part) for part in command], capture_output=True, text=True, **options)
    if done.returncode:
        raise AssertionError("%s exited %d\n%s%s" % (pathlib.Path(str(command[0])).name, done.returncode, done.stdout, done.stderr))
    return done.stdout


def headscale_config(work, port, tls=False):
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
        f"server_url: {'https' if tls else 'http'}://127.0.0.1:{port}",
        f"listen_addr: 127.0.0.1:{port}",
        *([f"tls_cert_path: {work / 'control.crt'}", f"tls_key_path: {work / 'control.key'}"] if tls else []),
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
    return {"schema_version": 2, "node": {"name": name, "roles": ["agent", "gateway"]},
            "mesh": {"lan": {"enabled": False}, "tailnet": {"enabled": True, "control_url": control, "authkey_file": str(key), "ephemeral": True}},
            "gateway": {"listen": f"127.0.0.1:{port}"},
            "agent": {"authz": {"tag:peer": grant, "tag:ingress": grant}}}


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--headscale", required=True, type=pathlib.Path)
    parser.add_argument("--binary", required=True, type=pathlib.Path)
    parser.add_argument("--expected-os", default={"linux": "linux", "darwin": "darwin"}.get(sys.platform, "linux"))
    parser.add_argument("--skip-go-test", action="store_true")
    parser.add_argument("--ingress", action="store_true", help="also run the OAuth protocol check through an ingress on hs-a")
    options = parser.parse_args()
    headscale, binary = options.headscale.resolve(), options.binary.resolve()
    control = f"https://127.0.0.1:{CONTROL_PORT}"
    node_env = dict(os.environ)
    processes, logs = [], []

    def spawn(command, name):
        log = open(work / f"{name}.log", "w+")
        logs.append(log)
        process = subprocess.Popen([str(part) for part in command], stdout=log, stderr=log, env=node_env)
        processes.append(process)
        return process, log

    with tempfile.TemporaryDirectory(prefix="xmesh-headscale-") as temporary:
        work = pathlib.Path(temporary).resolve()
        try:
            (work / "tls.cnf").write_text("""[req]
distinguished_name=dn
x509_extensions=extensions
prompt=no
[dn]
CN=localhost
[extensions]
subjectAltName=IP:127.0.0.1,DNS:localhost
basicConstraints=critical,CA:TRUE
keyUsage=critical,digitalSignature,keyEncipherment,keyCertSign
extendedKeyUsage=serverAuth
""")
            sh("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
               "-config", work / "tls.cnf", "-keyout", work / "control.key", "-out", work / "control.crt")
            node_env["SSL_CERT_FILE"] = str(work / "control.crt")
            tls_context = ssl.create_default_context(cafile=str(work / "control.crt"))
            config = headscale_config(work, CONTROL_PORT, tls=True)
            hs, hs_log = spawn([headscale, "-c", config, "serve"], "headscale")
            deadline = time.monotonic() + 60
            while True:
                try:
                    urllib.request.urlopen(control + "/health", timeout=2, context=tls_context).read()
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
            for name, port in ports.items():
                node = work / name
                (node / "workspace").mkdir(parents=True)
                cfg = xmesh_config(name, key_file, port, control)
                cfg["agent"].update({"default_cwd": str(node / "workspace"),
                                     "cwd_policy": {"allow": [str(node / "workspace")]}})
                command = [binary, "serve", "--config", node / "config.json", "--state-dir", node / "state"]
                if name == "hs-i":
                    cfg["node"]["roles"].append("ingress")
                    cfg["mesh"]["tailnet"]["authkey_file"] = str(ingress_key_file)
                    cfg["ingress"] = {"listen": "tunnel", "oauth": {"issuer": issuer, "clients": {
                        "codex": {"scopes": ["devices:read", "exec:*"]}}}}
                    command += ["--ingress", "--tunnel-listen", tunnel, "--oauth-password-file", password_file]
                (node / "config.json").write_text(json.dumps(cfg))
                nodes[name] = (command, f"127.0.0.1:{port}")
            documents = {}
            for name in nodes:
                node = work / name
                pairfile = node / "device-pair.json"
                sh(binary, "pair", "--config", node / "config.json", "--state-dir", node / "state", "--export", pairfile)
                documents[name] = (pairfile, json.loads(pairfile.read_text())["fingerprint"])
            for source in nodes:
                for target in nodes:
                    if source == target:
                        continue
                    pairfile, fingerprint = documents[target]
                    node = work / source
                    tags = "tag:peer,tag:device" + (",tag:ingress" if target == "hs-i" else "")
                    sh(binary, "pair", "--config", node / "config.json", "--state-dir", node / "state",
                       "--import", pairfile, "--fingerprint", fingerprint, "--tags", tags)
            for name, (command, gateway) in list(nodes.items()):
                process, log = spawn(command, name)
                nodes[name] = (process, log, gateway)
            tokens = {name: wait_for_default_token(binary, work / name / "state") for name in nodes}
            clients = {name: wait_for_gateway(process, gateway, log, tokens[name], seconds=90)
                       for name, (process, log, gateway) in nodes.items()}
            results = []
            for source, target in (("hs-a", "hs-b"), ("hs-b", "hs-a")):
                results.append(verify_peer(nodes[source][2], target, options.expected_os, token=tokens[source]))
            for client in clients.values():
                client.close()

            device_password = work / "device-password"
            device_password.write_text("device-pairing-" + os.urandom(16).hex())
            device_password.chmod(0o600)
            sh(binary, "control", "pair", "password", "set", "--state-dir", work / "hs-a" / "state",
               "--password-file", device_password)
            sandbox = sh(sys.executable, HERE / "smoke_sandbox.py", "--binary", binary,
                         "--control-url", control, "--auth-key-file", key_file,
                         "--peer", "hs-a:" + options.expected_os,
                         "--peer-password-file", "hs-a=" + str(device_password),
                         "--observer-gateway", nodes["hs-a"][2],
                         "--observer-token-file", work / "hs-a" / "state" / "pats" / "default.token",
                         env=node_env, timeout=180)
            sandbox_result = json.loads(sandbox.strip().splitlines()[-1])

            # The application calls above traverse real tsnet paths and the version 2 device link.
            # Legacy unauthenticated Agent HTTP probes cannot validate this protocol.
            probe = None
            oauth = None
            if options.ingress:
                oauth = json.loads(sh(sys.executable, HERE / "oauth_client.py", "--base", tunnel, "--issuer", issuer,
                                      "--password-file", password_file, "--preregistered", "codex",
                                      "--device", "hs-a", "--device", "hs-b"))
            print(json.dumps({"result": "PASS", "peer_checks": results, "tailnet_probe": probe is not None,
                              "oauth": oauth, "path": "tailnet", "sandbox": sandbox_result,
                              "checks": ["join", "public-key-pairing", "device-link", "peer-tools-both-ways", "ephemeral-network-stable-device"] + (["tailnet-connectivity"] if probe else [])
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
