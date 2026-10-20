// The CI runners page against the fake kernel (control API 1.8): GitHub accounts, binding a repository the
// three ways, editing and unbinding, and what an instance that needs the user offers. Tokens are typed into the
// real app and must never come back in any page text.

import { type Page, expect, test } from "@playwright/test";
import type { Capability } from "../../src/shared/control";
import { type Harness, launch } from "./helpers";

const ALL: Capability[] = ["status", "events", "peers", "sessions", "drain", "config.read", "config.write", "pair", "pats", "ingress", "audit", "ci"];
const GOOD_TOKEN = "github_pat_11AAAAAAA0abcdefghij_klmnopqrstuvwxyz0123456789";
const REG_TOKEN = "AABBCCDDEEFF1122334455667788";

let h: Harness;
test.afterEach(async () => {
  await h?.close();
});

async function openCi(capabilities: Capability[] = ALL): Promise<Page> {
  h = await launch({ kernel: { capabilities } });
  const page = await h.openPanel();
  await expect(page.getByTestId("state")).toHaveAttribute("data-state", /ok|degraded/);
  await page.getByTestId("nav-ci").click();
  await expect(page.getByTestId("page-ci")).toBeVisible();
  return page;
}

async function addAccount(page: Page, token = GOOD_TOKEN): Promise<void> {
  await page.getByTestId("ci-account-add").click();
  await page.getByTestId("ci-token").fill(token);
  await page.getByTestId("ci-account-save").click();
}

test("ci: a kernel without the capability gets the not-supported notice", async () => {
  const page = await openCi(ALL.filter((c) => c !== "ci"));
  await expect(page.getByTestId("not-supported")).toBeVisible();
  await expect(page.getByTestId("ci-add")).toHaveCount(0);
});

test("ci: an empty page invites the first binding; a refused token says why and the accepted one is masked", async () => {
  const page = await openCi();
  await expect(page.getByText("还没有绑定任何仓库")).toBeVisible();
  await expect(page.getByTestId("ci-accounts-none")).toBeVisible();

  await page.getByTestId("ci-account-add").click();
  await page.getByTestId("ci-token").fill("short");
  await expect(page.getByTestId("ci-account-save")).toBeDisabled();
  await expect(page.getByTestId("ci-token-invalid")).toBeVisible();
  await page.getByTestId("ci-token").fill("ghp_bad0000000000000000000000000000");
  await page.getByTestId("ci-account-save").click();
  await expect(page.getByTestId("ci-error")).toContainText("GitHub 不接受这个令牌");
  // The token field is cleared once the kernel has it.
  await expect(page.getByTestId("ci-token")).toHaveValue("");

  await page.getByTestId("ci-token").fill(GOOD_TOKEN);
  await page.getByTestId("ci-account-save").click();
  await expect(page.getByTestId("ci-account-gh-tzf1003")).toContainText("tzf1003");
  await expect(page.getByTestId("ci-account-gh-tzf1003")).toContainText("…");
  expect(h.kernel.panel.ci.tokens).toContain(GOOD_TOKEN);
  expect(await page.locator("body").innerText()).not.toContain(GOOD_TOKEN);
});

test("ci: bind a repository from an account; a public one needs a second confirmation", async () => {
  const page = await openCi();
  await addAccount(page);
  await expect(page.getByTestId("ci-account-gh-tzf1003")).toBeVisible();

  await page.getByTestId("ci-add").click();
  await expect(page.getByTestId("ci-way-account")).toHaveAttribute("aria-selected", "true");
  await expect(page.getByTestId("ci-create")).toBeDisabled();
  await page.getByTestId("ci-repo-tzf1003/xmesh").check();
  await page.getByTestId("ci-concurrency").fill("2");
  await page.getByTestId("ci-labels").fill("linux, gpu");
  await page.getByTestId("ci-create").click();
  await expect(page.getByTestId("ci-binding-xmesh")).toContainText("tzf1003/xmesh");
  await expect(page.getByTestId("ci-binding-xmesh")).toContainText("gpu");
  await expect(page.getByTestId("ci-state-xmesh-1")).toHaveAttribute("data-state", "idle");
  await expect(page.getByTestId("ci-state-xmesh-2")).toHaveAttribute("data-state", "idle");

  // The bound repository is offered no more.
  await page.getByTestId("ci-add").click();
  await expect(page.getByTestId("ci-repo-tzf1003/xmesh")).toBeDisabled();
  await page.getByTestId("ci-repo-more").click();
  await expect(page.getByTestId("ci-repo-tzf1003/notes")).toBeVisible();
  await page.getByTestId("ci-repo-tzf1003/oss-lib").check();
  await page.getByTestId("ci-create").click();
  await expect(page.getByTestId("confirm")).toContainText("公开仓库");
  await page.getByTestId("confirm-cancel").click();
  await expect(page.getByTestId("ci-binding-oss-lib")).toHaveCount(0);
  await page.getByTestId("ci-create").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("ci-binding-oss-lib")).toContainText("公开仓库");
});

test("ci: bind with a pasted registration token; the token is not kept", async () => {
  const page = await openCi();
  await page.getByTestId("ci-add").click();
  // Without an account the token way is the one offered first.
  await expect(page.getByTestId("ci-way-token")).toHaveAttribute("aria-selected", "true");
  await page.getByTestId("ci-repo").fill("not a repo");
  await expect(page.getByTestId("ci-repo-invalid")).toBeVisible();
  await expect(page.getByTestId("ci-create")).toBeDisabled();
  await page.getByTestId("ci-repo").fill("https://github.com/tzf1003/xmesh.git");
  await page.getByTestId("ci-reg-token").fill(REG_TOKEN);
  await page.getByTestId("ci-create").click();
  await expect(page.getByTestId("ci-binding-xmesh")).toContainText("注册令牌");
  expect(h.kernel.panel.ci.tokens).toContain(REG_TOKEN);
  expect(await page.locator("body").innerText()).not.toContain(REG_TOKEN);
});

test("ci: taking over a runner directory refuses one with the official service and explains how to remove it", async () => {
  const page = await openCi();
  await page.getByTestId("ci-add").click();
  await page.getByTestId("ci-way-import").click();
  await page.getByTestId("ci-dir").fill("/runners/service");
  await page.getByTestId("ci-dir-inspect").click();
  await expect(page.getByTestId("ci-inspect-service-command")).toContainText("svc.sh uninstall");
  await expect(page.getByTestId("ci-create")).toBeDisabled();

  await page.getByTestId("ci-dir").fill("/runners/ephemeral");
  await page.getByTestId("ci-dir-inspect").click();
  await expect(page.getByTestId("ci-inspect-ephemeral")).toBeVisible();
  await expect(page.getByTestId("ci-create")).toBeDisabled();

  await page.getByTestId("ci-dir").fill("/runners/missing");
  await page.getByTestId("ci-dir-inspect").click();
  await expect(page.getByTestId("ci-error")).toContainText("没有已注册的仓库级运行器");

  await page.getByTestId("ci-dir").fill("/runners/clean");
  await page.getByTestId("ci-dir-inspect").click();
  await expect(page.getByTestId("ci-inspection")).toContainText("tzf1003/legacy");
  await page.getByTestId("ci-create").click();
  await expect(page.getByTestId("ci-binding-legacy")).toContainText("接管的运行器");
});

test("ci: edit the scale, pause and resume, then unbind", async () => {
  const page = await openCi();
  await addAccount(page);
  await page.getByTestId("ci-add").click();
  await page.getByTestId("ci-repo-tzf1003/xmesh").check();
  await page.getByTestId("ci-create").click();
  await expect(page.getByTestId("ci-binding-xmesh")).toBeVisible();

  await page.getByTestId("ci-edit-xmesh").click();
  await expect(page.getByTestId("ci-edit-save")).toBeDisabled();
  await page.getByTestId("ci-edit-concurrency").fill("3");
  await page.getByTestId("ci-edit-labels").fill("bad label!");
  await expect(page.getByTestId("ci-edit-labels-invalid")).toBeVisible();
  await page.getByTestId("ci-edit-labels").fill("mac");
  await page.getByTestId("ci-edit-save").click();
  await expect(page.getByTestId("ci-state-xmesh-3")).toBeVisible();

  await page.getByTestId("ci-pause-xmesh").click();
  await expect(page.getByTestId("ci-state-xmesh-1")).toHaveAttribute("data-state", "stopped");
  await page.getByTestId("ci-pause-xmesh").click();
  await expect(page.getByTestId("ci-state-xmesh-1")).toHaveAttribute("data-state", "idle");

  // A running job is called out, and the user may choose not to wait for it.
  h.kernel.panel.ci.bindings[0]!.instances[0]!.state = "busy";
  h.kernel.panel.ci.bindings[0]!.instances[0]!.job = { name: "build", started_at: new Date().toISOString() };
  await expect(page.getByTestId("ci-state-xmesh-1")).toHaveAttribute("data-state", "busy");
  await page.getByTestId("ci-unbind-xmesh").click();
  await expect(page.getByTestId("ci-unbind-busy")).toBeVisible();
  await page.getByTestId("ci-unbind-force").check();
  await page.getByTestId("ci-unbind-confirm").click();
  await expect(page.getByTestId("ci-binding-xmesh")).toHaveCount(0, { timeout: 8000 });
  await expect(page.getByText("还没有绑定任何仓库")).toBeVisible();
});

test("ci: an account that bindings use cannot be deleted; an unused one can", async () => {
  const page = await openCi();
  await addAccount(page);
  await page.getByTestId("ci-add").click();
  await page.getByTestId("ci-repo-tzf1003/xmesh").check();
  await page.getByTestId("ci-create").click();
  await expect(page.getByTestId("ci-binding-xmesh")).toBeVisible();

  await page.getByTestId("ci-account-delete-gh-tzf1003").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("ci-account-error")).toContainText("还有绑定在用这个账号");

  await page.getByTestId("ci-unbind-xmesh").click();
  await page.getByTestId("ci-unbind-confirm").click();
  await expect(page.getByTestId("ci-binding-xmesh")).toHaveCount(0, { timeout: 8000 });
  await page.getByTestId("ci-account-delete-gh-tzf1003").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("ci-accounts-none")).toBeVisible();
});

test("ci: an instance that needs the user offers the matching step", async () => {
  const page = await openCi();
  await page.getByTestId("ci-add").click();
  await page.getByTestId("ci-repo").fill("tzf1003/xmesh");
  await page.getByTestId("ci-reg-token").fill(REG_TOKEN);
  await page.getByTestId("ci-create").click();
  await expect(page.getByTestId("ci-binding-xmesh")).toBeVisible();

  const instance = h.kernel.panel.ci.bindings[0]!.instances[0]!;
  instance.state = "broken";
  instance.problem = { code: "session_conflict", message: "这个运行器在别处也在运行，请先关掉另一个。" };
  await expect(page.getByTestId("ci-instance-xmesh-1")).toContainText("在别处也在运行");
  await expect(page.getByTestId("ci-step-xmesh-1")).toHaveText("重启");
  await page.getByTestId("ci-step-xmesh-1").click();
  await expect(page.getByTestId("toast").filter({ hasText: "已重启" })).toBeVisible();

  // GitHub removed the runner: with no account the user must paste a fresh registration token.
  instance.problem = { code: "runner_removed", message: "GitHub 上的运行器被删除了。" };
  await expect(page.getByTestId("ci-step-xmesh-1")).toHaveText("重新注册");
  await page.getByTestId("ci-step-xmesh-1").click();
  await expect(page.getByTestId("ci-repair-submit")).toBeDisabled();
  await page.getByTestId("ci-repair-token").fill(REG_TOKEN);
  await page.getByTestId("ci-repair-submit").click();
  await expect(page.getByTestId("ci-state-xmesh-1")).toHaveAttribute("data-state", "idle", { timeout: 8000 });
});

test("ci: notices show the command to run, and a draining node is called out", async () => {
  h = await launch({ kernel: { capabilities: ALL } });
  h.kernel.panel.ci.notices = [{ code: "linger_disabled", message: "x", command: "loginctl enable-linger $USER" }, { code: "git_missing", message: "y" }];
  h.kernel.panel.ci.draining = true;
  const page = await h.openPanel();
  await expect(page.getByTestId("state")).toHaveAttribute("data-state", /ok|degraded|draining/);
  await page.getByTestId("nav-ci").click();
  await expect(page.getByTestId("ci-notice-command-linger_disabled")).toHaveText("loginctl enable-linger $USER");
  await expect(page.getByTestId("ci-notice-git_missing")).toContainText("git");
  await expect(page.getByTestId("ci-draining")).toBeVisible();
});

test("ci: public token registration retains the token through confirmation", async()=>{
  const page=await openCi();
  await page.getByTestId("ci-add").click();
  await page.getByTestId("ci-repo").fill("tzf1003/oss-lib");
  await page.getByTestId("ci-reg-token").fill(REG_TOKEN);
  await page.getByTestId("ci-create").click();
  await expect(page.getByTestId("confirm")).toBeVisible();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("ci-add-dialog")).toHaveCount(0);
});
