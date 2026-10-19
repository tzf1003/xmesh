#!/usr/bin/env python3
"""Gate 2 fixed scenario: an exclusive stdio backend, two MCP sessions, an external kill.

The backend is scripts/fake_ida.py. The scenario follows docs/conformance.md, section gate 2:
the backend records the arguments it receives and none of them is `_lease_token`; after the
backend process is killed from outside, the old token fails on the next call and a new
acquire returns a larger token.
"""

# conformance: CON-TOOL-11 INV-04 RUL-06 CTR-BE-01 CTR-BE-02 CTR-LEASE-01
import argparse
import http.client
import json
import os
import pathlib
import signal
import subprocess
import sys
import tempfile
import time

from smoke_local import MCP, address, stop_runtime, wait_for_default_token, wait_for_gateway

HERE = pathlib.Path(__file__).resolve().parent


def records(log):
    if not log.exists():
        return []
    return [json.loads(line) for line in log.read_text(encoding="utf-8").splitlines() if line.strip()]


def calls(log):
    return [item for item in records(log) if item["event"] == "call"]


def error_of(result):
    assert result.get("isError"), result
    return result.get("structuredContent") or {}


def attempt(session, name, arguments):
    """tools/call that reports an MCP "unknown tool" protocol error as an error result.

    While the backend is down its tools can leave the Gateway catalog, so a call then fails at the
    protocol level instead of with a tool error.
    """
    try:
        return session.request("tools/call", {"name": name, "arguments": arguments})
    except AssertionError as failure:
        detail = failure.args[0] if failure.args else None
        if isinstance(detail, dict) and "unknown tool" in str(detail.get("error", {}).get("message", "")):
            return {"isError": True, "structuredContent": {"error": "unknown_tool"}}
        raise


def settled(session, name, arguments, seconds=20):
    """Call a tool, retrying while the Gateway reports the device connection as dropped.

    Right after the backend is killed, the first call through the Gateway can come back as
    not_found (device connection dropped) while the Gateway reconnects. The scenario cares about
    what the lease says once the connection is back, so wait that window out.
    """
    deadline = time.monotonic() + seconds
    while True:
        result = attempt(session, name, arguments)
        if not (result.get("isError") and (result.get("structuredContent") or {}).get("error") in ("not_found", "unknown_tool")):
            return result
        assert time.monotonic() < deadline, "device connection did not come back: %r" % result
        time.sleep(0.2)


def kill(pid):
    # SIGKILL does not exist on Windows; os.kill maps any other signal to TerminateProcess there.
    os.kill(pid, getattr(signal, "SIGKILL", signal.SIGTERM))


def wait_until(predicate, what, seconds=20):
    deadline = time.monotonic() + seconds
    while True:
        value = predicate()
        if value:
            return value
        assert time.monotonic() < deadline, "timed out waiting for " + what
        time.sleep(0.1)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=pathlib.Path)
    options = parser.parse_args()
    binary = options.binary.resolve()
    with tempfile.TemporaryDirectory(prefix="xmesh-backend-smoke-") as temporary:
        root = pathlib.Path(temporary).resolve()
        workspace = root / "workspace"
        workspace.mkdir()
        log = root / "fake-ida.jsonl"
        direct, gateway = address(), address()
        cfg = {"schema_version": 2, "node": {"name": "backend-smoke", "roles": ["agent", "gateway"]},
               "mesh": {"lan": {"enabled": True, "mdns": True}, "tailnet": {"enabled": False}}, "gateway": {"listen": gateway},
               "agent": {"default_cwd": str(workspace), "cwd_policy": {"allow": [str(workspace)]},
                         "authz": {"tag:peer": {"tools": ["*"], "backends": ["*"]}},
                         "backends": {"ida": {"transport": "stdio", "policy": "exclusive", "idle_timeout": "30m",
                                              "command": [sys.executable, str(HERE / "fake_ida.py"), str(log)]}}}}
        config = root / "config.json"
        config.write_text(json.dumps(cfg))
        with (root / "runtime.log").open("w+") as runtime_log:
            process = subprocess.Popen([str(binary), "serve", "--local", "--config", str(config),
                                        "--state-dir", str(root / "state"), "--listen", direct],
                                       stdout=runtime_log, stderr=runtime_log)
            try:
                gateway_token = wait_for_default_token(root / "state")
                first = wait_for_gateway(process, gateway, runtime_log, gateway_token)
                second = MCP(gateway, gateway_token)
                assert first.session != second.session
                # The backend starts with the Agent; remember which process it is.
                first_pid = wait_until(lambda: next((r["pid"] for r in records(log) if r["event"] == "start"), None),
                                       "the backend to start")
                wanted = {"local/ida.echo", "local/backend.acquire", "local/backend.renew", "local/backend.release"}
                # The Gateway fills its catalog from the Agent shortly after it starts listening.
                wait_until(lambda: wanted <= {tool["name"] for tool in first.request("tools/list", {})["tools"]},
                           "the Gateway to list the backend tools")

                # Lease: the first session holds it, the second one cannot use or take it.
                lease = first.call("local/backend.acquire", {"name": "ida", "ttl": "5m"})
                token = lease["token"]
                assert lease["resource"] == "backend:ida" and token >= 1, lease
                missing = error_of(second.request("tools/call", {"name": "local/ida.echo", "arguments": {}}))
                assert missing["error"] == "lease_required", missing
                borrowed = error_of(second.request("tools/call", {
                    "name": "local/ida.echo", "arguments": {"_lease_token": token}}))
                assert borrowed["error"] == "resource_busy" and borrowed["reason"] == "token_mismatch", borrowed
                busy = error_of(second.request("tools/call", {"name": "local/backend.acquire",
                                                              "arguments": {"name": "ida", "ttl": "5m"}}))
                assert busy["error"] == "resource_busy" and busy["reason"] == "held", busy
                assert not calls(log), "a rejected call reached the backend: %r" % calls(log)

                # Calls with the token pass through, with the token removed and ordinary fields intact.
                echoed = first.call("local/ida.echo", {"_lease_token": token, "token": "business-field", "value": 7})
                assert echoed["arguments"] == {"token": "business-field", "value": 7}, echoed
                seen = calls(log)
                assert len(seen) == 1 and "_lease_token" not in json.dumps(seen[0]), seen
                assert first.call("local/backend.renew", {"name": "ida", "_lease_token": token, "ttl": "5m"})["token"] == token

                # Kill the backend from outside the Agent.
                kill(first_pid)
                # The next call with the old token must fail. Besides the lease errors, not_found and
                # unknown_tool are tolerated here: see settled() and attempt().
                crashed = error_of(attempt(first, "local/ida.echo", {"_lease_token": token}))
                assert crashed["error"] in ("backend_unavailable", "resource_busy", "not_found", "unknown_tool"), crashed
                if crashed["error"] == "resource_busy":
                    assert crashed["reason"] == "token_stale", crashed

                # The Agent restarts it; the old token stays dead, a fresh acquire is larger.
                second_pid = wait_until(
                    lambda: next((r["pid"] for r in records(log) if r["event"] == "start" and r["pid"] != first_pid), None),
                    "the backend to restart")
                assert second_pid != first_pid

                def reacquire():
                    result = settled(second, "local/backend.acquire", {"name": "ida", "ttl": "5m"})
                    return None if result.get("isError") else result["structuredContent"]

                fresh = wait_until(reacquire, "a new lease after the crash")
                assert fresh["token"] > token, (fresh, token)
                stale = error_of(settled(first, "local/ida.echo", {"_lease_token": token}))
                assert stale["error"] == "resource_busy" and stale["reason"] in ("token_stale", "token_mismatch"), stale
                after = settled(second, "local/ida.echo", {"_lease_token": fresh["token"], "value": "after-restart"})
                assert not after.get("isError"), after
                after = after["structuredContent"]
                assert after["pid"] == second_pid and after["arguments"] == {"value": "after-restart"}, after
                assert all("_lease_token" not in json.dumps(item) for item in calls(log))
                assert second.call("local/backend.release", {"name": "ida", "_lease_token": fresh["token"]})["released"]
                first.close()
                second.close()
            finally:
                stop_runtime(process)
            print(json.dumps({"result": "PASS", "backend_pids": [first_pid, second_pid], "tokens": [token, fresh["token"]],
                              "checks": ["lease-required", "token-mismatch", "held", "token-stripped", "external-kill",
                                         "restart", "stale-token", "larger-token"]}))


if __name__ == "__main__":
    main()
