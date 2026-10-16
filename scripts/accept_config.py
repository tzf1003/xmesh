#!/usr/bin/env python3
"""Write the xmesh configuration for one real-environment acceptance node.

docs/validation/real-environment-runbook.md uses this instead of asking the operator to hand-write
YAML. The output is a JSON document, which `xmesh serve --config` reads as YAML. Each run creates
<out>/config.json, <out>/state (the state directory) and <out>/work (the only directory the Agent
may use), and prints the exact command that starts the node. Secrets are only referenced by path;
a missing OAuth password file is created with a random value (mode 0600) and never printed.

  lan        step A: LAN node discovered over mDNS, peers authorised through `xmesh pair`
  tailnet    steps D-F: tailnet node (Agent + Gateway) that joins with a registration key file
  ingress    steps B, C, E: tailnet node that also serves the OAuth ingress
"""

import argparse
import json
import pathlib
import re
import secrets
import sys

OFFICIAL_CONTROL = "https://controlplane.tailscale.com"
NAME = re.compile(r"^[A-Za-z0-9][A-Za-z0-9-]{0,62}$")
FULL_SCOPES = ["devices:read", "exec:*", "fs:*", "pty:*", "sys:*"]
GRANT = {"tools": ["*"], "backends": ["*"]}


def build(kind, options):
    """Return the configuration document for one node. `options` is an argparse namespace."""
    work = str(options.work)
    roles = options.roles or (["agent", "gateway", "ingress"] if kind == "ingress" else ["agent", "gateway"])
    config = {"node": {"name": options.name, "roles": roles},
              "gateway": {"listen": options.gateway_listen},
              "agent": {"default_cwd": work, "cwd_policy": {"allow": [work]}}}
    if options.isolation:
        config["agent"]["policy"] = {"isolation": options.isolation}
    if options.pat_file:
        config["gateway"]["pats"] = {"accept": {"token_file": str(options.pat_file), "scopes": ["devices:read", "sys:*", "exec:*", "pty:*"]}}
    if kind == "lan":
        config["mesh"] = {"mode": "lan", "lan": {"mdns": True}}
        # Peers are paired with --tags tag:peer,tag:device; the receiving side decides what they may call.
        config["agent"]["authz"] = {"tag:peer": dict(GRANT), "tag:device": dict(GRANT)}
        return config
    config["mesh"] = {"mode": "tailnet", "control_url": options.control_url, "authkey_file": str(options.authkey_file)}
    if options.ephemeral:
        config["mesh"]["ephemeral"] = True
    # Devices must accept calls from other devices and, for the ingress, from the tag:ingress node.
    config["agent"]["authz"] = {"tag:device": dict(GRANT), "tag:ingress": dict(GRANT)}
    if kind == "ingress":
        ingress = {"listen": options.listen, "max_pty_per_client": options.max_pty_per_client,
                   "oauth": {"issuer": options.issuer.rstrip("/"),
                             "clients": {options.client: {"scopes": list(options.scopes)}},
                             # claude.ai and Codex register themselves; this caps what they may use.
                             "dynamic_client_scopes": list(options.dynamic_scopes)}}
        if options.listen == "public:443":
            ingress["domain"] = options.domain
            ingress["tls"] = options.tls
            if options.tls == "file":
                ingress["cert_file"], ingress["key_file"] = str(options.cert_file), str(options.key_file)
        if options.pty_budget_authority:
            ingress["pty_budget_authority"] = options.pty_budget_authority
        config["ingress"] = ingress
    return config


def csv(text):
    return [item for item in (part.strip() for part in text.split(",")) if item]


def parser():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("kind", choices=["lan", "tailnet", "ingress"])
    p.add_argument("--name", required=True, help="node.name: letters, digits and hyphens")
    p.add_argument("--out", required=True, type=pathlib.Path, help="directory that receives config.json, state/ and work/")
    p.add_argument("--roles", type=csv, help="comma separated; default agent,gateway (plus ingress for `ingress`)")
    p.add_argument("--gateway-listen", default="127.0.0.1:8080")
    p.add_argument("--force", action="store_true", help="overwrite an existing config.json")
    p.add_argument("--isolation", choices=["L1", "L2"], help="agent.policy.isolation (step F sets L2 to see the refusal)")
    p.add_argument("--pat-file", type=pathlib.Path,
                   help="enable a Gateway PAT stored in this file (created with a random value when missing); "
                        "every Gateway request then needs `Authorization: Bearer <token>` (step F)")
    tail = p.add_argument_group("tailnet and ingress")
    tail.add_argument("--authkey-file", type=pathlib.Path, help="file holding the registration key (not the key itself)")
    tail.add_argument("--control-url", default=OFFICIAL_CONTROL)
    tail.add_argument("--ephemeral", action="store_true", help="mesh.ephemeral, for short-lived sandboxes")
    ing = p.add_argument_group("ingress only")
    ing.add_argument("--issuer", help="ingress.oauth.issuer: the public https origin clients reach")
    ing.add_argument("--listen", default="funnel", choices=["funnel", "tunnel", "public:443"])
    ing.add_argument("--domain", help="public:443 only")
    ing.add_argument("--tls", default="autocert", choices=["autocert", "file"])
    ing.add_argument("--cert-file", type=pathlib.Path)
    ing.add_argument("--key-file", type=pathlib.Path)
    ing.add_argument("--client", default="check", help="pre-registered client used by the scripts in scripts/accept_*.py")
    ing.add_argument("--scopes", type=csv, default=FULL_SCOPES, help="scopes of the pre-registered client")
    ing.add_argument("--dynamic-scopes", type=csv, default=FULL_SCOPES,
                     help="scope ceiling of clients that register themselves (claude.ai, Codex); "
                          "widened for the test, narrow it again afterwards")
    ing.add_argument("--max-pty-per-client", type=int, default=8)
    ing.add_argument("--pty-budget-authority", help="step E: device name that hosts the cross-host PTY budget")
    ing.add_argument("--password-file", type=pathlib.Path,
                     help="OAuth login password file; created with a random value when it does not exist")
    return p


def check(kind, options):
    if not NAME.match(options.name):
        sys.exit("error: --name must match " + NAME.pattern)
    if kind != "lan" and not options.authkey_file:
        sys.exit("error: --authkey-file is required for " + kind)
    if kind == "ingress":
        if not options.issuer or not re.match(r"^https://[^/]+$", options.issuer.rstrip("/")):
            sys.exit("error: --issuer must be an https origin without a path")
        if options.listen == "public:443" and not options.domain:
            sys.exit("error: --domain is required with --listen public:443")
        if options.listen == "public:443" and options.tls == "file" and not (options.cert_file and options.key_file):
            sys.exit("error: --tls file needs --cert-file and --key-file")
        if options.listen == "funnel" and options.control_url.rstrip("/") != OFFICIAL_CONTROL:
            sys.exit("error: Funnel needs the official control plane " + OFFICIAL_CONTROL)
        if options.pty_budget_authority and not NAME.match(options.pty_budget_authority):
            sys.exit("error: --pty-budget-authority must be a node name")
        if options.max_pty_per_client < 1:
            sys.exit("error: --max-pty-per-client must be positive")


def main(argv=None):
    options = parser().parse_args(argv)
    kind = options.kind
    check(kind, options)
    out = options.out.resolve()
    options.work = out / "work"
    config_path, state = out / "config.json", out / "state"
    if config_path.exists() and not options.force:
        sys.exit("error: %s exists; pass --force to replace it" % config_path)
    options.work.mkdir(parents=True, exist_ok=True)
    state.mkdir(parents=True, exist_ok=True)
    config_path.write_text(json.dumps(build(kind, options), indent=2) + "\n", encoding="utf-8")
    command = "xmesh serve --config %s --state-dir %s" % (config_path, state)
    note = []
    if options.pat_file:
        pat = options.pat_file.resolve()
        if not pat.exists():
            pat.write_text(secrets.token_urlsafe(32) + "\n", encoding="utf-8")
            pat.chmod(0o600)
            note.append("created the Gateway PAT file %s (mode 0600); pass it to `xmesh status --pat-file`" % pat)
    if kind == "ingress":
        password = (options.password_file or state / "oauth-password").resolve()
        if not password.exists():
            password.write_text(secrets.token_urlsafe(24) + "\n", encoding="utf-8")
            password.chmod(0o600)
            note.append("created the OAuth password file %s (mode 0600); read it from there, it is not printed" % password)
        if options.password_file:
            command += " --oauth-password-file %s" % password
        note.append("dynamic client scope ceiling is %s; narrow ingress.oauth.dynamic_client_scopes after the test"
                    % ",".join(options.dynamic_scopes))
    print(json.dumps({"config": str(config_path), "state_dir": str(state), "work_dir": str(options.work),
                      "start": command, "notes": note}, indent=2))


if __name__ == "__main__":
    main()
