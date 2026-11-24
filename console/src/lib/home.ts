// The Home overview in plain words: what the headline says, how many
// people can open each app, and when the cell last backed up.
import type { components } from "./api/schema";
import { appStatus, authMode } from "./phase.ts";

type App = components["schemas"]["App"];
type Change = components["schemas"]["ChangeRequest"];
type User = components["schemas"]["User"];
type Group = components["schemas"]["Group"];
type Backup = components["schemas"]["Backup"];

/** How many people can open an app: everyone, or the members of its groups. */
export function reach(app: App, people: User[], groups: Group[]): number {
  if (authMode(app) === "none" || app.groups.length === 0) return people.length;
  const names = new Set(people.map((p) => p.username));
  const members = new Set(groups.filter((g) => app.groups.includes(g.name)).flatMap((g) => g.members));
  return [...members].filter((m) => names.has(m)).length;
}

/** Whether people can use the app now: its last health check passed. */
export const isUp = (a: App) => a.conditions.some((c) => c.type === "Ready" && c.status === "True");

/** Down: the platform gave up on it and its health check fails. NeedsAttention
 * alone can also mean a failed change on an app that still runs. */
export const isDown = (a: App) => a.phase === "NeedsAttention" && !isUp(a);

/** An app's state for people: down and silent come from its health check. */
export function appState(a: App): { label: string; tone: "ok" | "wait" | "bad" | "none" } {
  if (isDown(a)) return { label: "Down", tone: "bad" };
  if (a.phase === "Ready" && !isUp(a)) return { label: "Not answering", tone: "wait" };
  return appStatus(a.phase);
}

const busy: Partial<Record<App["phase"], string>> = {
  "": "is starting", Installing: "is installing", Updating: "is updating", RollingBack: "is rolling back", Restoring: "is coming back from a backup", Removing: "is being removed",
};

/** Where the cell stands: it needs the admin, the platform is busy with it, or all is well. */
export type Mood = "you" | "busy" | "good";

/** The two lines at the top of Home and where the cell stands. */
export function hero(apps: App[], changes: Change[], title: (app: string) => string): { lines: [string, string]; mood: Mood } {
  const waiting = changes.filter((c) => c.phase === "WaitingForApproval");
  const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;
  const wait = waiting.length ? `${plural(waiting.length, "change waits", "changes wait")} for your OK.` : "";
  const fixFor = (a: App) => waiting.some((c) => c.app === a.name);
  const name = (list: App[]) => title(list[0]!.name);
  const down = apps.filter(isDown);
  if (down.length === 1) return { lines: [`${name(down)} is down.`, fixFor(down[0]!) ? "A fix is waiting for your OK." : "Ops can find out why."], mood: "you" };
  if (down.length > 1) return { lines: [`${down.length} apps are down.`, "Open them to see why."], mood: "you" };
  const stuck = apps.filter((a) => a.phase === "NeedsAttention");
  if (stuck.length) {
    const one = stuck.length === 1;
    return {
      lines: [one ? `${name(stuck)} needs a look.` : `${stuck.length} apps need a look.`, fixFor(stuck[0]!) ? "A fix is waiting for your OK." : one ? "A change did not go through; it still runs." : "Changes did not go through; they still run."],
      mood: "you",
    };
  }
  const moving = apps.filter((a) => busy[a.phase]);
  if (moving.length) {
    const first = moving.length === 1 ? `${name(moving)} ${busy[moving[0]!.phase]}.` : `${moving.length} apps are changing.`;
    return { lines: [first, wait || "The platform is on it."], mood: waiting.length ? "you" : "busy" };
  }
  // Ready but failing its health check: the platform waits out the timeout
  // before it acts.
  const silent = apps.filter((a) => !isUp(a));
  if (silent.length) {
    const first = silent.length === 1 ? `${name(silent)} does not answer.` : `${silent.length} apps do not answer.`;
    return { lines: [first, wait || "The platform keeps checking and acts if this goes on."], mood: waiting.length ? "you" : "busy" };
  }
  if (waiting.length) return { lines: ["Everything is running.", wait], mood: "you" };
  if (!apps.length) return { lines: ["The cell is ready.", "Add the first app."], mood: "good" };
  return { lines: ["Everything is running.", "Nothing waits for you."], mood: "good" };
}

/** The newest finished backup over all apps, or the newest failed one if that came later. */
export function lastBackup(backups: Backup[]): { at: string; ok: boolean } | null {
  const ended = backups
    .filter((b) => b.phase === "Completed" || b.phase === "Failed")
    .map((b) => ({ at: b.completedAt ?? b.createdAt, ok: b.phase === "Completed" }))
    .sort((x, y) => y.at.localeCompare(x.at));
  return ended[0] ?? null;
}
