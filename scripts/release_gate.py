#!/usr/bin/env python3
"""Refuse to release a commit whose CI is not green.

release.yml runs this before it builds anything. The tag push can arrive before CI has finished
on the same commit, so runs that are still going (or have not started) are waited for, up to
--wait seconds. One failed run is final. Several runs can exist per workflow (branch push, tag
push, re-runs); one success among them is enough.
"""

import argparse
import json
import subprocess
import sys
import time

REQUIRED = (".github/workflows/go.yml", ".github/workflows/contracts.yml", ".github/workflows/headscale.yml",
            ".github/workflows/onboarding.yml", ".github/workflows/computer.yml")
BAD = {"failure", "timed_out", "startup_failure", "action_required", "stale"}


def fetch(repo, sha):
    out = subprocess.run(["gh", "api", "--paginate", f"repos/{repo}/actions/runs?head_sha={sha}&per_page=100",
                          "--jq", ".workflow_runs[] | {path, status, conclusion, name}"],
                         check=True, capture_output=True, text=True).stdout
    return [json.loads(line) for line in out.splitlines() if line.strip()]


def judge(runs, required=REQUIRED):
    """Return ("ok" | "wait" | "fail", reasons)."""
    verdict, reasons = "ok", []
    for path in required:
        mine = [run for run in runs if run.get("path") == path]
        if any(run["conclusion"] == "success" for run in mine):
            continue
        if not mine:
            verdict = "wait" if verdict != "fail" else verdict
            reasons.append(f"{path}: no run yet")
        elif any(run["status"] != "completed" for run in mine):
            verdict = "wait" if verdict != "fail" else verdict
            reasons.append(f"{path}: still running")
        else:
            verdict = "fail"
            reasons.append(f"{path}: {', '.join(sorted({str(run['conclusion']) for run in mine}))}")
    return verdict, reasons


def main(argv=None, fetch=fetch, sleep=time.sleep):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", required=True)
    parser.add_argument("--sha", required=True)
    parser.add_argument("--wait", type=int, default=1800)
    parser.add_argument("--interval", type=int, default=30)
    options = parser.parse_args(argv)
    waited = 0
    while True:
        verdict, reasons = judge(fetch(options.repo, options.sha))
        if verdict == "ok":
            print(f"CI is green on {options.sha}")
            return 0
        print(f"{verdict}: " + "; ".join(reasons), flush=True)
        if verdict == "fail" or waited >= options.wait:
            print("refusing to release", file=sys.stderr)
            return 1
        sleep(options.interval)
        waited += options.interval


if __name__ == "__main__":
    sys.exit(main())
