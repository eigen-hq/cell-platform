<script lang="ts">
  // The agents of the cell: who they are and what they work on, the ask box,
  // and the work board (running tasks first, then finished ones). One task
  // opens as its own page under /agents/<task>: what it was asked, every API
  // call it made, its answer and the changes it proposed.
  import { api, must } from "../lib/api/client";
  import type { components } from "../lib/api/schema";
  import { ended, headline, open as isOpen, unsettled, type Change } from "../lib/change";
  import { isUp } from "../lib/home";
  import { newest, STALE } from "../lib/newest";
  import { itemPath } from "../lib/route";
  import { agents, crew, task as line, taskState, type Task } from "../lib/task";
  import { ago, stamp } from "../lib/time";
  import Avatar from "../lib/ui/Avatar.svelte";
  import Icon from "../lib/ui/Icon.svelte";
  import { agentIcon, type IconName } from "../lib/ui/icons";
  import Logo from "../lib/ui/Logo.svelte";
  import Tag from "../lib/ui/Tag.svelte";
  import Tile from "../lib/ui/Tile.svelte";
  import Composer from "./Composer.svelte";
  import Slip from "./Slip.svelte";

  type App = components["schemas"]["App"];

  let { open }: { open?: string; onnavigate: (href: string) => void } = $props();
  let tasks = $state<Task[] | null>(null);
  let apps = $state<App[]>([]);
  let changes = $state<Change[]>([]);
  let titles = $state<Map<string, string>>(new Map());
  let failed = $state("");
  // The task page: the one task, loaded on its own.
  let t = $state<Task | null>(null);
  let taskFailed = $state("");

  const msg = (e: unknown) => (e instanceof Error ? e.message : String(e));

  // What both pages need besides the tasks: apps (logos, titles) and the
  // changes (what a task proposed, and whether it still waits).
  async function context() {
    const [c, a, k] = await Promise.all([api.GET("/v0/changes"), api.GET("/v0/apps"), api.GET("/v0/catalog")]);
    const list = must(a);
    return {
      c: must(c),
      list,
      titles: new Map([...must(k).map((e) => [e.name, e.title] as const), ...list.map((x) => [x.name, x.title || x.name] as const)]),
    };
  }
  function keep(x: Awaited<ReturnType<typeof context>>) {
    changes = x.c;
    apps = x.list;
    titles = x.titles;
  }

  const listGuard = newest();
  async function loadList() {
    try {
      const answer = await listGuard(Promise.all([api.GET("/v0/tasks"), context()]));
      if (answer === STALE) return;
      tasks = must(answer[0]);
      keep(answer[1]);
      failed = "";
    } catch (e) {
      failed = msg(e);
    }
  }

  const taskGuard = newest();
  async function loadTask(name: string) {
    try {
      const answer = await taskGuard(Promise.all([api.GET("/v0/tasks/{name}", { params: { path: { name } } }), context()]));
      if (answer === STALE) return;
      t = must(answer[0]);
      keep(answer[1]);
      taskFailed = "";
    } catch (e) {
      taskFailed = msg(e);
    }
  }
  const reloadTask = () => open && loadTask(open);

  // The list polls; a task page polls while the task runs or one of its
  // changes is still open, so a decision made elsewhere shows up.
  $effect(() => {
    const name = open;
    if (!name) {
      loadList();
      const i = setInterval(loadList, 10000);
      return () => clearInterval(i);
    }
    t = null;
    taskFailed = "";
    loadTask(name);
    const i = setInterval(() => {
      if (!t || running(t) || unsettled(proposed)) loadTask(name);
    }, 5000);
    return () => clearInterval(i);
  });

  const title = (app: string) => titles.get(app) ?? app;
  const byName = $derived(new Map(apps.map((a) => [a.name, a])));
  const running = (x: Task) => x.phase === "Pending" || x.phase === "Running";
  const newestFirst = (x: Task, y: Task) => y.createdAt.localeCompare(x.createdAt);
  let working = $derived((tasks ?? []).filter(running).sort(newestFirst));
  let finished = $derived(
    (tasks ?? []).filter((x) => !running(x)).sort((x, y) => (y.finishedAt ?? y.createdAt).localeCompare(x.finishedAt ?? x.createdAt)),
  );
  const count = (kinds: Task["kind"][], live: boolean) => (tasks ?? []).filter((x) => kinds.includes(x.kind) && running(x) === live).length;
  const asker = (x: Task) => (x.requester.kind === "Operator" ? "the platform" : x.requester.kind === "Human" ? x.requester.name : `the ${x.requester.name} agent`);
  const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

  let lede = $derived(
    tasks
      ? `${working.length ? `${working.length === 1 ? "One task runs" : `${working.length} tasks run`} now` : "No agent is at work"}, ${plural(finished.length, "task")} finished. Agents only propose: every change they want waits for your approval.`
      : "",
  );

  // Suggestions for the ask box, from what the cell shows now.
  let suggestions = $derived.by((): { text: string; icon: IconName }[] => {
    const s: { text: string; icon: IconName }[] = [];
    const bad = apps.find((a) => !isUp(a));
    if (bad) s.push({ text: `Why does ${title(bad.name)} not answer?`, icon: "pulse" });
    s.push({ text: "What changed this week?", icon: "changes" });
    s.push({ text: "Which machine is fullest?", icon: "disk" });
    return s.slice(0, 3);
  });

  // The task page: the changes the task proposed, as the API has them now.
  let names = $derived([...new Set(t?.changes ?? [])]);
  let proposed = $derived(names.map((n) => changes.find((c) => c.name === n) ?? null));
  const took = (x: Task) => {
    if (!x.startedAt || !x.finishedAt) return "";
    const s = Math.max(0, Math.round((new Date(x.finishedAt).getTime() - new Date(x.startedAt).getTime()) / 1000));
    return s < 60 ? `${s} s` : `${Math.floor(s / 60)} min ${s % 60} s`;
  };
  // The answer: its first paragraph large, the rest under it.
  let answer = $derived.by(() => {
    const parts = (t?.result ?? "").trim().split(/\n\s*\n/);
    return { first: parts[0] ?? "", rest: parts.slice(1).join("\n\n") };
  });
</script>

{#if open}
  <nav class="crumbs" aria-label="Breadcrumb"><a href="/agents">Agents</a><span aria-hidden="true">/</span><span>Task</span></nav>
  {#if taskFailed}<p class="fail" role="alert">{taskFailed}</p>{/if}
  {#if t}
    {@const a = agents[t.kind]}
    {@const s = taskState(t)}
    {@const last = t.steps.at(-1)}
    <div class="page">
      <div class="main">
        <p class="asked">
          {#if t.requester.kind === "Human"}<Avatar name={t.requester.name} size={24} />{:else}<Tile icon={t.requester.kind === "Agent" ? agentIcon(t.requester.name) : "cell"} tone={t.requester.kind === "Agent" ? "agent" : "operator"} size={24} />{/if}
          <span>Asked by {asker(t)} {ago(t.createdAt)}{t.app ? " about" : ""}</span>
          {#if t.app}<a class="scope" href={itemPath("/apps", t.app)}><Logo app={byName.get(t.app) ?? { name: t.app }} title={title(t.app)} size={16} />{title(t.app)}</a>{/if}
        </p>
        <h1>{line(t, title(t.app))}</h1>

        <section class="card" aria-labelledby="t-answer">
          <div class="who">
            <Tile icon={agentIcon(a.name)} tone="agent" size={28} pulse={running(t)} />
            <h2 id="t-answer">{a.name} agent</h2>
            <span>{t.phase === "Succeeded" ? `answered${took(t) ? ` in ${took(t)}` : ""}` : t.phase === "Failed" ? "could not finish" : s.label.toLowerCase()}</span>
            {#if t.phase === "Failed"}<Tag tone="warn">Could not finish</Tag>{/if}
          </div>

          {#if answer.first}
            <p class="lead">{answer.first}</p>
            {#if answer.rest}<p class="more">{answer.rest}</p>{/if}
          {:else if !running(t)}
            <p class="more">The agent left no answer.</p>
          {/if}

          {#if running(t)}
            <div class="live">
              <div class="now">
                <span class="spinner" aria-hidden="true"></span>
                <span class="txt"><strong>Still working</strong> <span class="shimmer">{s.label}</span></span>
                {#if last}<span class="mono">{stamp(last.at)}</span>{/if}
              </div>
              {#if last}<p class="call mono">{last.call}</p>{/if}
              <div class="track"><div class="flow"></div></div>
            </div>
          {/if}

          {#if names.length}
            <h3>Changes it proposed <span>{names.length}</span></h3>
            <ul class="changes">
              {#each names as n, i (n)}
                {@const c = proposed[i]}
                {#if c && c.phase === "WaitingForApproval"}
                  <!-- keyed by the change: a newer request must not inherit a half-made decision -->
                  <li><Slip change={c} title={title(c.app)} app={byName.get(c.app)} admin={true} ondecided={reloadTask} /></li>
                {:else}
                  {@const e = c ? ended(c) : null}
                  <li>
                    <a class="change row" href={itemPath("/changes", n)}>
                      {#if c}<Logo app={byName.get(c.app) ?? { name: c.app }} title={title(c.app)} size={24} />{:else}<Tile icon="changes" size={24} />{/if}
                      <span class="ct">{c ? headline(c, title(c.app)) : n}</span>
                      {#if e}<Tag tone={e.tone === "ok" ? "ok" : e.tone === "bad" ? "warn" : "neutral"}>{e.label}</Tag>{/if}
                      <Icon name="right" size={14} stroke={2} />
                    </a>
                  </li>
                {/if}
              {/each}
            </ul>
          {/if}

          <div class="given">
            <h3>What it was given</h3>
            <!-- a request is prose; a Compose file keeps its lines -->
            <pre class="mono" class:wrap={t.kind !== "package"}>{t.input || "nothing"}</pre>
          </div>

          <div class="steps">
            <h3>Every step <span>{plural(t.steps.length, "API call")}, recorded before they were served</span></h3>
            {#if t.steps.length}
              <ol>
                {#each t.steps as st, i (i)}
                  <li><time class="mono" datetime={st.at}>{stamp(st.at)}</time><code class="mono">{st.call}</code></li>
                {/each}
              </ol>
            {:else}
              <p class="more">No calls yet.</p>
            {/if}
          </div>
        </section>
      </div>

      <aside class="facts" aria-label="About this task">
        <dl>
          <div><dt>Agent</dt><dd><Tile icon={agentIcon(a.name)} tone="agent" size={22} />{a.name}<small>{a.does}</small></dd></div>
          <div><dt>State</dt><dd><Tag tone={t.phase === "Succeeded" ? "ok" : t.phase === "Failed" ? "warn" : "agent"}>{s.label}</Tag></dd></div>
          {#if t.app}<div><dt>App</dt><dd><a class="go" href={itemPath("/apps", t.app)}>{title(t.app)}</a></dd></div>{/if}
          <div><dt>Asked</dt><dd class="mono">{new Date(t.createdAt).toLocaleString()}</dd></div>
          {#if t.startedAt}<div><dt>Started</dt><dd class="mono">{new Date(t.startedAt).toLocaleString()}</dd></div>{/if}
          {#if t.finishedAt}<div><dt>Finished</dt><dd class="mono">{new Date(t.finishedAt).toLocaleString()}</dd></div>{/if}
          <div><dt>Account</dt><dd class="mono">{t.assignee || "no agent yet"}</dd></div>
          <div><dt>Task</dt><dd class="mono">{t.name}</dd></div>
        </dl>
      </aside>
    </div>
  {:else if !taskFailed}
    <p class="empty plain">Reading the task</p>
  {/if}
{:else}
  {#if failed}<p class="fail" role="alert">{failed}</p>{/if}
  {#if tasks}
    <header class="page-head">
      <div>
        <h1>Agents</h1>
        <p>{lede}</p>
      </div>
    </header>

    <div class="composer"><Composer {suggestions} /></div>

    <section class="sec" aria-labelledby="h-crew">
      <div class="sec-head"><h2 id="h-crew">The crew</h2><span>{plural(crew.length, "agent")}</span></div>
      <ul class="crew">
        {#each crew as c (c.name)}
          {@const now = count(c.kinds, true)}
          <li>
            <Tile icon={agentIcon(c.name)} tone="agent" size={36} pulse={now > 0} />
            <span class="cn"><strong>{c.name}</strong><small>{c.does}</small></span>
            <span class="nums">
              <span><span class="big">{now}</span>running</span>
              <span><span class="big">{count(c.kinds, false)}</span>done</span>
            </span>
          </li>
        {/each}
      </ul>
    </section>

    <section class="sec" aria-labelledby="h-board">
      <div class="sec-head">
        <h2 id="h-board">Work board</h2>
        <span>{working.length} running, {finished.length} finished</span>
        <span class="end"><a href="/changes">All changes</a></span>
      </div>
      <ul class="list">
        {#each working as x (x.name)}
          {@const a = agents[x.kind]}
          {@const last = x.steps.at(-1)}
          <li>
            <a class="task row" href={itemPath("/agents", x.name)}>
              <span class="who">
                <Tile icon={agentIcon(a.name)} tone="agent" size={30} pulse />
                <span><strong>{a.name} agent</strong><small class="shimmer">{taskState(x).label}</small></span>
              </span>
              <span class="what">
                {#if x.app}<Logo app={byName.get(x.app) ?? { name: x.app }} title={title(x.app)} busy />{:else}<Tile icon="cell" size={36} />{/if}
                <span class="txt">
                  <strong>{line(x, title(x.app))}</strong>
                  <small>Asked by {asker(x)} {ago(x.createdAt)}</small>
                  <span class="bar flow" aria-hidden="true"></span>
                  {#if last}<span class="last"><span class="mono">{stamp(last.at)}</span><span class="mono">{last.call}</span></span>{/if}
                </span>
              </span>
              <Icon name="right" size={14} stroke={2} />
            </a>
          </li>
        {/each}
        {#each finished as x (x.name)}
          {@const a = agents[x.kind]}
          <li>
            <a class="done row" href={itemPath("/agents", x.name)}>
              <Tile icon={agentIcon(a.name)} tone="agent" size={26} />
              {#if x.app}<Logo app={byName.get(x.app) ?? { name: x.app }} title={title(x.app)} size={26} />{:else}<Tile icon="cell" size={26} />{/if}
              <span class="dt"><strong>{line(x, title(x.app))}</strong><small>{a.name} agent, asked by {asker(x)}</small></span>
              {#if x.phase === "Failed"}<Tag tone="warn">Could not finish</Tag>{:else}<Tag tone="ok">Done</Tag>{/if}
              <time class="mono" datetime={x.finishedAt ?? x.createdAt}>{ago(x.finishedAt ?? x.createdAt)}</time>
              <Icon name="right" size={14} stroke={2} />
            </a>
          </li>
        {/each}
        {#if !working.length && !finished.length}
          <li class="empty">No task yet. Hand one off above, or ask Ops about one app from its page.</li>
        {/if}
      </ul>
    </section>
  {:else if !failed}
    <p class="empty plain">Reading the agents</p>
  {/if}
{/if}

<style>
  .composer {
    margin-top: 32px;
  }
  .empty.plain {
    padding: 0;
  }
  .crew {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(min(340px, 100%), 1fr));
    gap: 12px;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .crew li {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 18px 20px;
    border-radius: var(--radius-panel);
    background: var(--color-surface-2);
    border: 1px solid var(--color-line);
  }
  .cn {
    flex: 1;
    min-width: 0;
  }
  .cn strong,
  .cn small,
  .task strong,
  .task small,
  .dt strong,
  .dt small {
    display: block;
  }
  .cn strong,
  .task strong,
  .dt strong {
    font-weight: 500;
  }
  .cn small,
  .dt small {
    font-size: 12.5px;
    color: var(--color-text);
  }
  .nums {
    flex: none;
    display: flex;
    gap: 20px;
    font-size: 12.5px;
    color: var(--color-text);
  }
  .nums > span {
    display: flex;
    flex-direction: column;
    align-items: flex-end;
  }
  .big {
    font-size: 32px;
    line-height: 1.05;
    letter-spacing: -0.04em;
    font-variant-numeric: tabular-nums;
    color: var(--color-ink);
  }
  .task {
    display: grid;
    grid-template-columns: 200px minmax(0, 1fr) auto;
    align-items: start;
    gap: 24px;
    padding: 18px 22px 20px;
    color: var(--color-muted);
  }
  .task .who,
  .task .what {
    display: flex;
    align-items: center;
    gap: 11px;
    min-width: 0;
    color: var(--color-ink);
  }
  .task .what {
    align-items: flex-start;
    gap: 14px;
  }
  .task .who small {
    font-size: 12.5px;
  }
  .txt {
    flex: 1;
    min-width: 0;
  }
  .txt strong {
    font-size: 15px;
  }
  .txt small {
    margin-top: 2px;
    color: var(--color-text);
    font-size: 14px;
  }
  .bar {
    display: block;
    height: 4px;
    margin-top: 14px;
    border-radius: 2px;
  }
  .last {
    display: flex;
    gap: 10px;
    margin-top: 8px;
    font-size: 12.5px;
    color: var(--color-muted);
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .last .mono:first-child {
    flex: none;
    color: var(--color-agent-ink);
  }
  .done {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 12px 22px;
    color: var(--color-muted);
  }
  .dt {
    flex: 1;
    min-width: 0;
    color: var(--color-ink);
  }
  .dt strong {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .done time {
    flex: none;
    font-size: 12px;
  }

  /* The task page. */
  .crumbs {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
    color: var(--color-muted);
  }
  .crumbs a:hover {
    color: var(--color-ink);
  }
  .crumbs span:last-child {
    color: var(--color-ink);
  }
  .page {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 300px;
    gap: 40px;
    align-items: start;
    margin-top: 28px;
  }
  .asked {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    margin: 0;
    font-size: 13px;
    color: var(--color-text);
  }
  .scope {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 24px;
    padding: 0 8px;
    border-radius: var(--radius-chip);
    background: var(--color-tile);
    color: var(--color-ink);
    font-size: 12.5px;
  }
  .main h1 {
    margin: 14px 0 0;
    font-size: 46px;
    line-height: 1.05;
    font-weight: 500;
    letter-spacing: -0.035em;
    text-wrap: balance;
    overflow-wrap: anywhere;
  }
  .card {
    margin-top: 32px;
    padding: 24px 28px 26px;
    border-radius: var(--radius-hero);
    background: var(--color-surface);
    border: 1px solid var(--color-line);
    box-shadow: var(--shadow-raised);
  }
  .card .who {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 9px;
  }
  .card h2 {
    margin: 0;
    font-size: 13.5px;
    font-weight: 500;
  }
  .card .who > span {
    font-size: 12.5px;
    color: var(--color-muted);
  }
  .lead {
    margin: 16px 0 0;
    font-size: 22px;
    line-height: 1.4;
    letter-spacing: -0.015em;
    text-wrap: pretty;
    white-space: pre-line;
    overflow-wrap: anywhere;
  }
  .more {
    margin: 12px 0 0;
    font-size: 15px;
    color: var(--color-text);
    white-space: pre-line;
    overflow-wrap: anywhere;
  }
  .live {
    margin-top: 18px;
    padding: 14px 16px 0;
    border-radius: var(--radius-box);
    background: var(--color-agent-wash);
    overflow: hidden;
  }
  .live .now {
    display: flex;
    align-items: center;
    gap: 12px;
    font-size: 13.5px;
  }
  .live .txt strong {
    display: inline;
    font-size: 13.5px;
    font-weight: 500;
    color: var(--color-agent-ink);
  }
  .live .now > .mono {
    flex: none;
    font-size: 12px;
    color: var(--color-text);
  }
  .call {
    margin: 6px 0 0 28px;
    font-size: 12px;
    color: var(--color-text);
    overflow-wrap: anywhere;
  }
  .track {
    height: 2px;
    margin: 12px -16px 0;
    background: var(--color-agent-edge);
  }
  .track .flow {
    height: 100%;
  }
  .card h3 {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 4px 10px;
    margin: 28px 0 8px;
    font-size: 13px;
    font-weight: 500;
  }
  .card h3 span {
    font-weight: 400;
    color: var(--color-muted);
  }
  .changes {
    margin: 0;
    padding: 0;
    list-style: none;
    border-radius: var(--radius-box);
    border: 1px solid var(--color-line);
    overflow: hidden;
  }
  .changes > li + li {
    border-top: 1px solid var(--color-hairline);
  }
  .change {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 12px 16px;
    color: var(--color-muted);
  }
  .ct {
    flex: 1;
    min-width: 0;
    color: var(--color-ink);
  }
  pre {
    overflow: auto;
    max-height: 320px;
    margin: 0;
    padding: 14px 16px;
    border-radius: var(--radius-box);
    background: var(--color-surface-2);
    border: 1px solid var(--color-hairline);
    font-size: 12.5px;
    line-height: 1.6;
  }
  .wrap {
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .steps ol {
    margin: 0;
    padding: 0;
    list-style: none;
    border-left: 1px solid var(--color-agent-edge);
  }
  .steps li {
    display: flex;
    gap: 14px;
    padding: 5px 0 5px 16px;
    font-size: 12.5px;
  }
  .steps time {
    flex: none;
    color: var(--color-muted);
  }
  .steps code {
    min-width: 0;
    color: var(--color-ink);
    overflow-wrap: anywhere;
  }
  .facts {
    border-radius: var(--radius-panel);
    background: var(--color-surface-2);
    border: 1px solid var(--color-line);
    padding: 8px 20px;
  }
  dl {
    margin: 0;
  }
  dl > div {
    padding: 12px 0;
  }
  dl > div + div {
    border-top: 1px solid var(--color-hairline);
  }
  dt {
    font-size: 12.5px;
    color: var(--color-muted);
  }
  dd {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px 8px;
    margin: 4px 0 0;
    font-size: 13px;
    overflow-wrap: anywhere;
  }
  dd small {
    flex-basis: 100%;
    color: var(--color-text);
    font-size: 12.5px;
  }
  dd.mono {
    font-size: 12px;
  }
  @media (max-width: 860px) {
    .task,
    .page {
      grid-template-columns: minmax(0, 1fr);
      gap: 14px;
    }
    .task > :global(svg) {
      display: none;
    }
    .page {
      gap: 24px;
    }
    .main h1 {
      font-size: 34px;
    }
    .card {
      padding: 20px;
    }
    .done time {
      display: none;
    }
  }
</style>
