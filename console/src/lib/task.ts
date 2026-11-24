// Agent tasks in plain words.
import type { components } from "./api/schema";

export type Task = components["schemas"]["Task"];

/** The agent that does a kind of task, and what it does. */
export const agents = {
  diagnose: { name: "Ops", does: "diagnoses apps, drafts fixes" },
  package: { name: "Packer", does: "makes an open-source project installable" },
} as const satisfies Record<Task["kind"], { name: string; does: string }>;

/** One line for a task. */
export function task(t: Task, title: string): string {
  return t.kind === "diagnose" ? `Find out what is wrong with ${title}` : `Package ${title} from a Compose file`;
}

/** Where a task stands. */
export function taskState(t: Task): { label: string; tone: "ok" | "wait" | "bad" } {
  switch (t.phase) {
    case "Pending":
      return { label: "Waiting for an agent", tone: "wait" };
    case "Running":
      return { label: `Working, ${t.steps.length} ${t.steps.length === 1 ? "step" : "steps"} so far`, tone: "wait" };
    case "Succeeded":
      return { label: "Done", tone: "ok" };
    case "Failed":
      return { label: "Could not finish", tone: "bad" };
  }
}
