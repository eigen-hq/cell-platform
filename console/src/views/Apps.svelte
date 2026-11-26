<script lang="ts">
  // The apps of the cell (Figma: 06 Apps): what is installed, for whom, its
  // state and last backup; one app opens in a drawer (07); new apps come
  // from the catalog and wait for approval like every change.
  import { api, must } from "../lib/api/client";
  import type { components } from "../lib/api/schema";
  import { appStatus, audience } from "../lib/phase";
  import { ago } from "../lib/time";
  import Avatar from "../lib/ui/Avatar.svelte";
  import Button from "../lib/ui/Button.svelte";
  import Status from "../lib/ui/Status.svelte";
  import AppDrawer from "./AppDrawer.svelte";
  import PackageForm from "./PackageForm.svelte";

  type App = components["schemas"]["App"];
  type Entry = components["schemas"]["CatalogEntry"];
  type Change = components["schemas"]["ChangeRequest"];

  let { open, onnavigate, admin }: { open?: string; onnavigate: (href: string) => void; admin: boolean } = $props();
  let apps = $state<App[] | null>(null);
  let catalog = $state<Entry[]>([]);
  let changes = $state<Change[]>([]);
  let failed = $state("");
  // An install on its way (the request) or refused (why), by catalog name.
  // Installs waiting for approval come from the change log instead.
  let asked = $state<Record<string, { text: string; bad?: boolean }>>({});

  async function load() {
    try {
      const [a, c, ch] = await Promise.all([api.GET("/v0/apps"), api.GET("/v0/catalog"), api.GET("/v0/changes")]);
      apps = must(a).sort((x, y) => (x.title || x.name).localeCompare(y.title || y.name));
      catalog = must(c);
      changes = must(ch);
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

  async function install(e: Entry) {
    asked[e.name] = { text: "asking" };
    try {
      must(await api.POST("/v0/apps", { body: { catalog: e.name, reason: "installed from the console" } }));
      await load();
      delete asked[e.name];
    } catch (err) {
      asked[e.name] = { text: err instanceof Error ? err.message : String(err), bad: true };
    }
  }

  const host = (url?: string) => (url ? url.replace(/^https?:\/\//, "").replace(/\/$/, "") : "");
  let down = $derived(apps?.filter((a) => a.phase === "NeedsAttention").length ?? 0);
  let installed = $derived(new Set(apps?.map((a) => a.name)));
  let offer = $derived(catalog.filter((e) => !installed.has(e.name)));
  let current = $derived(apps?.find((a) => a.name === open));
  // Installs not done yet, by app name: what the card says instead of Install.
  let underway = $derived(
    new Map(
      changes
        .filter((c) => c.action === "install" && ["", "Pending", "WaitingForApproval", "Running"].includes(c.phase))
        .map((c) => [c.app, c.phase === "WaitingForApproval" ? "waits for approval" : "installing"]),
    ),
  );
</script>

<section class="view-head">
  <h1>Apps</h1>
  <p>Everything that runs in your cell, and who it is for.</p>
</section>

{#if failed}
  <p class="fail">{failed}</p>
{:else if apps}
  <section>
    <h2 class="sec-title">Installed <small>{apps.length} {apps.length === 1 ? "app" : "apps"}{down ? `, ${down} down` : ""}</small></h2>
    {#if apps.length}
      <div class="table" role="table" aria-label="Installed apps">
        <div class="row head" role="row">
          <span role="columnheader">App</span><span role="columnheader">For</span><span role="columnheader">Status</span>
          <span role="columnheader">Last backup</span><span role="columnheader">Address</span><span></span>
        </div>
        {#each apps as a (a.name)}
          {@const s = appStatus(a.phase)}
          <a class="row" role="row" href="/apps/{a.name}" onclick={(e) => { if (!e.metaKey && !e.ctrlKey) { e.preventDefault(); onnavigate(`/apps/${a.name}`); } }}>
            <span class="app" role="cell">
              <Avatar name={a.title || a.name} tile size={40} />
              <span><strong>{a.title || a.name}</strong><small>{a.name} {a.runningVersion || a.version}</small></span>
            </span>
            <span class="for" role="cell">
              {#each audience(a) as g (g)}<span class="chip">{g}</span>{/each}
            </span>
            <span role="cell"><Status tone={s.tone} label={s.label} /></span>
            <span class="muted" role="cell">{ago(a.lastBackup?.at)}</span>
            <span class="muted addr" role="cell">{host(a.url)}</span>
            <svg class="chev" viewBox="0 0 24 24" aria-hidden="true"><path d="M9 6l6 6-6 6" /></svg>
          </a>
        {/each}
      </div>
    {:else}
      <p class="empty">Nothing installed yet. Pick a first app from the catalog below.</p>
    {/if}
  </section>

  <section class="add">
    <h2 class="sec-title">Add an app <small>from the catalog; it runs once approved</small></h2>
    {#if offer.length}
      <ul class="cards">
        {#each offer as e (e.name)}
          <li class="card">
            <div class="who">
              <Avatar name={e.title} tile size={40} />
              <span><strong>{e.title}</strong><small>{e.name} {e.version}</small></span>
            </div>
            <p>{e.auth === "oidc" ? "Signs in with the cell account" : e.auth === "proxy" ? "Behind the sign-in of the cell" : "Opens without sign-in"}</p>
            <div class="foot">
              <span class="chip">{e.source === "helm" ? "chart" : "image"}</span>
              {#if underway.has(e.name)}
                <small>{underway.get(e.name)}</small>
              {:else if asked[e.name] && !asked[e.name]!.bad}
                <small>{asked[e.name]!.text}</small>
              {:else}
                <Button kind="quiet" onclick={() => install(e)}>{asked[e.name] ? "Try again" : "Install"}</Button>
              {/if}
            </div>
            {#if asked[e.name]?.bad}<small class="fail">{asked[e.name]!.text}</small>{/if}
          </li>
        {/each}
      </ul>
    {:else}
      <p class="empty">Every app of the catalog is installed.</p>
    {/if}
  </section>

  <section>
    <h2 class="sec-title">From an open-source project <small>any project with a Compose file; Packer turns it into an app</small></h2>
    <PackageForm taken={installed} />
  </section>

  {#if current}
    <AppDrawer app={current} {admin} onclose={() => onnavigate("/apps")} onchange={load} />
  {/if}
{/if}

<style>
  section + section {
    margin-top: 48px;
  }
  .table {
    display: grid;
    grid-template-columns: minmax(200px, 1.4fr) minmax(120px, 1.2fr) 150px 150px minmax(120px, 1fr) 20px;
  }
  .row {
    display: grid;
    grid-column: 1 / -1;
    grid-template-columns: subgrid;
    align-items: center;
    gap: 16px;
    padding: 10px 12px;
    border-radius: var(--radius-part);
    color: inherit;
    text-decoration: none;
  }
  a.row:hover {
    background: var(--color-panel);
  }
  .row.head {
    padding-block: 0 6px;
    color: var(--color-muted);
    font-size: 13px;
    font-weight: 600;
  }
  .app,
  .who {
    display: flex;
    align-items: center;
    gap: 12px;
    min-width: 0;
  }
  .app strong,
  .who strong {
    display: block;
    font-weight: 600;
  }
  .app small,
  .who small {
    display: block;
    color: var(--color-muted);
    font-size: 13px;
  }
  .for {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .muted {
    color: var(--color-muted);
    font-size: 13.5px;
  }
  .addr {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .chev {
    width: 18px;
    height: 18px;
    fill: none;
    stroke: var(--color-dim);
    stroke-width: 2;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .empty {
    color: var(--color-muted);
  }
  .cards {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(230px, 1fr));
    gap: 12px;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .card {
    display: grid;
    gap: 12px;
    padding: 16px;
    border-radius: var(--radius-panel);
    background: var(--color-panel);
  }
  .card p {
    margin: 0;
    color: var(--color-muted);
    font-size: 13.5px;
  }
  .foot {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    min-height: 44px;
  }
  .foot small {
    color: var(--color-wait);
    font-weight: 600;
  }
  .card > .fail {
    font-size: 13px;
    font-weight: 600;
  }
  @media (max-width: 900px) {
    .table {
      grid-template-columns: 1fr auto 20px;
    }
    .row > :nth-child(2),
    .row > :nth-child(4),
    .row > :nth-child(5) {
      display: none;
    }
  }
</style>
