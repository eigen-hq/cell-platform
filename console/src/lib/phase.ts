// What a phase of the platform means to a person: a word and a light.
import type { components } from "./api/schema";

type Tone = "ok" | "wait" | "bad" | "none";
type App = components["schemas"]["App"];

const app: Record<App["phase"], [string, Tone]> = {
  "": ["Starting", "wait"],
  Installing: ["Installing", "wait"],
  Ready: ["Running", "ok"],
  Updating: ["Updating", "wait"],
  RollingBack: ["Rolling back", "wait"],
  Restoring: ["Restoring", "wait"],
  NeedsAttention: ["Needs attention", "bad"],
  Removing: ["Removing", "wait"],
};

/** How people get in: oidc and proxy need an account of the cell, none is open. */
export const authMode = (a: App) => (a.spec as { access?: { auth?: { mode?: string } } }).access?.auth?.mode ?? "oidc";

/** Who may open the app, as chips: its groups, or everyone. */
export const audience = (a: App) =>
  authMode(a) === "none" ? ["anyone, no sign-in"] : a.groups.length ? [...new Set(a.groups)] : ["everyone with an account"];

export const appStatus = (phase: App["phase"]) => {
  const [label, tone] = app[phase];
  return { label, tone };
};
