#!/usr/bin/env python3
"""Verify a desktop through an authenticated MCP client without persisting its image.

The control probe uses a finite wait action; it does not type or click. Native
input acceptance is performed in the isolated desktop window fixture.
"""

import argparse
import base64
import json
import pathlib
import time
import uuid

from smoke_local import MCP


def verify_computer(client, device):
    def call(tool, arguments=None):
        result = client.request("tools/call", {"name": device + "/computer." + tool,
                                               "arguments": arguments or {}})
        assert not result.get("isError"), {"tool": tool, "error": result.get("structuredContent")}
        return result, result["structuredContent"]

    deadline = time.monotonic() + 15
    while True:
        _, info = call("info")
        if info.get("available") and "keyboard" in info.get("capabilities", []):
            break
        assert time.monotonic() < deadline, {"desktop_state": info.get("state"), "reason": info.get("reason")}
        time.sleep(.1)
    opened = {"mode": "control", "ttl_seconds": 30}
    if info.get("default_interaction") == "background":
        targets = info.get("targets", [])
        assert targets, "background acceptance requires an available application window"
        opened.update(interaction="background", target_id=targets[0]["id"])
    _, session = call("open", opened)
    identity = {key: session[key] for key in ("session_id", "desktop_epoch", "lease_token")}
    try:
        images = []
        for _ in range(2):
            result, frame = call("observe", {**identity, "max_width": 1280})
            blocks = [block for block in result["content"] if block["type"] == "image"]
            assert len(blocks) == 1, "MCP did not deliver exactly one native image"
            block = blocks[0]
            data = base64.b64decode(block["data"], validate=True)
            assert block["mimeType"] in ("image/png", "image/jpeg"), "unsupported image MIME type"
            assert ((block["mimeType"] == "image/png" and data.startswith(b"\x89PNG\r\n\x1a\n")) or
                    (block["mimeType"] == "image/jpeg" and data.startswith(b"\xff\xd8"))), "image signature mismatch"
            assert frame["desktop_epoch"] == identity["desktop_epoch"]
            assert frame["coordinate_space"] == "image_pixels"
            assert 0 < frame["image_width"] <= 1280 and frame["image_height"] > 0
            images.append({"bytes": len(data), "width": frame["image_width"], "height": frame["image_height"]})
        action = {**identity, "action_id": "network-" + uuid.uuid4().hex,
                  "actions": [{"type": "wait", "duration_ms": 10}]}
        _, executed = call("act", action)
        assert executed["state"] == "executed" and executed["executed"] == 1, executed
        _, repeat = call("act", action)
        assert repeat == executed, "duplicate action changed its result"
        _, status = call("action_status", {**identity, "action_id": action["action_id"]})
        assert status == executed, "action status differs from execution"
        _, renewed = call("renew", {**identity, "ttl_seconds": 30})
        assert renewed["lease_token"] == identity["lease_token"], "renew changed the controller"
        return {"result": "PASS", "device": device, "images": images,
                "checks": ["native-image-content", "repeat-capture", "control-dispatch", "action-dedup", "action-status", "renew", "close"]}
    finally:
        call("close", identity)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--gateway", required=True)
    parser.add_argument("--device", required=True)
    parser.add_argument("--token-file", type=pathlib.Path, required=True)
    options = parser.parse_args()
    assert options.gateway.startswith("127.0.0.1:"), "use the local Gateway endpoint"
    client = MCP(options.gateway, options.token_file.read_text().strip())
    try:
        print(json.dumps(verify_computer(client, options.device)))
    finally:
        client.close()


if __name__ == "__main__":
    main()
