#!/usr/bin/env python3
"""A minimal OAuth client for the Gate 4 protocol path (CTR-OAUTH-01, CTR-OAUTH-02).

It talks to an ingress the way claude.ai or Codex would: discovery, dynamic client registration,
authorization code with PKCE and the single-subject password login, token exchange, refresh
rotation, then MCP tools/list and exec.run with the Bearer token. It proves the protocol path
only. It does not prove how claude.ai lists the tools.

The ingress is reached over loopback (a reverse tunnel in production). `--issuer` is the public
HTTPS origin the ingress was configured with; it is sent as the Host header and every URL the
server publishes must start with it.
"""

# conformance: CTR-OAUTH-01 CTR-OAUTH-02 CON-SEC-09
import argparse
import base64
import hashlib
import http.client
import json
import pathlib
import re
import secrets
import sys
import time
import urllib.parse

from smoke_local import MCP, powershell

REDIRECT = "http://127.0.0.1:53682/callback"


class Ingress:
    def __init__(self, base, issuer):
        self.base, self.issuer = base, issuer
        self.host = urllib.parse.urlsplit(issuer).netloc

    def request(self, method, path, body=None, headers=None):
        merged = {"Host": self.host, **(headers or {})}
        connection = http.client.HTTPConnection(self.base, timeout=20)
        try:
            connection.request(method, path, body, merged)
            response = connection.getresponse()
            return response.status, response, response.read().decode()
        finally:
            connection.close()

    def json(self, method, path, body=None, headers=None, expect=200):
        status, response, text = self.request(method, path, body, headers)
        assert status == expect, (method, path, status, text)
        return json.loads(text)


def pkce():
    verifier = secrets.token_urlsafe(48)
    challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).rstrip(b"=").decode()
    return verifier, challenge


def discover(ingress):
    status, response, _ = ingress.request("POST", "/mcp", json.dumps({"jsonrpc": "2.0", "id": 1, "method": "initialize"}),
                                          {"Content-Type": "application/json"})
    assert status == 401, "MCP without a token must be refused, got %s" % status
    challenge = response.getheader("WWW-Authenticate") or ""
    assert ingress.issuer + "/.well-known/oauth-protected-resource" in challenge, challenge
    resource = ingress.json("GET", "/.well-known/oauth-protected-resource")
    assert resource["authorization_servers"] == [ingress.issuer], resource
    meta = ingress.json("GET", "/.well-known/oauth-authorization-server")
    assert meta["issuer"] == ingress.issuer, meta
    for key, path in (("authorization_endpoint", "/oauth/authorize"), ("token_endpoint", "/oauth/token"),
                      ("registration_endpoint", "/oauth/register")):
        assert meta[key] == ingress.issuer + path, (key, meta[key])
    assert meta["code_challenge_methods_supported"] == ["S256"], meta
    assert "authorization_code" in meta["grant_types_supported"] and meta["response_types_supported"] == ["code"], meta
    return meta


def register(ingress, name):
    client = ingress.json("POST", "/oauth/register", json.dumps({"client_name": name, "redirect_uris": [REDIRECT],
                          "token_endpoint_auth_method": "none"}), {"Content-Type": "application/json"}, expect=201)
    assert client["client_id"] and "client_secret" not in client, client
    return client["client_id"]


def authorize(ingress, client_id, scope, password):
    """Run the browser half: open the login page, submit the password, return the authorization code."""
    verifier, challenge = pkce()
    state = secrets.token_urlsafe(16)
    query = urllib.parse.urlencode({
        "response_type": "code", "client_id": client_id, "redirect_uri": REDIRECT, "scope": scope, "state": state,
        "code_challenge": challenge, "code_challenge_method": "S256", "resource": ingress.issuer + "/mcp"})
    path = "/oauth/authorize?" + query
    # Without a challenge the server must refuse and issue no code.
    status, _, _ = ingress.request("GET", "/oauth/authorize?" + urllib.parse.urlencode({
        "response_type": "code", "client_id": client_id, "redirect_uri": REDIRECT, "state": state}))
    assert status == 400, status
    status, response, page = ingress.request("GET", path)
    assert status == 200, (status, page)
    csrf = re.search(r'name="csrf" value="([^"]+)"', page).group(1)
    cookie = next(value for key, value in response.getheaders() if key.lower() == "set-cookie").split(";")[0]
    form = urllib.parse.urlencode({"csrf": csrf, "password": password})
    status, response, text = ingress.request("POST", path, form, {
        "Content-Type": "application/x-www-form-urlencoded", "Cookie": cookie})
    assert status == 302, (status, text)
    target = urllib.parse.urlsplit(response.getheader("Location"))
    assert target.geturl().startswith(REDIRECT), target
    values = urllib.parse.parse_qs(target.query)
    assert values["state"] == [state], values
    return values["code"][0], verifier


def token_request(ingress, **fields):
    status, _, text = ingress.request("POST", "/oauth/token", urllib.parse.urlencode(fields),
                                      {"Content-Type": "application/x-www-form-urlencoded"})
    return status, json.loads(text)


def obtain_tokens(ingress, client_id, scope, password):
    code, verifier = authorize(ingress, client_id, scope, password)
    exchange = dict(grant_type="authorization_code", client_id=client_id, redirect_uri=REDIRECT,
                    resource=ingress.issuer + "/mcp")
    status, body = token_request(ingress, code=code, code_verifier="x" * 64, **exchange)
    assert status == 400 and body["error"] == "invalid_grant", (status, body)  # wrong verifier burns nothing usable
    code, verifier = authorize(ingress, client_id, scope, password)
    status, tokens = token_request(ingress, code=code, code_verifier=verifier, **exchange)
    assert status == 200 and tokens["token_type"].lower() == "bearer" and tokens["refresh_token"], (status, tokens)
    status, body = token_request(ingress, code=code, code_verifier=verifier, **exchange)
    assert status == 400 and body["error"] == "invalid_grant", "authorization codes are single use"
    return tokens


def check_refresh_rotation(ingress, client_id, tokens):
    status, rotated = token_request(ingress, grant_type="refresh_token", client_id=client_id,
                                    refresh_token=tokens["refresh_token"])
    assert status == 200 and rotated["refresh_token"] != tokens["refresh_token"], (status, rotated)
    status, body = token_request(ingress, grant_type="refresh_token", client_id=client_id,
                                 refresh_token=tokens["refresh_token"])
    assert status == 400 and body["error"] == "invalid_grant", "the old refresh token must stop working"
    return rotated


def mcp(ingress, tokens):
    # /mcp is served by the MCP SDK handler, which rejects a non-localhost Host header on a loopback
    # connection (DNS rebinding protection) with 403. The OAuth endpoints above are not behind it and
    # are checked with the public Host. A real tunnel must therefore rewrite Host for /mcp, as done here.
    return MCP(ingress.base, tokens["access_token"])


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base", required=True, help="host:port the ingress listens on (loopback)")
    parser.add_argument("--issuer", required=True, help="the configured ingress.oauth.issuer")
    parser.add_argument("--password-file", required=True, type=pathlib.Path)
    parser.add_argument("--device", required=True, action="append",
                        help="device whose exec.run must work; repeat for several (name or name:windows)")
    parser.add_argument("--preregistered", help="a client from ingress.oauth.clients that holds exec:*")
    options = parser.parse_args()
    password = options.password_file.read_text().strip()
    ingress = Ingress(options.base, options.issuer.rstrip("/"))
    checks = ["discovery", "401-challenge"]
    discover(ingress)

    # A dynamically registered client asks for the smallest scope and sees only what it may use.
    dynamic = register(ingress, "xmesh-oauth-check")
    tokens = obtain_tokens(ingress, dynamic, "devices:read", password)
    assert tokens["scope"].split() == ["devices:read"], tokens
    client = mcp(ingress, tokens)
    names = {tool["name"] for tool in client.request("tools/list", {})["tools"]}
    assert "devices.list" in names and not any(name.endswith("/exec.run") for name in names), sorted(names)
    devices = {row["id"] for row in client.call("devices.list")["devices"]}
    for entry in options.device:
        assert entry.split(":")[0] in devices, (entry, devices)
    client.close()
    tokens = check_refresh_rotation(ingress, dynamic, tokens)
    checks += ["dynamic-registration", "pkce", "scope-filtered-tools", "refresh-rotation"]

    # The pre-registered client holds exec scope: tools/list shows the device tools and exec.run works.
    client_id = options.preregistered or dynamic
    scope = "devices:read exec:*"
    tokens = obtain_tokens(ingress, client_id, scope, password)
    client = mcp(ingress, tokens)
    names = {tool["name"] for tool in client.request("tools/list", {})["tools"]}
    for entry in options.device:
        device, _, system = entry.partition(":")
        deadline = time.monotonic() + 30
        while device + "/exec.run" not in names and time.monotonic() < deadline:
            time.sleep(0.5)
            names = {tool["name"] for tool in client.request("tools/list", {})["tools"]}
        assert device + "/exec.run" in names, (device, sorted(names))
        marker = "XMESH_OAUTH_" + secrets.token_hex(4)
        run = (powershell("[Console]::Out.Write('%s')" % marker) if system == "windows"
               else {"cmd": "sh", "args": ["-c", "printf " + marker]})
        out = client.call(device + "/exec.run", run)
        assert out["exit_code"] == 0 and out["stdout"] == marker, out
        checks.append("exec.run@" + device)
    client.close()
    print(json.dumps({"result": "PASS", "issuer": ingress.issuer, "checks": checks}))


if __name__ == "__main__":
    sys.exit(main())
