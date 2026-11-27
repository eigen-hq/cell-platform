<script lang="ts">
  // The apps of the cell: what is installed, its state, where it runs and
  // its last backup; /apps/<name> opens one app's page. New apps come from
  // the catalog or a Compose file and wait for approval like every change.
  import { api, must } from "../lib/api/client";
  import type { components } from "../lib/api/schema";
  import { appState, isUp } from "../lib/home";
  import { follow } from "../lib/live";
  import { newest, STALE } from "../lib/newest";
  import { itemPath } from "../lib/route";
  import { ago } from "../lib/time";
  import Button from "../lib/ui/Button.svelte";
  import Drawer from "../lib/ui/Drawer.svelte";
  import Icon from "../lib/ui/Icon.svelte";
  import Logo from "../lib/ui/Logo.svelte";
  import AppPage from "./AppPage.svelte";
  import PackageForm from "./PackageForm.svelte";

  type App = components["schemas"]["App"];
  type Entry = components["schemas"]["CatalogEntry"];
  type Change = components["schemas"]["ChangeRequest"];
  type Node = components["schemas"]["Node"];

  let { open, admin }: { open?: string; onnavigate: (href: string) => void; admin: boolean } = $props();
  let apps = $state<App[] | null>(null);
  let nodes = $state<Node[]>([]);
  let catalog = $state<Entry[]>([]);
  let changes = $state<Change[]>([]);
  let failed = $state("");
  let adding = $state(false);
  // An install on its way (the request) or refused (why), by catalog name.
  // Installs waiting for approval come from the change log instead.
  let asked = $state<Record<string, { text: string; bad?: boolean }>>({});

  const guard = newest();
  async function load() {
    try {
      const answer = await guard(
        Promise.all([
          api.GET("/v0/apps"),
          api.GET("/v0/nodes"),
          // The catalog and the installs under way only feed "Add an app": admins only (E-63).
          admin ? api.GET("/v0/catalog") : null,
          admin ? api.GET("/v0/changes") : null,
        ]),
      );
      if (answer === STALE) return;
      const [a, n, c, ch] = answer;
      apps = must(a).sort((x, y) => (x.title || x.name).localeCompare(y.title || y.name));
      nodes = must(n);
      catalog = c ? must(c) : [];
      changes = ch ? must(ch) : [];
      failed = "";
    } catch (e) {
      failed = e instanceof Error ? e.message : String(e);
    }
  }

  async function install(e: Entry) {
    asked[e.name] = { text: "Asking" };
    try {
      must(await api.POST("/v0/apps", { body: { catalog: e.name, reason: "installed from the console" } }));
      await load();
      delete asked[e.name];
    } catch (err) {
      asked[e.name] = { text: err instanceof Error ? err.message : String(err), bad: true };
    }
  }

  const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;
  const host = (url?: string) => (url ? url.replace(/^https?:\/\//, "").replace(/\/$/, "") : "");
  // The machines with a pod of the app: the API knows it per machine.
  const where = (name: string) => nodes.filter((n) => n.apps.includes(name)).map((n) => n.name).sort();
  const tone = (a: App) => ({ ok: "ok", bad: "warn", wait: "neutral", none: "neutral" })[appState(a).tone];

  let current = $derived(apps?.find((a) => a.name === open));
  let installed = $derived(new Set(apps?.map((a) => a.name)));
  let offer = $derived(catalog.filter((e) => !installed.has(e.name)));
  let up = $derived(apps?.filter(isUp).length ?? 0);
  // Installs not done yet, by app name: what the row says instead of Install.
  let underway = $derived(
    new Map(
      changes
        .filter((c) => c.action === "install" && ["", "Pending", "WaitingForApproval", "Running"].includes(c.phase))
        .map((c) => [c.app, c.phase === "WaitingForApproval" ? "Waits for approval" : "Installing"]),
    ),
  );

  // Live events reload at once (a burst counts once); the poll covers a
  // stream that is away, quickly while an app or an install moves.
  const moving = $derived(underway.size > 0 || (apps?.some((a) => a.phase !== "Ready" && a.phase !== "NeedsAttention") ?? false));
  $effect(() => {
    load();
    const t = setInterval(load, moving ? 5000 : 15000);
    return () => clearInterval(t);
  });
  $effect(() => {
    let soon: ReturnType<typeof setTimeout> | undefined;
    const stop = follow(
      (signal) => api.GET("/v0/events", { parseAs: "stream", signal }),
      () => {
        clearTimeout(soon);
        soon = setTimeout(load, 300);
      },
      () => {},
    );
    return () => {
      clearTimeout(soon);
      stop();
    };
  });
</script>

{#if failed}<p class="fail" role="alert">{failed}</p>{/if}

{#if open}
  {#if current}
    {#key current.name}
      <AppPage app={current} {admin} machines={where(current.name)} offered={catalog.find((e) => e.name === current?.name)?.version ?? ""} onchange={load} />
    {/key}
  {:else if apps}
    <nav class="crumb" aria-label="Breadcrumb"><a href="/apps">Apps</a><span aria-hidden="true">/</span><span>{open}</span></nav>
    <p class="empty plain">There is no app named {open} in this cell. It may have just been removed. <a class="go" href="/apps">All apps<Icon name="right" size={13} stroke={2} /></a></p>
  {:else if !failed}
    <p class="empty plain">Reading the apps</p>
  {/if}
{:else}
  <header class="page-head">
    <div>
      <h1>Apps</h1>
      <p>
        {#if apps}
          {apps.length ? `${plural(apps.length, "app")}, ${up} of them ${up === 1 ? "answers" : "answer"}.` : "Nothing installed yet."}
          Each one runs from its manifest; every change to it waits for approval.
        {:else}
          Everything that runs in your cell.
        {/if}
      </p>
    </div>
    {#if admin}<Button size="lg" onclick={() => (adding = true)}><Icon name="plus" size={15} stroke={2} />Add an app</Button>{/if}
  </header>

  {#if apps}
    <section class="sec" aria-labelledby="h-installed">
      <div class="sec-head">
        <h2 id="h-installed">Installed</h2>
        <span>{plural(apps.length, "app")} on {plural(nodes.length, "machine")}</span>
      </div>
      {#if apps.length}
        <ul class="list" aria-label="Installed apps">
          {#each apps as a (a.name)}
            {@const s = appState(a)}
            {@const on = where(a.name)}
            <li>
              <a class="app row" href={itemPath("/apps", a.name)}>
                <Logo app={a} title={a.title || a.name} size={36} />
                <span class="who">
                  <strong>{a.title || a.name}</strong>
                  <small class="mono">{a.name} {a.runningVersion || a.version}</small>
                </span>
                <span class="state {tone(a)}"><span class="dot" class:pulse-ok={tone(a) === "ok"} aria-hidden="true"></span>{s.label}</span>
                <span class="meta"><Icon name="server" size={13} stroke={2} />{on.length ? on.join(", ") : "no machine yet"}</span>
                <span class="meta"><Icon name="backups" size={13} stroke={2} />{a.lastBackup ? `backed up ${ago(a.lastBackup.at)}` : "no backup yet"}</span>
                <span class="meta addr mono">{host(a.url)}</span>
                <Icon name="right" size={14} stroke={2} />
              </a>
            </li>
          {/each}
        </ul>
      {:else}
        <p class="empty plain">Nothing installed yet.{admin ? " Add a first app from the catalog or a Compose file." : ""}</p>
      {/if}
    </section>
  {:else if !failed}
    <p class="empty plain">Reading the apps</p>
  {/if}

  <!-- Adding an app is a write: viewers see what runs, not the catalog (E-63). -->
  {#if admin && adding}
    <Drawer title="Add an app" subtitle="Every install waits for approval, like every change." onclose={() => (adding = false)}>
      <section class="part" aria-labelledby="h-catalog">
        <h3 id="h-catalog">From the catalog</h3>
        {#if offer.length}
          <ul class="list">
            {#each offer as e (e.name)}
              <li class="entry">
                <Logo app={{ name: e.name, spec: e.manifest }} title={e.title} size={32} />
                <span class="who">
                  <strong>{e.title}</strong>
                  <small>{e.auth === "oidc" ? "Signs in with the cell account" : e.auth === "proxy" ? "Behind the sign-in of the cell" : "Opens without sign-in"}, <span class="mono">{e.version}</span></small>
                </span>
                {#if underway.has(e.name)}
                  <small class="busy">{underway.get(e.name)}</small>
                {:else if asked[e.name] && !asked[e.name]!.bad}
                  <small class="busy">{asked[e.name]!.text}</small>
                {:else}
                  <Button kind="outline" size="sm" onclick={() => install(e)}>{asked[e.name] ? "Try again" : "Install"}</Button>
                {/if}
                {#if asked[e.name]?.bad}<p class="fail" role="alert">{asked[e.name]!.text}</p>{/if}
              </li>
            {/each}
          </ul>
        {:else}
          <p class="empty plain">Every app of the catalog is installed.</p>
        {/if}
      </section>
      <section class="part" aria-labelledby="h-package">
        <h3 id="h-package">From a Compose file or a manifest</h3>
        <p class="hint">Packer turns a Compose file into an app; a manifest installs as written.</p>
        <PackageForm taken={installed} />
      </section>
    </Drawer>
  {/if}
{/if}

<style>
  .crumb {
    display: flex;
    gap: 8px;
    font-size: 13px;
    color: var(--color-muted);
  }
  .crumb a:hover {
    color: var(--color-ink);
  }
  .crumb span:last-child {
    color: var(--color-ink);
  }
  .empty.plain {
    padding: 0;
    margin-top: 24px;
  }
  .empty .go {
    margin-left: 8px;
  }
  .app {
    display: grid;
    grid-template-columns: auto minmax(160px, 1.4fr) 150px minmax(110px, 1fr) minmax(150px, 1fr) minmax(0, 1fr) auto;
    align-items: center;
    gap: 16px;
    padding: 14px 20px;
    color: var(--color-muted);
  }
  .who {
    min-width: 0;
  }
  .who strong,
  .who small {
    display: block;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .who strong {
    font-weight: 500;
    color: var(--color-ink);
  }
  .who small {
    font-size: 12px;
    color: var(--color-muted);
  }
  .state {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
    color: var(--color-text);
  }
  .state .dot {
    flex: none;
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--color-muted);
  }
  .state.ok .dot {
    background: var(--color-ok);
  }
  .state.warn {
    color: var(--color-warn-ink);
  }
  .state.warn .dot {
    background: var(--color-warn);
  }
  .meta {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
    font-size: 12.5px;
    color: var(--color-text);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .addr {
    display: block;
    color: var(--color-muted);
    font-size: 12px;
  }
  .part h3 {
    margin: 0 0 10px;
    font-size: 15px;
    font-weight: 500;
  }
  .hint {
    margin: -4px 0 14px;
    font-size: 13px;
    color: var(--color-text);
  }
  .entry {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 12px;
    padding: 12px 16px;
  }
  .entry .who {
    flex: 1;
  }
  .entry .fail {
    flex-basis: 100%;
    margin: 0;
  }
  .busy {
    font-size: 12.5px;
    color: var(--color-warn-ink);
  }
  @media (max-width: 860px) {
    .app {
      grid-template-columns: auto minmax(0, 1fr) auto;
      gap: 6px 14px;
    }
    .app > :global(svg) {
      display: none;
    }
    .app .meta {
      grid-column: 2 / -1;
    }
    .app .addr {
      display: none;
    }
  }
</style>
