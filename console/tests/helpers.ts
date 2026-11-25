import type { Page } from "@playwright/test";

export function env(name: string): string {
  const v = process.env[name];
  if (!v) throw new Error(`${name} is not set`);
  return v;
}

// Every error in the console's own pages fails the test: CSP refusals show
// up only there. The identity's pages are not ours to judge.
export function watchErrors(page: Page, origin: string): string[] {
  const errors: string[] = [];
  page.on("console", (m) => m.type() === "error" && m.location().url.startsWith(origin) && errors.push(m.text()));
  page.on("pageerror", (e) => page.url().startsWith(origin) && errors.push(e.message));
  return errors;
}

export async function signIn(page: Page, username: string, password: string, start = "/") {
  await page.goto(start);
  await page.waitForURL(/\/if\/flow\//);
  // authentik asks for the name, then the password, each as its own step.
  const uid = page.locator('input[name="uidField"]');
  await uid.fill(username);
  await uid.press("Enter");
  const pw = page.getByRole("textbox", { name: "Password" });
  await pw.fill(password);
  await pw.press("Enter");
  // Back at the console once the identity let the user in (or refused).
  const origin = new URL(env("CONSOLE_URL")).origin;
  await page.waitForURL((u) => u.origin === origin && !u.searchParams.has("code"), { timeout: 30_000 });
}
