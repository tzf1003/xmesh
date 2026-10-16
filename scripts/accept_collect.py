#!/usr/bin/env python3
"""Collect the "what to hand back" evidence of a real-environment acceptance step, secrets removed.

Prints one JSON document: the host (OS, architecture), the xmesh version, optionally the node status
and `devices.list`, and the last audit records reduced to tool, device, status and error (no
arguments, paths or output). Anything that looks like a registration key or a bearer token is
replaced before it is printed or written. Read the result once before sharing it anyway.

  python3 scripts/accept_collect.py --binary bin/xmesh --state-dir ~/xmesh-a/state \\
      --endpoint http://127.0.0.1:8080/mcp --out evidence-lan-a.json
"""

import argparse
import datetime
import json
import pathlib
import platform
import re
import subprocess
import sys

AUDIT_FIELDS = ("time", "event", "caller", "oauth_client", "device", "tool", "status", "error", "duration_ms")
SECRETS = [
    (re.compile(r"(?i)\b(tskey|hskey)-[A-Za-z0-9_-]+"), r"\1-REDACTED"),
    (re.compile(r"(?i)\b(bearer)\s+[A-Za-z0-9._~+/=-]{8,}"), r"\1 REDACTED"),
    (re.compile(r'(?i)("?(?:password|token|secret|authkey|access_token|refresh_token)"?\s*[:=]\s*)"?[^\s",}]{6,}"?'),
     r"\1REDACTED"),
]


def redact(text):
    for pattern, replacement in SECRETS:
        text = pattern.sub(replacement, text)
    return text


def run(command, timeout=60):
    try:
        done = subprocess.run([str(part) for part in command], capture_output=True, text=True, timeout=timeout)
    except (OSError, subprocess.TimeoutExpired) as error:
        return {"command": " ".join(map(str, command)), "error": redact(str(error))}
    return {"command": " ".join(map(str, command)), "exit_code": done.returncode,
            "output": redact((done.stdout + done.stderr).strip())[-4000:]}


def audit_tail(state_dir, count):
    files = sorted((pathlib.Path(state_dir) / "audit").glob("*.jsonl"))
    records = []
    for path in reversed(files):
        for line in reversed(path.read_text(encoding="utf-8", errors="replace").splitlines()):
            try:
                row = json.loads(line)
            except ValueError:
                continue
            records.append({key: row.get(key) for key in AUDIT_FIELDS if key in row})
            if len(records) >= count:
                return list(reversed(records))
    return list(reversed(records))


def collect(binary, state_dir, endpoints, count):
    report = {"collected_at": datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds"),
              "host": {"system": platform.system(), "release": platform.release(), "machine": platform.machine()},
              "version": run([binary, "version"]), "status": []}
    for endpoint in endpoints:
        for tool in ("devices.list", "local/sys.info"):
            report["status"].append(run([binary, "status", "--endpoint", endpoint, "--tool", tool, "--json"]))
    if state_dir:
        report["audit"] = audit_tail(state_dir, count)
    return report


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--state-dir", help="state directory of the node; its audit/ is summarised")
    parser.add_argument("--endpoint", action="append", default=[], help="Gateway MCP URL to query; repeatable")
    parser.add_argument("--audit-lines", type=int, default=40)
    parser.add_argument("--out", type=pathlib.Path)
    options = parser.parse_args(argv)
    text = json.dumps(collect(options.binary, options.state_dir, options.endpoint, options.audit_lines), indent=2,
                      ensure_ascii=False)
    if options.out:
        options.out.write_text(text + "\n", encoding="utf-8")
        print("wrote %s" % options.out)
    else:
        print(text)


if __name__ == "__main__":
    sys.exit(main())
