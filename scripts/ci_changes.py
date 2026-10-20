#!/usr/bin/env python3
"""Select CI suites from the complete Git diff, including deleted paths."""

import fnmatch
import json
import os
from pathlib import Path
import re
import subprocess

SUITES = ("go", "headscale", "ui", "ui_mcp", "computer")


def select(paths, full=False):
    selected = dict.fromkeys(SUITES, full)
    selected["extended"] = full
    for path in paths:
        def matches(*patterns):
            return any(fnmatch.fnmatchcase(path, pattern) for pattern in patterns)

        # Shared build configuration and CI edits exercise the entire pipeline.
        if matches(".github/workflows/*", "go.mod", "go.sum", "go.work*",
                   "scripts/ci_changes.py", "scripts/test_ci_changes.py",
                   "scripts/release_gate.py", "scripts/test_scripts.py"):
            selected.update(dict.fromkeys(SUITES, True))
        if matches("*.go", "cmd/*", "internal/*", "meshadmin/*", "api/*",
                   "testdata/*", "examples/*", "docs/contracts/*",
                   "scripts/smoke*.py", "scripts/accept*.py"):
            selected["go"] = True
        if selected["go"] or matches("scripts/headscale_ci.py", "scripts/coexist_ci.py",
                                     "scripts/oauth_client.py"):
            selected["headscale"] = True
        if matches("ui/*", "api/control/*", "docs/contracts/*", "scripts/gen-onboarding-types.py"):
            selected["ui"] = True
            selected["ui_mcp"] = True
        if matches("packages/ui-mcp/*", "internal/backend/*", "internal/exec/*",
                   "internal/pty/*", "internal/config/*", "internal/role/gateway/*",
                   "internal/app/*", "cmd/xmesh/*"):
            selected["ui_mcp"] = True
        if matches("computer-native/*", "internal/computer/*", "internal/desktopbroker/*",
                   "internal/app/*", "internal/config/*", "internal/control/*", "cmd/xmesh/*",
                   "scripts/*computer*.py", "scripts/headscale_ci.py", "scripts/oauth_client.py",
                   "ui/src/main/computer*", "ui/src/main/update/*", "ui/src/shared/*",
                   "ui/scripts/*", "ui/package*.json", "ui/tsconfig*", "ui/electron.vite.config.*"):
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
