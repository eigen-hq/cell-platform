<script lang="ts">
  // The cell at a glance (Figma: 01 Home): a headline that says what needs
  // the admin, one shape per app, what waits for a yes, the apps, what
  // happened lately and who works in the cell. Everything comes from the
  // API; each part links to the view that holds the details.
  import { api, must } from "../lib/api/client";
  import type { components } from "../lib/api/schema";
  import { ended, headline, type Change } from "../lib/change";
  import { appState, hero, isDown, isUp, lastBackup, reach } from "../lib/home";
  import { follow } from "../lib/live";
  import { newest, STALE } from "../lib/newest";
    import { itemPath } from "../lib/route";
  import { crew, type Task } from "../lib/task";
  import { ago } from "../lib/time";
  import AgentFace from "../lib/ui/AgentFace.svelte";
  import Avatar from "../lib/ui/Avatar.svelte";
  import StatStrip, { type Stat } from "../lib/ui/StatStrip.svelte";
  import Status from "../lib/ui/Status.svelte";
  import Ask from "./Ask.svelte";
  import Slip from "./Slip.svelte";

  type App = components["schemas"]["App"];
  type User = components["schemas"]["User"];
  type Group = components["schemas"]["Group"];
  type Backup = components["schemas"]["Backup"];

  const AGENTS = "cell-agents";
  // Viewers read the state of the cell only (E-63): no people, no tasks.
  let { admin }: { admin: boolean } = $props();
  let apps = $state<App[] | null>(null);
  let changes = $state<Change[]>([]);
  let tasks = $state<Task[]>([]);
  let users = $state<User[]>([]);
  let groups = $state<Group[]>([]);
  let backups = $state<Backup[]>([]);
  let titles = $state<Map<string, string>>(new Map());
  let target = $state<components["schemas"]["BackupTarget"] | null>(null);
  let failed = $state("");

  const guard = newest();
  async function load() {
    try {
      const answer = await guard(
        (async () => {
          const [a, c, t, u, g, k, bt] = await Promise.all([
            api.GET("/v0/apps"), api.GET("/v0/changes"), admin ? api.GET("/v0/tasks") : null, admin ? api.GET("/v0/users") : null,
            admin ? api.GET("/v0/groups") : null, api.GET("/v0/catalog"),
            api.GET("/v0/backup-target"),
          ]);
          const list = must(a);
          const b = await Promise.all(list.map((x) => api.GET("/v0/apps/{name}/backups", { params: { path: { name: x.name } } })));
          return {
            list, c: must(c), t: t ? must(t) : [], u: u ? must(u) : [], g: g ? must(g) : [], k: must(k), bt: must(bt), b: b.flatMap((r) => must(r)),
          };
        })(),
      );
      if (answer === STALE) return;
      apps = answer.list.sort((x, y) => (x.title || x.name).localeCompare(y.title || y.name));
      changes = answer.c;
      tasks = answer.t;
      users = answer.u;
      groups = answer.g;
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
  // Agents go by their name with a capital, people by their username.
  const who = (c: Change) => (c.requester.kind === "Agent" ? c.requester.name.replace(/^./, (x) => x.toUpperCase()) : c.requester.name);
  let people = $derived(users.filter((u) => !u.groups.includes(AGENTS)));
  let waiting = $derived(changes.filter((c) => c.phase === "WaitingForApproval").sort((x, y) => y.createdAt.localeCompare(x.createdAt)));
  let top = $derived(hero(apps ?? [], changes, title));
  let running = $derived(apps?.filter(isUp).length ?? 0);
  let backup = $derived(lastBackup(backups));
  let reaches = $derived(new Map((apps ?? []).map((a) => [a.name, reach(a, people, groups)])));
  let most = $derived(Math.max(1, ...reaches.values()));
  let busyTasks = $derived(tasks.filter((t) => t.phase === "Pending" || t.phase === "Running"));
  let recent = $derived([...changes].sort((x, y) => y.createdAt.localeCompare(x.createdAt)).slice(0, 8));
  let teams = $derived(groups.filter((g) => g.name !== AGENTS));
  let noTarget = $derived(target?.configured === false);

  const clock = (iso: string) => {
    const d = new Date(iso);
    const days = (Date.now() - d.getTime()) / 86400_000;
    const time = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hourCycle: "h23" });
    if (d.toDateString() === new Date().toDateString()) return time;
    if (days < 6) return `${d.toLocaleDateString([], { weekday: "short" })} ${time}`;
    return d.toLocaleDateString([], { day: "numeric", month: "short" });
  };
  const backupText = (b: { at: string; ok: boolean } | null) =>
    !b ? "none yet" : `${new Date(b.at).toDateString() === new Date().toDateString() ? "today at " : ""}${clock(b.at)}`;

  let stats = $derived.by((): Stat[] => {
    if (!apps) return [];
    return [
      { label: "State", value: { you: "Needs you", busy: "Platform at work", good: "All good" }[top.mood], tone: top.mood === "good" ? "ok" : "wait" },
      { label: "Apps up", value: `${running} / ${apps.length}`, tone: apps.some(isDown) ? "bad" : running < apps.length ? "wait" : "ok" },
      { label: "Waiting for you", value: String(waiting.length), tone: waiting.length ? "wait" : "ok" },
      { label: "Last backup", value: backup ? `${clock(backup.at)} ${backup.ok ? "ok" : "failed"}` : "none yet", tone: !backup ? "none" : backup.ok ? "ok" : "bad" },
      ...(admin ? [{ label: "Agents", value: busyTasks.length ? `${busyTasks.length} at work` : "idle", tone: busyTasks.length ? "wait" : "none" } as Stat] : []),
    ];
  });
  const shades = ["sun", "rose", "lilac"] as const;
</script>

{#if failed}<p class="fail" role="alert">{failed}</p>{/if}
{#if apps}
  <StatStrip items={stats} />

  <section class="hero">
    <div class="words">
      <h1>{top.lines[0]}<br /><span>{top.lines[1]}</span></h1>
      <p>
        {running} of {apps.length} {apps.length === 1 ? "app is" : "apps are"} running{#if admin}{" "}for {people.length} {people.length === 1 ? "person" : "people"}{/if}.
        Last backup {backupText(backup)}{backup && !backup.ok ? ", and it failed" : ""}.
      </p>
      <p class="live" class:up={live.up} title={live.why}>{live.up ? "Live" : live.tried ? "Reconnecting to the cell" : "Connecting"}</p>
    </div>
    {#if apps.length}
      <figure class="shelf">
        <ul aria-label={admin ? "Apps by how many people can open them" : "Apps"}>
          {#each apps as a, i (a.name)}
            {@const s = appState(a)}
            {@const n = reaches.get(a.name) ?? 0}
            {@const down = isDown(a)}
            <li class:down>
              <a href={itemPath("/apps", a.name)} aria-label="{title(a.name)}: {s.label}{admin ? `, ${n} of ${people.length} people can open it` : ''}">
                <span
                  class="pill {down ? 'lying' : s.tone === 'wait' ? 'busy' : shades[i % 3]}"
                  style:--h={down ? "34px" : `${Math.round(44 + 96 * (n / most))}px`}
                ></span>
                <span class="name">{title(a.name)}</span>
                <small>{s.tone === "ok" ? "\u00a0" : down ? "is down" : s.label.toLowerCase()}</small>
              </a>
            </li>
          {/each}
        </ul>
        <figcaption>One shape per app.{admin ? " Taller means more people can open it." : ""} <a href="/apps">All apps</a></figcaption>
      </figure>
    {/if}
  </section>

  {#if admin}<Ask />{/if}

  <div class="grid">
    <section aria-labelledby="needs">
      <header>
        <h2 id="needs" class="sec-title">Needs you <small>{waiting.length} waiting for a yes or a no</small></h2>
        <a href="/approvals">all approvals</a>
      </header>
      {#if noTarget}
        <ul class="rows">
          <li>
            <a href="/backups">
              <span class="plus warn" aria-hidden="true">!</span>
              <span><strong>{admin ? "Set a backup target" : "No backup target"}</strong><small>backups have nowhere to go yet</small></span>
              <Status tone="bad" label="Not set" />
            </a>
          </li>
        </ul>
      {/if}
      {#if waiting[0]}
        <!-- keyed: a newer request must not inherit a half-made decision -->
        {#key waiting[0].name}
          <Slip change={waiting[0]} title={title(waiting[0].app)} {admin} ondecided={load} />
        {/key}
        <ul class="rows">
          {#each waiting.slice(1, 5) as c (c.name)}
            <li>
              <a href="/approvals">
                {#if c.requester.kind === "Agent"}<AgentFace name={c.requester.name} size={30} />{:else}<Avatar name={c.requester.name} />{/if}
                <span><strong>{headline(c, title(c.app))}</strong><small>{c.name}, asked by {who(c)}, {ago(c.createdAt)}</small></span>
                <Status tone="wait" label="Waiting" />
              </a>
            </li>
          {/each}
        </ul>
      {:else if !noTarget}
        <div class="calm">
          <svg viewBox="0 0 64 64" aria-hidden="true"><circle cx="32" cy="32" r="26" /><path d="M21 33l7 7 15-16" /></svg>
          <p>Nothing waits for you. Changes that agents or people ask for show up here.</p>
        </div>
      {/if}
    </section>

    <section aria-labelledby="apps">
      <header>
        <h2 id="apps" class="sec-title">Apps <small>{apps.length} installed</small></h2>
        <a href="/apps">open apps</a>
      </header>
      <ul class="apps">
        {#each apps as a (a.name)}
          {@const s = appState(a)}
          {@const n = reaches.get(a.name) ?? 0}
          <li>
            <a href={itemPath("/apps", a.name)}>
              <Avatar name={title(a.name)} tile size={30} />
              <span><strong>{title(a.name)}</strong><small>{a.version ? `${a.name} ${a.version}` : a.name}</small></span>
              <Status tone={s.tone} label={s.label} />
              {#if admin}
                <span class="reach" title="{n} of {people.length} people can open it">
                  <span class="bar"><span style:width="{people.length ? (100 * n) / people.length : 0}%"></span></span>
                  <small>{n}/{people.length}</small>
                </span>
              {/if}
            </a>
          </li>
        {/each}
        {#if admin}
          <li>
            <a href="/apps" class="add">
              <span class="plus" aria-hidden="true">+</span>
              <span><strong>Add an app</strong><small>from the catalog</small></span>
            </a>
          </li>
        {/if}
      </ul>
    </section>

    <section aria-labelledby="activity">
      <header>
        <h2 id="activity" class="sec-title">Activity <small>every change, who asked, how it ended</small></h2>
        <a href="/history">full history</a>
      </header>
      {#if recent.length}
        <ol class="feed">
          {#each recent as c (c.name)}
            {@const e = ended(c)}
            <li>
              <a href={itemPath("/history", c.name)}>
                <time datetime={c.createdAt}>{clock(c.createdAt)}</time>
                {#if c.requester.kind === "Agent"}<AgentFace name={c.requester.name} size={26} />{:else}<Avatar name={c.requester.name} size={24} />{/if}
                <span><strong>{who(c)}</strong> asked to {headline(c, title(c.app)).replace(/^./, (x) => x.toLowerCase())}.</span>
                <Status tone={e.tone} label={e.label} />
              </a>
            </li>
          {/each}
        </ol>
      {:else}
        <p class="empty">No changes yet.</p>
      {/if}
    </section>

    {#if admin}
    <section aria-labelledby="operators">
      <header>
        <h2 id="operators" class="sec-title">Who works here <small>{crew.length} agents, the rules, {people.length} people</small></h2>
        <a href="/agents">agents</a>
      </header>
      <ul class="rows">
        {#each crew as a (a.name)}
          {@const at = busyTasks.filter((t) => a.kinds.includes(t.kind)).length}
          <li>
            <a href="/agents">
              <AgentFace name={a.name} size={30} />
              <span><strong>{a.name}</strong><small>{a.does}</small></span>
              <Status tone={at ? "wait" : "none"} label={at ? `${at} at work` : "Idle"} />
            </a>
          </li>
        {/each}
        <li>
          <div class="static">
            <svg class="rule" viewBox="0 0 30 30" aria-hidden="true"><rect x="8" y="8" width="14" height="14" rx="3" transform="rotate(45 15 15)" /></svg>
            <span><strong>Rules</strong><small>backups, updates, rollbacks; no AI</small></span>
            <Status tone="ok" label="Always on" />
          </div>
        </li>
        <li>
          <a href="/organization">
            <span class="faces" aria-hidden="true">
              {#each people.slice(0, 4) as p (p.username)}<Avatar name={p.name || p.username} size={24} />{/each}
            </span>
            <span><strong>{people.length} {people.length === 1 ? "person" : "people"}</strong><small>{teams.length} {teams.length === 1 ? "group" : "groups"}</small></span>
            {#if people.length > 4}<span class="chip">+{people.length - 4}</span>{/if}
          </a>
        </li>
      </ul>
    </section>
    {/if}
  </div>
{/if}

<style>
  .hero {
    position: relative;
    isolation: isolate;
    display: grid;
    grid-template-columns: minmax(0, 1.05fr) minmax(0, 1fr);
    align-items: end;
    gap: 40px;
    margin: 48px 0 56px;
  }
  /* the one soft glow of the brand, behind the shapes */
  .hero::before {
    content: "";
    position: absolute;
    z-index: -1;
    right: 2%;
    top: -10px;
    width: min(560px, 60vw);
    height: 280px;
    border-radius: 50%;
    background: var(--paint-glow);
    filter: blur(40px);
    opacity: 0.45;
    pointer-events: none;
  }
  h1 {
    margin: 0 0 18px;
    font: 600 clamp(2.4rem, 5vw, 4.2rem) / 1.02 var(--font-display);
    letter-spacing: -0.038em;
    text-wrap: balance;
  }
  h1 span {
    background: var(--paint-headline);
    -webkit-background-clip: text;
    background-clip: text;
    color: transparent;
  }
  .words p {
    margin: 0;
    max-width: 56ch;
    color: var(--color-muted);
    font-size: 1.1rem;
  }
  .words .live {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    margin-top: 14px;
    font-size: 0.85rem;
  }
  .live::before {
    content: "";
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--color-wait);
  }
  .live.up::before {
    background: var(--color-ok);
    box-shadow: 0 0 0 4px color-mix(in srgb, var(--color-ok) 25%, transparent);
  }
  .shelf {
    margin: 0;
  }
  .shelf ul {
    display: flex;
    align-items: flex-end;
    gap: 10px;
    margin: 0;
    padding: 0 0 0;
    list-style: none;
    border-bottom: 1px solid var(--color-line);
    overflow-x: auto;
  }
  .shelf li {
    flex: 1 1 0;
    min-width: 46px;
    max-width: 70px;
  }
  .shelf li.down {
    flex-basis: 80px;
    max-width: 100px;
  }
  .shelf li a {
    display: grid;
    justify-items: center;
    gap: 6px;
    padding-bottom: 8px;
    text-decoration: none;
  }
  .pill {
    display: block;
    width: 100%;
    max-width: 52px;
    height: var(--h);
    border-radius: 999px;
    box-shadow: 0 18px 30px -18px var(--color-base);
    transition: transform 0.2s ease;
  }
  .shelf li a:hover .pill {
    transform: translateY(-4px);
  }
  .pill.sun {
    background: var(--shelf-sun);
  }
  .pill.rose {
    background: var(--shelf-rose);
  }
  .pill.lilac {
    background: var(--shelf-lilac);
  }
  .pill.busy {
    background: none;
    border: 2px dashed var(--color-muted);
  }
  .pill.lying {
    max-width: 96px;
    background: var(--shelf-down);
  }
  .shelf .name {
    max-width: 100%;
    overflow: hidden;
    font-size: 0.8rem;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .shelf small {
    margin-top: -6px;
    color: var(--color-muted);
    font-size: 0.7rem;
  }
  .shelf .down .name,
  .shelf .down small {
    color: var(--color-bad);
  }
  figcaption {
    margin-top: 12px;
    color: var(--color-muted);
    font-size: 0.85rem;
  }
  figcaption a {
    color: var(--color-ink);
  }

  .grid {
    display: grid;
    grid-template-columns: minmax(0, 1.1fr) minmax(0, 1fr);
    gap: 56px 48px;
  }
  @media (max-width: 960px) {
    .hero,
    .grid {
      grid-template-columns: 1fr;
    }
  }
  header {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 16px;
  }
  header h2 {
    margin: 0;
  }
  header small {
    margin-left: 8px;
    color: var(--color-muted);
    font: 400 0.82rem var(--font-sans);
    letter-spacing: 0;
  }
  header > a {
    font-size: 0.85rem;
  }
  ul,
  ol {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .rows {
    margin-top: 14px;
  }
  .rows a,
  .rows .static,
  .apps a {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    align-items: center;
    gap: 14px;
    padding: 10px 10px;
    border-radius: var(--radius-part);
    text-decoration: none;
  }
  .rows a:hover,
  .apps a:hover,
  .feed a:hover {
    background: var(--color-sunk);
  }
  li span > strong {
    display: block;
    font-weight: 600;
  }
  li span > small {
    display: block;
    color: var(--color-muted);
    font-size: 0.82rem;
  }
  .apps a {
    grid-template-columns: auto minmax(0, 1fr) 120px minmax(110px, 160px);
  }
  .reach {
    display: grid;
    grid-template-columns: 1fr auto;
    align-items: center;
    gap: 10px;
  }
  .reach small {
    color: var(--color-muted);
    font-size: 0.8rem;
    font-variant-numeric: tabular-nums;
  }
  .bar {
    height: 6px;
    border-radius: 999px;
    background: var(--color-sunk);
    overflow: hidden;
  }
  .bar span {
    display: block;
    height: 100%;
    border-radius: inherit;
    background: var(--color-info);
  }
  .add .plus,
  .plus.warn {
    display: grid;
    place-items: center;
    width: 30px;
    height: 30px;
    border: 1.5px dashed var(--color-muted);
    border-radius: 50%;
    color: var(--color-muted);
  }
  .plus.warn {
    border: 0;
    background: color-mix(in srgb, var(--color-bad) 22%, transparent);
    color: var(--color-bad);
    font-weight: 700;
  }
  .add {
    grid-template-columns: auto minmax(0, 1fr) !important;
  }
  .feed a {
    display: grid;
    grid-template-columns: 82px auto minmax(0, 1fr) auto;
    align-items: center;
    gap: 12px;
    padding: 9px 10px;
    border-radius: var(--radius-part);
    text-decoration: none;
  }
  .feed time {
    color: var(--color-muted);
    font-size: 0.82rem;
    font-variant-numeric: tabular-nums;
  }
  .feed strong {
    display: inline;
    font-weight: 600;
  }
  .faces {
    display: flex;
  }
  .faces :global(*:not(:first-child)) {
    margin-left: -8px;
  }
  .rule {
    width: 30px;
    height: 30px;
  }
  .rule rect {
    fill: var(--color-ink);
    opacity: 0.85;
  }
  .calm {
    display: grid;
    grid-template-columns: auto 1fr;
    align-items: center;
    gap: 18px;
    padding: 24px;
    border-radius: var(--radius-panel);
    background: var(--color-panel);
  }
  .calm svg {
    width: 52px;
    height: 52px;
    fill: none;
    stroke: var(--color-ok);
    stroke-width: 3;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .calm p,
  .empty {
    margin: 0;
    color: var(--color-muted);
  }
</style>
