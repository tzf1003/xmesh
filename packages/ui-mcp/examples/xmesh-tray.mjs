export default async ({ electronApp, page, expect, step }) => {
  await step("tray-pause", async () => {
    await electronApp.evaluate(() =>
      globalThis.__xmeshUiTest.clickMenu("action.drain"),
    );
    await expect(page.getByTestId("state")).toHaveAttribute(
      "data-state",
      "draining",
    );
  });
  await step("tray-resume", async () => {
    await electronApp.evaluate(() =>
      globalThis.__xmeshUiTest.clickMenu("action.resume"),
    );
    await expect(page.getByTestId("state")).toHaveAttribute(
      "data-state",
      /ok|degraded/,
    );
  });
};
