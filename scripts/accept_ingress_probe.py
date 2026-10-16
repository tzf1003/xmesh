#!/usr/bin/env python3
"""Check from outside that an ingress is reachable and speaks the OAuth discovery protocol.

Step B runs this before adding the connector in claude.ai or Codex: if it fails, the connector will
fail too, and the message here says which part is wrong. It needs no password and registers nothing.
It checks that /mcp without a token answers 401 with a resource-metadata challenge, and that the
protected-resource and authorization-server metadata point at the configured issuer with S256 PKCE
(the checks of oauth_client.discover). It does not prove how claude.ai lists the tools.

  python3 scripts/accept_ingress_probe.py --ingress https://mynode.tail1234.ts.net
"""

import argparse
import json
import sys

import accept_net
import oauth_client


def probe(spec):
    endpoint = accept_net.Endpoint(spec)
    meta = oauth_client.discover(endpoint)
    return {"result": "PASS", "issuer": endpoint.issuer, "checks": ["401-challenge", "protected-resource",
            "authorization-server-metadata", "pkce-s256"], "registration_endpoint": meta["registration_endpoint"]}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--ingress", required=True, help="https://origin, or https://origin=host:port for a loopback tunnel")
    options = parser.parse_args(argv)
    try:
        print(json.dumps(probe(options.ingress)))
    except (AssertionError, OSError, ValueError) as error:
        print(json.dumps({"result": "FAIL", "ingress": options.ingress, "error": str(error)[:500]}))
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
