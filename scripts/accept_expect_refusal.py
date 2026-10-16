#!/usr/bin/env python3
"""Step F: a Windows node asked for L2 isolation must refuse, with rule sandbox.unavailable.

With `agent.policy.isolation: L2` and the AppContainer runner off (`agent.policy.windows_appcontainer`
unset, the default) the node has to refuse `exec.run` and `pty.open` instead of running the command
unisolated. With the runner on, use accept_windows_l2.py instead. PASS
means both calls came back as policy_denied / sandbox.unavailable and no process was started. A
successful exec or PTY is a FAIL (isolation was silently skipped).

  python3 scripts/accept_expect_refusal.py --gateway 127.0.0.1:8080 --pat-file C:\\xmesh\\gateway.pat
"""

import argparse
import json
import pathlib
import sys

from smoke_local import MCP, powershell


def refusal(client, name, arguments):
    result = client.request("tools/call", {"name": name, "arguments": arguments})
    content = result.get("structuredContent") or {}
    return {"tool": name, "refused": bool(result.get("isError")), "error": content.get("error"), "rule": content.get("rule")}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--gateway", default="127.0.0.1:8080")
    parser.add_argument("--pat-file", type=pathlib.Path)
    parser.add_argument("--rule", default="sandbox.unavailable")
    options = parser.parse_args(argv)
    token = options.pat_file.read_text().strip() if options.pat_file else None
    client = MCP(options.gateway, token)
    try:
        checks = [refusal(client, "local/exec.run", powershell("[Console]::Out.Write('SHOULD_NOT_RUN')")),
                  refusal(client, "local/pty.open", {"cols": 80, "rows": 24})]
    finally:
        client.close()
    good = all(check["refused"] and check["error"] == "policy_denied" and check["rule"] == options.rule for check in checks)
    print(json.dumps({"result": "PASS" if good else "FAIL", "expected": options.rule, "checks": checks}))
    return 0 if good else 1


if __name__ == "__main__":
    sys.exit(main())
