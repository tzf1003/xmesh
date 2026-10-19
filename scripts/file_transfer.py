#!/usr/bin/env python3
"""Stream files through xmesh MCP. Uses Python's standard library.

python3 scripts/file_transfer.py --endpoint http://127.0.0.1:8080/mcp \
    --token-file /path/to/pat upload ./local.bin /remote/path.bin --force
python3 scripts/file_transfer.py --endpoint https://gateway.example/mcp \
    --token-file /path/to/token --device laptop download /remote/path.bin ./local.bin
"""

import argparse
import base64
import hashlib
import http.client
import json
import os
from pathlib import Path
import tempfile
from urllib.parse import urlsplit

CHUNK = 1 << 20


class Client:
    def __init__(self, endpoint, token):
        self.url = urlsplit(endpoint)
        if self.url.scheme not in ("http", "https") or not self.url.hostname or self.url.username or self.url.fragment:
            raise ValueError("endpoint must be an HTTP(S) URL")
        self.token, self.session, self.counter = token, None, 0
        self.rpc("initialize", {"protocolVersion": "2025-11-25", "capabilities": {},
                               "clientInfo": {"name": "xmesh-file-transfer", "version": "1"}})
        self.rpc("notifications/initialized", {}, notification=True)

    def rpc(self, method, params, notification=False):
        self.counter += 1
        message = {"jsonrpc": "2.0", "method": method, "params": params}
        if not notification:
            message["id"] = self.counter
        headers = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream",
                   "MCP-Protocol-Version": "2025-11-25"}
        if self.session:
            headers["Mcp-Session-Id"] = self.session
        if self.token:
            headers["Authorization"] = "Bearer " + self.token
        transport = http.client.HTTPSConnection if self.url.scheme == "https" else http.client.HTTPConnection
        connection = transport(self.url.hostname, self.url.port, timeout=60)
        try:
            target = self.url.path or "/mcp"
            if self.url.query:
                target += "?" + self.url.query
            connection.request("POST", target, json.dumps(message), headers)
            response = connection.getresponse()
            body = response.read().decode()
            if response.status not in (200, 202):
                raise RuntimeError("MCP HTTP status %d" % response.status)
            self.session = response.getheader("Mcp-Session-Id") or self.session
            if notification:
                return None
            if response.getheader("Content-Type", "").startswith("text/event-stream"):
                messages = [json.loads(line[5:].strip()) for line in body.splitlines() if line.startswith("data:")]
                reply = next(m for m in messages if m.get("id") == self.counter)
            else:
                reply = json.loads(body)
            if "error" in reply:
                raise RuntimeError("MCP protocol error")
            return reply["result"]
        finally:
            connection.close()

    def call(self, name, args):
        result = self.rpc("tools/call", {"name": name, "arguments": args})
        out = result["structuredContent"]
        if result.get("isError"):
            raise RuntimeError("%s: %s" % (out.get("error"), out.get("message")))
        return out


def upload(client, tool, source, target, force):
    with source.open("rb") as stream:
        digest = hashlib.file_digest(stream, "sha256").hexdigest()
        stream.seek(0)
        offset, transfer_id = 0, ""
        data = stream.read(CHUNK)
        while True:
            following = stream.read(CHUNK)
            out = client.call(tool, {"path": target, "data": base64.b64encode(data).decode(), "sha256": digest,
                                    "force": force, "final": not following, "transfer_id": transfer_id, "byte_offset": offset})
            offset += len(data)
            if not following:
                if not out["complete"] or out["sha256"] != digest or out["bytes"] != offset:
                    raise RuntimeError("upload integrity check failed")
                return
            if out["next_offset"] != offset:
                raise RuntimeError("upload offset mismatch")
            transfer_id, data = out["transfer_id"], following


def download(client, tool, source, target, force):
    if os.path.lexists(target) and not force:
        raise FileExistsError("download target exists; use --force to replace it")
    fd, temporary = tempfile.mkstemp(prefix=".xmesh-download-", dir=target.parent)
    try:
        digest, offset, transfer_id = hashlib.sha256(), 0, ""
        with os.fdopen(fd, "wb") as stream:
            while True:
                out = client.call(tool, {"path": source, "transfer_id": transfer_id, "byte_offset": offset})
                data = base64.b64decode(out["data"], validate=True)
                if len(data) != out["bytes"] or hashlib.sha256(data).hexdigest() != out["sha256"]:
                    raise RuntimeError("download chunk checksum mismatch")
                stream.write(data)
                digest.update(data)
                offset += len(data)
                if out["next_offset"] != offset:
                    raise RuntimeError("download offset mismatch")
                if out["eof"]:
                    if digest.hexdigest() != out["file_sha256"] or offset != out["file_bytes"]:
                        raise RuntimeError("download file checksum mismatch")
                    break
                transfer_id = out["transfer_id"]
            stream.flush()
            os.fsync(stream.fileno())
        if force:
            os.replace(temporary, target)
        else:
            os.link(temporary, target)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--endpoint", required=True)
    parser.add_argument("--token-file", type=Path)
    parser.add_argument("--device", default="local")
    sub = parser.add_subparsers(dest="direction", required=True)
    for direction in ("upload", "download"):
        command = sub.add_parser(direction)
        command.add_argument("source")
        command.add_argument("target")
        command.add_argument("--force", action="store_true")
    args = parser.parse_args()
    token = args.token_file.read_text().strip() if args.token_file else ""
    client = Client(args.endpoint, token)
    tool = args.device + "/fs." + args.direction
    if args.direction == "upload":
        upload(client, tool, Path(args.source), args.target, args.force)
    else:
        download(client, tool, args.source, Path(args.target), args.force)
    print("File transfer complete")


if __name__ == "__main__":
    main()
