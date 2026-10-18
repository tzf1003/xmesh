import assert from "node:assert/strict";
import { test } from "node:test";
import { clipboardText, isSafeGatewayUrl } from "../../src/main/connect";
import { translator } from "../../src/shared/i18n";

const URL = "http://192.168.1.20:8080/mcp";
const TOKEN = "tok-0123456789_abcdEF";

test("each gateway kind carries the token, in its own format", () => {
  assert.equal(clipboardText("gatewayUrl", URL, TOKEN), `${URL}\nAuthorization: Bearer ${TOKEN}`);
  assert.equal(
    clipboardText("claudeCodeCommand", URL, TOKEN),
    `claude mcp add --transport http xmesh ${URL} --header "Authorization: Bearer ${TOKEN}"`,
  );
  assert.equal(
    clipboardText("codexConfig", URL, TOKEN),
    `[mcp_servers.xmesh]\nurl = "${URL}"\nhttp_headers = { Authorization = "Bearer ${TOKEN}" }\n`,
  );
});

test("without a token no gateway kind produces text: never a tokenless command, and no placeholder", () => {
  for (const kind of ["gatewayUrl", "claudeCodeCommand", "codexConfig"] as const) {
    assert.equal(clipboardText(kind, URL), undefined, `${kind} without a token argument`);
    assert.equal(clipboardText(kind, URL, ""), undefined, `${kind} with an empty token`);
    assert.equal(clipboardText(kind, URL, "short"), undefined, `${kind} with a token too short to be one`);
    assert.equal(clipboardText(kind, URL, `${TOKEN} extra`), undefined, `${kind} with a space in the token`);
  }
  const text = clipboardText("claudeCodeCommand", URL, TOKEN) ?? "";
  assert.doesNotMatch(text, /<PAT>|<token>/i);
});

test("a token with shell or TOML metacharacters is refused rather than quoted", () => {
  for (const bad of ['tok"en-value-1', "tok$en-value-1", "tok`en-value-1", "tok\\en-value-1", "tok\nen-value-1"]) {
    for (const kind of ["gatewayUrl", "claudeCodeCommand", "codexConfig"] as const) {
      assert.equal(clipboardText(kind, URL, bad), undefined, `${kind} ${JSON.stringify(bad)}`);
    }
  }
});

test("an unsafe or missing gateway URL refuses every gateway kind, whatever the token", () => {
  for (const bad of [undefined, "", "javascript:alert(1)", "http://x/$(id)", 'http://x/"', "http://x y/mcp"]) {
    for (const kind of ["gatewayUrl", "claudeCodeCommand", "codexConfig"] as const) {
      assert.equal(clipboardText(kind, bad, TOKEN), undefined, `${kind} ${String(bad)}`);
    }
  }
  assert.equal(isSafeGatewayUrl(URL), true);
});

test("mesh login commands need neither a URL nor a token", () => {
  assert.match(clipboardText("meshLoginRead", undefined) ?? "", /mesh login/);
  assert.equal(clipboardText("meshLoginWrite", undefined, TOKEN), clipboardText("meshLoginWrite", undefined));
});

test("the tray's copy-failed string is in both tables", () => {
  assert.notEqual(translator("zh")("menu.copyFailed"), translator("en")("menu.copyFailed"));
});
