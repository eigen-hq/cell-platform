// The usage history of a machine or an app (E-87).
import type { components } from "./api/schema";

export type Usage = components["schemas"]["Usage"];

/** The spans the API keeps a history for, with their length in ms. */
export const spans = [
  ["1h", "1 hour", 36e5],
  ["24h", "24 hours", 864e5],
  ["7d", "7 days", 6048e5],
] as const;
export type Span = (typeof spans)[number][0];
export const spanMs = (s: Span) => spans.find((x) => x[0] === s)![2];

/** One figure of the history, the points that have it. */
export const series = (u: Usage, key: "cpu" | "memory") => u.points.flatMap((p) => (p[key] === undefined ? [] : [{ at: p.at, v: p[key] }]));
