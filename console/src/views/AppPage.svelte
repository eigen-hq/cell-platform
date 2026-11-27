<script lang="ts">
  // One app on its own page: its state, address, version and who can open
  // it; what waits for a decision and what changed; its backups; for admins
  // the ask box, the actions and what runs inside (pods, logs, events).
  // Every action that changes the app is a change request.
  import { api, must } from "../lib/api/client";
  import type { components } from "../lib/api/schema";
  import { ended, headline, open, withChange, type Change } from "../lib/change";
  import { appState, isDown, isUp } from "../lib/home";
  import { newest, STALE } from "../lib/newest";
  import { audience, authMode } from "../lib/phase";
  import { itemPath } from "../lib/route";
  import { textOf } from "../lib/text";
  import { ago, stamp } from "../lib/time";
  import Button from "../lib/ui/Button.svelte";
  import Drawer from "../lib/ui/Drawer.svelte";
  import Icon from "../lib/ui/Icon.svelte";
  import Logo from "../lib/ui/Logo.svelte";
  import Tag from "../lib/ui/Tag.svelte";
  import Spans from "../lib/ui/Spans.svelte";
  import Tile from "../lib/ui/Tile.svelte";
  import UsageChart from "../lib/ui/UsageChart.svelte";
  import { series, spanMs, type Span, type Usage } from "../lib/usage";
  import Composer from "./Composer.svelte";
  import Slip from "./Slip.svelte";

  type App = components["schemas"]["App"];
  type Backup = components["schemas"]["Backup"];
  type AppEvent = components["schemas"]["AppEvent"];
  type Group = components["schemas"]["Group"];
  type Pod = components["schemas"]["Pod"];
  type Container = Pod["containers"][number];

  let {
    app,
    admin,
    machines,
    offered,
    onchange,
  }: { app: App; admin: boolean; machines: string[]; offered: string; onchange: () => void } = $props();
  let backups = $state<Backup[]>([]);
  let changes = $state<Change[]>([]);
  let events = $state<AppEvent[]>([]);
  let pods = $state<Pod[] | null>(null);
  let failed = $state("");
  let note = $state<{ text: string; bad?: boolean; task?: string } | null>(null);
  let confirming = $state("");

  // A poll that started before an action may answer after it: drop it.
  const guard = newest();
  async function load() {
    const name = app.name;
    const path = { params: { path: { name } } };
    try {
      const answer = await guard(
        Promise.all([
          api.GET("/v0/apps/{name}/backups", path),
          api.GET("/v0/changes", { params: { query: { app: name } } }),
          // Pods and events show what runs inside: for admins only (E-63).
          admin ? api.GET("/v0/apps/{name}/pods", path) : null,
          admin ? api.GET("/v0/apps/{name}/events", path) : null,
        ]),
      );
      if (answer === STALE) return;
      const [b, c, p, e] = answer;
      backups = must(b).sort((x, y) => y.createdAt.localeCompare(x.createdAt));
      changes = must(c).sort((x, y) => y.createdAt.localeCompare(x.createdAt));
      pods = p ? must(p) : null;
      events = e ? must(e).sort((x, y) => y.last.localeCompare(x.last)) : [];
      failed = "";
    } catch (e) {
      failed = e instanceof Error ? e.message : String(e);
    }
  }

  $effect(() => {
    load();
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  });

  // What all of the app's containers used over the span, for admins,
  // read again every minute.
  let span = $state<Span>("24h");
  let history = $state<{ end: number; usage?: Usage; failed?: string } | null>(null);
  $effect(() => {
    if (!admin) return;
    const name = app.name;
    const range = span;
    let gone = false;
    async function read() {
      const end = Date.now();
      let h: { end: number; usage?: Usage; failed?: string };
      try {
        h = { end, usage: must(await api.GET("/v0/apps/{name}/usage", { params: { path: { name }, query: { range } } })) };
      } catch (e) {
        h = { end, failed: why(e) };
      }
      if (!gone) history = h;
    }
    read();
    const t = setInterval(read, 60000);
    return () => {
      gone = true;
      clearInterval(t);
    };
  });
  // An app has no fixed size: its axis reaches a little past its peak.
  const roof = (pts: { v: number }[]) => 1.15 * pts.reduce((m, p) => Math.max(m, p.v), 0) || 1;

  const why = (e: unknown) => (e instanceof Error ? e.message : String(e));
  // Runs an action, then says what became of it.
  async function act(what: () => Promise<{ text: string; task?: string }>) {
    confirming = "";
    note = null;
    try {
      note = await what();
      onchange();
      await load();
    } catch (e) {
      note = { text: why(e), bad: true };
    }
  }
  // What the API says about the change: it waits for a yes, or it runs.
  // It counts at once: a failed reload must not bring back the button that asked for it.
  function outcome(c: Change) {
    changes = withChange(changes, c);
    return { text: c.approval.required ? "Sent for approval. It runs once someone approves it." : "Running now." };
  }
  const path = $derived({ params: { path: { name: app.name } } });

  // A backup is not a change: it starts at once.
  const backupNow = () => act(async () => (must(await api.POST("/v0/apps/{name}/backups", path)), { text: "Backup started." }));
  const restore = (b: Backup) =>
    act(async () => outcome(must(await api.POST("/v0/apps/{name}/restore", { ...path, body: { backup: b.name, reason: "restored from the console" } }))));
  const remove = (deleteData: boolean) =>
    act(async () => outcome(must(await api.DELETE("/v0/apps/{name}", { params: { path: { name: app.name }, query: { deleteData, reason: "removed from the console" } } }))));
  // A diagnosis is an agent task, not a change: it only reads.
  const diagnose = () =>
    act(async () => {
      const t = must(await api.POST("/v0/tasks", { body: { kind: "diagnose", app: app.name } }));
      return { text: "Ops is looking into it.", task: t.name };
    });

  // Who can open it: the groups ticked, proposed as one change.
  let groups = $state<Group[]>([]);
  let editing = $state<string[] | null>(null);
  async function editAccess() {
    note = null;
    try {
      groups = must(await api.GET("/v0/groups"));
      editing = [...new Set(app.groups)];
    } catch (e) {
      note = { text: why(e), bad: true };
    }
  }
  const toggle = (g: string) => (editing = editing!.includes(g) ? editing!.filter((x) => x !== g) : [...editing!, g]);
  const saveAccess = () =>
    act(async () => {
      const c = must(await api.PUT("/v0/apps/{name}/access", { ...path, body: { groups: editing ?? [], reason: "access changed in the console" } }));
      editing = null;
      return outcome(c);
    });

  // A new version: typed, or the one the catalog offers. It is a change
  // like any other: backup first, rolled back if the health check fails.
  let nextVersion = $state<string | null>(null);
  const wanted = $derived(nextVersion?.trim() ?? "");
  const updating = $derived(changes.some((c) => open(c) && c.action === "update"));
  const restarting = $derived(changes.some((c) => open(c) && c.action === "restart"));
  // One request at a time: a second click must not ask for a second update.
  let asking = $state(false);
  async function askUpdate() {
    if (asking) return;
    asking = true;
    await act(async () => {
      const c = must(await api.PATCH("/v0/apps/{name}", { ...path, body: { patch: { version: wanted }, reason: `version ${wanted} from the console` } }));
      nextVersion = null;
      return outcome(c);
    });
    asking = false;
  }
  // A restart starts new pods; settings and data stay. Shown again only
  // once no restart of this app is on its way.
  async function restart() {
    if (asking) return;
    asking = true;
    await act(async () => outcome(must(await api.POST("/v0/apps/{name}/restart", { ...path, body: { reason: "restarted from the console" } }))));
    asking = false;
  }

  // The end of a container's log, one click away.
  let log = $state<{ pod: string; container: string; previous: boolean; text: string } | null>(null);
  let logFailed = $state("");
  let reading = $state(false);
  async function readLog(pod: string, container: string, previous: boolean) {
    reading = true;
    logFailed = "";
    try {
      const r = await api.GET("/v0/apps/{name}/logs", { params: { path: { name: app.name }, query: { pod, container, previous, tail: 300 } }, parseAs: "text" });
      log = { pod, container, previous, text: textOf(r) };
    } catch (e) {
      logFailed = why(e);
    } finally {
      reading = false;
    }
  }

  // A container that is not ready and has crashed before counts as failing.
  const dot = (c: Container) => (c.ready ? "ok" : c.state === "terminated" || c.restarts > 0 ? "bad" : "wait");
  const word = (c: Container) => (c.ready ? "ready" : c.reason ? c.reason : c.state === "terminated" ? "stopped" : c.state === "waiting" ? "waiting" : "starting");
  // Use next to the most the container may take: memory past the limit
  // ends it (OOMKilled), processor past it is only slowed.
  const mb = (b: number) => (b >= 1 << 30 ? `${(b / (1 << 30)).toFixed(1)} GB` : `${Math.round(b / (1 << 20))} MB`);
  const cores = (v: number) => (v < 0.1 ? v.toFixed(3) : v.toFixed(2)).replace(/\.?0+$/, "");
  function usage(c: Container) {
    const part = (used: string | undefined, limit: string | undefined, unit: string) =>
      used ? `${used}${limit ? ` of ${limit}` : ""} ${unit}` : limit ? `at most ${limit} ${unit}` : "";
    const mem = part(c.memoryUsed === undefined ? undefined : mb(c.memoryUsed), c.memoryLimit === undefined ? undefined : mb(c.memoryLimit), "memory");
    const cpu = part(c.cpuUsed === undefined ? undefined : cores(c.cpuUsed), c.cpuLimit === undefined ? undefined : cores(c.cpuLimit), "cores");
    return [mem, cpu].filter(Boolean).join(", ");
  }
  const tight = (c: Container) => c.memoryUsed !== undefined && c.memoryLimit !== undefined && c.memoryUsed >= 0.9 * c.memoryLimit;

  const title = $derived(app.title || app.name);
  const s = $derived(appState(app));
  const host = $derived(app.url?.replace(/^https?:\/\//, "").replace(/\/$/, "") ?? "");
  const trouble = $derived(app.conditions.filter((c) => c.status === "False" && c.message));
  const ready = $derived(app.conditions.find((c) => c.type === "Ready"));
  const sentence = $derived.by(() => {
    if (isDown(app)) return `${title} is down.`;
    if (app.phase === "Ready" && !isUp(app)) return `${title} does not answer.`;
    if (app.phase === "NeedsAttention") return `${title} needs a look.`;
    if (app.phase === "Ready") return `${title} is running.`;
    return `${title} is ${s.label.toLowerCase()}.`;
  });
  const runs = $derived(app.runningVersion || app.version);
  const waiting = $derived(changes.filter((c) => c.phase === "WaitingForApproval"));
  const recent = $derived(changes.filter((c) => c.phase !== "WaitingForApproval").slice(0, 8));
  const lastOk = $derived(backups.find((b) => b.phase === "Completed"));
  const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;
  const tagTone = (t: "ok" | "wait" | "bad" | "none") => (t === "ok" ? "ok" : t === "bad" ? "warn" : "neutral");
  const backupTone = (b: Backup) => (b.phase === "Completed" ? "ok" : b.phase === "Failed" ? "warn" : "neutral");
  const backupWord = (b: Backup) => (b.phase === "Completed" ? "Done" : b.phase === "Failed" ? "Failed" : b.phase === "Running" ? "Running" : "Queued");
</script>

{#snippet confirm(key: string, label: string, run: () => void, danger = false)}
  {#if confirming === key}
    <span class="sure">
      <Button kind={danger ? "warn" : "primary"} size="sm" onclick={run}>Yes, {label.toLowerCase()}</Button>
      <Button kind="plain" size="sm" onclick={() => (confirming = "")}>Cancel</Button>
    </span>
  {:else}
    <Button kind={danger ? "warn" : "outline"} size="sm" onclick={() => (confirming = key)}>{label}</Button>
  {/if}
{/snippet}

<nav class="crumb" aria-label="Breadcrumb"><a href="/apps">Apps</a><span aria-hidden="true">/</span><span>{title}</span></nav>

<header class="page-head head">
  <div>
    <p class="id">
      <Logo {app} {title} size={56} />
      <span class="name">{title}</span>
      {#if host}<span class="chip mono"><Icon name="globe" size={12} stroke={2} />{host}</span>{/if}
      {#each machines as m (m)}<a class="chip mono" href="/machines"><Icon name="server" size={12} stroke={2} />{m}</a>{/each}
    </p>
    <h1>{sentence}</h1>
    <p>
      Version <span class="mono">{runs}</span>{app.runningVersion && app.runningVersion !== app.version ? `, asked for ${app.version}` : ""}.
      {app.lastBackup ? `Backed up ${ago(app.lastBackup.at)}.` : "No backup yet."}
      Open to {audience(app).join(", ")}.
    </p>
  </div>
  {#if app.url}
    <a class="open" href={app.url} target="_blank" rel="noopener noreferrer">Open {title}<Icon name="external" size={14} stroke={2} /></a>
  {/if}
</header>

{#if failed}<p class="fail" role="alert">{failed}</p>{/if}

{#if admin}
  <div class="composer"><Composer app={app.name} {title} big={false} /></div>
{/if}

{#if note}
  <p class={note.bad ? "fail" : "note"} role={note.bad ? "alert" : "status"}>
    {note.text}
    {#if note.task}<a class="go" href={itemPath("/agents", note.task)}>Follow Ops<Icon name="right" size={13} stroke={2} /></a>{/if}
  </p>
{/if}

{#if trouble.length}
  <section class="trouble" aria-labelledby="h-wrong">
    <Tile icon="pulse" tone="warn" size={30} />
    <div>
      <h2 id="h-wrong">What is wrong</h2>
      {#each trouble as c (c.type)}<p><strong>{c.reason || c.type}</strong> {c.message}</p>{/each}
    </div>
    {#if admin}<Button kind="outline" onclick={diagnose}>Let Ops diagnose it</Button>{/if}
  </section>
{/if}

<section class="facts" aria-label="{title} at a glance">
  <div class="fact">
    <h2><Tile icon="pulse" tone={s.tone === "ok" ? "ok" : s.tone === "bad" ? "warn" : "neutral"} size={26} />Health</h2>
    <span class="big">{s.label}</span>
    <span class="sub">{isUp(app) ? "The last health check passed." : ready?.message || "The health check does not pass yet."}</span>
  </div>
  <div class="fact">
    <h2><Tile icon="shield" size={26} />Version</h2>
    <span class="big mono">{runs}</span>
    <span class="sub">
      {#if updating}An update waits or runs.{:else if admin && offered && offered !== app.version}The catalog has {offered}.{:else}An update backs up first and rolls back by itself if the health check fails.{/if}
    </span>
    {#if admin && nextVersion === null}
      <span class="more"><button class="go" onclick={() => (nextVersion = offered && offered !== app.version ? offered : app.version)}>Change version<Icon name="arrow" size={13} stroke={2.2} /></button></span>
    {/if}
  </div>
  <div class="fact">
    <h2><Tile icon="backups" size={26} />Last backup</h2>
    <span class="big">{lastOk ? ago(lastOk.completedAt ?? lastOk.createdAt) : "none"}</span>
    <span class="sub">{backups.length ? `${plural(backups.length, "backup")} kept.` : "Nothing backed up yet."}</span>
    {#if admin}<span class="more"><button class="go" onclick={backupNow}>Back up now<Icon name="arrow" size={13} stroke={2.2} /></button><a href="/backups">All backups</a></span>{/if}
  </div>
</section>

{#if waiting.length}
  <section class="sec" aria-labelledby="h-waiting">
    <div class="sec-head">
      <h2 id="h-waiting">Waiting for a decision</h2>
      <span>{admin ? `${waiting.length} for you` : `${waiting.length} for an admin`}</span>
    </div>
    <ul class="list">
      {#each waiting as c (c.name)}
        <li><Slip change={c} {title} {app} {admin} ondecided={load} /></li>
      {/each}
    </ul>
  </section>
{/if}

<div class="cols">
  <section class="sec" aria-labelledby="h-changes">
    <div class="sec-head">
      <h2 id="h-changes">Changes</h2>
      <span>{recent.length ? "newest first" : ""}</span>
      <span class="end"><a href="/changes">Change log</a></span>
    </div>
    {#if recent.length}
      <ul class="list">
        {#each recent as c (c.name)}
          {@const e = ended(c)}
          <li>
            <a class="line row" href={itemPath("/changes", c.name)}>
              <span class="txt">{headline(c, title)}<small>{c.requester.name}, {ago(c.createdAt)}</small></span>
              <Tag tone={tagTone(e.tone)}>{e.label}</Tag>
            </a>
          </li>
        {/each}
      </ul>
    {:else}
      <p class="empty plain">No change to {title} yet.</p>
    {/if}
  </section>

  <section class="sec" aria-labelledby="h-backups">
    <div class="sec-head">
      <h2 id="h-backups">Backups</h2>
      <span>{backups.length ? `${backups.length}, newest first` : "none yet"}</span>
    </div>
    {#if backups.length}
      <ul class="list">
        {#each backups.slice(0, 6) as b (b.name)}
          <li class="line">
            <span class="txt">{ago(b.completedAt ?? b.createdAt)}<small>{b.reason || b.message || b.name}</small></span>
            <Tag tone={backupTone(b)}>{backupWord(b)}</Tag>
            {#if admin && b.phase === "Completed"}{@render confirm(`restore:${b.name}`, "Bring back", () => restore(b))}{/if}
          </li>
        {/each}
      </ul>
    {:else}
      <p class="empty plain">{title} has no backup yet.</p>
    {/if}
  </section>
</div>

<section class="sec" aria-labelledby="h-access">
  <div class="sec-head">
    <h2 id="h-access">Who can open it</h2>
    <span>{authMode(app) === "none" ? "no sign-in of the cell" : app.groups.length ? plural(new Set(app.groups).size, "group") : "everyone with an account"}</span>
    {#if admin && authMode(app) !== "none"}<span class="end"><button class="go" onclick={editAccess}>Change who can open it</button></span>{/if}
  </div>
  <p class="chips">
    {#each audience(app) as g (g)}<Tag>{g}</Tag>{/each}
  </p>
  {#if authMode(app) === "none"}<p class="hint">This app has no sign-in of the cell, so it cannot be limited to groups.</p>{/if}
</section>

{#if admin}
  <section class="sec" aria-labelledby="h-runs">
    <div class="sec-head">
      <h2 id="h-runs">What runs</h2>
      <span>pods and their containers</span>
      <span class="end"><Spans bind:value={span} /></span>
    </div>
    {#if history?.usage}
      {@const cpu = series(history.usage, "cpu")}
      {@const memory = series(history.usage, "memory")}
      <div class="history">
        <UsageChart label="Processor, all containers" points={cpu} step={history.usage.step} end={history.end} span={spanMs(span)} max={roof(cpu)} format={(v) => `${cores(v)} cores`} />
        <UsageChart label="Memory, all containers" points={memory} step={history.usage.step} end={history.end} span={spanMs(span)} max={roof(memory)} format={mb} />
      </div>
    {:else if history?.failed}
      <p class="quiet-note">No history: {history.failed}</p>
    {/if}
    {#if pods}
      {#if pods.length}
        <ul class="list">
          {#each pods as p (p.name)}
            <li class="pod">
              <p class="ph"><strong class="mono" title={p.name}>{p.name}</strong><small>{p.phase.toLowerCase()}, started {ago(p.createdAt)}</small></p>
              <ul class="containers">
                {#each p.containers as c (c.name)}
                  <li>
                    <span class="cdot {dot(c)}" aria-hidden="true"></span>
                    <span class="what">
                      <strong>{c.name}</strong> <small>{word(c)}{c.restarts ? `, ${plural(c.restarts, "restart")}` : ""}</small>
                      {#if c.lastExit}<small class="block">last exit {ago(c.lastExit.at)}: {c.lastExit.reason}, code {c.lastExit.exitCode}</small>{/if}
                      {#if c.message}<small class="block">{c.message}</small>{/if}
                      {#if usage(c)}<small class="block" class:tight={tight(c)}>{usage(c)}{tight(c) ? ", close to its memory limit" : ""}</small>{/if}
                    </span>
                    <span class="read">
                      <Button kind="outline" size="sm" disabled={reading} onclick={() => readLog(p.name, c.name, false)}><Icon name="logs" size={13} stroke={2} />Log</Button>
                      {#if c.restarts}<Button kind="plain" size="sm" disabled={reading} onclick={() => readLog(p.name, c.name, true)}>Before the restart</Button>{/if}
                    </span>
                  </li>
                {/each}
              </ul>
            </li>
          {/each}
        </ul>
      {:else}
        <p class="empty plain">Nothing runs right now.</p>
      {/if}
    {/if}
    {#if logFailed}<p class="fail" role="alert">{logFailed}</p>{/if}
    {#if log}
      <div class="logbox">
        <p>
          <span>{log.container} in {log.pod}{log.previous ? ", before the restart" : ""}, the last 300 lines</span>
          <button class="go" disabled={reading} onclick={() => log && readLog(log.pod, log.container, log.previous)}>Reload</button>
          <button class="go" onclick={() => (log = null)}>Close the log</button>
        </p>
        <pre class="mono">{log.text || "The log is empty."}</pre>
      </div>
    {/if}
  </section>

  <section class="sec" aria-labelledby="h-events">
    <div class="sec-head">
      <h2 id="h-events">Events</h2>
      <span>the newest first</span>
    </div>
    {#if events.length}
      <ol class="log mono">
        {#each events.slice(0, 12) as e, i (i)}
          <li class:warn={e.type === "Warning"}><time datetime={e.last}>{stamp(e.last)}</time><span>{e.reason}: {e.message}</span></li>
        {/each}
      </ol>
    {:else}
      <p class="empty plain">Nothing happened lately.</p>
    {/if}
  </section>

  <!-- Every action is a write: viewers see the state only (E-63). -->
  <section class="sec" aria-labelledby="h-actions">
    <div class="sec-head">
      <h2 id="h-actions">Actions</h2>
      <span>A restart or a removal is a change: it runs once someone approves it. A diagnosis only reads.</span>
    </div>
    <div class="actions">
      {#if !trouble.length}<Button kind="outline" size="sm" onclick={diagnose}>Let Ops diagnose it</Button>{/if}
      {#if !restarting && !asking}{@render confirm("restart", "Restart", restart)}{/if}
      {@render confirm("remove", "Remove, keep the data", () => remove(false))}
      {@render confirm("purge", "Remove and delete the data", () => remove(true), true)}
    </div>
  </section>
{:else}
  <p class="hint sec">Pods, events and logs are for admins: they can hold anything the app prints.</p>
{/if}

{#if admin && nextVersion !== null}
  <Drawer title="Update {title}" subtitle="Runs {runs}. The update backs up first and rolls back by itself if the health check fails." onclose={() => (nextVersion = null)}>
    <form class="form" onsubmit={(e) => (e.preventDefault(), askUpdate())}>
      <label class="field">New version <input class="mono" bind:value={nextVersion} autocomplete="off" spellcheck="false" /></label>
      {#if offered && offered !== app.version && wanted !== offered}<button type="button" class="go" onclick={() => (nextVersion = offered)}>Take {offered} from the catalog</button>{/if}
      {#if updating}<p class="hint">An update of this app is already on its way.</p>{/if}
      {#if note?.bad}<p class="fail" role="alert">{note.text}</p>{/if}
      <p class="hint">The update is a change: it runs once someone approves it.</p>
      <div class="actions">
        <Button type="submit" disabled={!wanted || wanted === app.version || updating || asking}>Ask for the update</Button>
        <Button kind="plain" onclick={() => (nextVersion = null)}>Cancel</Button>
      </div>
    </form>
  </Drawer>
{/if}

{#if admin && editing}
  <Drawer title="Who can open {title}" subtitle="The new groups are a change: they apply once someone approves it." onclose={() => (editing = null)}>
    <div class="form">
      <fieldset class="picks">
        <legend class="vh">Groups</legend>
        {#each groups as g (g.name)}
          <label class="pick row" class:on={editing.includes(g.name)}>
            <input type="checkbox" checked={editing.includes(g.name)} onchange={() => toggle(g.name)} />
            <span>{g.name}</span><small>{plural(g.members.length, "member")}</small>
          </label>
        {:else}
          <p class="hint">There is no group yet.</p>
        {/each}
      </fieldset>
      <p class="hint">{editing.length ? "Only members of the ticked groups." : "No group ticked: everyone with an account of the cell."}</p>
      {#if note?.bad}<p class="fail" role="alert">{note.text}</p>{/if}
      <div class="actions">
        <Button onclick={saveAccess}>Propose</Button>
        <Button kind="plain" onclick={() => (editing = null)}>Cancel</Button>
      </div>
    </div>
  </Drawer>
{/if}

<style>
  .crumb {
    display: flex;
    gap: 8px;
    font-size: 13px;
    color: var(--color-muted);
  }
  .crumb a:hover,
  .crumb span:last-child {
    color: var(--color-ink);
  }
  .head {
    margin-top: 24px;
  }
  .page-head p.id {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 10px;
    margin: 0 0 18px;
    font-size: 13px;
  }
  .name {
    font-weight: 500;
    color: var(--color-ink);
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    height: 22px;
    padding: 0 8px;
    border-radius: var(--radius-chip);
    background: var(--color-tile);
    color: var(--color-text);
    font-size: 11.5px;
  }
  a.chip:hover {
    color: var(--color-ink);
  }
  .open {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    height: 40px;
    padding: 0 16px 0 18px;
    border-radius: var(--radius-tile);
    background: var(--color-ink);
    color: var(--color-on-ink);
    font-weight: 500;
  }
  .composer {
    margin-top: 32px;
  }
  .note {
    margin: 16px 0 0;
    padding: 10px 14px;
    border-radius: var(--radius-control);
    background: var(--color-surface-2);
    border: 1px solid var(--color-line);
  }
  .note .go,
  .fail .go {
    margin-left: 8px;
  }
  .trouble {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-start;
    gap: 14px;
    margin-top: 32px;
    padding: 16px 20px;
    border-radius: var(--radius-panel);
    background: var(--color-warn-bg);
    border: 1px solid var(--color-warn-edge);
    color: var(--color-warn-text);
  }
  .trouble > div {
    flex: 1 1 300px;
  }
  .trouble h2 {
    margin: 0 0 4px;
    font-size: 15px;
    font-weight: 500;
    color: var(--color-ink);
  }
  .trouble p {
    margin: 0;
  }
  .facts {
    display: flex;
    flex-wrap: wrap;
    margin-top: 40px;
    border-radius: var(--radius-hero);
    background: var(--color-surface);
    border: 1px solid var(--color-line);
    overflow: hidden;
  }
  .fact {
    flex: 1 1 260px;
    display: flex;
    flex-direction: column;
    padding: 22px 24px 18px;
    box-shadow: -1px 0 0 var(--color-hairline);
  }
  .fact h2 {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    font-size: 13.5px;
    font-weight: 500;
    color: var(--color-text);
  }
  .big {
    margin-top: 8px;
    font-size: 48px;
    line-height: 1.05;
    letter-spacing: -0.045em;
    font-variant-numeric: tabular-nums;
    overflow-wrap: anywhere;
  }
  .big.mono {
    font-size: 40px;
    letter-spacing: -0.03em;
  }
  .sub {
    flex: 1;
    margin-top: 6px;
    font-size: 13px;
    color: var(--color-text);
  }
  .more {
    display: flex;
    align-items: center;
    gap: 16px;
    margin-top: 16px;
    font-size: 13px;
    color: var(--color-text);
  }
  .more a:hover {
    color: var(--color-ink);
  }
  .cols {
    display: grid;
    grid-template-columns: minmax(0, 1.2fr) minmax(0, 1fr);
    gap: 0 40px;
    align-items: start;
  }
  .line {
    display: flex;
    align-items: center;
    gap: 12px;
    min-height: 54px;
    padding: 8px 18px;
  }
  .txt {
    flex: 1;
    min-width: 0;
  }
  .txt small {
    display: block;
    font-size: 12px;
    color: var(--color-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .empty.plain {
    padding: 0;
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin: 0;
  }
  .hint {
    margin: 8px 0 0;
    font-size: 13px;
    color: var(--color-text);
  }
  .pod {
    padding: 14px 18px;
  }
  .ph {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 4px 10px;
    margin: 0 0 6px;
  }
  .ph strong {
    font-size: 12.5px;
    font-weight: 500;
  }
  small {
    font-size: 12.5px;
    color: var(--color-muted);
  }
  .containers {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .containers li {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    align-items: start;
    gap: 10px;
    padding: 6px 0;
  }
  .what strong {
    font-weight: 500;
  }
  .block {
    display: block;
  }
  .tight {
    color: var(--color-warn-ink);
  }
  .cdot {
    width: 8px;
    height: 8px;
    margin-top: 6px;
    border-radius: 50%;
    background: var(--color-muted);
  }
  .cdot.ok {
    background: var(--color-ok);
  }
  .cdot.bad {
    background: var(--color-warn);
    box-shadow: 0 0 0 3px var(--color-warn-bg);
  }
  .read {
    display: flex;
    flex-wrap: wrap;
    justify-content: end;
    gap: 6px;
  }
  .logbox {
    margin-top: 12px;
  }
  .logbox p {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 16px;
    margin: 0 0 6px;
    font-size: 13px;
    color: var(--color-text);
  }
  .logbox p span {
    flex: 1;
  }
  pre {
    max-height: 340px;
    margin: 0;
    padding: 14px 16px;
    overflow: auto;
    border-radius: var(--radius-tile);
    background: var(--color-surface-2);
    border: 1px solid var(--color-hairline);
    font-size: 12px;
    line-height: 1.6;
    white-space: pre-wrap;
    word-break: break-word;
  }
  .log {
    margin: 0;
    padding: 14px 16px;
    border-radius: var(--radius-panel);
    background: var(--color-surface-2);
    border: 1px solid var(--color-line);
    list-style: none;
    font-size: 12.5px;
    line-height: 1.7;
  }
  .log li {
    display: flex;
    gap: 14px;
  }
  .log time {
    flex: none;
    color: var(--color-muted);
  }
  .log span {
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .log .warn span {
    color: var(--color-warn-ink);
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }
  .sure {
    display: inline-flex;
    gap: 6px;
  }
  .form {
    display: grid;
    gap: 14px;
    justify-items: start;
  }
  .form .field {
    justify-self: stretch;
  }
  .picks {
    display: grid;
    justify-self: stretch;
    margin: 0;
    padding: 0;
    border: 0;
  }
  .pick {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 10px 12px;
    border-radius: var(--radius-control);
    cursor: pointer;
  }
  .pick span {
    flex: 1;
  }
  .pick.on {
    background: var(--color-agent-wash);
  }
  .pick input {
    margin: 0;
    accent-color: var(--color-agent);
  }
  @media (max-width: 860px) {
    .cols {
      grid-template-columns: minmax(0, 1fr);
    }
    .containers li {
      grid-template-columns: auto minmax(0, 1fr);
    }
    .read {
      grid-column: 2;
      justify-content: start;
    }
  }
  .history {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
    gap: 14px 28px;
    margin-bottom: 18px;
  }
  .quiet-note {
    margin: 0 0 14px;
    font-size: 12.5px;
    color: var(--color-muted);
  }
</style>
