// A list someone edits while the source may change underneath (for example
// group members while another admin edits them too).

const same = (a: string[], b: string[]) => {
  const x = new Set(a), y = new Set(b);
  return x.size === y.size && [...x].every((v) => y.has(v));
};
const uniq = (l: string[]) => [...new Set(l)];

/**
 * Where an edit stands: "clean" (nothing changed; follow the source),
 * "edited" (changes on the base the editor started from) or "conflict"
 * (the source moved since; saving would overwrite someone else's change).
 */
export function draftState(base: string[], draft: string[], current: string[]): "clean" | "edited" | "conflict" {
  if (!same(base, current)) return same(base, draft) ? "clean" : "conflict";
  return same(base, draft) ? "clean" : "edited";
}

/**
 * An edit with its source: the newest membership the server confirmed,
 * from a save's answer or a newly loaded snapshot. A reload that fails or
 * is dropped brings no snapshot, so it cannot roll the source back.
 */
export type Edit = { base: string[]; draft: string[]; source: string[] };

export const begin = (source: string[]): Edit => ({ base: uniq(source), draft: uniq(source), source: uniq(source) });

/** A newly loaded source; an untouched edit follows it. */
export function snapshot(e: Edit, source: string[]): Edit {
  return draftState(e.base, e.draft, e.source) === "clean" ? begin(source) : { ...e, source: uniq(source) };
}

/** The server answered a save with what it now holds. */
export const saved = (members: string[]): Edit => begin(members);

export const toggle = (e: Edit, v: string): Edit => ({ ...e, draft: e.draft.includes(v) ? e.draft.filter((x) => x !== v) : [...e.draft, v] });

export const startOver = (e: Edit): Edit => begin(e.source);

/** A list with one named item swapped for the server's newer answer. */
export const replaced = <T extends { name: string }>(list: T[], item: T): T[] => list.map((x) => (x.name === item.name ? item : x));

/** What a save sends: only who was ticked or unticked, never the whole list. */
export const changes = (e: Edit) => ({
  add: e.draft.filter((v) => !e.base.includes(v)),
  remove: e.base.filter((v) => !e.draft.includes(v)),
});

export const stateOf = (e: Edit) => draftState(e.base, e.draft, e.source);
