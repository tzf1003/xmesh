#!/usr/bin/env python3
"""Exercise LAN/Tailnet coexistence in two Docker network namespaces.

Requires Docker, openssl, a native xmesh binary and xmesh/Headscale binaries for
Docker's platform. All containers, network, credentials and files belong to this
run and are removed on exit. A temporary test CA is trusted only by its processes.
Use --force-derp to repeat the same resource recovery checks through DERP.
"""

import argparse, base64, concurrent.futures, hashlib, json, os, pathlib, secrets, ssl, subprocess, sys, tempfile, time, urllib.request

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from smoke_local import MCP

parser = argparse.ArgumentParser()
parser.add_argument("--binary", required=True)
parser.add_argument("--host-binary")
parser.add_argument("--headscale", required=True)
parser.add_argument(
    "--force-derp",
    action="store_true",
    help="Disable direct UDP in tsnet so peer traffic uses the embedded DERP",
)
options = parser.parse_args()
image = "python:3.12-slim-bookworm"
container_user = f"{os.getuid()}:{os.getgid()}"
suffix = secrets.token_hex(4)
network = "xmesh-coexist-" + suffix
containers = []


def run(*args, timeout=30):
    done = subprocess.run(
        list(map(str, args)), capture_output=True, text=True, timeout=timeout
    )
    if done.returncode:
        raise AssertionError(str(args[0]) + " failed: " + done.stderr[-2000:])
    return done.stdout.strip()


def docker(*args, timeout=30):
    return run("docker", *args, timeout=timeout)


def control(name, *args):
    return json.loads(
        docker(
            "exec",
            name,
            "/bin/xmesh",
            "control",
            *args,
            "--state-dir",
            f"/work/{name}/state",
        )
    )


def wait_for(label, predicate, seconds=45):
    deadline = time.monotonic() + seconds
    last = None
    while time.monotonic() < deadline:
        try:
            last = predicate()
            if last:
                return last
        except (OSError, ValueError, AssertionError, TimeoutError) as error:
            last = str(error)
        time.sleep(0.15)
    raise AssertionError(label + " not reached: " + str(last))


def pairrecord(root, name):
    return json.loads((root / name / "device-pair.json").read_text())


with tempfile.TemporaryDirectory(prefix="xmesh-coexist-live-") as temp:
    root = pathlib.Path(temp).resolve()
    try:
        print("creating isolated network", flush=True)
        docker("network", "create", network)
        cnf = root / "tls.cnf"
        cnf.write_text(
            "[req]\ndistinguished_name=dn\nx509_extensions=extensions\nprompt=no\n[dn]\nCN=hs-control\n[extensions]\nsubjectAltName=DNS:hs-control,DNS:localhost,IP:127.0.0.1\nbasicConstraints=critical,CA:TRUE\nkeyUsage=critical,digitalSignature,keyEncipherment,keyCertSign\nextendedKeyUsage=serverAuth\n"
        )
        run(
            "openssl",
            "req",
            "-x509",
            "-newkey",
            "rsa:2048",
            "-nodes",
            "-days",
            "1",
            "-config",
            cnf,
            "-keyout",
            root / "control.key",
            "-out",
            root / "control.crt",
        )
        (root / "policy.json").write_text(
            json.dumps(
                {
                    "tagOwners": {"tag:device": ["xmesh@"]},
                    "acls": [
                        {
                            "action": "accept",
                            "src": ["100.64.0.0/10", "fd7a:115c:a1e0::/48"],
                            "dst": ["100.64.0.0/10:*", "fd7a:115c:a1e0::/48:*"],
                        }
                    ],
                }
            )
        )
        hs = "hs-control-" + suffix
        config = 'server_url: https://hs-control:18090\nlisten_addr: 0.0.0.0:18090\ntls_cert_path: /work/control.crt\ntls_key_path: /work/control.key\nmetrics_listen_addr: 127.0.0.1:19090\ngrpc_listen_addr: 127.0.0.1:19091\ngrpc_allow_insecure: true\nnoise:\n  private_key_path: /work/noise.key\nprefixes:\n  v4: 100.64.0.0/10\n  v6: fd7a:115c:a1e0::/48\n  allocation: sequential\ndatabase:\n  type: sqlite\n  sqlite:\n    path: /work/db.sqlite\nderp:\n  server:\n    enabled: true\n    region_id: 999\n    region_code: xmesh\n    region_name: xmesh-test\n    stun_listen_addr: 0.0.0.0:3478\n    private_key_path: /work/derp.key\n    automatically_add_embedded_derp_region: true\n  urls: []\n  paths: []\n  auto_update_enabled: false\ndns:\n  magic_dns: false\n  base_domain: xmesh.test\n  override_local_dns: false\n  nameservers:\n    global: []\npolicy:\n  mode: file\n  path: /work/policy.json\nunix_socket: /work/headscale.sock\nunix_socket_permission: "0770"\nlog:\n  level: info\n'
        (root / "headscale.yaml").write_text(config)
        docker(
            "run",
            "--user",
            container_user,
            "-e",
            "HOME=/tmp",
            "-d",
            "--name",
            hs,
            "--network",
            network,
            "--network-alias",
            "hs-control",
            "-p",
            "127.0.0.1::18090",
            "-v",
            f"{root}:/work",
            "-v",
            f"{pathlib.Path(options.headscale).resolve()}:/bin/headscale:ro",
            image,
            "/bin/headscale",
            "-c",
            "/work/headscale.yaml",
            "serve",
        )
        containers.append(hs)
        port = docker("port", hs, "18090/tcp").rsplit(":", 1)[1]
        tls = ssl.create_default_context(cafile=str(root / "control.crt"))
        wait_for(
            "Headscale health",
            lambda: (
                urllib.request.urlopen(
                    "https://127.0.0.1:" + port + "/health", context=tls, timeout=2
                ).status
                == 200
            ),
        )

        def hscli(*args):
            return json.loads(
                docker(
                    "exec",
                    hs,
                    "/bin/headscale",
                    "-c",
                    "/work/headscale.yaml",
                    "-o",
                    "json",
                    *args,
                )
            )

        user = hscli("users", "create", "xmesh")
        key = hscli(
            "preauthkeys",
            "create",
            "--user",
            str(user["id"]),
            "--reusable",
            "--tags",
            "tag:device",
            "--expiration",
            "1h",
        )["key"]
        (root / "authkey").write_text(key)
        (root / "authkey").chmod(384)
        names = ["coexist-a-" + suffix, "coexist-b-" + suffix]
        docs = {}
        configs = {}
        (root / "fake_ida.py").write_text(
            pathlib.Path(__file__).resolve().with_name("fake_ida.py").read_text()
        )
        for name in names:
            node = root / name
            (node / "workspace").mkdir(parents=True)
            cfg = {
                "schema_version": 2,
                "node": {"name": name, "roles": ["agent", "gateway"]},
                "mesh": {
                    "lan": {"enabled": True, "mdns": True},
                    "tailnet": {
                        "enabled": True,
                        "control_url": "https://hs-control:18090",
                        "authkey_file": "/work/authkey",
                    },
                },
                "gateway": {"listen": "0.0.0.0:8080", "access": ["localhost", "lan"]},
                "agent": {
                    "default_cwd": f"/work/{name}/workspace",
                    "cwd_policy": {"allow": [f"/work/{name}/workspace"]},
                    "authz": {"tag:peer": {"tools": ["*"], "backends": ["*"]}},
                },
            }
            cfg["agent"]["backends"] = {
                kind: {
                    "transport": "stdio",
                    "policy": policy,
                    "idle_timeout": "30m",
                    "command": [
                        "python3",
                        "/work/fake_ida.py",
                        f"/work/{name}/{kind}.jsonl",
                    ],
                }
                for kind, policy in [
                    ("exclusive", "exclusive"),
                    ("session", "per-caller"),
                ]
            }
            configs[name] = cfg
            (node / "config.json").write_text(json.dumps(cfg))
            run(
                options.host_binary or options.binary,
                "pair",
                "--config",
                node / "config.json",
                "--state-dir",
                node / "state",
                "--export",
                node / "device-pair.json",
            )
            docs[name] = pairrecord(root, name)
        for a, b in [names, names[::-1]]:
            run(
                options.host_binary or options.binary,
                "pair",
                "--config",
                root / a / "config.json",
                "--state-dir",
                root / a / "state",
                "--import",
                root / b / "device-pair.json",
                "--fingerprint",
                docs[b]["fingerprint"],
            )
        gateways = {}
        for name in names:
            docker(
                "run",
                "--user",
                container_user,
                "-e",
                "HOME=/tmp",
                "-d",
                "--name",
                name,
                "--network",
                network,
                "-p",
                "127.0.0.1::8080",
                "-v",
                f"{root}:/work",
                "-v",
                f"{pathlib.Path(options.binary).resolve()}:/bin/xmesh:ro",
                "-e",
                "SSL_CERT_FILE=/work/control.crt",
                *(
                    ["-e", "TS_DEBUG_ALWAYS_USE_DERP=true"]
                    if options.force_derp
                    else []
                ),
                image,
                "/bin/xmesh",
                "serve",
                "--config",
                f"/work/{name}/config.json",
                "--state-dir",
                f"/work/{name}/state",
            )
            containers.append(name)
            gateways[name] = (
                "127.0.0.1:" + docker("port", name, "8080/tcp").rsplit(":", 1)[1]
            )

        def peers(name):
            return control(name, "peers")["peers"]

        def remote(name):
            return next((p for p in peers(name) if not p["self"]), {})

        for name in names:
            wait_for(
                name + " both paths",
                lambda name=name: (
                    {p["kind"] for p in remote(name).get("paths", []) if p["reachable"]}
                    == {"lan", "tailnet"}
                ),
                90,
            )
        a, b = names
        print("both paths discovered", flush=True)
        first = remote(a)
        assert first["active_path"] == "lan", first
        assert len(peers(a)) == 2, peers(a)
        token = (root / a / "state" / "pats" / "default.token").read_text().strip()
        client = MCP(gateways[a], token)
        prefix = b + "/"
        call = lambda name, args=None: client.call(prefix + name, args)
        wait_for(
            "backend catalog",
            lambda: (
                prefix + "session.echo"
                in {t["name"] for t in client.request("tools/list", {})["tools"]}
            ),
        )
        terminal = call("pty.open", {"cols": 80, "rows": 24})
        sid = terminal["session_id"]
        backend_pid = call("session.echo")["pid"]
        lease = call("backend.acquire", {"name": "exclusive", "ttl": "8s"})
        body = bytes([0, 255, 13, 10]) * 2**19
        digest = hashlib.sha256(body).hexdigest()
        upload = call(
            "fs.upload",
            {
                "path": "artifact.bin",
                "byte_offset": 0,
                "data": base64.b64encode(body[: 2**20]).decode(),
                "sha256": digest,
                "final": False,
            },
        )
        rootb = root / b / "workspace"
        count, started, release = (
            rootb / "count",
            rootb / "started",
            rootb / "release",
        )
        command = f"echo execution >> /work/{b}/workspace/count; touch /work/{b}/workspace/started; while [ ! -f /work/{b}/workspace/release ]; do sleep 0.05; done; printf EXEC_DONE"
        executor = concurrent.futures.ThreadPoolExecutor(1)
        future = executor.submit(
            call,
            "exec.run",
            {"cmd": "sh", "args": ["-c", command], "idempotency_key": "live-once"},
        )
        wait_for("exec running", lambda: started.exists())

        def configure(name, kind, enabled):
            configs[name] = json.loads((root / name / "config.json").read_text())
            configs[name]["mesh"][kind]["enabled"] = enabled
            (root / name / "config.json").write_text(json.dumps(configs[name]))
            control(name, "config", "reload")

        start = time.monotonic()
        for name in names:
            configure(name, "lan", False)
        wait_for(
            "Tailnet failover", lambda: remote(a).get("active_path") == "tailnet", 20
        )
        elapsed = time.monotonic() - start
        release.write_text("continue")
        result = future.result(timeout=20)
        assert result["stdout"] == "EXEC_DONE", result
        assert count.read_text().count("execution") == 1
        owner = call("pty.list")
        assert any(
            (
                p["id"] == sid
                and p["owner"] == terminal["owner"]
                and (p["pid"] == terminal["pid"])
                for p in owner["sessions"]
            )
        ), owner
        assert call("session.echo")["pid"] == backend_pid
        time.sleep(9)
        assert call("exclusive.echo", {"_lease_token": lease["token"]})["pid"]
        final = call(
            "fs.upload",
            {
                "path": "artifact.bin",
                "transfer_id": upload["transfer_id"],
                "byte_offset": 2**20,
                "data": base64.b64encode(body[2**20 :]).decode(),
                "sha256": digest,
                "final": True,
            },
        )
        assert final["sha256"] == digest, final
        download = call("fs.download", {"path": "artifact.bin"})
        for name in names:
            configure(name, "lan", True)
        wait_for(
            "LAN restored",
            lambda: any(
                (
                    p["kind"] == "lan" and p["reachable"]
                    for p in remote(a).get("paths", [])
                )
            ),
            30,
        )
        assert remote(a)["active_path"] == "tailnet", remote(a)
        print("Tailnet retained after LAN recovery", flush=True)
        members = hscli("nodes", "list")
        target = next(
            (
                p
                for p in members
                if p.get("given_name") == a
                or p.get("hostname") == a
                or p.get("name") == a
            )
        )
        docker(
            "exec",
            hs,
            "/bin/headscale",
            "-c",
            "/work/headscale.yaml",
            "nodes",
            "delete",
            "--identifier",
            str(target["id"]),
            "--force",
        )
        wait_for(
            "LAN after Tailnet revocation",
            lambda: remote(a).get("active_path") == "lan",
            25,
        )
        assert call("sys.info")["hostname"] == b
        after = call("pty.list")
        assert any(
            (
                p["id"] == sid and p["owner"] == terminal["owner"]
                for p in after["sessions"]
            )
        ), after
        second = call(
            "fs.download",
            {
                "path": "artifact.bin",
                "transfer_id": download["transfer_id"],
                "byte_offset": download["next_offset"],
            },
        )
        assert (
            base64.b64decode(download["data"]) + base64.b64decode(second["data"])
            == body
        )
        assert second["file_sha256"] == digest
        call("backend.release", {"name": "exclusive", "_lease_token": lease["token"]})
        call("pty.close", {"session_id": sid})
        audit = control(a, "audit")["events"]
        assert any(
            (
                e["event"] == "device_link_online"
                and e["tool"] == "device.link.tailnet"
                for e in audit
            )
        ), audit
        control(
            b,
            "pair",
            "import",
            "--file",
            f"/work/{a}/device-pair.json",
            "--fingerprint",
            docs[a]["fingerprint"],
            "--tags",
            "tag:admin",
        )
        docker(
            "exec",
            a,
            "/bin/xmesh",
            "drain",
            "--config",
            f"/work/{a}/config.json",
            "--state-dir",
            f"/work/{a}/state",
            "--scope",
            "device",
            "--device",
            docs[b]["device_id"],
        )
        assert control(b, "status")["drain"]["draining"]
        client.close()
        executor.shutdown(wait=True)
        print(
            json.dumps(
                {
                    "result": "PASS",
                    "platform": "linux/arm64",
                    "network_namespaces": 2,
                    "forced_derp": options.force_derp,
                    "checks": [
                        "single-device-two-paths",
                        "initial-lan",
                        "in-flight-exec-once",
                        "pty-identity",
                        "tailnet-failover",
                        "sticky-tailnet",
                        "tailnet-revocation-independent-trust",
                        "lan-recovery",
                        "per-caller-backend",
                        "gateway-lease-heartbeat",
                        "chunked-upload-download-hash",
                        "remote-drain-device-trust",
                    ],
                    "failover_seconds": round(elapsed, 3),
                }
            ),
            flush=True,
        )
    except BaseException:
        for name in containers:
            try:
                if name.startswith("coexist-"):
                    print(
                        json.dumps(
                            {
                                "node": name,
                                "status": control(name, "status"),
                                "peers": control(name, "peers"),
                                "discovered": control(name, "pair", "discover"),
                            }
                        ),
                        file=sys.stderr,
                    )
            except BaseException:
                pass
            try:
                output = subprocess.run(
                    ["docker", "logs", "--tail", "30", name],
                    capture_output=True,
                    text=True,
                )
                print(
                    name + " logs: " + (output.stdout + output.stderr)[-5000:],
                    file=sys.stderr,
                )
            except BaseException:
                pass
        raise
    finally:
        for name in containers[::-1]:
            try:
                docker("rm", "-f", name)
            except BaseException:
                pass
        try:
            docker("network", "rm", network)
        except BaseException:
            pass
