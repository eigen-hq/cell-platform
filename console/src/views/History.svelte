<script lang="ts">
  // Every change of the cell (Figma: 25 History): the last 30 days at a
  // glance, then the change log with who asked, who decided and how it
  // ended; one change opens in a drawer under /history/<name>. Export
  // gives the shown rows as CSV.
  import { api, must } from "../lib/api/client";
  import type { components } from "../lib/api/schema";
  import { ended, headline, people, type Change } from "../lib/change";
  import { csv } from "../lib/csv";
  import { ago } from "../lib/time";
  import Avatar from "../lib/ui/Avatar.svelte";
  import DayStrip from "../lib/ui/DayStrip.svelte";
  import Risk from "../lib/ui/Risk.svelte";
  import ChangeDrawer from "./ChangeDrawer.svelte";

  type Task = components["schemas"]["Task"];
  type Backup = components["schemas"]["Backup"];

  let { open, onnavigate, admin }: { open?: string; onnavigate: (href: string) => void; admin: boolean } = $props();
  let changes = $state<Change[] | null>(null);
  let tasks = $state<Task[]>([]);
  let backups = $state<Backup[]>([]);
  let titles = $state<Map<string, string>>(new Map());
  let failed = $state("");
  let filter = $state<"all" | "waiting" | "people" | "agents" | "not">("all");

  async function load() {
    try {
      // Tasks can quote logs: only admins read them (E-63).
      const [c, t, a, k] = await Promise.all([api.GET("/v0/changes"), admin ? api.GET("/v0/tasks") : null, api.GET("/v0/apps"), api.GET("/v0/catalog")]);
      const apps = must(a);
      const b = await Promise.all(apps.map((x) => api.GET("/v0/apps/{name}/backups", { params: { path: { name: x.name } } })));
      changes = must(c);
      tasks = t ? must(t) : [];
      backups = b.flatMap((r) => must(r));
      titles = new Map([...must(k).map((e) => [e.name, e.title] as const), ...apps.map((x) => [x.name, x.title || x.name] as const)]);
      failed = "";
    } catch (e) {
      failed = e instanceof Error ? e.message : String(e);
    }
  }

  $effect(() => {
    load();
    const t = setInterval(load, 15000);
    return () => clearInterval(t);
  });

  const title = (app: string) => titles.get(app) ?? app;
  const filters = [
    ["all", "All"],
    ["waiting", "Waiting"],
    ["people", "By people"],
    ["agents", "By agents"],
    ["not", "Not applied"],
  ] as const;
  let shown = $derived(
    (changes ?? []).filter((c) =>
      filter === "waiting" ? c.phase === "WaitingForApproval"
      : filter === "people" ? c.requester.kind === "Human"
      : filter === "agents" ? c.requester.kind === "Agent"
      : filter === "not" ? ["Rejected", "RolledBack", "Failed"].includes(c.phase)
      : true,
    ),
  );

  // The last 30 days, oldest first; a day's square shows the worst that
  // happened on it.
  const days = Array.from({ length: 30 }, (_, i) => {
    const d = new Date();
    d.setHours(0, 0, 0, 0);
    d.setDate(d.getDate() - 29 + i);
    return d;
  });
  const dayOf = (iso: string) => {
    const d = new Date(iso);
    d.setHours(0, 0, 0, 0);
    return days.findIndex((x) => x.getTime() === d.getTime());
  };
  const rank = { bad: 4, wait: 3, ok: 2, info: 1, none: 0 } as const;
  type Tone = keyof typeof rank;
  function row(items: { at: string; tone: Tone }[]) {
    const out: (Tone | null)[] = days.map(() => null);
    for (const it of items) {
      const i = dayOf(it.at);
      if (i >= 0 && (out[i] === null || rank[it.tone] > rank[out[i]!])) out[i] = it.tone;
    }
    return out;
  }
  let strip = $derived([
    { label: "Changes", days: row((changes ?? []).map((c) => ({ at: c.createdAt, tone: ended(c).tone }))) },
    ...(admin ? [{ label: "Agent tasks", days: row(tasks.map((t) => ({ at: t.createdAt, tone: t.phase === "Failed" ? "bad" : "info" }))) }] : []),
    { label: "Backups", days: row(backups.map((b) => ({ at: b.createdAt, tone: b.phase === "Failed" ? "bad" : b.phase === "Completed" ? "ok" : "wait" }))) },
  ]);

  function exportCsv() {
    const body = csv([
      ["change", "app", "action", "what", "requester", "decided by", "decision", "risk", "asked", "finished", "ended", "reason"],
      ...shown.map((c) => [
        c.name, c.app, c.action, headline(c, title(c.app)), `${c.requester.kind}:${c.requester.name}`, c.approval.by ?? "",
        c.approval.decision ?? "", c.risk, c.createdAt, c.finishedAt ?? "", ended(c).label, c.reason,
      ]),
    ]);
    const a = document.createElement("a");
    a.href = URL.createObjectURL(new Blob([body], { type: "text/csv" }));
    a.download = `changes-${new Date().toISOString().slice(0, 10)}.csv`;
    a.click();
    URL.revokeObjectURL(a.href);
  }

  const go = (e: MouseEvent, href: string) => {
    if (e.metaKey || e.ctrlKey) return;
    e.preventDefault();
    onnavigate(href);
  };
  const when = (iso: string) => {
    const s = ago(iso);
    return s.endsWith("ago") || s === "just now" ? s : new Date(iso).toLocaleDateString([], { weekday: "short", day: "numeric", month: "short" });
  };
  let current = $derived(changes?.find((c) => c.name === open));
</script>

<section class="view-head">
  <h1>History</h1>
  <p>Every change, who asked for it, who approved it and how it ended.</p>
</section>

{#if failed}
  <p class="fail">{failed}</p>
{:else if changes}
  <DayStrip rows={strip} {days} />

  <div class="bar">
    <div class="tabs" role="tablist" aria-label="Which changes">
      {#each filters as [key, label] (key)}
        <button role="tab" aria-selected={filter === key} onclick={() => (filter = key)}>{label}</button>
      {/each}
    </div>
    <button class="export" onclick={exportCsv} disabled={!shown.length}>
      <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 4v11M7 10l5 5 5-5M5 20h14" /></svg>Export
    </button>
  </div>

  {#if shown.length}
    <div class="table" role="table" aria-label="Changes">
      <div class="row head" role="row">
        <span role="columnheader">Change</span><span role="columnheader">What</span>
        <span role="columnheader">Risk</span><span role="columnheader">When</span><span role="columnheader">Ended</span><span></span>
      </div>
      {#each shown as c (c.name)}
        {@const e = ended(c)}
        <a class="row" role="row" href="/history/{c.name}" onclick={(ev) => go(ev, `/history/${c.name}`)}>
          <span class="ref" role="cell">{c.name}</span>
          <span class="what" role="cell">
            <Avatar name={c.requester.name} size={24} />
            <span><strong>{headline(c, title(c.app))}</strong><small>{people(c)}</small></span>
          </span>
          <span class="risk" role="cell"><Risk level={c.risk} short /></span>
          <span class="muted" role="cell">{when(c.createdAt)}</span>
          <span role="cell"><span class="badge {e.tone}">{e.label}</span></span>
          <svg class="chev" viewBox="0 0 24 24" aria-hidden="true"><path d="M9 6l6 6-6 6" /></svg>
        </a>
      {/each}
    </div>
  {:else}
    <p class="muted empty">No changes here.</p>
  {/if}
  <p class="muted foot">{shown.length} of {changes.length} shown.</p>

  {#if current}
    <ChangeDrawer change={current} title={title(current.app)} onclose={() => onnavigate("/history")} />
  {/if}
{/if}

<style>
  .bar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    margin: 28px 0 12px;
  }
  .tabs {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    padding: 5px;
    border-radius: var(--radius-pill);
    background: var(--nav-bg);
  }
  .tabs button {
    border: 0;
    padding: 8px 14px;
    border-radius: var(--radius-pill);
    background: none;
    color: var(--nav-ink);
    font-weight: 600;
  }
  .tabs [aria-selected="true"] {
    background: var(--nav-current-bg);
    color: var(--nav-current-ink);
  }
  .export {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    border: 0;
    padding: 9px 16px;
    border-radius: var(--radius-pill);
    background: var(--color-panel);
    font-weight: 600;
  }
  .export svg {
    width: 16px;
    height: 16px;
    fill: none;
    stroke: currentColor;
    stroke-width: 2;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .table {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) 90px 130px 110px 18px;
  }
  .row {
    display: grid;
    grid-column: 1 / -1;
    grid-template-columns: subgrid;
    align-items: center;
    gap: 16px;
    padding: 12px;
    border-radius: var(--radius-part);
    color: inherit;
    text-decoration: none;
  }
  a.row:hover {
    background: var(--color-panel);
  }
  .row.head {
    padding-block: 0 6px;
    color: var(--color-muted);
    font-size: 13px;
    font-weight: 600;
  }
  .ref,
  .muted {
    color: var(--color-muted);
    font-size: 13.5px;
  }
  .what {
    display: flex;
    align-items: center;
    gap: 14px;
  }
  .what strong,
  .what small {
    display: block;
  }
  .what strong {
    font-weight: 600;
  }
  .what small {
    color: var(--color-muted);
    font-size: 13px;
  }
  .risk {
    font-size: 13.5px;
  }
  .badge {
    display: inline-block;
    padding: 4px 11px;
    border-radius: var(--radius-pill);
    font-size: 12.5px;
    font-weight: 700;
    white-space: nowrap;
  }
  .badge.wait { background: var(--paint-attention); color: var(--slip-ink); }
  .badge.ok { background: color-mix(in srgb, var(--color-ok) 22%, transparent); color: var(--color-ok); }
  .badge.bad { background: color-mix(in srgb, var(--color-bad) 22%, transparent); color: var(--color-bad); }
  .badge.none { background: var(--color-sunk); color: var(--color-ink); }
  .chev {
    width: 18px;
    height: 18px;
    fill: none;
    stroke: var(--color-dim);
    stroke-width: 2;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .foot {
    margin-top: 16px;
  }
  .empty {
    padding: 20px 12px;
  }
  @media (max-width: 900px) {
    .table {
      grid-template-columns: minmax(0, 1fr) auto 18px;
    }
    .row > :nth-child(1),
    .row > :nth-child(3),
    .row > :nth-child(4) {
      display: none;
    }
  }
</style>
