#!/usr/bin/env python3
"""Step E: the cross-host PTY budget (CTR-GW-06) with two real ingresses.

Two ingress hosts share `ingress.pty_budget_authority` (the device that hosts the admission lease)
and the same `ingress.max_pty_per_client`. This script logs in to each ingress as the same
pre-registered OAuth client, then fires --attempts concurrent `<device>/pty.open` calls, spread over
the ingresses. Exactly --limit terminals may open; every other attempt must fail with
policy_denied / rule quota.exceeded. `pty.list` on the device confirms how many of ours are live.
All terminals are closed afterwards.

With --expect-unavailable (run it after stopping xmesh on the authority device) every ingress must
refuse `pty.open` with rule quota.unavailable and no terminal may be created.

The pre-registered client needs `pty:<device>` (scripts/accept_config.py gives it pty:*) and both
ingresses must accept the same password; copy one password file to both hosts.

  python3 scripts/accept_pty_budget.py --ingress https://a.example.com --ingress https://b.example.com \\
      --password-file oauth-password --device home-server --limit 3
"""

import argparse
import concurrent.futures
import json
import pathlib
import sys

import accept_net
import oauth_client


def login(endpoint, client, password, device):
    tokens = oauth_client.obtain_tokens(endpoint, client, "devices:read pty:" + device, password)
    return tokens["access_token"]


def classify(ok, content):
    """Name an outcome: opened, a policy rule, or the raw error."""
    if ok and content.get("session_id"):
        return "opened"
    return (content.get("rule") or content.get("error") or "unknown") if not ok else "unknown"


def burst(endpoints, tokens, device, attempts):
    """Open `attempts` terminals concurrently, each in its own MCP session. Returns (outcomes, held)."""
    def attempt(index):
        endpoint = endpoints[index % len(endpoints)]
        session = accept_net.Session(endpoint, tokens[endpoint.issuer])
        ok, content = session.call(device + "/pty.open", {"cols": 80, "rows": 24})
        return classify(ok, content), session, content.get("session_id"), content

    with concurrent.futures.ThreadPoolExecutor(max_workers=attempts) as pool:
        return list(pool.map(attempt, range(attempts)))


def verdict_limit(outcomes, limit, live):
    names = [outcome[0] for outcome in outcomes]
    opened = names.count("opened")
    problems = []
    if opened != limit:
        problems.append("%d terminals opened, expected exactly %d" % (opened, limit))
    other = sorted({name for name in names if name not in ("opened", "quota.exceeded")})
    if other:
        problems.append("unexpected failures: " + ", ".join(other))
    if live is not None and live != opened:
        problems.append("pty.list shows %d of our terminals live, %d were reported opened" % (live, opened))
    return problems


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--ingress", action="append", required=True,
                        help="https://origin or https://origin=host:port; give at least two")
    parser.add_argument("--password-file", required=True, type=pathlib.Path)
    parser.add_argument("--device", required=True, help="the pty_budget_authority device (or any device)")
    parser.add_argument("--client", default="check", help="pre-registered OAuth client in ingress.oauth.clients")
    parser.add_argument("--limit", type=int, default=3, help="the configured max_pty_per_client")
    parser.add_argument("--attempts", type=int, default=10)
    parser.add_argument("--expect-unavailable", action="store_true")
    options = parser.parse_args(argv)
    if len(options.ingress) < 2:
        parser.error("step E needs at least two ingresses")
    if options.attempts <= options.limit and not options.expect_unavailable:
        parser.error("--attempts must exceed --limit")
    password = options.password_file.read_text().strip()
    endpoints = [accept_net.Endpoint(spec) for spec in options.ingress]
    held = []
    try:
        tokens = {endpoint.issuer: login(endpoint, options.client, password, options.device) for endpoint in endpoints}
        outcomes = burst(endpoints, tokens, options.device, len(endpoints) if options.expect_unavailable else options.attempts)
        held = outcomes
        names = [outcome[0] for outcome in outcomes]
        if options.expect_unavailable:
            problems = ["%s: %s" % (name, json.dumps(item[3])[:200]) for name, item in zip(names, outcomes)
                        if name != "quota.unavailable"]
            report = {"mode": "authority-down", "outcomes": names}
        else:
            ours = {outcome[2] for outcome in outcomes if outcome[2]}
            ok, listing = outcomes[0][1].call(options.device + "/pty.list")
            live = None
            if ok:
                live = len([row for row in listing.get("sessions", [])
                            if row.get("id") in ours and row.get("state") in ("active", "orphaned")])
            problems = verdict_limit(outcomes, options.limit, live)
            report = {"mode": "limit", "limit": options.limit, "opened": names.count("opened"),
                      "refused_quota_exceeded": names.count("quota.exceeded"), "live_per_pty_list": live,
                      "outcomes": names}
        report["result"] = "FAIL" if problems else "PASS"
        if problems:
            report["problems"] = problems
        print(json.dumps(report))
        return 1 if problems else 0
    except (AssertionError, OSError, RuntimeError) as error:
        print(json.dumps({"result": "FAIL", "error": str(error)[:500]}))
        return 1
    finally:
        for _, session, session_id, _ in held:
            try:
                if session_id:
                    session.call(options.device + "/pty.close", {"session_id": session_id})
            except (OSError, RuntimeError):
                pass
            session.close()


if __name__ == "__main__":
    sys.exit(main())
