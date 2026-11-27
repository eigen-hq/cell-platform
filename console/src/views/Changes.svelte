<script lang="ts">
  // Every change of the cell: what waits for a decision on top, then the
  // change log with who asked, who decided and how it ended; one change
  // opens as its own page under /changes/<name>. Export gives the shown
  // rows of the log as CSV.
  import { api, must } from "../lib/api/client";
  import type { components } from "../lib/api/schema";
  import { backupOf, ended, groupsOf, headline, open as underWay, people, safety, trail, type Change } from "../lib/change";
  import { csv } from "../lib/csv";
  import { follow } from "../lib/live";
  import { newest, STALE } from "../lib/newest";
  import { itemPath } from "../lib/route";
  import { ago } from "../lib/time";
  import Button from "../lib/ui/Button.svelte";
  import Icon from "../lib/ui/Icon.svelte";
  import Logo from "../lib/ui/Logo.svelte";
  import Tag from "../lib/ui/Tag.svelte";
  import Slip from "./Slip.svelte";

  type App = components["schemas"]["App"];

  let { open, admin }: { open?: string; onnavigate: (href: string) => void; admin: boolean } = $props();
  let changes = $state<Change[] | null>(null);
  let apps = $state<App[]>([]);
  let titles = $state<Map<string, string>>(new Map());
  let failed = $state("");
  let filter = $state<"all" | "people" | "agents" | "not">("all");

  const guard = newest();
  async function load() {
    try {
      const answer = await guard(Promise.all([api.GET("/v0/changes"), api.GET("/v0/apps"), api.GET("/v0/catalog")]));
      if (answer === STALE) return;
      const [c, a, k] = answer;
      changes = must(c).sort((x, y) => y.createdAt.localeCompare(x.createdAt));
      apps = must(a);
      titles = new Map([...must(k).map((e) => [e.name, e.title] as const), ...apps.map((x) => [x.name, x.title || x.name] as const)]);
      failed = "";
    } catch (e) {
      failed = e instanceof Error ? e.message : String(e);
    }
  }

  // Live events reload at once (a burst counts once); the poll covers a
  // stream that is away.
  $effect(() => {
    load();
    const t = setInterval(load, 15000);
    let soon: ReturnType<typeof setTimeout> | undefined;
    const stop = follow(
      (signal) => api.GET("/v0/events", { parseAs: "stream", signal }),
      () => {
        clearTimeout(soon);
        soon = setTimeout(load, 300);
      },
      () => {},
    );
    return () => {
      clearInterval(t);
      clearTimeout(soon);
      stop();
    };
  });

  const title = (app: string) => titles.get(app) ?? app;
  const byName = $derived(new Map(apps.map((a) => [a.name, a])));
  const by = (c: Change) => (c.requester.kind === "Agent" ? "agent" : c.requester.kind === "Operator" ? "operator" : "person");
  const who = (c: Change) => (c.requester.kind === "Agent" ? `${c.requester.name.replace(/^./, (x) => x.toUpperCase())} agent` : c.requester.kind === "Operator" ? "Operator" : c.requester.name);
  const tone = (c: Change) => ({ ok: "ok", bad: "warn", wait: "neutral", none: "neutral" } as const)[ended(c).tone];
  const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;
  const clock = (iso: string) => {
    const d = new Date(iso);
    if (d.toDateString() === new Date().toDateString()) return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hourCycle: "h23" });
    return d.toLocaleDateString([], { day: "numeric", month: "short" });
  };
  const full = (iso: string) => new Date(iso).toLocaleString([], { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });

  let waiting = $derived((changes ?? []).filter((c) => c.phase === "WaitingForApproval"));
  let log = $derived((changes ?? []).filter((c) => c.phase !== "WaitingForApproval"));
  const filters = [
    ["all", "All"],
    ["people", "By people"],
    ["agents", "By agents"],
    ["not", "Not applied"],
  ] as const;
  let shown = $derived(
    log.filter((c) =>
      filter === "people" ? c.requester.kind === "Human"
      : filter === "agents" ? c.requester.kind === "Agent"
      : filter === "not" ? ["Rejected", "RolledBack", "Failed"].includes(c.phase)
      : true,
    ),
  );

  let lede = $derived.by(() => {
    if (!changes) return "";
    const month = changes.filter((c) => Date.now() - new Date(c.createdAt).getTime() < 30 * 86400_000).length;
    const running = changes.filter((c) => underWay(c) && c.phase !== "WaitingForApproval").length;
    const wait = waiting.length ? `${waiting.length === 1 ? "One waits" : `${waiting.length} wait`} for a decision from ${admin ? "you" : "an admin"}` : "Nothing waits for a decision";
    return `${wait}${running ? `, ${running} under way` : ""}. ${plural(month, "change")} in the last 30 days.`;
  });

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

  let current = $derived(open === undefined ? undefined : changes?.find((c) => c.name === open));
  let patch = $derived(current?.patch && Object.keys(current.patch).length ? JSON.stringify(current.patch, null, 2) : "");
  let groups = $derived(current && groupsOf(current));
</script>

{#if failed}<p class="fail" role="alert">{failed}</p>{/if}

{#if open !== undefined}
  <a class="go back" href="/changes"><span class="flip"><Icon name="right" size={13} stroke={2} /></span>All changes</a>
  {#if current}
    {@const c = current}
    {@const e = ended(c)}
    <header class="page-head">
      <div>
        <h1>{headline(c, title(c.app))}</h1>
        <p>{people(c).replace(/^./, (x) => x.toUpperCase())}{c.approval.at ? `, decided ${ago(c.approval.at)}` : ""}.</p>
      </div>
      <Logo app={byName.get(c.app) ?? { name: c.app }} title={title(c.app)} size={48} />
    </header>

    {#if c.phase === "WaitingForApproval"}
      <section class="sec" aria-labelledby="h-wait">
        <div class="sec-head"><h2 id="h-wait">Waiting for a decision</h2></div>
        <ul class="list">
          {#key c.name}<li><Slip change={c} title={title(c.app)} app={byName.get(c.app)} {admin} ondecided={load} /></li>{/key}
        </ul>
      </section>
    {/if}

    <section class="sec" aria-labelledby="h-state">
      <div class="sec-head">
        <h2 id="h-state">Where it stands</h2>
        <span>{c.finishedAt ? `ended ${ago(c.finishedAt)}` : c.startedAt ? `started ${ago(c.startedAt)}` : `asked ${ago(c.createdAt)}`}</span>
      </div>
      <div class="sheet">
        <ol class="trail" aria-label="Steps">
          {#each trail(c) as s, i (i)}
            <li class={s.state}>
              <span class="mark" aria-hidden="true">{#if s.state === "done"}<Icon name="check" size={12} stroke={2.4} />{:else if s.state === "failed"}<Icon name="x" size={12} stroke={2.4} />{/if}</span>
              {s.step}{s.state === "skipped" ? ": nothing to back up" : ""}
            </li>
          {/each}
        </ol>
        <dl class="facts">
          <div><dt>State</dt><dd><Tag tone={tone(c)}>{e.label}</Tag></dd></div>
          <div><dt>Risk</dt><dd><Tag tone={c.risk === "low" ? "neutral" : "warn"}>{c.risk} risk</Tag></dd></div>
          <div><dt>What protects it</dt><dd>{safety(c)}</dd></div>
          {#if c.reason}<div><dt>Why</dt><dd>{c.reason}</dd></div>{/if}
          {#if c.message}<div><dt>Message</dt><dd>{c.message}</dd></div>{/if}
          {#if c.action === "restore"}<div><dt>Brings back</dt><dd class="mono">{backupOf(c)}</dd></div>{/if}
          {#if groups}<div><dt>Open to</dt><dd>{groups.length ? groups.join(", ") : "everyone"}</dd></div>{/if}
          {#if c.backup}<div><dt>Backup before</dt><dd class:mono={c.backup !== "none"}>{c.backup === "none" ? "nothing to back up" : c.backup}</dd></div>{/if}
          {#if c.restore}<div><dt>Restore</dt><dd class="mono">{c.restore}</dd></div>{/if}
          <div><dt>Asked</dt><dd>{full(c.createdAt)}</dd></div>
          {#if c.finishedAt}<div><dt>Ended</dt><dd>{full(c.finishedAt)}</dd></div>{/if}
          <div><dt>Reference</dt><dd class="mono">{c.name}</dd></div>
        </dl>
        {#if patch}
          <details><summary>Show the details</summary><pre class="mono">{patch}</pre></details>
        {/if}
        {#if c.task && admin}<a class="go" href={itemPath("/agents", c.task)}>How the agent got there<Icon name="right" size={13} stroke={2} /></a>{/if}
      </div>
    </section>
  {:else if changes}
    <p class="empty plain">No change is called {open}. It may be older than the log keeps.</p>
  {:else if !failed}
    <p class="empty plain">Reading the change</p>
  {/if}
{:else if changes}
  <header class="page-head">
    <div>
      <h1>Changes</h1>
      <p>{lede}</p>
    </div>
  </header>

  <section class="sec" aria-labelledby="h-waiting">
    <div class="sec-head">
      <h2 id="h-waiting">Waiting for a decision</h2>
      <span>{waiting.length ? `${waiting.length} for ${admin ? "you" : "an admin"}, newest first` : ""}</span>
    </div>
    <ul class="list">
      {#each waiting as c (c.name)}
        <!-- keyed by the change: a newer request must not inherit a half-made decision -->
        <li><Slip change={c} title={title(c.app)} app={byName.get(c.app)} {admin} ondecided={load} /></li>
      {:else}
        <li class="empty">Nothing waits for a decision. New requests from people and agents show up here.</li>
      {/each}
    </ul>
  </section>

  <section class="sec" aria-labelledby="h-log">
    <div class="sec-head">
      <h2 id="h-log">Change log</h2>
      <span>{shown.length} of {log.length}</span>
    </div>
    <div class="bar">
      <div class="tabs" role="tablist" aria-label="Which changes">
        {#each filters as [key, label] (key)}
          <button role="tab" aria-selected={filter === key} onclick={() => (filter = key)}>{label}</button>
        {/each}
      </div>
      <Button kind="outline" size="sm" onclick={exportCsv} disabled={!shown.length}><Icon name="download" size={14} stroke={2} />Export</Button>
    </div>
    {#if shown.length}
      <ul class="log" aria-labelledby="h-log">
        {#each shown as c (c.name)}
          <li>
            <a class="row" href={itemPath("/changes", c.name)}>
              <time class="mono" datetime={c.createdAt} title={full(c.createdAt)}>{clock(c.createdAt)}</time>
              <Logo app={byName.get(c.app) ?? { name: c.app }} title={title(c.app)} size={24} />
              <span class="dt">{headline(c, title(c.app))}</span>
              <span class="by {by(c)}"><span aria-hidden="true"></span>{who(c)}</span>
              <Tag tone={tone(c)}>{ended(c).label}</Tag>
              <Icon name="right" size={13} stroke={2} />
            </a>
          </li>
        {/each}
      </ul>
    {:else}
      <p class="empty plain">No changes here.</p>
    {/if}
  </section>
{:else if !failed}
  <p class="empty plain">Reading the change log</p>
{/if}

<style>
  .back {
    margin-bottom: 20px;
    font-size: 13px;
  }
  .flip {
    display: inline-flex;
    transform: scaleX(-1);
  }
  .bar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 8px;
  }
  .tabs {
    display: flex;
    flex-wrap: wrap;
    gap: 2px;
    padding: 3px;
    border-radius: var(--radius-control);
    background: var(--color-tile);
  }
  .tabs button {
    height: 28px;
    padding: 0 12px;
    border: 0;
    border-radius: calc(var(--radius-control) - 2px);
    background: none;
    color: var(--color-text);
    font-size: 13px;
  }
  .tabs button:hover {
    color: var(--color-ink);
  }
  .tabs [aria-selected="true"] {
    background: var(--color-surface);
    color: var(--color-ink);
    box-shadow: var(--shadow-tab);
  }
  .log {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .log li {
    border-bottom: 1px solid var(--color-line);
  }
  .log a {
    display: flex;
    align-items: center;
    gap: 12px;
    height: 46px;
    margin: 0 -10px;
    padding: 0 10px;
    border-radius: var(--radius-control);
    font-size: 13px;
    color: var(--color-ink);
  }
  .log a > :global(svg) {
    flex: none;
    color: var(--color-muted);
  }
  .log time {
    flex: none;
    width: 52px;
    font-size: 12px;
    color: var(--color-muted);
  }
  .dt {
    flex: 1;
    min-width: 0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .by {
    flex: none;
    display: inline-flex;
    align-items: center;
    gap: 7px;
    width: 140px;
    font-size: 12px;
    color: var(--color-muted);
  }
  .by span {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--color-operator);
  }
  .by.agent span {
    background: var(--color-agent);
  }
  .by.person span {
    background: var(--color-ink);
  }
  .empty.plain {
    padding: 0;
  }
  .sheet {
    display: grid;
    gap: 24px;
    padding: 22px 24px;
    border-radius: var(--radius-panel);
    background: var(--color-surface);
    border: 1px solid var(--color-line);
  }
  .trail {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .trail li {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    height: 32px;
    padding: 0 12px 0 7px;
    border: 1px solid var(--color-chip-line);
    border-radius: var(--radius-control);
    font-size: 13px;
  }
  .mark {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 18px;
    height: 18px;
    border-radius: 50%;
    border: 1.5px solid var(--color-line-strong);
  }
  .trail .done .mark {
    border-color: var(--color-ok);
    background: var(--color-ok);
    color: var(--color-on-ink);
  }
  .trail .failed {
    border-color: var(--color-warn-edge);
    background: var(--color-warn-bg);
    color: var(--color-warn-text);
  }
  .trail .failed .mark {
    border-color: var(--color-warn);
    background: var(--color-warn);
    color: var(--color-on-ink);
  }
  .trail .open,
  .trail .skipped {
    color: var(--color-muted);
  }
  .trail .skipped .mark {
    border-style: dashed;
  }
  .facts {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
    gap: 18px 32px;
    margin: 0;
  }
  .facts dt {
    font-size: 12px;
    color: var(--color-muted);
  }
  .facts dd {
    margin: 4px 0 0;
    font-size: 14px;
    overflow-wrap: anywhere;
  }
  .facts dd.mono {
    font-size: 12.5px;
  }
  details {
    font-size: 13px;
    color: var(--color-text);
  }
  summary {
    cursor: pointer;
  }
  pre {
    margin: 10px 0 0;
    padding: 12px 14px;
    max-height: 320px;
    overflow: auto;
    border-radius: var(--radius-tile);
    background: var(--color-surface-2);
    border: 1px solid var(--color-hairline);
    font-size: 12px;
  }
  @media (max-width: 860px) {
    .by {
      display: none;
    }
    .sheet {
      padding: 18px;
    }
  }
</style>
