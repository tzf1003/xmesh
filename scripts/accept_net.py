"""Reach an xmesh OAuth ingress the way the real-environment acceptance scripts need to (stdlib only).

oauth_client.py talks to a loopback listener over plain HTTP. A real ingress is a public https origin
(Funnel or public:443), or a loopback listener behind a reverse tunnel. `Endpoint` covers both:

  https://mcp.example.com                    direct TLS to the origin
  https://mcp.example.com=127.0.0.1:9000     plain HTTP to a loopback tunnel listener; the origin is
                                             sent as the Host header on the OAuth paths, the way
                                             oauth_client.py does it

`Session` is a small MCP client that returns tool errors instead of raising, because the acceptance
steps care about the exact error an ingress gives.
"""

import http.client
import json
import ssl
import urllib.parse

import oauth_client

PROTOCOL = "2025-11-25"


class Endpoint(oauth_client.Ingress):
    def __init__(self, spec):
        issuer, _, base = spec.partition("=")
        issuer = issuer.rstrip("/")
        if not (urllib.parse.urlsplit(issuer).scheme == "https" and urllib.parse.urlsplit(issuer).netloc):
            raise ValueError("an ingress is written https://origin or https://origin=host:port, got %r" % spec)
        super().__init__(base or urllib.parse.urlsplit(issuer).netloc, issuer)
        self.tunnel = bool(base)

    def connect(self, host=None):
        if self.tunnel:
            return http.client.HTTPConnection(self.base, timeout=30)
        return http.client.HTTPSConnection(self.host, timeout=30, context=ssl.create_default_context())

    def request(self, method, path, body=None, headers=None):
        merged = {"Host": self.host, **(headers or {})}
        connection = self.connect()
        try:
            connection.request(method, path, body, merged)
            response = connection.getresponse()
            return response.status, response, response.read().decode()
        finally:
            connection.close()


class Session:
    """One MCP session on an ingress, authenticated with an OAuth access token."""

    def __init__(self, endpoint, token):
        self.endpoint, self.token, self.session, self.counter = endpoint, token, None, 0
        self.rpc("initialize", {"protocolVersion": PROTOCOL, "capabilities": {},
                                "clientInfo": {"name": "xmesh-accept", "version": "1"}})
        self.rpc("notifications/initialized", {}, notification=True)

    def headers(self):
        headers = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream",
                   "MCP-Protocol-Version": PROTOCOL, "Authorization": "Bearer " + self.token}
        if self.session:
            headers["Mcp-Session-Id"] = self.session
        if self.endpoint.tunnel:
            # /mcp sits behind the SDK's DNS-rebinding guard, which wants a loopback Host (see oauth_client.mcp).
            headers["Host"] = self.endpoint.base
        return headers

    def rpc(self, method, params, notification=False):
        self.counter += 1
        body = {"jsonrpc": "2.0", "method": method, "params": params}
        if not notification:
            body["id"] = self.counter
        connection = self.endpoint.connect()
        try:
            connection.request("POST", "/mcp", json.dumps(body), self.headers())
            response = connection.getresponse()
            data = response.read().decode()
            if response.status not in (200, 202):
                raise RuntimeError("HTTP %s from %s/mcp: %s" % (response.status, self.endpoint.issuer, data[:300]))
            self.session = response.getheader("Mcp-Session-Id") or self.session
            if notification:
                return None
            if response.getheader("Content-Type", "").startswith("text/event-stream"):
                messages = [json.loads(line[6:]) for line in data.splitlines() if line.startswith("data: ")]
                result = next(item for item in messages if item.get("id") == self.counter)
            else:
                result = json.loads(data)
            if "error" in result:
                raise RuntimeError("JSON-RPC error: %s" % json.dumps(result["error"]))
            return result["result"]
        finally:
            connection.close()

    def call(self, name, arguments=None):
        """Return (ok, structured content) without raising on a tool error."""
        result = self.rpc("tools/call", {"name": name, "arguments": arguments or {}})
        return not result.get("isError"), result.get("structuredContent") or {}

    def close(self):
        if not self.session:
            return
        connection = self.endpoint.connect()
        try:
            connection.request("DELETE", "/mcp", headers=self.headers())
            connection.getresponse().read()
        except (OSError, http.client.HTTPException):
            pass
        finally:
            connection.close()
            self.session = None
