import { expect, test } from "@playwright/test";
import { launch, navigate } from "./helpers";

test("delegation shows its switch, runners, live sessions and tasks, and the switch writes the configuration", async () => {
  const h = await launch({ kernel: { capabilities: ["status", "events", "peers", "sessions", "config.read", "config.write", "delegate"] } });
  try {
    const page = await h.openPanel();
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await navigate(page, "delegate");
    await expect(page.getByTestId("delegate-state")).toHaveText("已关闭");
    await expect(page.getByTestId("delegate-runners")).toContainText("claude");
    await expect(page.getByTestId("delegate-sessions")).toContainText("foo refactor");
    await expect(page.getByTestId("delegate-tasks")).toContainText("node-abc/s7f");
    await expect(page.getByTestId("delegate-task-state-t-5e0a7c41b9d24f6e8a13c0d7f2e4b916")).toHaveText("运行中");
    await expect(page.getByTestId("delegate-replies")).toContainText("r-8f1c9a2e6b7d4e8f9a0b1c2d3e4f5a6b");
    await page.getByTestId("delegate-reply-preview-r-8f1c9a2e6b7d4e8f9a0b1c2d3e4f5a6b").click();
    await expect(page.getByTestId("delegate-reply-preview-body")).toContainText("Tests pass on branch main.");
    await page.getByTestId("delegate-reply-approve").click();
    await expect(page.getByTestId("delegate-reply-state-r-8f1c9a2e6b7d4e8f9a0b1c2d3e4f5a6b")).toHaveText("已发送");
    await page.getByTestId("delegate-toggle").click();
    await expect(page.getByTestId("delegate-state")).toHaveText("已开启");
    expect((JSON.parse(h.kernel.panel.content) as { agent: { delegate: { enabled: boolean } } }).agent.delegate.enabled).toBe(true);
    await page.screenshot({ path: test.info().outputPath("delegate-page.png"), fullPage: true });
    expect(errors).toEqual([]);
  } finally { await h.close(); }
});

test("kernels without the delegate capability explain that the page is unavailable", async () => {
  const h = await launch();
  try {
    const page = await h.openPanel();
    await navigate(page, "delegate");
    await expect(page.getByTestId("page-delegate")).toHaveCount(0);
    await expect(page.getByTestId("not-supported")).toContainText("Agent 委托");
  } finally { await h.close(); }
});
