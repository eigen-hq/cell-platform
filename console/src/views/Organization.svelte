<script lang="ts">
  // People, groups and agent accounts (Figma: 18 Organization). Apps are
  // attached to groups; everyone in a group gets them. Groups are flat
  // today (departments and teams are still open in the backlog). One group
  // opens under /organization/<group> to change its members.
  import { api, must } from "../lib/api/client";
  import type { components } from "../lib/api/schema";
  import { authMode } from "../lib/phase";
  import { itemPath } from "../lib/route";
  import { newest, STALE } from "../lib/newest";
  import { replaced } from "../lib/draft";
  import AgentFace from "../lib/ui/AgentFace.svelte";
  import Avatar from "../lib/ui/Avatar.svelte";
  import Button from "../lib/ui/Button.svelte";
  import CommandBox from "../lib/ui/CommandBox.svelte";
  import StatStrip, { type Stat } from "../lib/ui/StatStrip.svelte";
  import GroupDrawer from "./GroupDrawer.svelte";

  type User = components["schemas"]["User"];
  type Group = components["schemas"]["Group"];
  type App = components["schemas"]["App"];

  const ADMINS = "cell-admins";
  const AGENTS = "cell-agents";

  let { open, onnavigate }: { open?: string; onnavigate: (href: string) => void } = $props();
  let users = $state<User[] | null>(null);
  let groups = $state<Group[]>([]);
  let apps = $state<App[]>([]);
  let failed = $state("");

  // A poll that started before a save may answer after the reload: drop it.
  const guard = newest();
  async function load() {
    try {
      const answer = await guard(Promise.all([api.GET("/v0/users"), api.GET("/v0/groups"), api.GET("/v0/apps")]));
      if (answer === STALE) return;
      const [u, g, a] = answer;
      users = must(u);
      groups = must(g).sort((x, y) => x.name.localeCompare(y.name));
      apps = must(a);
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

  // The person form and the group form, each with its own outcome.
  let adding = $state(false);
  let person = $state({ username: "", name: "", email: "", password: "", groups: [] as string[] });
  let personNote = $state<{ text: string; bad?: boolean } | null>(null);
  let newGroup = $state("");
  let groupNote = $state<{ text: string; bad?: boolean } | null>(null);
  let token = $state<{ user: string; value: string } | null>(null);
  let tokenError = $state("");

  const msg = (e: unknown) => (e instanceof Error ? e.message : String(e));

  async function addPerson(e: SubmitEvent) {
    e.preventDefault();
    personNote = null;
    try {
      const body = { username: person.username.trim(), name: person.name.trim(), email: person.email.trim() || undefined, password: person.password || undefined, groups: person.groups };
      must(await api.POST("/v0/users", { body }));
      personNote = { text: `${body.name || body.username} can sign in now.` };
      person = { username: "", name: "", email: "", password: "", groups: [] };
      adding = false;
      await load();
    } catch (err) {
      personNote = { text: msg(err), bad: true };
    }
  }

  async function addGroup(e: SubmitEvent) {
    e.preventDefault();
    groupNote = null;
    try {
      must(await api.POST("/v0/groups", { body: { name: newGroup.trim() } }));
      groupNote = { text: `Group ${newGroup.trim()} is there. Open it to add people.` };
      newGroup = "";
      await load();
    } catch (err) {
      groupNote = { text: msg(err), bad: true };
    }
  }

  // One app password at a time: each request makes a credential, and only
  // the one on screen can be copied.
  let minting = $state(false);
  async function appPassword(u: User) {
    if (minting) return;
    minting = true;
    token = null;
    tokenError = "";
    try {
      token = { user: u.username, value: must(await api.POST("/v0/users/{name}/tokens", { params: { path: { name: u.username } } })).token };
    } catch (err) {
      tokenError = `${u.username}: ${msg(err)}`;
    } finally {
      minting = false;
    }
  }

  const isAgent = (u: User) => u.groups.includes(AGENTS);
  let people = $derived((users ?? []).filter((u) => !isAgent(u)));
  let agentAccounts = $derived((users ?? []).filter(isAgent));
  let teams = $derived(groups.filter((g) => g.name !== AGENTS));
  // Apps by the group they are attached to; "" holds the apps for everyone.
  let byGroup = $derived.by(() => {
    const m = new Map<string, App[]>();
    for (const a of apps) {
      if (authMode(a) === "none") continue;
      for (const g of a.groups.length ? new Set(a.groups) : [""]) m.set(g, [...(m.get(g) ?? []), a]);
    }
    return m;
  });
  let stats = $derived.by((): Stat[] => [
    { label: "People", value: String(people.length), tone: "ok" },
    { label: "Groups", value: String(teams.length), tone: "ok" },
    { label: "Admins", value: String(new Set(groups.find((g) => g.name === ADMINS)?.members ?? []).size), tone: "ok" },
    { label: "Agent accounts", value: String(agentAccounts.length), tone: "ok" },
    { label: "Apps for everyone", value: String(byGroup.get("")?.length ?? 0), tone: "ok" },
  ]);
  const name = (u: string) => users?.find((x) => x.username === u)?.name || u;
  const title = (a: App) => a.title || a.name;
  let current = $derived(teams.find((g) => g.name === open));
</script>

<section class="view-head">
  <h1>Organization</h1>
  <p>Apps are attached to groups. Everyone in a group gets them.</p>
</section>

<!-- A failed poll keeps what is on screen, open edits included. -->
{#if failed}
  <p class="fail" role="alert">{failed}</p>
{/if}
{#if users}
  <StatStrip items={stats} />

  <section class="tree" aria-label="Who gets which apps">
    <div class="root">
      <span class="all">all</span>
      <span><strong>Everyone</strong><small>{people.length} people</small></span>
      <span class="gets"><small>gets</small></span>
      <span class="tiles">
        {#each byGroup.get("") ?? [] as a (a.name)}<Avatar name={title(a)} tile size={24} />{:else}<small>no app yet</small>{/each}
      </span>
    </div>
    <ul class="groups">
      {#each teams as g (g.name)}
        {@const members = [...new Set(g.members)]}
        <li>
          <a href={itemPath("/organization", g.name)}>
            <span class="gh"><strong>{g.name}</strong><small>{members.length} {members.length === 1 ? "person" : "people"}</small></span>
            <span class="faces">{#each members.slice(0, 4) as m (m)}<Avatar name={name(m)} size={22} />{/each}{#if members.length > 4}<small>+{members.length - 4}</small>{/if}</span>
            <span class="tiles">
              {#each byGroup.get(g.name) ?? [] as a (a.name)}<Avatar name={title(a)} tile size={24} />{/each}
              {#if g.name === ADMINS}<span class="chip">runs the console</span>{/if}
            </span>
          </a>
        </li>
      {/each}
      <li class="new">
        <form onsubmit={addGroup}>
          <label for="new-group"><strong>New group</strong><small>lowercase, digits, . _ -</small></label>
          <input id="new-group" bind:value={newGroup} placeholder="e.g. studio" pattern="[a-z0-9][a-z0-9._\-]{'{1,62}'}" required />
          <Button kind="quiet" type="submit">Add group</Button>
          {#if groupNote}<small class:fail={groupNote.bad} role="status">{groupNote.text}</small>{/if}
        </form>
      </li>
    </ul>
    {#if agentAccounts.length}
      <div class="agents">
        <span class="faces">{#each agentAccounts.slice(0, 3) as a (a.username)}<AgentFace name={a.username.replace(/^agent-/, "")} size={28} />{/each}</span>
        <span><strong>Agents</strong><small>{agentAccounts.length} {agentAccounts.length === 1 ? "account that works" : "accounts that work"} here too</small></span>
        {#each agentAccounts as a (a.username)}<span class="chip">{a.username}</span>{/each}
      </div>
    {/if}
  </section>

  <section class="people">
    <div class="head">
      <h2 class="sec-title">People <small>{people.length}</small></h2>
      {#if !adding}<Button kind="quiet" onclick={() => { adding = true; personNote = null; }}>Add a person</Button>{/if}
    </div>
    {#if personNote}<p class="note" class:fail={personNote.bad} role="status">{personNote.text}</p>{/if}
    {#if adding}
      <form class="add" onsubmit={addPerson}>
        <label>Username<input bind:value={person.username} required pattern="[a-z0-9][a-z0-9._\-]{'{1,62}'}" autocomplete="off" /></label>
        <label>Name<input bind:value={person.name} required autocomplete="off" /></label>
        <label>Email<input type="email" bind:value={person.email} autocomplete="off" /></label>
        <label>First password<input type="password" bind:value={person.password} autocomplete="new-password" /></label>
        <fieldset>
          <legend>Groups</legend>
          {#each teams as g (g.name)}
            <label class="pick"><input type="checkbox" bind:group={person.groups} value={g.name} />{g.name}</label>
          {/each}
        </fieldset>
        <div class="row"><Button type="submit">Add</Button><Button kind="quiet" type="button" onclick={() => (adding = false)}>Cancel</Button></div>
      </form>
    {/if}
    {#if token}
      <div class="token" role="status">
        <p><strong>App password for {token.user}</strong>, shown only now. It signs in to the API in that person's name.</p>
        <CommandBox command={token.value} />
        <Button kind="quiet" onclick={() => (token = null)}>Done</Button>
      </div>
    {/if}
    {#if tokenError}<p class="note fail" role="alert">{tokenError}</p>{/if}
    <ul class="list">
      {#each people as u (u.username)}
        <li>
          <Avatar name={u.name || u.username} size={30} />
          <span class="who"><strong>{u.name || u.username}</strong><small>{u.username}{u.email ? ` · ${u.email}` : ""}</small></span>
          <span class="in">{#each [...new Set(u.groups)] as g (g)}<span class="chip">{g}</span>{/each}</span>
          <Button kind="quiet" disabled={minting} onclick={() => appPassword(u)}>App password</Button>
        </li>
      {/each}
    </ul>
  </section>

  {#if current}
    {#key current.name}
    <GroupDrawer
      group={current}
      {people}
      apps={(byGroup.get(current.name) ?? []).map(title)}
      onclose={() => onnavigate("/organization")}
      onchange={(g) => {
        // The save's answer counts at once, even if the reload fails.
        if (g) groups = replaced(groups, g);
        load();
      }}
    />
    {/key}
  {/if}
{/if}

<style>
  .tree {
    position: relative;
    display: grid;
    justify-items: center;
    gap: 34px;
    margin-top: 40px;
  }
  .root {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 14px 18px;
    border-radius: var(--radius-panel);
    background: var(--color-panel);
  }
  .root::after {
    content: "";
    position: absolute;
    top: 72px;
    left: 8%;
    right: 8%;
    height: 22px;
    border: 2px dashed var(--color-line);
    border-bottom: 0;
    border-radius: 14px 14px 0 0;
    pointer-events: none;
  }
  .all {
    display: grid;
    place-items: center;
    width: 28px;
    height: 28px;
    border-radius: 8px;
    background: var(--color-sunk);
    font-size: 11.5px;
    font-weight: 700;
  }
  .root strong,
  .root small,
  .gh strong,
  .gh small,
  .agents strong,
  .agents small {
    display: block;
  }
  small {
    color: var(--color-muted);
    font-size: 13px;
  }
  .gets {
    min-width: 64px;
    padding-bottom: 6px;
    background: repeating-linear-gradient(90deg, var(--color-ok) 0 5px, transparent 5px 9px) bottom / 100% 2px no-repeat;
    text-align: center;
  }
  .tiles,
  .faces {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px;
  }
  .groups {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
    gap: 12px;
    width: 100%;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .groups a,
  .groups form {
    display: grid;
    align-content: start;
    gap: 12px;
    height: 100%;
    padding: 16px;
    border-radius: var(--radius-panel);
    background: var(--color-panel);
    color: inherit;
    text-decoration: none;
  }
  .groups a:hover {
    outline: 2px solid var(--color-line);
  }
  .gh strong {
    font: 600 1.05rem var(--font-display);
  }
  .new label strong,
  .new label small {
    display: block;
  }
  .new form {
    background: none;
    outline: 2px dashed var(--color-line);
    outline-offset: -2px;
  }
  .agents {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 12px;
    padding: 12px 18px;
    border-radius: var(--radius-panel);
    background: var(--color-panel);
  }
  .people {
    display: grid;
    gap: 12px;
    margin-top: 48px;
  }
  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  .head .sec-title {
    margin: 0;
  }
  input {
    width: 100%;
    padding: 9px 12px;
    border: 2px solid var(--color-line);
    border-radius: var(--radius-part);
    background: var(--color-sunk);
    color: inherit;
    font: inherit;
  }
  .pick input {
    width: auto;
  }
  .add {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
    gap: 12px;
    padding: 16px;
    border-radius: var(--radius-panel);
    background: var(--color-panel);
  }
  .add label {
    display: grid;
    gap: 6px;
    font-size: 13.5px;
    font-weight: 600;
  }
  fieldset {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    grid-column: 1 / -1;
    margin: 0;
    padding: 0;
    border: 0;
  }
  legend {
    margin-bottom: 6px;
    font-size: 13.5px;
    font-weight: 600;
  }
  .add .pick {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    padding: 6px 12px;
    border-radius: var(--radius-pill);
    background: var(--color-sunk);
  }
  .row {
    display: flex;
    gap: 8px;
    grid-column: 1 / -1;
  }
  .token {
    display: grid;
    gap: 10px;
    justify-items: start;
    padding: 16px;
    border-radius: var(--radius-panel);
    background: color-mix(in srgb, var(--color-wait) 16%, var(--color-panel));
  }
  .token p {
    margin: 0;
  }
  .token :global(.box) {
    width: 100%;
  }
  .list {
    display: grid;
    gap: 6px;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .list li {
    display: grid;
    grid-template-columns: 30px minmax(0, 1.2fr) minmax(0, 1fr) auto;
    align-items: center;
    gap: 14px;
    padding: 10px 12px;
    border-radius: var(--radius-part);
  }
  .list li:hover {
    background: var(--color-panel);
  }
  .who strong,
  .who small {
    display: block;
  }
  .in {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .note {
    margin: 0;
    padding: 12px 14px;
    border-radius: var(--radius-part);
    background: color-mix(in srgb, var(--color-info) 16%, transparent);
    font-weight: 600;
  }
  .note.fail,
  small.fail {
    color: var(--color-bad);
  }
  @media (max-width: 760px) {
    .list li {
      grid-template-columns: 30px 1fr auto;
    }
    .in {
      display: none;
    }
    .root::after {
      display: none;
    }
  }
</style>
