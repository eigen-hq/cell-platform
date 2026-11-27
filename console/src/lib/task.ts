// Agent tasks in plain words.
import type { components } from "./api/schema";

export type Task = components["schemas"]["Task"];
export type Finding = NonNullable<components["schemas"]["TaskAnswer"]["findings"]>[number];

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
 * (E-99, also from another admin), no new step, no new answer. A stopped
 * task is always read again: a post of its agent that the API already
 * served may land after the stop, and no field of the task tells when
 * (E-101).
 */
export function transcriptHolds(seen: Task | null, now: Task): boolean {
  return (
    !!seen &&
    now.phase !== "Pending" &&
    now.phase !== "Running" &&
    !now.stopped &&
    seen.name === now.name &&
    seen.phase === now.phase &&
    seen.steps.length === now.steps.length &&
    seen.finishedAt === now.finishedAt &&
    (seen.reply?.at ?? "") === (now.reply?.at ?? "")
  );
}

/** The heading of an answer's findings: the causes, for a diagnosis. */
export const findingsTitle = (kind: Task["kind"]) => (kind === "diagnose" ? "Causes" : "What it found");

/** A finding's status in plain words. */
export const findingState: Record<Finding["status"], string> = { confirmed: "Confirmed", suspected: "Suspected", ruledOut: "Ruled out" };

/** The key of an option in the list: a later answer that puts another
 * change in its place gets a fresh row, so no half-made decision on the
 * old change carries over to the new one. */
export const optionKey = (o: { change?: string }, i: number) => `${i}:${o.change ?? ""}`;

/** The changes the task proposed that no option of its answer names (E-100), in order. */
export function looseChanges(t: Task): string[] {
  const named = new Set((t.answer?.options ?? []).map((o) => o.change));
  return [...new Set(t.changes ?? [])].filter((n) => !named.has(n));
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
      // A person's stop is no failure of the agent (E-101).
      return t.stopped ? { label: `Stopped by ${t.stopped.by}`, tone: "wait" } : { label: "Could not finish", tone: "bad" };
    case "WaitingForInput":
      return { label: "Waiting for your reply", tone: "wait" };
  }
}
