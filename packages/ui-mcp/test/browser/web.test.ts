import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { Controller } from "../../src/controller.js";
import type { RunRequest, ScriptRequest } from "../../src/types.js";
import { cleanup, config, site, executable, finished } from "../helpers.js";

test("Web batches, prevalidation, deduplication, evidence and session isolation", async () => {
  const web = await site(),
    c = config();
  c.environments.web = {
    driver: "web",
    url: web.url,
    allowed_origins: [web.url],
    executable: executable(),
    script_enabled: true,
    coverage: ["ui"],
    fixtures: "mock",
  };
  const controller = new Controller(c);
  try {
    const opened = await controller.open("web"),
      id = opened.session_id as string;
    const snapshot = (await controller.snapshot(id)) as { aria: string };
    assert.match(snapshot.aria, /Username/);
    const request: RunRequest = {
      protocol_version: 1,
      session_id: id,
      request_id: "query",
      steps: [
        {
          id: "fill",
          action: "fill",
          target: { label: "Username" },
          value: "alice",
        },
        {
          id: "click",
          action: "click",
          target: { role: "button", name: "Search" },
        },
        {
          id: "assert",
          action: "assert",
          target: { testId: "result" },
          expect: { text: "alice" },
        },
        { id: "capture", action: "screenshot" },
      ],
    };
    const accepted = controller.submit(request);
    assert.equal(controller.submit(request).deduplicated, true);
    assert.throws(
      () =>
        controller.submit({ ...request, steps: [] } as unknown as RunRequest),
      /protocol/,
    );
    const result = await finished(controller, accepted.run_id);
    assert.equal(result.status, "passed", JSON.stringify(result));
    assert.equal(web.writes(), 1);
    assert.ok(result.artifacts.some((a) => a.mime_type === "image/png"));
    assert.throws(
      () =>
        controller.submit({
          ...request,
          request_id: "bad",
          steps: [
            request.steps[1]!,
            {
              id: "bad",
              action: "upload",
              target: { label: "Attachment" },
              file: "../outside",
            },
          ],
        }),
      /upload root/,
    );
    assert.equal(web.writes(), 1);
    assert.throws(
      () =>
        controller.submit({
          ...request,
          steps: [
            { id: "different", action: "read", target: { testId: "result" } },
          ],
        }),
      /different content/,
    );
    const second = await controller.open("web");
    const isolated = controller.submit({
      protocol_version: 1,
      session_id: second.session_id as string,
      request_id: "isolated",
      steps: [
        {
          id: "empty",
          action: "assert",
          target: { label: "Username" },
          expect: { value: "" },
        },
      ],
    });
    assert.equal(
      (await finished(controller, isolated.run_id)).status,
      "passed",
    );
    const failure = controller.submit({
      protocol_version: 1,
      session_id: id,
      request_id: "failure",
      steps: [{ id: "ambiguous", action: "click", target: { text: "Same" } }],
    });
    const failed = await finished(controller, failure.run_id);
    assert.equal(failed.status, "failed");
    assert.equal(failed.error?.code, "ambiguous_target");
    assert.ok(failed.artifacts.some((a) => a.name === "failure.png"));
    const artifact = failed.artifacts[0]!;
    assert.ok(controller.artifact(artifact.id).data);
    const other = new Controller({
      ...c,
      state_dir: path.join(c.state_dir, "other"),
    });
    try {
      assert.throws(() => other.artifact(artifact.id), /unknown artifact/);
    } finally {
      await other.close();
    }
    const scripted = controller.submit(
      {
        protocol_version: 1,
        session_id: id,
        request_id: "script",
        code: 'export default async ({page,step,expect})=>{await step("title",async()=>{await expect(page.getByLabel("Username")).toHaveValue("alice")})}',
      } as ScriptRequest,
      true,
    );
    assert.equal(
      (await finished(controller, scripted.run_id)).status,
      "passed",
    );
  } finally {
    await controller.close();
    await web.close();
    cleanup(c);
  }
});

test("active scripts can be cancelled and timed out while control requests remain responsive", async () => {
  const web = await site(),
    c = config();
  c.environments.web = {
    driver: "web",
    url: web.url,
    allowed_origins: [web.url],
    executable: executable(),
    script_enabled: true,
  };
  const controller = new Controller(c);
  try {
    const s = await controller.open("web");
    const run = controller.submit(
      {
        protocol_version: 1,
        session_id: s.session_id as string,
        request_id: "loop",
        code: "export default async()=>{while(true){}}",
        timeout_ms: 1000,
      },
      true,
    );
    assert.ok(controller.result(run.run_id));
    const result = await finished(controller, run.run_id, 15000);
    assert.equal(result.status, "timed_out");
    assert.equal(result.outcome_unknown, true);
    const next = await controller.open("web");
    const pending = controller.submit({
      protocol_version: 1,
      session_id: next.session_id as string,
      request_id: "cancel",
      steps: [
        {
          id: "waiting",
          action: "wait",
          target: { text: "never" },
          expect: { visible: true },
        },
      ],
    });
    await new Promise((resolve) => setTimeout(resolve, 50));
    await controller.cancel(pending.run_id);
    assert.equal(controller.result(pending.run_id).status, "cancelled");
  } finally {
    await controller.close();
    await web.close();
    cleanup(c);
  }
});
