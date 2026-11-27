// Agent tasks in plain words.
import type { components } from "./api/schema";

export type Task = components["schemas"]["Task"];

/** The longest request the ask box sends (the API's limit). */
export const MAX_ASK = 4000;

const ops = { name: "Ops", does: "diagnoses apps, answers asks, drafts fixes" };
const packer = { name: "Packer", does: "makes an open-source project installable" };

/** The agent that does a kind of task, and what it does. */
export const agents = { diagnose: ops, package: packer, ask: ops } as const satisfies Record<Task["kind"], { name: string; does: string }>;

/** Each agent once, with the kinds of task it does. */
export const crew: { name: string; does: string; kinds: Task["kind"][] }[] = [
  { ...ops, kinds: ["diagnose", "ask"] },
  { ...packer, kinds: ["package"] },
];

/** One line for a task. */
export function task(t: Task, title: string): string {
  switch (t.kind) {
    case "diagnose":
      return `Find out what is wrong with ${title}`;
    case "package":
      return `Package ${title} from a Compose file`;
    case "ask": {
      const q = (t.input ?? "").trim().split("\n")[0] ?? "";
      const short = q.length > 90 ? `${q.slice(0, 89)}…` : q;
      return t.app ? `About ${title}: ${short}` : short || "A question about the cell";
    }
  }
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
