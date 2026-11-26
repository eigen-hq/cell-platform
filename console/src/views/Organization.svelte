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
  import { resultRow } from "../lib/row";
  import GroupDrawer from "./GroupDrawer.svelte";

  type User = components["schemas"]["User"];
  type Group = components["schemas"]["Group"];
  type App = components["schemas"]["App"];
  type Mail = components["schemas"]["Mail"];
  type Models = components["schemas"]["Models"];
  type Usage = components["schemas"]["ModelsUsage"]["accounts"];

  const ADMINS = "cell-admins";
  const AGENTS = "cell-agents";

  let { open, onnavigate }: { open?: string; onnavigate: (href: string) => void } = $props();
  let users = $state<User[] | null>(null);
  let groups = $state<Group[]>([]);
  let apps = $state<App[]>([]);
  let failed = $state("");
  let mail = $state<Mail | null>(null);
  let models = $state<Models | null>(null);

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

  // Mail settings are for admins only; a failure stays in their panel.
  let mailFailed = $state("");
  async function loadMail() {
    try {
      mail = must(await api.GET("/v0/mail"));
      mailFailed = "";
    } catch (e) {
      mailFailed = e instanceof Error ? e.message : String(e);
    }
  }
  let modelsFailed = $state("");
  async function loadModels() {
    try {
      models = must(await api.GET("/v0/models"));
      modelsFailed = "";
    } catch (e) {
      modelsFailed = e instanceof Error ? e.message : String(e);
    }
  }
  // Tokens per agent and app since the gateway last started.
  let usage = $state<Usage | null>(null);
  let usageFailed = $state("");
  async function loadUsage() {
    try {
      usage = must(await api.GET("/v0/models/usage")).accounts;
      usageFailed = "";
    } catch (e) {
      usageFailed = e instanceof Error ? e.message : String(e);
    }
  }
  const accountName = (a: string) => (a === "cell-agent" ? "Platform agent" : a.replace(/^app-/, ""));
  const tokens = (n: number) => n.toLocaleString("en");
  let usageMax = $derived(Math.max(1, ...(usage ?? []).map((a) => a.inputTokens + a.outputTokens)));

  $effect(() => {
    load();
    loadMail();
    loadModels();
    loadUsage();
    const t = setInterval(() => {
      load();
      loadUsage();
    }, 15000);
    return () => clearInterval(t);
  });

  // The person form and the group form, each with its own outcome.
  let adding = $state(false);
  let person = $state({ username: "", name: "", email: "", password: "", groups: [] as string[] });
  let personNote = $state<{ text: string; bad?: boolean } | null>(null);
  let newGroup = $state("");
  let groupNote = $state<{ text: string; bad?: boolean } | null>(null);
  // A credential on screen: an app password or an invite link, shown once.
  let token = $state<{ title: string; text: string; value: string } | null>(null);
  let tokenError = $state("");
  // The person the note, credential or error above is about: it shows in
  // that person's row, where the button was, so nothing moves under the
  // pointer. null shows it above the list (resultRow).
  let about = $state<string | null>(null);

  const msg = (e: unknown) => (e instanceof Error ? e.message : String(e));

  async function addPerson(e: SubmitEvent) {
    e.preventDefault();
    if (minting) return;
    minting = true;
    personNote = null;
    try {
      const body = { username: person.username.trim(), name: person.name.trim(), email: person.email.trim() || undefined, password: person.password || undefined, groups: person.groups };
      const u = must(await api.POST("/v0/users", { body }));
      person = { username: "", name: "", email: "", password: "", groups: [] };
      adding = false;
      // Without a first password the person sets one through an invite.
      if (body.password) {
        about = null;
        personNote = { text: `${u.name || u.username} can sign in now.` };
      }
      else await issueInvite(u);
      await load();
    } catch (err) {
      about = null;
      personNote = { text: msg(err), bad: true };
    } finally {
      minting = false;
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

  // One credential at a time (app password, invite, a new person's invite):
  // each request makes one, and only the one on screen can be copied. Every
  // button that asks for one is off while another is on its way.
  let minting = $state(false);
  async function appPassword(u: User) {
    if (minting) return;
    minting = true;
    token = null;
    tokenError = "";
    personNote = null;
    about = u.username;
    try {
      token = { title: `App password for ${u.username}`, text: "shown only now. It signs in to the API in that person's name.", value: must(await api.POST("/v0/users/{name}/tokens", { params: { path: { name: u.username } } })).token };
    } catch (err) {
      tokenError = `${u.username}: ${msg(err)}`;
    } finally {
      minting = false;
    }
  }

  async function invite(u: User) {
    if (minting) return;
    minting = true;
    try {
      await issueInvite(u);
    } finally {
      minting = false;
    }
  }
  async function issueInvite(u: User) {
    token = null;
    tokenError = "";
    personNote = null;
    about = u.username;
    try {
      const inv = must(await api.POST("/v0/users/{name}/invite", { params: { path: { name: u.username } } }));
      const until = new Date(inv.expires).toLocaleDateString();
      if (inv.mailed) personNote = { text: `Invite mailed to ${u.email}. The link works until ${until}.` };
      else if (inv.link) token = { title: `Invite link for ${u.name || u.username}`, text: `valid until ${until}. Hand it over yourself: it signs in as that person, so send it only to them.`, value: inv.link };
      else throw new Error("the invite came back without a link");
    } catch (err) {
      tokenError = `${u.username}: ${msg(err)}`;
    }
  }

  // System mail: invites and warnings to the admins go out through this server.
  let editingMail = $state(false);
  let mailForm = $state({ host: "", port: "", username: "", password: "", from: "", tls: false });
  let mailNote = $state<{ text: string; bad?: boolean } | null>(null);
  let savingMail = $state(false);
  function editMail() {
    const m = mail;
    mailForm = { host: m?.host ?? "", port: m?.port ? String(m.port) : "", username: m?.username ?? "", password: "", from: m?.from ?? "", tls: m?.tls ?? false };
    mailNote = null;
    editingMail = true;
  }
  async function saveMail(e: SubmitEvent) {
    e.preventDefault();
    if (savingMail) return;
    savingMail = true;
    mailNote = null;
    try {
      const f = mailForm;
      const body = { host: f.host.trim(), port: f.port ? Number(f.port) : undefined, username: f.username.trim() || undefined, password: f.password || undefined, from: f.from.trim(), tls: f.tls };
      mail = must(await api.PUT("/v0/mail", { body }));
      editingMail = false;
      mailNote = { text: `The server took the login. Invites and warnings go out by mail now.` };
    } catch (err) {
      mailNote = { text: msg(err), bad: true };
    } finally {
      savingMail = false;
    }
  }

  // The model provider: every agent of the cell reaches it through the
  // model gateway; its key stays there.
  let editingModels = $state(false);
  let modelsForm = $state({ provider: "anthropic" as "anthropic" | "openai", baseUrl: "", model: "", apiKey: "", tokensPerHour: null as number | null });
  let modelsNote = $state<{ text: string; bad?: boolean } | null>(null);
  let savingModels = $state(false);
  function editModels() {
    const m = models;
    modelsForm = { provider: m?.provider ?? "anthropic", baseUrl: m?.baseUrl ?? "", model: m?.model ?? "", apiKey: "", tokensPerHour: m?.tokensPerHour ?? null };
    modelsNote = null;
    editingModels = true;
  }
  async function saveModels(e: SubmitEvent) {
    e.preventDefault();
    if (savingModels) return;
    savingModels = true;
    modelsNote = null;
    try {
      const f = modelsForm;
      const body = { provider: f.provider, baseUrl: f.baseUrl.trim() || undefined, model: f.model.trim() || undefined, apiKey: f.apiKey || undefined, tokensPerHour: f.tokensPerHour || undefined };
      models = must(await api.PUT("/v0/models", { body }));
      editingModels = false;
      modelsNote = { text: `The provider took the key. Agents use ${models.model} within a minute.` };
    } catch (err) {
      modelsNote = { text: msg(err), bad: true };
    } finally {
      savingModels = false;
    }
  }

  const isAgent = (u: User) => u.groups.includes(AGENTS);
  let people = $derived((users ?? []).filter((u) => !isAgent(u)));
  let row = $derived(resultRow(about, people));
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
    {#if row === null}{@render results()}{/if}
    {#if adding}
      <form class="add" onsubmit={addPerson}>
        <label>Username<input bind:value={person.username} required pattern="[a-z0-9][a-z0-9._\-]{'{1,62}'}" autocomplete="off" /></label>
        <label>Name<input bind:value={person.name} required autocomplete="off" /></label>
        <label>Email<input type="email" bind:value={person.email} autocomplete="off" /></label>
        <label>First password<input type="password" bind:value={person.password} autocomplete="new-password" placeholder="empty: send an invite" /></label>
        <fieldset>
          <legend>Groups</legend>
          {#each teams as g (g.name)}
            <label class="pick"><input type="checkbox" bind:group={person.groups} value={g.name} />{g.name}</label>
          {/each}
        </fieldset>
        <div class="row"><Button type="submit" disabled={minting}>Add</Button><Button kind="quiet" type="button" onclick={() => (adding = false)}>Cancel</Button></div>
      </form>
    {/if}
    {#snippet results()}
      {#if personNote}<p class="note" class:fail={personNote.bad} role="status">{personNote.text}</p>{/if}
      {#if token}
        <div class="token" role="status">
          <p><strong>{token.title}</strong>, {token.text}</p>
          <CommandBox command={token.value} />
          <Button kind="quiet" onclick={() => (token = null)}>Done</Button>
        </div>
      {/if}
      {#if tokenError}<p class="note fail" role="alert">{tokenError}</p>{/if}
    {/snippet}
    <ul class="list">
      {#each people as u (u.username)}
        <li>
          <Avatar name={u.name || u.username} size={30} />
          <span class="who"><strong>{u.name || u.username}</strong><small>{u.username}{u.email ? ` · ${u.email}` : ""}</small></span>
          <span class="in">{#each [...new Set(u.groups)] as g (g)}<span class="chip">{g}</span>{/each}</span>
          <span class="acts">
            <Button kind="quiet" disabled={minting} onclick={() => invite(u)}>Invite</Button>
            <Button kind="quiet" disabled={minting} onclick={() => appPassword(u)}>App password</Button>
          </span>
          {#if row === u.username}<div class="result">{@render results()}</div>{/if}
        </li>
      {/each}
    </ul>
  </section>

  <section class="mail">
    <div class="head">
      <h2 class="sec-title">System mail</h2>
      {#if !editingMail}<Button kind="quiet" onclick={editMail}>{mail?.configured ? "Change" : "Set up"}</Button>{/if}
    </div>
    {#if mailNote}<p class="note" class:fail={mailNote.bad} role="status">{mailNote.text}</p>{/if}
    {#if mailFailed}<p class="note fail" role="alert">{mailFailed}</p>{/if}
    {#if editingMail}
      <form class="add" onsubmit={saveMail}>
        <label>Server<input bind:value={mailForm.host} required placeholder="smtp.example.com" autocomplete="off" /></label>
        <label>Port<input type="number" min="1" max="65535" bind:value={mailForm.port} placeholder={mailForm.tls ? "465" : "587"} /></label>
        <label>Sender<input type="email" bind:value={mailForm.from} required placeholder="cell@example.com" autocomplete="off" /></label>
        <label>Login<input bind:value={mailForm.username} autocomplete="off" /></label>
        <label>Password<input type="password" bind:value={mailForm.password} autocomplete="new-password" placeholder={mail?.configured && mailForm.username.trim() && mailForm.username.trim() === mail.username && mailForm.host.trim() === mail.host ? "empty keeps the password" : ""} /></label>
        <label class="pick"><input type="checkbox" bind:checked={mailForm.tls} />TLS from the start (port 465)</label>
        <div class="row">
          <Button type="submit" disabled={savingMail}>{savingMail ? "Checking the server" : "Check and save"}</Button>
          <Button kind="quiet" type="button" onclick={() => { editingMail = false; mailNote = null; }}>Cancel</Button>
        </div>
      </form>
    {:else if mail?.configured}
      <p class="mail-line"><span class="chip">{mail.from}</span><small>through {mail.host}:{mail.port}{mail.username ? ` as ${mail.username}` : ""}, {mail.tls ? "TLS" : "STARTTLS"}. Admins with a mail address hear when an app needs attention.</small></p>
    {:else}
      <p class="mail-line"><small>No mail server yet. Invites come back as links to hand over, and warnings stay in the console.</small></p>
    {/if}
  </section>

  <section class="mail">
    <div class="head">
      <h2 class="sec-title">Models</h2>
      {#if !editingModels}<Button kind="quiet" onclick={editModels}>{models?.configured ? "Change" : "Set up"}</Button>{/if}
    </div>
    {#if modelsNote}<p class="note" class:fail={modelsNote.bad} role="status">{modelsNote.text}</p>{/if}
    {#if modelsFailed}<p class="note fail" role="alert">{modelsFailed}</p>{/if}
    {#if editingModels}
      <form class="add" onsubmit={saveModels}>
        <label>Provider<select bind:value={modelsForm.provider}><option value="anthropic">Anthropic</option><option value="openai">OpenAI-compatible</option></select></label>
        <label>Address<input type="url" bind:value={modelsForm.baseUrl} required={modelsForm.provider === "openai"} placeholder={modelsForm.provider === "openai" ? "https://api.openai.com/v1" : "https://api.anthropic.com"} autocomplete="off" /></label>
        <label>Model<input bind:value={modelsForm.model} required={modelsForm.provider === "openai"} placeholder={modelsForm.provider === "openai" ? "gpt-5" : "claude-opus-5-5"} autocomplete="off" /></label>
        <label>Tokens per hour<input type="number" min="1" step="1" bind:value={modelsForm.tokensPerHour} placeholder="no limit" /><small>for each agent and app on its own</small></label>
        <label>Key<input type="password" bind:value={modelsForm.apiKey} autocomplete="new-password" placeholder={models?.configured && models.provider === modelsForm.provider && (models.baseUrl ?? "") === modelsForm.baseUrl.trim().replace(/\/$/, "") ? "empty keeps the key" : ""} /></label>
        <div class="row">
          <Button type="submit" disabled={savingModels}>{savingModels ? "Checking the key" : "Check and save"}</Button>
          <Button kind="quiet" type="button" onclick={() => { editingModels = false; modelsNote = null; }}>Cancel</Button>
        </div>
      </form>
    {:else if models?.configured}
      <p class="mail-line"><span class="chip">{models.model}</span><small>from {models.provider === "openai" ? models.baseUrl : "Anthropic"}, for every agent and app of the cell, {models.tokensPerHour ? `at most ${tokens(models.tokensPerHour)} tokens an hour each` : "without a limit"}</small></p>
    {:else}
      <p class="mail-line"><small>No model provider yet. Agent tasks wait until one is set.</small></p>
    {/if}
    {#if usageFailed}<p class="note fail" role="alert">{usageFailed}</p>{/if}
    {#if usage?.length}
      <ul class="usage" aria-label="Tokens per account since the gateway last started">
        {#each usage as a (a.account)}
          <li>
            <span class="who">{accountName(a.account)}</span>
            <span class="bar" aria-hidden="true">
              <span class="in" style:width="{(a.inputTokens / usageMax) * 100}%"></span><span class="out" style:width="{(a.outputTokens / usageMax) * 100}%"></span>
            </span>
            <small>{tokens(a.inputTokens)} in · {tokens(a.outputTokens)} out</small>
          </li>
        {/each}
      </ul>
      <small>Tokens since the model gateway last started.</small>
    {/if}
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
  .acts {
    display: flex;
    gap: 6px;
  }
  .mail-line {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 10px;
    margin: 0;
  }
  .mail,
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
  input,
  select {
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
    align-content: start;
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
  .list .result {
    display: grid;
    gap: 6px;
    grid-column: 1 / -1;
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
  .usage {
    display: grid;
    gap: 8px;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .usage li {
    display: grid;
    grid-template-columns: minmax(120px, 180px) 1fr auto;
    align-items: center;
    gap: 12px;
  }
  .usage .who {
    font-weight: 600;
    overflow-wrap: anywhere;
  }
  .usage .bar {
    display: flex;
    height: 12px;
    border-radius: 999px;
    overflow: hidden;
    background: color-mix(in srgb, var(--color-info) 10%, transparent);
  }
  .usage .in {
    background: var(--color-info);
  }
  .usage .out {
    background: var(--color-ok);
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
