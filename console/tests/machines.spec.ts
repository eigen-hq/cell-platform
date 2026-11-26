import { expect, test } from "@playwright/test";
import { env, signIn, watchErrors } from "./helpers";

// The machines of the cell and the join command (E-44). The local cell runs
// on one machine and takes no more; the command itself is the API's answer.
test("the admin sees the machines and gets a join command", async ({ page, baseURL }) => {
  const errors = watchErrors(page, baseURL!);
  // A login started on /machines comes back to /machines.
  await signIn(page, env("CONSOLE_ADMIN_USER"), env("CONSOLE_ADMIN_PASSWORD"), "/machines");
  await expect(page.getByRole("heading", { name: "Machines", level: 1 })).toBeVisible();
  await expect(page.getByText(/One cell on one machine\./)).toBeVisible();
  await expect(page.getByText("Machines and links")).toBeVisible();
  await expect(page.getByText(/· runs the cell$/)).toBeVisible();
  await expect(page.getByText("cell services")).toBeVisible();

  await page.getByRole("button", { name: "Add machine" }).click();
  await expect(page.getByText("this cell takes no more machines")).toBeVisible();

  // The key comes apart from the command, to paste when it asks (E-64).
  const command = 's=$(curl -fsSL https://api.cell.example/v0/nodes/join.sh) && sudo bash -c "$s"';
  const key = `abcdef.${"x".repeat(16)}`;
  await page.route("**/v0/nodes/joins", (route) =>
    route.fulfill({ status: 201, contentType: "application/json", body: JSON.stringify({ command, key, expiresAt: "2030-01-01T12:00:00Z" }) }),
  );
  await page.getByRole("button", { name: "Add machine" }).click();
  await expect(page.getByRole("heading", { name: "Add a machine" })).toBeVisible();
  await expect(page.getByText(command)).toBeVisible();
  await expect(page.getByText(key, { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Copy the join key" })).toBeVisible();
  await page.getByRole("button", { name: "Done" }).click();
  await expect(page.getByRole("button", { name: "Add machine" })).toBeVisible();

  // A reload keeps the view (the console routes in the browser).
  await page.reload();
  await expect(page.getByRole("heading", { name: "Machines", level: 1 })).toBeVisible();
  // The browser reports the refused join (409) itself; nothing else may fail.
  expect(errors.filter((e) => !e.includes("status of 409"))).toEqual([]);
});
