#!/usr/bin/env python3
"""A stdio MCP server that stands in for IDA in the gate 2 scenario.

Every start and every tools/call is appended to a JSONL log, so a test can read back exactly
which arguments reached the backend (the lease token must never be among them).

    fake_ida.py LOGFILE
"""

import json
import os
import sys

TOOLS = [
    {"name": "echo", "description": "Return the arguments", "inputSchema": {"type": "object"}},
    {"name": "decompile", "description": "Pretend to decompile a function",
     "inputSchema": {"type": "object", "properties": {"address": {"type": "string"}}}},
]


def log(path, record):
    with open(path, "a", encoding="utf-8") as handle:
        handle.write(json.dumps(record, sort_keys=True) + "\n")
        handle.flush()
        os.fsync(handle.fileno())


def reply(identifier, result=None, error=None):
    message = {"jsonrpc": "2.0", "id": identifier}
    if error is not None:
        message["error"] = error
    else:
        message["result"] = result
    sys.stdout.write(json.dumps(message) + "\n")
    sys.stdout.flush()


def main():
    if len(sys.argv) != 2:
        sys.exit("usage: fake_ida.py LOGFILE")
    path = sys.argv[1]
    log(path, {"event": "start", "pid": os.getpid()})
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        request = json.loads(line)
        method, identifier = request.get("method"), request.get("id")
        if identifier is None:
            continue  # notifications
        params = request.get("params") or {}
        if method == "initialize":
            reply(identifier, {"protocolVersion": params.get("protocolVersion", "2025-11-25"),
                               "capabilities": {"tools": {"listChanged": False}},
                               "serverInfo": {"name": "fake-ida", "version": "1"}})
        elif method == "ping":
            reply(identifier, {})
        elif method == "tools/list":
            reply(identifier, {"tools": TOOLS})
        elif method == "tools/call":
            arguments = params.get("arguments") or {}
            log(path, {"event": "call", "pid": os.getpid(), "tool": params.get("name"), "arguments": arguments})
            reply(identifier, {"content": [{"type": "text", "text": json.dumps(arguments, sort_keys=True)}],
                               "structuredContent": {"pid": os.getpid(), "tool": params.get("name"),
                                                     "arguments": arguments}})
        else:
            reply(identifier, error={"code": -32601, "message": "method not found: " + str(method)})


if __name__ == "__main__":
    main()
