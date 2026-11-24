<script lang="ts">
  // The agents of the cell (Figma: 21 Agents): who did the work in the last
  // 30 days (rules, agents, people), the agents with their task counts, a
  // work board (waiting for you, agents working, done this week) and what
  // may happen without a person. One task opens under /agents/<task>.
  import { api, must } from "../lib/api/client";
  import type { components } from "../lib/api/schema";
  import { headline, type Change } from "../lib/change";
  import { agents, task as line, taskState, type Task } from "../lib/task";
  import { ago } from "../lib/time";
  import AgentFace from "../lib/ui/AgentFace.svelte";
  import Avatar from "../lib/ui/Avatar.svelte";
  import Risk from "../lib/ui/Risk.svelte";
  import TaskDrawer from "./TaskDrawer.svelte";

  type Backup = components["schemas"]["Backup"];

  let { open, onnavigate }: { open?: string; onnavigate: (href: string) => void } = $props();
  let tasks = $state<Task[] | null>(null);
  let changes = $state<Change[]>([]);
  let backups = $state<Backup[]>([]);
  let titles = $state<Map<string, string>>(new Map());
  let failed = $state("");

  async function load() {
    try {
      const [t, c, a, k] = await Promise.all([api.GET("/v0/tasks"), api.GET("/v0/changes"), api.GET("/v0/apps"), api.GET("/v0/catalog")]);
      const apps = must(a);
      const b = await Promise.all(apps.map((x) => api.GET("/v0/apps/{name}/backups", { params: { path: { name: x.name } } })));
      tasks = must(t);
      changes = must(c);
      backups = b.flatMap((r) => must(r));
      titles = new Map([...must(k).map((e) => [e.name, e.title] as const), ...apps.map((x) => [x.name, x.title || x.name] as const)]);
      failed = "";
    } catch (e) {
      failed = e instanceof Error ? e.message : String(e);
    }
  }

  $effect(() => {
    load();
    const t = setInterval(load, 10000);
    return () => clearInterval(t);
  });

  const title = (app: string) => titles.get(app) ?? app;
  const since = (days: number) => (iso: string | null | undefined) => !!iso && Date.now() - new Date(iso).getTime() < days * 86400_000;
  const month = since(30);
  const week = since(7);

  // Rules: scheduled and pre-change backups and automatic rollbacks;
  // agents: their tasks; people: the changes they asked for.
  let work = $derived.by(() => {
    const rules =
      backups.filter((b) => month(b.createdAt) && (b.reason === "scheduled" || b.reason === "pre-change")).length +
      changes.filter((c) => month(c.createdAt) && c.phase === "RolledBack").length;
    const byAgents = (tasks ?? []).filter((t) => month(t.createdAt)).length;
    const you = changes.filter((c) => month(c.createdAt) && c.requester.kind === "Human").length;
    return { rules, agents: byAgents, you, all: Math.max(1, rules + byAgents + you) };
  });
  let waiting = $derived(changes.filter((c) => c.phase === "WaitingForApproval"));
  let working = $derived((tasks ?? []).filter((t) => t.phase === "Pending" || t.phase === "Running"));
  let done = $derived((tasks ?? []).filter((t) => (t.phase === "Succeeded" || t.phase === "Failed") && week(t.finishedAt)));
  const count = (kind: Task["kind"]) => (tasks ?? []).filter((t) => t.kind === kind && month(t.createdAt)).length;

  const go = (e: MouseEvent, href: string) => {
    if (e.metaKey || e.ctrlKey) return;
    e.preventDefault();
    onnavigate(href);
  };
</script>

<section class="view-head">
  <h1>Agents</h1>
  <p>Rules do the routine, agents do the rest. You decide, they work.</p>
</section>

{#if failed}
  <p class="fail">{failed}</p>
{:else if tasks}
  <section>
    <h2 class="sec-title">Who did the work <small>last 30 days</small></h2>
    <div class="share" role="img" aria-label="Rules {work.rules}, agents {work.agents}, you {work.you}">
      {#each [["rules", "Rules", work.rules], ["agents", "Agents", work.agents], ["you", "You", work.you]] as const as [k, label, n] (k)}
        {#if n}<span class={k} style:flex-grow={n / work.all}>{label} {n}</span>{/if}
      {/each}
      {#if !work.rules && !work.agents && !work.you}<span class="none">Nothing yet</span>{/if}
    </div>

    <ul class="who">
      {#each Object.entries(agents) as [kind, a] (kind)}
        <li>
          <AgentFace name={a.name} size={44} />
          <div><strong>{a.name}</strong><small>{a.does}</small><span>{count(kind as Task["kind"])} {count(kind as Task["kind"]) === 1 ? "task" : "tasks"}</span></div>
        </li>
      {/each}
      <li>
        <svg class="rule" viewBox="0 0 40 40" aria-hidden="true"><path d="M20 4l16 16-16 16L4 20z" /><circle cx="20" cy="20" r="4" /></svg>
        <div><strong>Rules</strong><small>backup, update, rollback; no AI</small><span>{work.rules} {work.rules === 1 ? "run" : "runs"}</span></div>
      </li>
    </ul>
  </section>

  <section>
    <h2 class="sec-title">Work board <small>click a card for every step the agent took</small></h2>
    <div class="board">
      <div class="col">
        <h3><i class="wait"></i>Waiting for you <span>{waiting.length}</span></h3>
        {#each waiting as c (c.name)}
          <a class="card" href="/approvals" onclick={(e) => go(e, "/approvals")}>
            {#if c.requester.kind === "Agent"}<AgentFace name={c.requester.name} size={26} />{:else}<Avatar name={c.requester.name} size={24} />{/if}
            <span><strong>{headline(c, title(c.app))}</strong><small>{c.name}, {ago(c.createdAt)}</small></span>
            <Avatar name={title(c.app)} tile size={24} />
          </a>
        {:else}
          <p class="empty">Nothing waits for you.</p>
        {/each}
      </div>
      <div class="col">
        <h3><i class="ok"></i>Agents working <span>{working.length}</span></h3>
        {#each working as t (t.name)}
          <a class="card" href="/agents/{t.name}" onclick={(e) => go(e, `/agents/${t.name}`)}>
            <AgentFace name={agents[t.kind].name} size={26} />
            <span><strong>{line(t, title(t.app))}</strong><small>{taskState(t).label.toLowerCase()}</small></span>
            <Avatar name={title(t.app)} tile size={24} />
          </a>
        {:else}
          <p class="empty">No agent is at work.</p>
        {/each}
      </div>
      <div class="col">
        <h3><i class="ok"></i>Done this week <span>{done.length}</span></h3>
        {#each done as t (t.name)}
          <a class="card" href="/agents/{t.name}" onclick={(e) => go(e, `/agents/${t.name}`)}>
            <AgentFace name={agents[t.kind].name} size={26} />
            <span><strong>{line(t, title(t.app))}</strong><small class:bad={t.phase === "Failed"}>{t.phase === "Failed" ? "could not finish" : "done"}, {ago(t.finishedAt)}</small></span>
            <Avatar name={title(t.app)} tile size={24} />
          </a>
        {:else}
          <p class="empty">Nothing finished this week.</p>
        {/each}
      </div>
    </div>
  </section>

  <section>
    <h2 class="sec-title">What may happen without you</h2>
    <ul class="rules">
      <li><span class="l1"><Risk level="low" short /></span><div><strong>Read</strong><small>status, logs, events, manifests</small></div><span class="badge ok">Always allowed</span></li>
      <li><span class="l2"><Risk level="medium" short /></span><div><strong>Reversible</strong><small>settings and updates, saved first, rolled back on failure</small></div><span class="badge wait">Asks a person</span></li>
      <li><span class="l3"><Risk level="high" short /></span><div><strong>Risky</strong><small>restoring over the current state, removing an app, deleting data</small></div><span class="badge wait">Always asks a person</span></li>
    </ul>
    <p class="note">Agents only propose: every change they want waits in Approvals.</p>
  </section>

  {#if open}
    <TaskDrawer name={open} {title} onclose={() => onnavigate("/agents")} {onnavigate} />
  {/if}
{/if}

<style>
  section + section {
    margin-top: 48px;
  }
  .share {
    display: flex;
    overflow: hidden;
    height: 40px;
    border-radius: var(--radius-pill);
    background: var(--color-panel);
    font-size: 13.5px;
    font-weight: 700;
    color: var(--brand-paper-ink);
  }
  .share span {
    display: flex;
    align-items: center;
    min-width: max-content;
    padding: 0 18px;
  }
  .share .rules { background: var(--color-ok); }
  .share .agents { background: var(--avatar-lavender); }
  .share .you { background: var(--brand-paper-bg); }
  .share .none { color: var(--color-muted); }
  .who {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
    gap: 12px;
    margin: 14px 0 0;
    padding: 0;
    list-style: none;
  }
  .who li {
    display: flex;
    gap: 12px;
    padding: 16px;
    border-radius: var(--radius-panel);
    background: var(--color-panel);
  }
  .who strong,
  .who small,
  .who span {
    display: block;
  }
  .who strong {
    font: 600 1.1rem var(--font-display);
  }
  .who small {
    color: var(--color-muted);
    font-size: 13.5px;
  }
  .who span {
    margin-top: 10px;
    color: var(--color-muted);
    font-size: 13.5px;
  }
  .rule {
    flex: none;
    width: 40px;
    height: 40px;
  }
  .rule path { fill: var(--brand-paper-bg); }
  .rule circle { fill: var(--brand-paper-ink); }
  .board {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 12px;
  }
  .col {
    display: grid;
    align-content: start;
    gap: 8px;
    padding: 12px;
    border-radius: var(--radius-panel);
    background: var(--color-panel);
  }
  h3 {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 2px 4px 4px;
    font-size: 13.5px;
  }
  h3 span {
    margin-left: auto;
  }
  h3 i {
    width: 9px;
    height: 9px;
    border-radius: 50%;
  }
  h3 .wait { background: var(--color-wait); }
  h3 .ok { background: var(--color-ok); }
  .card {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 12px;
    border-radius: var(--radius-part);
    background: var(--color-sunk);
    color: inherit;
    text-decoration: none;
  }
  .card:hover {
    outline: 2px solid var(--color-line);
  }
  .card > span {
    flex: 1;
    min-width: 0;
  }
  .card strong,
  .card small {
    display: block;
  }
  .card strong {
    font-size: 14px;
    font-weight: 600;
  }
  .card small {
    color: var(--color-muted);
    font-size: 13px;
  }
  .card small.bad {
    color: var(--color-bad);
  }
  .empty {
    margin: 4px;
    color: var(--color-muted);
    font-size: 13.5px;
  }
  .rules {
    display: grid;
    gap: 18px;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .rules li {
    display: grid;
    grid-template-columns: 96px 1fr auto;
    align-items: center;
    gap: 16px;
  }
  .rules strong,
  .rules small {
    display: block;
  }
  .rules small {
    color: var(--color-muted);
    font-size: 13.5px;
  }
  .l1 { color: var(--color-ok); }
  .l2 { color: var(--color-wait); }
  .l3 { color: var(--color-bad); }
  .badge {
    padding: 4px 12px;
    border-radius: var(--radius-pill);
    font-size: 12.5px;
    font-weight: 700;
  }
  .badge.ok { background: color-mix(in srgb, var(--color-ok) 22%, transparent); color: var(--color-ok); }
  .badge.wait { background: var(--paint-attention); color: var(--slip-ink); }
  .note {
    margin: 16px 0 0;
    color: var(--color-muted);
    font-size: 13.5px;
  }
  @media (max-width: 900px) {
    .board {
      grid-template-columns: 1fr;
    }
  }
</style>
