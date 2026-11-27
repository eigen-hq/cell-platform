<script lang="ts">
  // The cell at a glance: one sentence on where it stands, the ask box,
  // what agents do and what waits for a yes, the machines with their apps,
  // four numbers and what was done lately. Everything comes from the API;
  // each part links to the view that holds the details.
  import { api, must } from "../lib/api/client";
  import type { components } from "../lib/api/schema";
  import type { Change } from "../lib/change";
  import { doneLately, hero, isDown, isUp, lastBackup } from "../lib/home";
  import { follow } from "../lib/live";
  import { newest, STALE } from "../lib/newest";
  import { itemPath } from "../lib/route";
  import { agents, task, taskState, type Task } from "../lib/task";
  import { machineName } from "../lib/text";
  import { ago } from "../lib/time";
  import Icon from "../lib/ui/Icon.svelte";
  import { agentIcon, type IconName } from "../lib/ui/icons";
  import Logo from "../lib/ui/Logo.svelte";
  import Tag from "../lib/ui/Tag.svelte";
  import Tile from "../lib/ui/Tile.svelte";
  import Composer from "./Composer.svelte";
  import Slip from "./Slip.svelte";

  type App = components["schemas"]["App"];
  type Backup = components["schemas"]["Backup"];
  type Node = components["schemas"]["Node"];

  // Viewers read the state of the cell only (E-63): no tasks, no asks.
  let { admin, cell }: { admin: boolean; cell: string; onnavigate?: (href: string) => void } = $props();
  let apps = $state<App[] | null>(null);
  let changes = $state<Change[]>([]);
  let tasks = $state<Task[]>([]);
  let nodes = $state<Node[]>([]);
  let backups = $state<Backup[]>([]);
  let titles = $state<Map<string, string>>(new Map());
  let target = $state<components["schemas"]["BackupTarget"] | null>(null);
  let failed = $state("");

  const guard = newest();
  async function load() {
    try {
      const answer = await guard(
        (async () => {
          const [a, c, t, n, k, bt] = await Promise.all([
            api.GET("/v0/apps"), api.GET("/v0/changes"), admin ? api.GET("/v0/tasks") : null, api.GET("/v0/nodes"),
            api.GET("/v0/catalog"), api.GET("/v0/backup-target"),
          ]);
          const list = must(a);
          const b = await Promise.all(list.map((x) => api.GET("/v0/apps/{name}/backups", { params: { path: { name: x.name } } })));
          return { list, c: must(c), t: t ? must(t) : [], n: must(n), k: must(k), bt: must(bt), b: b.flatMap((r) => must(r)) };
        })(),
      );
      if (answer === STALE) return;
      apps = answer.list.sort((x, y) => (x.title || x.name).localeCompare(y.title || y.name));
      changes = answer.c;
      tasks = answer.t;
      nodes = answer.n.sort((x, y) => x.name.localeCompare(y.name));
      backups = answer.b;
      target = answer.bt;
      titles = new Map([...answer.k.map((e) => [e.name, e.title] as const), ...answer.list.map((x) => [x.name, x.title || x.name] as const)]);
      failed = "";
    } catch (e) {
      failed = e instanceof Error ? e.message : String(e);
    }
  }

  // Live events reload the page's data at once (a burst counts once); the
  // slow poll covers what the stream does not carry, such as agent tasks.
  let live = $state<{ up: boolean; why?: string; tried?: boolean }>({ up: false });
  $effect(() => {
    load();
    const t = setInterval(load, 30000);
    let soon: ReturnType<typeof setTimeout> | undefined;
    const stop = follow(
      (signal) => api.GET("/v0/events", { parseAs: "stream", signal }),
      () => {
        clearTimeout(soon);
        soon = setTimeout(load, 300);
      },
      (up, why) => (live = { up, why, tried: true }),
    );
    return () => {
      clearInterval(t);
      clearTimeout(soon);
      stop();
    };
  });

  const title = (app: string) => titles.get(app) ?? app;
  const byName = $derived(new Map((apps ?? []).map((a) => [a.name, a])));
  const who = (c: Change) => (c.requester.kind === "Agent" ? `${c.requester.name.replace(/^./, (x) => x.toUpperCase())} agent` : c.requester.kind === "Operator" ? "Operator" : c.requester.name);
  let waiting = $derived(changes.filter((c) => c.phase === "WaitingForApproval").sort((x, y) => y.createdAt.localeCompare(x.createdAt)));
  let top = $derived(hero(apps ?? [], changes, title));
  let running = $derived(apps?.filter(isUp).length ?? 0);
  let down = $derived(apps?.filter(isDown).length ?? 0);
  let backup = $derived(lastBackup(backups));
  let working = $derived(tasks.filter((t) => t.phase === "Pending" || t.phase === "Running").sort((x, y) => y.createdAt.localeCompare(x.createdAt)));
  // Apps an agent works on right now: their tile gets the moving ring.
  let busyApps = $derived(new Set(working.flatMap((t) => (t.app ? [t.app] : []))));
  let noTarget = $derived(target?.configured === false);
  let month = $derived(changes.filter((c) => Date.now() - new Date(c.createdAt).getTime() < 30 * 86400_000));
  const pct = (used: number | undefined, all: number) => (used === undefined || !all ? undefined : Math.round((100 * used) / all));
  let fullest = $derived(
    nodes.map((n) => ({ n, p: pct(n.diskUsed, n.disk) })).filter((x) => x.p !== undefined).sort((x, y) => y.p! - x.p!)[0],
  );

  const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;
  const clock = (iso: string) => {
    const d = new Date(iso);
    if (d.toDateString() === new Date().toDateString()) return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hourCycle: "h23" });
    return d.toLocaleDateString([], { day: "numeric", month: "short" });
  };
  let lede = $derived.by(() => {
    if (!apps) return "";
    const parts = [
      `${running} of ${plural(apps.length, "app")} ${apps.length === 1 ? "answers" : "answer"}`,
      backup ? `${backup.ok ? "backed up" : "the last backup failed"} ${ago(backup.at)}` : "no backup yet",
    ];
    const agentsLine = admin && working.length ? `${working.length === 1 ? "One agent is" : `${working.length} agents are`} working` : "";
    const wait = waiting.length ? `${waiting.length === 1 ? "one decision waits" : `${waiting.length} decisions wait`} for ${admin ? "you" : "an admin"}` : "";
    const tail = [agentsLine, wait].filter(Boolean).join(", ");
    return `${parts.join(", ")}.${tail ? ` ${tail.replace(/^./, (x) => x.toUpperCase())}.` : ""}`;
  });

  // Suggestions for the ask box, from what the cell shows now.
  let suggestions = $derived.by((): { text: string; icon: IconName }[] => {
    const s: { text: string; icon: IconName }[] = [];
    const bad = apps?.find((a) => !isUp(a));
    if (bad) s.push({ text: `Why does ${title(bad.name)} not answer?`, icon: "pulse" });
    if (fullest && fullest.p! >= 70) s.push({ text: `What fills ${machineName(fullest.n.name)}?`, icon: "server" });
    s.push({ text: "What changed this week?", icon: "changes" });
    if (nodes.length) s.push({ text: "Which machine is fullest?", icon: "disk" });
    return s.slice(0, 3);
  });

  // Done lately: finished changes and backups, newest first.
  let done = $derived(doneLately(changes, backups, title, who));
</script>

{#if failed}<p class="fail" role="alert">{failed}</p>{/if}
{#if apps}
  <header class="page-head">
    <div>
      <h1>{top.lines[0]}</h1>
      <p>{lede}</p>
    </div>
    <p class="live" class:up={live.up} title={live.why}><span class="dot" class:pulse-ok={live.up} aria-hidden="true"></span>{live.up ? "Live" : live.tried ? "Reconnecting to the cell" : "Connecting"}</p>
  </header>

  {#if admin}
    <div class="composer"><Composer {suggestions} /></div>
  {/if}

  {#if noTarget}
    <a class="needs row" href="/backups">
      <Tile icon="backups" tone="warn" size={30} />
      <span><strong>{admin ? "Set a backup target" : "No backup target"}</strong><small>Backups have nowhere to go yet, so no app is backed up.</small></span>
      <Icon name="right" size={14} stroke={2} />
    </a>
  {/if}

  {#if admin || waiting.length}
    <section class="sec" aria-labelledby="h-agents">
      <div class="sec-head">
        <h2 id="h-agents">{admin ? "Agents" : "Waiting for a decision"}</h2>
        <span>{admin ? `${working.length} working, ${waiting.length} waiting for you` : `${waiting.length} for an admin`}</span>
        <span class="end">{#if admin}<a href="/agents">All tasks</a>{/if}<a href="/changes">All changes</a></span>
      </div>
      <ul class="list">
        {#each waiting.slice(0, 5) as c (c.name)}
          <!-- keyed by the change: a newer request must not inherit a half-made decision -->
          <li><Slip change={c} title={title(c.app)} app={byName.get(c.app)} {admin} ondecided={load} /></li>
        {/each}
        {#each working.slice(0, 5) as t (t.name)}
          {@const a = agents[t.kind]}
          {@const last = t.steps.at(-1)}
          <li>
            <a class="task row" href={itemPath("/agents", t.name)}>
              <span class="who">
                <Tile icon={agentIcon(a.name)} tone="agent" size={30} pulse />
                <span><strong>{a.name} agent</strong><small class="shimmer">{taskState(t).label}</small></span>
              </span>
              <span class="what">
                {#if t.app}<Logo app={byName.get(t.app) ?? { name: t.app }} title={title(t.app)} busy />{:else}<Tile icon="cell" size={36} />{/if}
                <span class="txt">
                  <strong>{task(t, title(t.app ?? ""))}</strong>
                  <small>Asked by {t.requester.name} {ago(t.createdAt)}</small>
                  <span class="bar flow" aria-hidden="true"></span>
                  {#if last}<span class="now"><span class="mono">{clock(last.at)}</span>{last.call}</span>{/if}
                </span>
              </span>
              <Icon name="right" size={14} stroke={2} />
            </a>
          </li>
        {/each}
        {#if admin && !waiting.length && !working.length}
          <li class="empty">No agent is at work and nothing waits for you. Hand off a task above, or ask Ops about one app from its page.</li>
        {/if}
      </ul>
    </section>
  {/if}

  <section class="sec" aria-labelledby="h-cell">
    <div class="sec-head">
      <h2 id="h-cell">Your cell</h2>
      <span>{plural(apps.length, "app")} on {plural(nodes.length, "machine")}.{admin && busyApps.size ? " A moving ring shows where an agent works right now." : ""}</span>
      <span class="end">{#if admin}<a href="/machines">Add a machine</a>{/if}</span>
    </div>
    <div class="board">
      <div class="map">
        {#each nodes as n (n.name)}
          {@const disk = pct(n.diskUsed, n.disk)}
          {@const look = !n.ready || (disk ?? 0) >= 85}
          <a class="plate" class:look href="/machines">
            <span class="ph">
              <Tile icon="server" size={30} />
              <span class="pn"><strong>{machineName(n.name)}</strong><small>{plural(n.apps.length, "app")}{n.system ? ", runs the cell" : ""}</small></span>
              {#if !n.ready}<Tag tone="warn">not ready</Tag>{:else if look}<Tag tone="warn">needs a look</Tag>{:else}<span class="ok pulse-ok" aria-label="ready"></span>{/if}
            </span>
            <span class="meter">
              <span class="ml"><span><Icon name="disk" size={12} stroke={2} />Disk</span><span class="mono" class:hot={look}>{disk === undefined ? (n.unmeasured ? "not measured" : "n/a") : `${disk}%`}</span></span>
              <span class="track"><span class:hot={look} style:width="{disk ?? 0}%"></span></span>
            </span>
            {#if n.apps.length}
              <span class="tiles">
                {#each n.apps as name (name)}
                  {@const a = byName.get(name)}
                  <span class="t">
                    <Logo app={a ?? { name }} title={title(name)} size={42} busy={admin && busyApps.has(name)} />
                    {#if a && !isUp(a)}<span class="badge" aria-label="not answering"></span>{/if}
                    <small>{title(name)}</small>
                  </span>
                {/each}
              </span>
            {:else}
              <span class="none">No app runs here yet.</span>
            {/if}
            <span class="pf mono">{n.address}</span>
          </a>
        {:else}
          <p class="empty">No machine reports yet.</p>
        {/each}
      </div>
      <div class="stats">
        <a class="stat row" href="/apps">
          <span class="sl"><Icon name="apps" size={14} stroke={2} />Apps</span>
          <span class="big"><span>{running}</span>answer</span>
          <span class="sub" class:hot={down}>{down ? `${plural(down, "app")} down` : running < apps.length ? `${apps.length - running} not answering yet` : apps.length ? "All of them answer" : "None installed yet"}</span>
        </a>
        <a class="stat row" href="/backups">
          <span class="sl"><Icon name="backups" size={14} stroke={2} />Backups</span>
          <span class="big"><span>{backup ? clock(backup.at) : "none"}</span>{backup ? "last copy" : ""}</span>
          <span class="sub" class:hot={noTarget || (backup && !backup.ok)} class:good={backup?.ok && !noTarget}>{noTarget ? "No target set" : backup && !backup.ok ? "The last one failed" : target?.where ? `Kept in ${target.where}` : "Target set"}</span>
        </a>
        <a class="stat row" href="/machines">
          <span class="sl"><Icon name="server" size={14} stroke={2} />Machines</span>
          <span class="big"><span>{nodes.length}</span>{nodes.filter((n) => n.ready).length === nodes.length ? "ready" : `${nodes.filter((n) => n.ready).length} ready`}</span>
          <span class="sub" class:hot={(fullest?.p ?? 0) >= 85}>{fullest ? `${machineName(fullest.n.name)} disk at ${fullest.p}%` : "No disk measured"}</span>
        </a>
        <a class="stat row" href="/changes">
          <span class="sl"><Icon name="changes" size={14} stroke={2} />Changes</span>
          <span class="big"><span>{month.length}</span>in 30 days</span>
          <span class="sub">{waiting.length ? `${waiting.length} waiting for a decision` : "Each one in the change log"}</span>
        </a>
      </div>
    </div>
  </section>

  <section class="sec" aria-labelledby="h-done">
    <div class="sec-head">
      <h2 id="h-done">Done lately</h2>
      <span>{done.length}</span>
      <span class="end"><a href="/changes">Change log</a></span>
    </div>
    {#if done.length}
      <ul class="done">
        {#each done as d (d.key)}
          <li>
            <a class="row" href={d.href}>
              <time class="mono" datetime={d.at}>{clock(d.at)}</time>
              {#if d.app}<Logo app={byName.get(d.app) ?? { name: d.app }} title={title(d.app)} size={24} />{:else}<Tile icon="cell" size={24} />{/if}
              <span class="dt">{d.text}</span>
              {#if d.tag}<Tag tone={d.tag.tone}>{d.tag.label}</Tag>{/if}
              <span class="by {d.by}"><span aria-hidden="true"></span>{d.who}</span>
            </a>
          </li>
        {/each}
      </ul>
    {:else}
      <p class="empty plain">Nothing yet. Changes and backups show up here once they ran.</p>
    {/if}
  </section>
{:else if !failed}
  <p class="empty plain">Reading {cell}</p>
{/if}

<style>
  .live {
    flex: none;
    display: inline-flex;
    align-items: center;
    gap: 8px;
    margin: 0 0 6px;
    font-size: 13px;
    color: var(--color-muted);
  }
  .live .dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--color-warn);
  }
  .live.up .dot {
    background: var(--color-ok);
  }
  .composer {
    margin-top: 32px;
  }
  .needs {
    display: flex;
    align-items: center;
    gap: 14px;
    margin-top: 32px;
    padding: 16px 20px;
    border-radius: var(--radius-panel);
    background: var(--color-warn-bg);
    border: 1px solid var(--color-warn-edge);
    color: var(--color-warn-text);
  }
  .needs:hover {
    background: var(--color-warn-bg);
  }
  .needs > span {
    flex: 1;
  }
  .needs strong,
  .needs small {
    display: block;
  }
  .needs strong {
    font-weight: 500;
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
  .task strong {
    display: block;
    font-weight: 500;
  }
  .task .who small {
    display: block;
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
    display: block;
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
  .now {
    display: flex;
    gap: 10px;
    margin-top: 8px;
    font-size: 12.5px;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .now .mono {
    color: var(--color-agent-ink);
  }
  .board {
    border-radius: var(--radius-hero);
    border: 1px solid var(--color-line);
    overflow: hidden;
    background: var(--color-surface);
  }
  .map {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
    gap: 22px;
    padding: 28px 24px;
    background-color: var(--color-surface-2);
    background-image: radial-gradient(var(--color-line) 1px, transparent 1.3px);
    background-size: 16px 16px;
  }
  .plate {
    display: flex;
    flex-direction: column;
    gap: 16px;
    min-height: 260px;
    padding: 16px 18px;
    border-radius: var(--radius-panel);
    background: var(--color-surface);
    box-shadow: 0 0 0 1px var(--color-line), var(--shadow-raised);
  }
  @supports (corner-shape: squircle) {
    .plate {
      border-radius: 36px;
      corner-shape: squircle;
    }
  }
  .plate.look {
    box-shadow: 0 0 0 1px var(--color-warn-edge), var(--shadow-raised);
  }
  .ph {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .pn {
    flex: 1;
    min-width: 0;
  }
  .pn strong,
  .pn small {
    display: block;
  }
  .pn strong {
    font-weight: 600;
  }
  .pn small {
    font-size: 12px;
    color: var(--color-muted);
  }
  .ok {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--color-ok);
  }
  .ml {
    display: flex;
    justify-content: space-between;
    font-size: 12px;
    color: var(--color-text);
  }
  .ml > span {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
  .ml .mono {
    color: var(--color-muted);
  }
  .ml .hot {
    color: var(--color-warn-ink);
  }
  .meter .track {
    display: block;
    margin-top: 6px;
    height: 4px;
    border-radius: 2px;
    background: var(--color-hairline);
    overflow: hidden;
  }
  .meter .track span {
    display: block;
    height: 100%;
    background: var(--color-ok-dot);
  }
  .meter .track span.hot {
    background: var(--color-warn);
  }
  .tiles {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 10px 8px;
  }
  .t {
    position: relative;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 5px;
    min-width: 0;
  }
  .t small {
    max-width: 100%;
    font-size: 11.5px;
    color: var(--color-text);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .badge {
    position: absolute;
    top: -3px;
    left: calc(50% + 14px);
    width: 9px;
    height: 9px;
    border-radius: 50%;
    background: var(--color-warn);
    box-shadow: 0 0 0 2px var(--color-surface);
  }
  .none {
    color: var(--color-muted);
    font-size: 13px;
  }
  .pf {
    margin-top: auto;
    font-size: 11.5px;
    color: var(--color-muted);
  }
  .stats {
    display: flex;
    flex-wrap: wrap;
    border-top: 1px solid var(--color-hairline);
  }
  .stat {
    flex: 1 1 220px;
    padding: 18px 24px 20px;
    box-shadow: -1px 0 0 var(--color-hairline);
  }
  .sl {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
    font-weight: 500;
    color: var(--color-text);
  }
  .big {
    display: flex;
    align-items: baseline;
    gap: 10px;
    margin-top: 4px;
    color: var(--color-text);
  }
  .big span {
    font-size: 48px;
    line-height: 1.05;
    letter-spacing: -0.045em;
    font-variant-numeric: tabular-nums;
    color: var(--color-ink);
  }
  .sub {
    display: block;
    margin-top: 4px;
    font-size: 12.5px;
    color: var(--color-text);
  }
  .sub.hot {
    color: var(--color-warn-ink);
  }
  .sub.good {
    color: var(--color-ok);
  }
  .done {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(min(480px, 100%), 1fr));
    column-gap: 40px;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .done li {
    border-bottom: 1px solid var(--color-line);
  }
  .done a {
    display: flex;
    align-items: center;
    gap: 12px;
    height: 46px;
    margin: 0 -10px;
    padding: 0 10px;
    border-radius: var(--radius-control);
    font-size: 13px;
  }
  .done time {
    flex: none;
    width: 48px;
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
  @media (max-width: 860px) {
    .task {
      grid-template-columns: minmax(0, 1fr);
      gap: 14px;
    }
    .task > :global(svg) {
      display: none;
    }
  }
</style>
