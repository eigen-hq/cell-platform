// A change request in plain words: what it does, in which steps, and what
// protects the app while it runs (docs/architektur.md: snapshot, change,
// health check, rollback on failure).
import type { components } from "./api/schema";

export type Change = components["schemas"]["ChangeRequest"];

const obj = (v: unknown): Record<string, unknown> => (v && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, unknown>) : {});

/** The groups a change gives the app, if it sets them. */
export function groupsOf(c: Change): string[] | undefined {
  const p = obj(c.patch);
  const g = obj(p.access).groups;
  return Array.isArray(g) ? [...new Set(g.filter((x): x is string => typeof x === "string"))] : undefined;
}

/** The backup a restore brings back; without one, the API takes the newest. */
export const backupOf = (c: Change) => c.restores || "the newest backup";

/** One sentence for the change; title is the app's name for people. */
export function headline(c: Change, title: string): string {
  const p = obj(c.patch);
  switch (c.action) {
    case "install":
      return `Install ${title}`;
    case "remove":
      return c.deleteData ? `Remove ${title} and delete its data` : `Remove ${title}, keep its data`;
    case "restore":
      return `Bring back ${title} from a backup`;
    case "update": {
      const keys = Object.keys(p);
      if (keys.length === 1 && typeof p.version === "string") return `Update ${title} to ${p.version}`;
      if (keys.length === 1 && keys[0] === "access" && Object.keys(obj(p.access)).every((k) => k === "groups")) return `Change who can open ${title}`;
      return `Change the settings of ${title}`;
    }
  }
}

/** The steps the platform runs once the change is approved. */
export const steps = (c: Change): string[] =>
  ({
    install: ["Check the manifest", "Install", "Health check", "Done"],
    update: ["Backup", "Apply the change", "Health check", "Done"],
    restore: ["Back up the current state", "Bring the backup back", "Health check", "Done"],
    remove: ["Backup", "Remove", "Done"],
  })[c.action];

/** What keeps the app safe while the change runs. */
export const safety = (c: Change): string =>
  ({
    install: "Counts as running only once its health check passes",
    update: "Backup first, rolls back by itself if the health check fails",
    restore: "The current state is backed up first",
    remove: c.deleteData ? "Backup first; the backups stay in the bucket" : "Backup first; the data stays in the cell for a reinstall",
  })[c.action];

/** Whether a change still waits for someone's decision or is under way. */
export const open = (c: Change) => c.phase === "WaitingForApproval" || c.phase === "Pending" || c.phase === "" || c.phase === "Running";
