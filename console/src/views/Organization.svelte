<script lang="ts">
  // People, groups and agent accounts, the system mail and the model
  // provider. Apps are attached to groups; everyone in a group gets them.
  // Groups are flat today (departments and teams are still open in the
  // backlog). One group opens under /organization/<group> to change its
  // members; every other form opens in a drawer.
  import { api, must } from "../lib/api/client";
  import type { components } from "../lib/api/schema";
  import { authMode } from "../lib/phase";
  import { itemPath } from "../lib/route";
  import { newest, STALE } from "../lib/newest";
  import { replaced } from "../lib/draft";
  import Avatar from "../lib/ui/Avatar.svelte";
  import Button from "../lib/ui/Button.svelte";
  import Drawer from "../lib/ui/Drawer.svelte";
  import { agentIcon } from "../lib/ui/icons";
  import Logo from "../lib/ui/Logo.svelte";
  import Tag from "../lib/ui/Tag.svelte";
  import Tile from "../lib/ui/Tile.svelte";
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
  const usageGuard = newest();
  async function loadUsage() {
    try {
      const answer = await usageGuard(api.GET("/v0/models/usage"));
      if (answer === STALE) return;
      usage = must(answer).accounts;
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
  let creatingGroup = $state(false);
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
      creatingGroup = false;
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

  // Removing a person asks once more in their row; it cannot be undone.
  let removing = $state<string | null>(null);
  async function remove(u: User) {
    if (minting) return;
    minting = true;
    token = null;
    tokenError = "";
    personNote = null;
    about = u.username;
    try {
      must(await api.DELETE("/v0/users/{name}", { params: { path: { name: u.username } } }));
      removing = null;
      about = null;
      personNote = { text: `${u.name || u.username} is removed: their account, sessions and app passwords are gone.` };
      await load();
    } catch (err) {
      removing = null;
      tokenError = `${u.username}: ${msg(err)}`;
    } finally {
      minting = false;
    }
  }

  // System mail: invites and warnings to the admins go out through this server.
  let editingMail = $state(false);
  let mailForm = $state({ host: "", port: "", username: "", password: "", from: "", tls: false });
  let mailNote = $state<{ text: string; bad?: boolean } | null>(null);
  let savingMail = $state(false);
  function editMail() {
    // A relay replaces the own server: nothing of it to keep.
    const m = mail?.own ? null : mail;
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

  // The cell's own mail server (E-84): no outside service, but receivers
  // take its mail only with the DNS records below and port 25 open.
  let usingOwn = $state(false);
  async function useOwnMail() {
    if (usingOwn) return;
    usingOwn = true;
    mailNote = null;
    try {
      mail = must(await api.PUT("/v0/mail", { body: { own: true } }));
      mailNote = { text: "The cell sends its own mail now." };
    } catch (err) {
      mailNote = { text: msg(err), bad: true };
    } finally {
      usingOwn = false;
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
  let admins = $derived(new Set(groups.find((g) => g.name === ADMINS)?.members ?? []).size);
  const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;
  let lede = $derived(
    `${plural(people.length, "person", "people")} in ${plural(teams.length, "group")}, ${plural(admins, "admin")}. Apps are attached to groups; everyone in a group gets them.`,
  );
  // The credential on screen, copied on request.
  let copied = $state("");
  async function copy(value: string) {
    try {
      await navigator.clipboard.writeText(value);
      copied = "Copied";
    } catch {
      copied = "Copy failed";
    }
    setTimeout(() => (copied = ""), 1600);
  }
  const name = (u: string) => users?.find((x) => x.username === u)?.name || u;
  const title = (a: App) => a.title || a.name;
  let current = $derived(teams.find((g) => g.name === open));
</script>

<!-- A failed poll keeps what is on screen, open edits included. -->
{#if failed}<p class="fail" role="alert">{failed}</p>{/if}
{#if users}
  <header class="page-head">
    <div>
      <h1>Organization</h1>
      <p>{lede}</p>
    </div>
  </header>

  <section class="sec" aria-labelledby="h-people">
    <div class="sec-head">
      <h2 id="h-people">People</h2>
      <span>{people.length}</span>
      <span class="end"><Button kind="outline" size="sm" onclick={() => { adding = true; personNote = null; }}>Add a person</Button></span>
    </div>
    {#if row === null && !adding}{@render results()}{/if}
    {#snippet results()}
      {#if personNote}<p class="note" class:fail={personNote.bad} role="status">{personNote.text}</p>{/if}
      {#if token}
        <div class="token" role="status">
          <p><strong>{token.title}</strong>, {token.text}</p>
          <div class="copy">
            <code class="mono">{token.value}</code>
            <Button kind="outline" size="sm" onclick={() => copy(token!.value)}>{copied || "Copy"}</Button>
            <Button kind="plain" size="sm" onclick={() => (token = null)}>Done</Button>
          </div>
        </div>
      {/if}
      {#if tokenError}<p class="fail" role="alert">{tokenError}</p>{/if}
    {/snippet}
    <ul class="list">
      {#each people as u (u.username)}
        <li>
          <div class="person row">
            <Avatar name={u.name || u.username} size={30} />
            <span class="who"><strong>{u.name || u.username}</strong><small>{u.username}{u.email ? `, ${u.email}` : ""}</small></span>
            <span class="in">{#each [...new Set(u.groups)] as g (g)}<Tag>{g}</Tag>{/each}</span>
            <span class="acts">
              {#if removing === u.username}
                <small>Remove {u.name || u.username} for good?</small>
                <Button kind="warn" size="sm" disabled={minting} onclick={() => remove(u)}>Remove</Button>
                <Button kind="plain" size="sm" disabled={minting} onclick={() => (removing = null)}>Keep</Button>
              {:else}
                <Button kind="plain" size="sm" disabled={minting} onclick={() => invite(u)}>Invite</Button>
                <Button kind="plain" size="sm" disabled={minting} onclick={() => appPassword(u)}>App password</Button>
                <Button kind="plain" size="sm" disabled={minting} onclick={() => (removing = u.username)}>Remove</Button>
              {/if}
            </span>
          </div>
          {#if row === u.username}<div class="result">{@render results()}</div>{/if}
        </li>
      {:else}
        <li class="empty">No person yet.</li>
      {/each}
    </ul>
  </section>

  <section class="sec" aria-labelledby="h-groups">
    <div class="sec-head">
      <h2 id="h-groups">Groups</h2>
      <span>{teams.length}</span>
      <span class="end"><Button kind="outline" size="sm" onclick={() => { creatingGroup = true; groupNote = null; }}>New group</Button></span>
    </div>
    {#if groupNote && !creatingGroup}<p class="note" class:fail={groupNote.bad} role="status">{groupNote.text}</p>{/if}
    <ul class="list" aria-label="Who gets which apps">
      <li>
        <div class="group">
          <Tile icon="users" size={30} />
          <span class="who"><strong>Everyone</strong><small>{plural(people.length, "person", "people")}</small></span>
          <span class="faces"></span>
          <span class="apps">
            {#each byGroup.get("") ?? [] as a (a.name)}<Logo app={a} title={title(a)} size={24} />{:else}<small>no app yet</small>{/each}
          </span>
        </div>
      </li>
      {#each teams as g (g.name)}
        {@const members = [...new Set(g.members)]}
        <li>
          <a class="group row" href={itemPath("/organization", g.name)}>
            <Tile icon={g.name === ADMINS ? "key" : "users"} size={30} />
            <span class="who"><strong>{g.name}</strong><small>{plural(members.length, "person", "people")}</small></span>
            <span class="faces">{#each members.slice(0, 4) as m (m)}<Avatar name={name(m)} size={22} />{/each}{#if members.length > 4}<small>+{members.length - 4}</small>{/if}</span>
            <span class="apps">
              {#each byGroup.get(g.name) ?? [] as a (a.name)}<Logo app={a} title={title(a)} size={24} />{/each}
              {#if g.name === ADMINS}<Tag>runs the console</Tag>{/if}
            </span>
          </a>
        </li>
      {/each}
    </ul>
  </section>

  {#if agentAccounts.length}
    <section class="sec" aria-labelledby="h-agents">
      <div class="sec-head">
        <h2 id="h-agents">Agent accounts</h2>
        <span>{agentAccounts.length === 1 ? "one account that works here too" : `${agentAccounts.length} accounts that work here too`}</span>
      </div>
      <ul class="list">
        {#each agentAccounts as a (a.username)}
          {@const short = a.username.replace(/^agent-/, "")}
          <li class="agent">
            <Tile icon={agentIcon(short)} tone="agent" size={28} />
            <span class="who"><strong>{a.name || short}</strong><small class="mono">{a.username}</small></span>
          </li>
        {/each}
      </ul>
    </section>
  {/if}

  <section class="sec" aria-labelledby="h-mail">
    <div class="sec-head">
      <h2 id="h-mail">System mail</h2>
      <span>invites and warnings to the admins</span>
      <span class="end">
        {#if mail?.ownServer && !mail.own}<Button kind="outline" size="sm" disabled={usingOwn} onclick={useOwnMail}>{usingOwn ? "Checking delivery" : "Use the cell's own server"}</Button>{/if}
        <Button kind="outline" size="sm" onclick={editMail}>{mail?.configured && !mail.own ? "Change" : mail?.ownServer ? "Use another server" : "Set up"}</Button>
      </span>
    </div>
    {#if mailNote && !editingMail}<p class="note" class:fail={mailNote.bad} role="status">{mailNote.text}</p>{/if}
    {#if mailFailed}<p class="fail" role="alert">{mailFailed}</p>{/if}
    <div class="panel">
      {#if mail?.own}
        <p class="line"><Tile icon="mail" tone="ok" size={28} /><span><Tag mono>{mail.from}</Tag> from the cell's own mail server, straight to each receiver. Admins with a mail address hear when an app needs attention.</span></p>
      {:else if mail?.configured}
        <p class="line"><Tile icon="mail" tone="ok" size={28} /><span><Tag mono>{mail.from}</Tag> through {mail.host}:{mail.port}{mail.username ? ` as ${mail.username}` : ""}, {mail.tls ? "TLS" : "STARTTLS"}. Admins with a mail address hear when an app needs attention.</span></p>
      {:else}
        <p class="line"><Tile icon="mail" size={28} /><span>No mail server yet. Invites come back as links to hand over, and warnings stay in the console.</span></p>
      {/if}
      {#if mail?.ownServer}
        {#if mail.own}
          <p class="hint">Keep these records: without them receivers file the cell's mail as spam.</p>
        {:else}
          <ol class="mail-steps">
            <li>Ask your hosting provider to allow outgoing mail on port 25. Many block it for new servers until asked.</li>
            <li>Set the PTR record below at the hosting provider, in the server's network settings.</li>
            <li>Add the other records below in your DNS. Each shows Set once DNS answers with it.</li>
            <li>Press <em>Use the cell's own server</em>. The cell first checks that it reaches your people's mail servers and changes nothing if it does not.</li>
          </ol>
        {/if}
        <ul class="records">
          {#each mail.ownServer.records as r (r.type + r.name)}
            <li>
              <Tag tone={r.set ? "ok" : "warn"}>{r.set ? "Set" : "Missing"}</Tag>
              <code class="mono">{r.type}</code>
              <code class="mono name">{r.name}</code>
              <code class="mono value">{r.value}</code>
            </li>
          {/each}
        </ul>
      {/if}
    </div>
  </section>

  <section class="sec" aria-labelledby="h-models">
    <div class="sec-head">
      <h2 id="h-models">Models</h2>
      <span>the provider every agent and app of the cell uses</span>
      <span class="end"><Button kind="outline" size="sm" onclick={editModels}>{models?.configured ? "Change" : "Set up"}</Button></span>
    </div>
    {#if modelsNote && !editingModels}<p class="note" class:fail={modelsNote.bad} role="status">{modelsNote.text}</p>{/if}
    {#if modelsFailed}<p class="fail" role="alert">{modelsFailed}</p>{/if}
    <div class="panel">
      {#if models?.configured}
        <p class="line"><Tile icon="spark" tone="agent" size={28} /><span><Tag mono>{models.model}</Tag> from {models.provider === "openai" ? models.baseUrl : "Anthropic"}, for every agent and app of the cell, {models.tokensPerHour ? `at most ${tokens(models.tokensPerHour)} tokens an hour each` : "without a limit"}.</span></p>
      {:else}
        <p class="line"><Tile icon="spark" size={28} /><span>No model provider yet. Agent tasks wait until one is set.</span></p>
      {/if}
      {#if usageFailed}<p class="fail" role="alert">{usageFailed}</p>{/if}
      {#if usage?.length}
        <ul class="usage" aria-label="Tokens per account since the gateway last started">
          {#each usage as a (a.account)}
            <li>
              <span class="acct">{accountName(a.account)}</span>
              <span class="bar" aria-hidden="true">
                <span class="tin" style:width="{(a.inputTokens / usageMax) * 100}%"></span><span class="tout" style:width="{(a.outputTokens / usageMax) * 100}%"></span>
              </span>
              <small class="mono">{tokens(a.inputTokens)} in, {tokens(a.outputTokens)} out</small>
            </li>
          {/each}
        </ul>
        <p class="hint">Tokens since the model gateway last started.</p>
      {/if}
    </div>
  </section>

  {#if adding}
    <Drawer title="Add a person" subtitle="Without a first password the person gets an invite." onclose={() => (adding = false)}>
      <form class="form" onsubmit={addPerson}>
        {#if personNote?.bad}<p class="fail" role="alert">{personNote.text}</p>{/if}
        <label class="field">Username<input bind:value={person.username} required pattern="[a-z0-9][a-z0-9._\-]{'{1,62}'}" autocomplete="off" /></label>
        <label class="field">Name<input bind:value={person.name} required autocomplete="off" /></label>
        <label class="field">Email<input type="email" bind:value={person.email} autocomplete="off" /></label>
        <label class="field">First password<input type="password" bind:value={person.password} autocomplete="new-password" placeholder="empty: send an invite" /></label>
        <fieldset>
          <legend>Groups</legend>
          {#each teams as g (g.name)}
            <label class="pick"><input type="checkbox" bind:group={person.groups} value={g.name} />{g.name}</label>
          {/each}
        </fieldset>
        <div class="buttons"><Button type="submit" disabled={minting}>Add</Button><Button kind="plain" onclick={() => (adding = false)}>Cancel</Button></div>
      </form>
    </Drawer>
  {/if}

  {#if creatingGroup}
    <Drawer title="New group" subtitle="Open it afterwards to add people." onclose={() => (creatingGroup = false)}>
      <form class="form" onsubmit={addGroup}>
        {#if groupNote?.bad}<p class="fail" role="alert">{groupNote.text}</p>{/if}
        <label class="field">Name<input bind:value={newGroup} placeholder="e.g. studio" pattern="[a-z0-9][a-z0-9._\-]{'{1,62}'}" required autocomplete="off" /><small>lowercase, digits, . _ -</small></label>
        <div class="buttons"><Button type="submit">Add group</Button><Button kind="plain" onclick={() => (creatingGroup = false)}>Cancel</Button></div>
      </form>
    </Drawer>
  {/if}

  {#if editingMail}
    <Drawer title="System mail" onclose={() => { editingMail = false; mailNote = null; }}>
      <form class="form" onsubmit={saveMail}>
        {#if mailNote?.bad}<p class="fail" role="alert">{mailNote.text}</p>{/if}
        <label class="field">Server<input bind:value={mailForm.host} required placeholder="smtp.example.com" autocomplete="off" /></label>
        <label class="field">Port<input type="number" min="1" max="65535" bind:value={mailForm.port} placeholder={mailForm.tls ? "465" : "587"} /></label>
        <label class="field">Sender<input type="email" bind:value={mailForm.from} required placeholder="cell@example.com" autocomplete="off" /></label>
        <label class="field">Login<input bind:value={mailForm.username} autocomplete="off" /></label>
        <label class="field">Password<input type="password" bind:value={mailForm.password} autocomplete="new-password" placeholder={mail?.configured && mailForm.username.trim() && mailForm.username.trim() === mail.username && mailForm.host.trim() === mail.host ? "empty keeps the password" : ""} /></label>
        <label class="pick"><input type="checkbox" bind:checked={mailForm.tls} />TLS from the start (port 465)</label>
        <div class="buttons">
          <Button type="submit" disabled={savingMail}>{savingMail ? "Checking the server" : "Check and save"}</Button>
          <Button kind="plain" onclick={() => { editingMail = false; mailNote = null; }}>Cancel</Button>
        </div>
      </form>
    </Drawer>
  {/if}

  {#if editingModels}
    <Drawer title="Models" onclose={() => { editingModels = false; modelsNote = null; }}>
      <form class="form" onsubmit={saveModels}>
        {#if modelsNote?.bad}<p class="fail" role="alert">{modelsNote.text}</p>{/if}
        <label class="field">Provider<select bind:value={modelsForm.provider}><option value="anthropic">Anthropic</option><option value="openai">OpenAI-compatible</option></select></label>
        <label class="field">Address<input type="url" bind:value={modelsForm.baseUrl} required={modelsForm.provider === "openai"} placeholder={modelsForm.provider === "openai" ? "https://api.openai.com/v1" : "https://api.anthropic.com"} autocomplete="off" /></label>
        <label class="field">Model<input bind:value={modelsForm.model} required={modelsForm.provider === "openai"} placeholder={modelsForm.provider === "openai" ? "gpt-5" : "claude-opus-5-5"} autocomplete="off" /></label>
        <label class="field">Tokens per hour<input type="number" min="1" step="1" bind:value={modelsForm.tokensPerHour} placeholder="no limit" /><small>for each agent and app on its own</small></label>
        <label class="field">Key<input type="password" bind:value={modelsForm.apiKey} autocomplete="new-password" placeholder={models?.configured && models.provider === modelsForm.provider && (models.baseUrl ?? "") === modelsForm.baseUrl.trim().replace(/\/$/, "") ? "empty keeps the key" : ""} /></label>
        <div class="buttons">
          <Button type="submit" disabled={savingModels}>{savingModels ? "Checking the key" : "Check and save"}</Button>
          <Button kind="plain" onclick={() => { editingModels = false; modelsNote = null; }}>Cancel</Button>
        </div>
      </form>
    </Drawer>
  {/if}

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
{:else if !failed}
  <p class="empty plain">Reading the organization</p>
{/if}

<style>
  .empty.plain {
    padding: 0;
  }
  .note {
    margin: 0 0 12px;
    padding: 10px 14px;
    border-radius: var(--radius-control);
    background: var(--color-ok-tile);
    color: var(--color-ink);
  }
  .note.fail {
    background: var(--color-warn-bg);
    color: var(--color-warn-text);
  }
  .who {
    flex: 1;
    min-width: 0;
  }
  .who strong,
  .who small {
    display: block;
  }
  .who strong {
    font-weight: 500;
  }
  .who small {
    font-size: 12.5px;
    color: var(--color-text);
    overflow-wrap: anywhere;
  }
  .person,
  .group,
  .agent {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 12px 20px;
  }
  .in,
  .apps,
  .faces {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px;
  }
  .in {
    flex: 0 1 auto;
    justify-content: flex-end;
  }
  .acts {
    flex: none;
    display: flex;
    align-items: center;
    gap: 2px;
  }
  .acts small {
    margin-right: 8px;
    color: var(--color-warn-text);
  }
  .result {
    padding: 0 20px 14px 64px;
  }
  .token {
    margin: 0 0 12px;
    padding: 14px 16px;
    border-radius: var(--radius-box);
    background: var(--color-surface);
    border: 1px solid var(--color-line);
  }
  .result .token {
    margin: 0;
  }
  .token p {
    margin: 0;
    color: var(--color-text);
  }
  .token strong {
    font-weight: 500;
    color: var(--color-ink);
  }
  .copy {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    margin-top: 10px;
  }
  .copy code {
    flex: 1 1 260px;
    min-width: 0;
    padding: 8px 12px;
    border-radius: var(--radius-control);
    background: var(--color-surface-2);
    border: 1px solid var(--color-hairline);
    font-size: 12.5px;
    overflow-wrap: anywhere;
    user-select: all;
  }
  .group .faces {
    flex: 0 0 140px;
    gap: 0;
  }
  .group .faces > :global(*) + :global(*) {
    margin-left: -6px;
  }
  .group .faces small,
  .apps small {
    margin-left: 8px;
    font-size: 12px;
    color: var(--color-muted);
  }
  .group .apps {
    flex: 0 1 280px;
    justify-content: flex-end;
  }
  .panel {
    padding: 18px 20px;
    border-radius: var(--radius-panel);
    background: var(--color-surface-2);
    border: 1px solid var(--color-line);
  }
  .line {
    display: flex;
    align-items: flex-start;
    gap: 12px;
    margin: 0;
    color: var(--color-text);
  }
  .line > span {
    padding-top: 3px;
  }
  .hint {
    margin: 14px 0 0;
    font-size: 12.5px;
    color: var(--color-muted);
  }
  .mail-steps {
    margin: 16px 0 0;
    padding-left: 20px;
    color: var(--color-text);
  }
  .mail-steps li + li {
    margin-top: 4px;
  }
  .records {
    margin: 14px 0 0;
    padding: 0;
    list-style: none;
    border-top: 1px solid var(--color-hairline);
  }
  .records li {
    display: grid;
    grid-template-columns: 76px 56px minmax(0, 1fr) minmax(0, 2fr);
    align-items: center;
    gap: 12px;
    padding: 8px 0;
    border-bottom: 1px solid var(--color-hairline);
    font-size: 12.5px;
  }
  .records code {
    overflow-wrap: anywhere;
  }
  .records .value {
    color: var(--color-text);
  }
  .usage {
    display: grid;
    gap: 10px;
    margin: 18px 0 0;
    padding: 0;
    list-style: none;
  }
  .usage li {
    display: grid;
    grid-template-columns: 160px minmax(0, 1fr) auto;
    align-items: center;
    gap: 14px;
    font-size: 13px;
  }
  .acct {
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .bar {
    display: flex;
    height: 6px;
    border-radius: 3px;
    background: var(--color-track);
    overflow: hidden;
  }
  .tin {
    background: var(--color-agent);
  }
  .tout {
    background: var(--color-agent-edge);
  }
  .usage small {
    font-size: 12px;
    color: var(--color-muted);
  }
  .form {
    display: grid;
    gap: 16px;
  }
  .field small {
    color: var(--color-muted);
  }
  fieldset {
    display: flex;
    flex-wrap: wrap;
    gap: 8px 16px;
    margin: 0;
    padding: 12px 14px;
    border: 1px solid var(--color-line);
    border-radius: var(--radius-control);
  }
  legend {
    padding: 0 4px;
    font-size: 13px;
    color: var(--color-text);
  }
  .pick {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    font-size: 13.5px;
  }
  .buttons {
    display: flex;
    gap: 8px;
    margin-top: 4px;
  }
  @media (max-width: 860px) {
    .person {
      flex-wrap: wrap;
    }
    .in {
      justify-content: flex-start;
    }
    .acts {
      flex-basis: 100%;
      flex-wrap: wrap;
      margin-left: 30px;
    }
    .result {
      padding-left: 20px;
    }
    .group {
      flex-wrap: wrap;
    }
    .group .faces {
      flex-basis: auto;
    }
    .records li {
      grid-template-columns: 76px 1fr;
    }
    .records .name,
    .records .value {
      grid-column: 1 / -1;
    }
    .usage li {
      grid-template-columns: minmax(0, 1fr) auto;
    }
    .usage .bar {
      grid-column: 1 / -1;
      grid-row: 2;
    }
  }
</style>
