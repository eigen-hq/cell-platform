import { expect, test } from "@playwright/test";
import { env, signIn, watchErrors } from "./helpers";

// The machines of the cell and the join command (E-44), on a cell of one
// machine or more. The join answers are the browser's own: the API's refusal
// and a real key are its tests' (nodes_test.go), and a run makes no key.
test("the admin sees the machines and gets a join command", async ({ page, baseURL }) => {
  const errors = watchErrors(page, baseURL!);
  // A login started on /machines comes back to /machines.
  await signIn(page, env("CONSOLE_ADMIN_USER"), env("CONSOLE_ADMIN_PASSWORD"), "/machines");
  await expect(page.getByRole("heading", { name: "Machines", level: 1 })).toBeVisible();
  await expect(page.getByText(/^(One machine|\d+ machines), /)).toBeVisible();
  await expect(page.getByRole("heading", { name: "What runs where", level: 2 })).toBeVisible();
  // One machine runs the cell, its services and its load.
  const plate = page.getByRole("article").filter({ hasText: /runs the cell, / });
  await expect(plate).toHaveCount(1);
  await expect(plate.getByText("cell services")).toBeVisible();
  await expect(plate.getByText("Processor").first()).toBeVisible();
  await expect(plate.getByText("Kubernetes")).toBeVisible();

  // A cell that takes no more machines refuses a join; the console says why.
  await page.route("**/v0/nodes/joins", (route) =>
    route.fulfill({ status: 409, contentType: "application/json", body: JSON.stringify({ error: "this cell takes no more machines (it runs on one machine only)" }) }),
  );
  await page.getByRole("button", { name: "Add a machine" }).click();
  await expect(page.getByRole("alert").filter({ hasText: "this cell takes no more machines" })).toBeVisible();
  await expect(page.getByRole("dialog")).toHaveCount(0);

  // The key comes apart from the command, to paste when it asks (E-64).
  const command = 'curl -fsSL https://api.cell.example/v0/nodes/join.sh | bash';
  const key = `abcdef.${"x".repeat(16)}`;
  await page.unroute("**/v0/nodes/joins");
  await page.route("**/v0/nodes/joins", (route) =>
    route.fulfill({ status: 201, contentType: "application/json", body: JSON.stringify({ command, key, expiresAt: "2030-01-01T12:00:00Z" }) }),
  );
  await page.getByRole("button", { name: "Add a machine" }).click();
  const drawer = page.getByRole("dialog");
  await expect(drawer.getByRole("heading", { name: "Add a machine" })).toBeVisible();
  await expect(drawer.getByText(command)).toBeVisible();
  await expect(drawer.getByText(key, { exact: true })).toBeVisible();
  await expect(drawer.getByRole("button", { name: "Copy the command" })).toBeVisible();
  await expect(drawer.getByRole("button", { name: "Copy the join key" })).toBeVisible();
  await drawer.getByRole("button", { name: "Done" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Add a machine" })).toBeVisible();

  // A reload keeps the view (the console routes in the browser).
  await page.reload();
  await expect(page.getByRole("heading", { name: "Machines", level: 1 })).toBeVisible();
  // The browser reports the refused join (409) itself; nothing else may fail.
  expect(errors.filter((e) => !e.includes("status of 409"))).toEqual([]);
});
