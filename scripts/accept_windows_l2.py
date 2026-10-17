#!/usr/bin/env python3
"""Windows L2 with the AppContainer runner on: what must hold on a node that can run it.

Run it on the Windows machine, against a node started with `agent.policy.isolation: L2` and
`agent.policy.windows_appcontainer: true` (see docs/validation/windows-l2-acceptance.md). Reading
the NTFS change journal needs an elevated Administrator or a LocalSystem service, so the success
path below only applies to such a node. On an ordinary account the node refuses L2 at startup:
run with --expect-refused to check that it says why. Every command below runs inside the sandbox
through `exec.run`. PASS means:

  1. sys.info reports no sandbox.unavailable warning (the runner started and recovered),
  2. a command cannot read a file outside the workspace,
  3. a command can write inside the workspace and the file is there afterwards,
  4. a command cannot open the node's control pipe,

With --expect-refused, PASS means instead: sys.info warns about sandbox.unavailable and gives a
reason, and exec.run is refused with policy_denied / sandbox.unavailable and a reason, and does
not run the command.

  python3 scripts/accept_windows_l2.py --gateway 127.0.0.1:8080 --pat-file C:\\xmesh\\gateway.pat ^
      --workspace C:\\xmesh\\work --state-dir C:\\xmesh\\state
"""

import argparse
import json
import pathlib
import sys
import tempfile
import uuid

from smoke_local import MCP

SECRET = "host-only-secret"


def run(client, *args):
    result = client.request("tools/call", {"name": "local/exec.run", "arguments": {"cmd": "cmd.exe", "args": ["/d", "/c", *args]}})
    content = result.get("structuredContent") or {}
    return {"is_error": bool(result.get("isError")), "exit_code": content.get("exit_code"), "error": content.get("error"),
            "rule": content.get("rule"), "reason": content.get("reason"), "text": f"{content.get('stdout', '')}{content.get('stderr', '')}"}


def judge_refusal(warnings, result):
    """Checks for a node whose L2 runner is unavailable: refused, and the reason is visible."""
    named = "sandbox.unavailable" in warnings and len(warnings.split("sandbox.unavailable", 1)[1].strip(" ：:）)\n")) > 0
    refused = result["error"] == "policy_denied" and result["rule"] == "sandbox.unavailable"
    return [
        {"check": "sys.info names sandbox.unavailable with a reason", "ok": named, "detail": warnings or "no warnings"},
        {"check": "exec.run is refused with sandbox.unavailable", "ok": refused and result["exit_code"] is None, "detail": result},
        {"check": "exec.run refusal carries a reason", "ok": bool(result.get("reason")), "detail": result},
    ]


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--gateway", default="127.0.0.1:8080")
    parser.add_argument("--pat-file", type=pathlib.Path)
    parser.add_argument("--expect-refused", action="store_true", help="expect L2 to be unavailable (ordinary account) and check the refusal explains why")
    parser.add_argument("--workspace", type=pathlib.Path, required=True, help="the node's default_cwd")
    parser.add_argument("--state-dir", type=pathlib.Path, required=True, help="the node's state directory (holds control.json)")
    options = parser.parse_args(argv)
    token = options.pat_file.read_text().strip() if options.pat_file else None
    checks = []

    def check(name, ok, detail):
        checks.append({"check": name, "ok": bool(ok), "detail": detail})

    outside = pathlib.Path(tempfile.mkdtemp(prefix="xmesh-l2-outside-"))
    secret = outside / "secret.txt"
    secret.write_text(SECRET)
    marker = f"l2-accept-{uuid.uuid4().hex[:8]}.txt"
    client = MCP(options.gateway, token)
    try:
        info = client.call("local/sys.info")
        warnings = "\n".join(str(item) for item in info.get("warnings") or [])
        if options.expect_refused:
            checks.extend(judge_refusal(warnings, run(client, "echo", "x")))
            print(json.dumps({"result": "PASS" if all(item["ok"] for item in checks) else "FAIL", "checks": checks}, indent=2))
            return 0 if all(item["ok"] for item in checks) else 1
        check("runner started", "sandbox.unavailable" not in warnings, warnings or "no warnings")

        read = run(client, "type", str(secret))
        check("read outside the workspace is refused", read["exit_code"] not in (0, None) and SECRET not in read["text"], read)

        write = run(client, f"echo x>{marker}")
        check("write inside the workspace works", write["exit_code"] == 0 and (options.workspace / marker).exists(), write)

        try:
            endpoint = json.loads((options.state_dir / "control.json").read_text())["endpoint"]
        except (OSError, ValueError, KeyError) as error:
            check("control pipe is refused", False, f"control.json unreadable: {error}")
        else:
            pipe = run(client, "type", endpoint)
            check("control pipe is refused", pipe["exit_code"] not in (0, None) and "denied" in pipe["text"].lower(), pipe)
    finally:
        client.close()
        try:
            (options.workspace / marker).unlink()
        except OSError:
            pass
        try:
            secret.unlink()
            outside.rmdir()
        except OSError:
            pass
    good = all(item["ok"] for item in checks)
    print(json.dumps({"result": "PASS" if good else "FAIL", "checks": checks}, indent=2))
    return 0 if good else 1


if __name__ == "__main__":
    sys.exit(main())
