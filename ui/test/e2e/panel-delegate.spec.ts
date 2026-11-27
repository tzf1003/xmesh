import { expect, test } from "@playwright/test";
import { launch, navigate } from "./helpers";

test("delegation shows its switch, runners, live sessions and tasks, and can cancel a running task", async () => {
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
    await expect(page.getByTestId("delegate-task-cancel-t-19ab3e02c5f84d7aa6e1b9d0c3f27a48")).toHaveCount(0);

    await page.getByTestId("delegate-toggle").click();
    await expect(page.getByTestId("delegate-state")).toHaveText("已开启");
    expect((JSON.parse(h.kernel.panel.content) as { agent: { delegate: { enabled: boolean } } }).agent.delegate.enabled).toBe(true);

    await page.getByTestId("delegate-task-cancel-t-5e0a7c41b9d24f6e8a13c0d7f2e4b916").click();
    await expect(page.getByTestId("delegate-task-state-t-5e0a7c41b9d24f6e8a13c0d7f2e4b916")).toHaveText("已取消");
    await expect(page.getByTestId("delegate-limits")).toContainText("运行中 0 个");
    await expect(page.getByTestId("delegate-task-cancel-t-5e0a7c41b9d24f6e8a13c0d7f2e4b916")).toHaveCount(0);

    await page.screenshot({ path: test.info().outputPath("delegate-page.png"), fullPage: true });
    expect(errors).toEqual([]);
  } finally { await h.close(); }
});

test("allow_write and allow_sessions require confirmation with risk explanation before persisting", async () => {
  const h = await launch({ kernel: { capabilities: ["status", "events", "peers", "sessions", "config.read", "config.write", "delegate"] } });
  try {
    h.kernel.panel.delegate.allow_write = false;
    h.kernel.panel.delegate.allow_sessions = false;
    const page = await h.openPanel();
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await navigate(page, "delegate");

    const allowWrite = page.getByTestId("delegate-allow-write");
    const allowSessions = page.getByTestId("delegate-allow-sessions");
    await expect(allowWrite).not.toBeChecked();
    await expect(allowSessions).not.toBeChecked();
    await expect(page.getByTestId("delegate-policy")).toContainText("允许写入：未允许 · 允许注入活会话：未允许");
    await expect(page.getByTestId("delegate-sessions")).toHaveCount(0);

    // Cancelling the confirmation leaves both the UI and config untouched.
    await allowWrite.click();
    await expect(page.getByTestId("confirm")).toBeVisible();
    await expect(page.getByTestId("delegate-confirm-risk")).toContainText("放宽安全边界");
    await page.getByTestId("confirm-cancel").click();
    await expect(page.getByTestId("confirm")).toHaveCount(0);
    await expect(allowWrite).not.toBeChecked();
    expect(h.kernel.panel.writes).toHaveLength(0);

    // Confirming allow_write persists and updates the page immediately.
    await allowWrite.click();
    await expect(page.getByTestId("confirm-ok")).toHaveClass(/danger/);
    await page.getByTestId("confirm-ok").click();
    await expect(page.getByTestId("confirm")).toHaveCount(0);
    await expect(allowWrite).toBeChecked();
    await expect(page.getByTestId("delegate-policy")).toContainText("允许写入：已允许");
    expect((JSON.parse(h.kernel.panel.content) as { agent: { delegate: { allow_write: boolean } } }).agent.delegate.allow_write).toBe(true);

    // Confirming allow_sessions persists and reveals live sessions immediately.
    await allowSessions.click();
    await expect(page.getByTestId("delegate-confirm-risk")).toContainText("交互权限");
    await page.getByTestId("confirm-ok").click();
    await expect(allowSessions).toBeChecked();
    await expect(page.getByTestId("delegate-policy")).toContainText("允许注入活会话：已允许");
    await expect(page.getByTestId("delegate-sessions")).toContainText("foo refactor");
    expect((JSON.parse(h.kernel.panel.content) as { agent: { delegate: { allow_sessions: boolean } } }).agent.delegate.allow_sessions).toBe(true);

    // Disabling allow_sessions also confirms and hides live sessions again.
    await allowSessions.click();
    await expect(page.getByTestId("confirm")).toBeVisible();
    await expect(page.getByTestId("delegate-confirm-risk")).toHaveCount(0);
    await page.getByTestId("confirm-ok").click();
    await expect(allowSessions).not.toBeChecked();
    await expect(page.getByTestId("delegate-sessions")).toHaveCount(0);
    expect((JSON.parse(h.kernel.panel.content) as { agent: { delegate: { allow_sessions: boolean } } }).agent.delegate.allow_sessions).toBe(false);

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
