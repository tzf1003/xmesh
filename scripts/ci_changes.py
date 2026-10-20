#!/usr/bin/env python3
"""Select CI suites from the complete Git diff, including deleted paths."""

import fnmatch
import json
import os
from pathlib import Path
import re
import subprocess

SUITES = ("go", "windows_native", "l2_unix", "headscale", "ui", "ui_mcp", "computer")

# Keep expensive integration suites scoped to their runtime dependencies.
NETWORK_PACKAGES = {
    "app", "audit", "backend", "backendenv", "config", "connectivity", "control",
    "cwd", "device", "devicelink", "exec", "filelock", "fs", "identity", "lease",
    "localendpoint", "mcpmux", "mdns", "mesh", "oauth", "policy", "privdir", "pty",
    "quota", "role", "sandbox", "spake2", "toolerr", "winbox", "winjob",
}
WINDOWS_PACKAGES = {
    "app", "config", "control", "cwd", "exec", "filelock", "fs", "identity",
    "localendpoint", "policy", "privdir", "pty", "quota", "role", "sandbox",
    "toolerr", "winbox", "winjob",
}
L2_PACKAGES = {
    "app", "config", "control", "cwd", "exec", "identity", "localendpoint",
    "policy", "privdir", "pty", "sandbox", "toolerr",
}
COMPUTER_PACKAGES = {
    "app", "computer", "config", "control", "desktopbroker", "localendpoint",
    "privdir", "role", "toolerr",
}
MCP_PACKAGES = {
    "app", "backend", "backendenv", "config", "control", "cwd", "exec", "identity",
    "lease", "localendpoint", "mcpmux", "mesh", "oauth", "policy", "privdir", "pty",
    "quota", "role", "sandbox", "toolerr", "winbox", "winjob",
}
KNOWN_PACKAGES = (NETWORK_PACKAGES | WINDOWS_PACKAGES | L2_PACKAGES
                  | COMPUTER_PACKAGES | MCP_PACKAGES | {"ci", "onboarding"})
WORKFLOW_SUITES = {
    "headscale.yml": ("headscale",),
    "ui.yml": ("ui",),
    "ui-mcp.yml": ("ui_mcp",),
    "computer.yml": ("computer",),
    "computer-release.yml": ("computer",),
    "windows-diagnostics.yml": ("windows_native", "l2_unix"),
    "ui-package.yml": ("ui", "computer"),
    "pages.yml": (),
}


def select(paths, full=False):
    selected = dict.fromkeys(SUITES, full)
    selected["extended"] = full
    for path in paths:
        def matches(*patterns):
            return any(fnmatch.fnmatchcase(path, pattern) for pattern in patterns)

        if path.endswith(".md"):
            continue
        if path.startswith(".github/workflows/"):
            for suite in WORKFLOW_SUITES.get(path.rsplit("/", 1)[-1], SUITES):
                selected[suite] = True
            continue
        if matches("go.mod", "go.sum", "go.work*", "scripts/ci_changes.py",
                   "scripts/test_ci_changes.py", "scripts/release_gate.py", "scripts/test_scripts.py"):
            selected.update(dict.fromkeys(SUITES, True))
        if matches("*.go", "cmd/*", "internal/*", "meshadmin/*", "api/*",
                   "testdata/*", "examples/*", "docs/contracts/schemas/*",
                   "scripts/smoke*.py", "scripts/accept*.py"):
            selected["go"] = True
        if path.startswith("internal/"):
            package = path.split("/")[1]
            if package not in KNOWN_PACKAGES:
                selected.update(dict.fromkeys(SUITES, True))
            for suite, packages in [("headscale", NETWORK_PACKAGES),
                                    ("windows_native", WINDOWS_PACKAGES),
                                    ("l2_unix", L2_PACKAGES),
                                    ("computer", COMPUTER_PACKAGES), ("ui_mcp", MCP_PACKAGES)]:
                if package in packages:
                    selected[suite] = True
        if matches("cmd/*", "api/*", "docs/contracts/schemas/config.schema.json"):
            for suite in ("windows_native", "l2_unix", "headscale", "computer", "ui_mcp"):
                selected[suite] = True
        if matches("meshadmin/*", "scripts/headscale_ci.py", "scripts/coexist_ci.py",
                   "scripts/oauth_client.py"):
            selected["headscale"] = True
        if matches("ui/*", "api/control/*", "docs/contracts/schemas/control-*",
                   "docs/contracts/schemas/onboarding.schema.json", "scripts/gen-onboarding-types.py"):
            selected["ui"] = True
        if matches("packages/ui-mcp/*", "ui/src/main/*", "ui/src/preload/*", "ui/src/shared/*",
                   "ui/scripts/build-mcp.mjs", "ui/scripts/flip-fuses.mjs", "ui/package*.json",
                   "ui/tsconfig*", "ui/electron.vite.config.*"):
            selected["ui_mcp"] = True
        if matches("computer-native/*", "scripts/*computer*.py", "scripts/headscale_ci.py",
                   "scripts/oauth_client.py", "ui/src/main/*", "ui/src/shared/*",
                   "ui/scripts/*computer*", "ui/scripts/test-native-desktop.mjs",
                   "ui/scripts/build-*.mjs", "ui/package*.json", "ui/tsconfig*",
                   "ui/electron.vite.config.*"):
            selected["computer"] = True
    return selected


def plan(env=os.environ, diff=None):
    event = env.get("GITHUB_EVENT_NAME", "")
    full = (event in {"schedule", "workflow_dispatch"}
            or env.get("GITHUB_REF", "").startswith("refs/tags/"))
    if full:
        return select([], full=True)
    base = env.get("CI_BASE_SHA", "")
    if not re.fullmatch(r"[0-9a-fA-F]{40}", base) or set(base) == {"0"}:
        return select([], full=True)
    if diff is None:
        def diff(base):
            return subprocess.check_output([
                "git", "diff", "--name-only", "--no-renames", "-z", base, "HEAD", "--",
            ]).decode("utf-8", errors="surrogateescape").split("\0")
    try:
        return select(diff(base))
    except subprocess.CalledProcessError:
        # A force-push can remove the base commit from the fetched history.
        return select([], full=True)


def main():
    selected = plan()
    print(json.dumps(selected, indent=2))
    with Path(os.environ["GITHUB_OUTPUT"]).open("a", encoding="utf-8") as output:
        for name, enabled in selected.items():
            output.write(f"{name}={str(enabled).lower()}\n")


if __name__ == "__main__":
    main()
