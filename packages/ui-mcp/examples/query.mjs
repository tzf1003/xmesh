export default async ({ page, expect, params, step }) => {
  await step("query", async () => {
    await page.getByLabel("Username").fill(params.username ?? "test-user");
    await page.getByRole("button", { name: "Search" }).click();
    await expect(page.getByTestId("results")).toContainText(
      params.username ?? "test-user",
    );
  });
};
