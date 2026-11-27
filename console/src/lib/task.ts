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
/**
 * A task, then its transcript. The agent posts its last items before it
 * finishes the task, so a transcript read after a finished task is whole.
 * A transcript that cannot be read leaves the task readable.
 */
export async function withTranscript<T, I>(
  task: () => Promise<T>,
  items: (t: T) => Promise<I>,
): Promise<{ task: T; items: I | null; err: string }> {
  const t = await task();
  try {
    return { task: t, items: await items(t), err: "" };
  } catch (e) {
    return { task: t, items: null, err: e instanceof Error ? e.message : String(e) };
  }
}

/**
 * Whether the transcript read with task seen still holds for the task as it
 * is now: the agent is not at work on it and nothing moved since, no reply
 * (E-99, also from another admin), no new step, no new answer.
 */
export function transcriptHolds(seen: Task | null, now: Task): boolean {
  return (
    !!seen &&
    now.phase !== "Pending" &&
    now.phase !== "Running" &&
    seen.name === now.name &&
    seen.phase === now.phase &&
    seen.steps.length === now.steps.length &&
    seen.finishedAt === now.finishedAt &&
    (seen.reply?.at ?? "") === (now.reply?.at ?? "")
  );
}

/** Where a task stands. */
export function taskState(t: Task): { label: string; tone: "ok" | "wait" | "bad" } {
  switch (t.phase) {
    case "Pending":
      return { label: t.reply ? "Reply sent, waiting for the agent" : "Waiting for an agent", tone: "wait" };
    case "Running":
      return { label: `Working, ${t.steps.length} ${t.steps.length === 1 ? "step" : "steps"} so far`, tone: "wait" };
    case "Succeeded":
      return { label: "Done", tone: "ok" };
    case "Failed":
      return { label: "Could not finish", tone: "bad" };
    case "WaitingForInput":
      return { label: "Waiting for your reply", tone: "wait" };
  }
}
