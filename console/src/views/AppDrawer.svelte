<script lang="ts">
  // One app in the drawer (Figma: 07 Drawer - app): its state, who may open
  // it, its backups, what happened inside, and the actions on it. Every
  // action that changes the app is a change request and waits for approval.
  import { api, must } from "../lib/api/client";
  import type { components } from "../lib/api/schema";
  import { appStatus, audience, authMode } from "../lib/phase";
  import { ago, stamp } from "../lib/time";
  import Avatar from "../lib/ui/Avatar.svelte";
  import Button from "../lib/ui/Button.svelte";
  import Drawer from "../lib/ui/Drawer.svelte";
  import Status from "../lib/ui/Status.svelte";
  import { open } from "../lib/change";
  import { newest, STALE } from "../lib/newest";
  import AppPods from "./AppPods.svelte";

  type App = components["schemas"]["App"];
  type Backup = components["schemas"]["Backup"];
  type AppEvent = components["schemas"]["AppEvent"];
  type Change = components["schemas"]["ChangeRequest"];
  type Group = components["schemas"]["Group"];

  let { app, admin, onclose, onchange }: { app: App; admin: boolean; onclose: () => void; onchange: () => void } = $props();
  let backups = $state<Backup[]>([]);
  let events = $state<AppEvent[]>([]);
  let pending = $state<Change[]>([]);
  let groups = $state<Group[]>([]);
  let note = $state<{ text: string; bad?: boolean } | null>(null);
  let confirming = $state("");
  let editing = $state<string[] | null>(null);

  // A poll that started before an action may answer after it: drop it.
  const guard = newest();
  async function load(name: string) {
    const answer = await guard(Promise.all([
      api.GET("/v0/apps/{name}/backups", { params: { path: { name } } }),
      api.GET("/v0/apps/{name}/events", { params: { path: { name } } }),
      api.GET("/v0/changes", { params: { query: { app: name } } }),
    ]));
    if (answer === STALE) return;
    const [b, e, c] = answer;
    backups = must(b);
    events = must(e);
    pending = must(c).filter(open);
  }

  $effect(() => {
    const name = app.name;
    const run = () => load(name).catch((e) => (note = { text: e instanceof Error ? e.message : String(e), bad: true }));
    run();
    const t = setInterval(run, 5000);
    return () => clearInterval(t);
  });

  // Runs an action, then says what became of it.
  async function act(what: () => Promise<string>) {
    confirming = "";
    note = null;
    try {
      note = { text: await what() };
      onchange();
      await load(app.name);
    } catch (e) {
      note = { text: e instanceof Error ? e.message : String(e), bad: true };
    }
  }
  const outcome = (c: Change) => (c.approval.required ? "Sent for approval. It runs once someone approves it." : "Running now.");
  const path = $derived({ params: { path: { name: app.name } } });

  const backupNow = () => act(async () => (must(await api.POST("/v0/apps/{name}/backups", path)), "Backup started."));
  const restore = (b: Backup) =>
    act(async () => outcome(must(await api.POST("/v0/apps/{name}/restore", { ...path, body: { backup: b.name, reason: "restored from the console" } }))));
  const remove = (deleteData: boolean) =>
    act(async () => outcome(must(await api.DELETE("/v0/apps/{name}", { params: { path: { name: app.name }, query: { deleteData, reason: "removed from the console" } } }))));
  const diagnose = () =>
    act(async () => (must(await api.POST("/v0/tasks", { body: { kind: "diagnose", app: app.name } })), "Ops is looking into it. Its findings show up under Agents."));
  async function editAccess() {
    try {
      groups = must(await api.GET("/v0/groups"));
      editing = [...new Set(app.groups)];
    } catch (e) {
      note = { text: e instanceof Error ? e.message : String(e), bad: true };
    }
  }
  const saveAccess = () =>
    act(async () => {
      const c = must(await api.PUT("/v0/apps/{name}/access", { ...path, body: { groups: editing ?? [], reason: "access changed in the console" } }));
      editing = null;
      return outcome(c);
    });
  // A new version: typed, or the one the catalog offers. It is a change
  // like any other: backup first, rolled back if the health check fails.
  let nextVersion = $state<string | null>(null);
  let offered = $state("");
  async function editVersion() {
    note = null;
    try {
      offered = must(await api.GET("/v0/catalog")).find((e) => e.name === app.name)?.version ?? "";
      nextVersion = offered && offered !== app.version ? offered : app.version;
    } catch (e) {
      note = { text: e instanceof Error ? e.message : String(e), bad: true };
    }
  }
  const updating = $derived(pending.some((c) => c.action === "update"));
  const wanted = $derived(nextVersion?.trim() ?? "");
  // One request at a time: a second click must not ask for a second update.
  let asking = $state(false);
  async function askUpdate() {
    if (asking) return;
    asking = true;
    await act(async () => {
      const c = must(await api.PATCH("/v0/apps/{name}", { ...path, body: { patch: { version: wanted }, reason: `version ${wanted} from the console` } }));
      nextVersion = null;
      pending = [...pending.filter((x) => x.name !== c.name), c];
      return outcome(c);
    });
    asking = false;
  }
  const toggle = (g: string) => (editing = editing!.includes(g) ? editing!.filter((x) => x !== g) : [...editing!, g]);

  const s = $derived(appStatus(app.phase));
  const host = $derived(app.url?.replace(/^https?:\/\//, "").replace(/\/$/, "") ?? "");
  const version = $derived(app.runningVersion && app.runningVersion !== app.version ? `${app.runningVersion} → ${app.version}` : app.version);
  const trouble = $derived(app.conditions.filter((c) => c.status === "False" && c.message));
  const actions = { remove: "Remove, keep the data", purge: "Remove and delete the data" } as const;
</script>

{#snippet confirm(key: string, label: string, run: () => void, danger = false)}
  {#if confirming === key}
    <span class="sure">
      <Button kind={danger ? "danger" : "quiet"} onclick={run}>Yes, {label.toLowerCase()}</Button>
      <Button kind="quiet" onclick={() => (confirming = "")}>Cancel</Button>
    </span>
  {:else}
    <Button kind={danger ? "danger" : "quiet"} onclick={() => (confirming = key)}>{label}</Button>
  {/if}
{/snippet}

<Drawer title={app.title || app.name} subtitle="{app.name} {app.version}{host ? ` / ${host}` : ''}" {onclose}>
  <div class="state">
    <Avatar name={app.title || app.name} tile size={40} />
    <Status tone={s.tone} label={s.label} />
    {#if app.url}<a class="open" href={app.url} target="_blank" rel="noopener noreferrer">Open <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M14 5h5v5M19 5l-8 8M18 14v5H5V6h5" /></svg></a>{/if}
  </div>

  <dl class="tiles">
    <div><dt>Version</dt><dd>{version}</dd></div>
    <div><dt>Backup</dt><dd>{ago(app.lastBackup?.at)}</dd></div>
    <div><dt>For</dt><dd>{authMode(app) === "none" ? "anyone" : app.groups.length ? `${new Set(app.groups).size} ${new Set(app.groups).size === 1 ? "group" : "groups"}` : "everyone"}</dd></div>
  </dl>

  {#if note}<p class="note" class:fail={note.bad} role="status">{note.text}</p>{/if}

  {#if trouble.length}
    <section class="trouble">
      <h3>What is wrong</h3>
      {#each trouble as c (c.type)}<p><strong>{c.reason || c.type}</strong> {c.message}</p>{/each}
      <Button onclick={diagnose}>Let Ops diagnose it</Button>
    </section>
  {/if}

  {#if pending.length}
    <section>
      <h3>Waiting <small>changes of this app not done yet</small></h3>
      <ul class="list">
        {#each pending as c (c.name)}
          <li><span class="chip">{c.action}</span><span>{c.reason || c.action}</span><small>{c.phase === "WaitingForApproval" ? "needs approval" : c.phase.toLowerCase()}</small></li>
        {/each}
      </ul>
    </section>
  {/if}

  <section>
    <h3>Version {#if nextVersion === null}<button class="link" onclick={editVersion}>change</button>{/if}</h3>
    {#if nextVersion !== null}
      <form class="version" onsubmit={(e) => (e.preventDefault(), askUpdate())}>
        <label>New version <input bind:value={nextVersion} autocomplete="off" spellcheck="false" /></label>
        {#if offered && offered !== app.version && wanted !== offered}<button type="button" class="link" onclick={() => (nextVersion = offered)}>the catalog has {offered}</button>{/if}
        <div class="row">
          <Button type="submit" disabled={!wanted || wanted === app.version || updating || asking}>Ask for the update</Button>
          <Button type="button" kind="quiet" onclick={() => (nextVersion = null)}>Cancel</Button>
        </div>
        {#if updating}<p class="hint">An update of this app is already on its way.</p>{/if}
      </form>
    {:else}
      <p class="hint">Runs {app.runningVersion || app.version}. An update backs the app up first and rolls back by itself if the health check fails.</p>
    {/if}
  </section>

  <section>
    <h3>Who can open it {#if !editing && authMode(app) !== "none"}<button class="link" onclick={editAccess}>change</button>{/if}</h3>
    {#if editing}
      <div class="groups">
        {#each groups as g (g.name)}
          <label class="pick" class:on={editing.includes(g.name)}>
            <input type="checkbox" checked={editing.includes(g.name)} onchange={() => toggle(g.name)} />{g.name}<small>{g.members.length}</small>
          </label>
        {/each}
      </div>
      <p class="hint">{editing.length ? "Only members of the ticked groups." : "No group ticked: everyone with an account of the cell."}</p>
      <div class="row"><Button onclick={saveAccess}>Propose</Button><Button kind="quiet" onclick={() => (editing = null)}>Cancel</Button></div>
    {:else}
      <div class="groups">
        {#each audience(app) as g (g)}<span class="chip">{g}</span>{/each}
      </div>
      {#if authMode(app) === "none"}<p class="hint">This app has no sign-in of the cell, so it cannot be limited to groups.</p>{/if}
    {/if}
  </section>

  <section>
    <h3>Backups <small>{backups.length ? `${backups.length}, newest first` : "none yet"}</small></h3>
    <ul class="list">
      {#each backups.slice(0, 6) as b (b.name)}
        <li>
          <Status tone={b.phase === "Completed" ? "ok" : b.phase === "Failed" ? "bad" : "wait"} label={ago(b.createdAt)} />
          <span class="muted">{b.reason || b.phase.toLowerCase() || "pending"}</span>
          {#if b.phase === "Completed"}{@render confirm(`restore:${b.name}`, "Bring back", () => restore(b))}{/if}
        </li>
      {/each}
    </ul>
    <div class="row"><Button kind="quiet" onclick={backupNow}>Back up now</Button></div>
  </section>

  <section>
    <h3>What runs <small>pods and their containers</small></h3>
    {#key app.name}<AppPods app={app.name} {admin} />{/key}
  </section>

  <section>
    <h3>Look inside <small>the newest events</small></h3>
    {#if events.length}
      <ol class="log">
        {#each events.slice(0, 12) as e}
          <li class:warn={e.type === "Warning"}><time>{stamp(e.last)}</time><span>{e.reason}: {e.message}</span></li>
        {/each}
      </ol>
    {:else}
      <p class="hint">Nothing happened lately.</p>
    {/if}
  </section>

  <section class="actions">
    {#if !trouble.length}<Button kind="quiet" onclick={diagnose}>Let Ops diagnose it</Button>{/if}
    {@render confirm("remove", actions.remove, () => remove(false))}
    {@render confirm("purge", actions.purge, () => remove(true), true)}
  </section>
</Drawer>

<style>
  .state {
    display: flex;
    align-items: center;
    gap: 14px;
  }
  .open {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    margin-left: auto;
    padding: 9px 16px;
    border-radius: var(--radius-pill);
    background: var(--color-sunk);
    font-weight: 600;
    text-decoration: none;
  }
  .open svg {
    width: 15px;
    height: 15px;
    fill: none;
    stroke: currentColor;
    stroke-width: 2;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .tiles {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 10px;
    margin: 0;
  }
  .tiles div {
    padding: 12px 14px;
    border-radius: var(--radius-part);
    background: var(--color-sunk);
  }
  dt {
    color: var(--color-muted);
    font-size: 13px;
    font-weight: 600;
  }
  dd {
    margin: 4px 0 0;
    font-weight: 700;
  }
  section {
    display: grid;
    gap: 12px;
  }
  h3 {
    display: flex;
    align-items: baseline;
    gap: 10px;
    margin: 0;
    font: 600 1.25rem / 1.2 var(--font-display);
    letter-spacing: -0.015em;
  }
  h3 small {
    color: var(--color-muted);
    font: 600 13px var(--font-sans);
  }
  .link {
    margin-left: auto;
    border: 0;
    padding: 0;
    background: none;
    font-size: 13.5px;
    font-weight: 600;
    text-decoration: underline;
  }
  .note {
    margin: 0;
    padding: 12px 14px;
    border-radius: var(--radius-part);
    background: color-mix(in srgb, var(--color-info) 16%, transparent);
    font-weight: 600;
  }
  .note.fail {
    background: color-mix(in srgb, var(--color-bad) 16%, transparent);
  }
  .trouble {
    padding: 16px;
    border-radius: var(--radius-part);
    background: color-mix(in srgb, var(--color-bad) 12%, transparent);
  }
  .trouble p {
    margin: 0;
    font-size: 14px;
  }
  .trouble :global(button) {
    justify-self: start;
  }
  .groups,
  .version {
    display: grid;
    justify-items: start;
    gap: 10px;
  }
  .version label {
    display: grid;
    gap: 4px;
    color: var(--color-muted);
    font-size: 0.85rem;
  }
  .version input {
    width: 16ch;
    padding: 8px 12px;
    border: 1px solid var(--color-line);
    border-radius: var(--radius-part);
    background: var(--color-sunk);
    color: var(--color-ink);
    font: 600 15px ui-monospace, SFMono-Regular, Menlo, monospace;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }
  .pick {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    padding: 6px 12px;
    border-radius: var(--radius-pill);
    background: var(--color-sunk);
    font-size: 13.5px;
    font-weight: 600;
    cursor: pointer;
  }
  .pick.on {
    background: var(--color-selected);
    color: var(--color-on-selected);
  }
  .pick input {
    margin: 0;
  }
  .pick small {
    opacity: 0.7;
  }
  .hint,
  .muted {
    margin: 0;
    color: var(--color-muted);
    font-size: 13.5px;
  }
  .list {
    display: grid;
    gap: 6px;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .list li {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 10px 14px;
    min-height: 44px;
    padding: 4px 4px 4px 14px;
    border-radius: var(--radius-part);
    background: var(--color-sunk);
  }
  .list li > :global(:last-child:not(:first-child)) {
    margin-left: auto;
  }
  .list small {
    color: var(--color-wait);
    font-weight: 600;
    padding-right: 10px;
  }
  .log {
    margin: 0;
    padding: 14px;
    border-radius: var(--radius-part);
    background: var(--color-sunk);
    list-style: none;
    font: 13px/1.6 ui-monospace, SFMono-Regular, Menlo, monospace;
  }
  .log li {
    display: flex;
    gap: 14px;
  }
  .log time {
    flex: none;
    color: var(--color-muted);
  }
  .log .warn span {
    color: var(--color-wait);
  }
  .sure {
    display: inline-flex;
    gap: 6px;
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }
</style>
