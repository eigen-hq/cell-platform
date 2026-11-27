<script lang="ts">
  // The console: login, then the shell (cell, tabs, account) with the view
  // the path picks.
  import { onMount } from "svelte";
  import { finishSignIn, signIn, signOut, token } from "./lib/api/auth";
  import { api, must, type Caller } from "./lib/api/client";
  import { newest, STALE } from "./lib/newest";
  import { route } from "./lib/route";
  import { tone } from "./lib/tone.svelte";
  import Avatar from "./lib/ui/Avatar.svelte";
  import Button from "./lib/ui/Button.svelte";
  import Icon from "./lib/ui/Icon.svelte";
  import type { IconName } from "./lib/ui/icons";
  import Notice from "./lib/ui/Notice.svelte";
  import Agents from "./views/Agents.svelte";
  import Apps from "./views/Apps.svelte";
  import Backups from "./views/Backups.svelte";
  import Changes from "./views/Changes.svelte";
  import Home from "./views/Home.svelte";
  import Machines from "./views/Machines.svelte";
  import Organization from "./views/Organization.svelte";

  const cell = location.hostname.replace(/^console\./, "");

  let screen = $state<{ kind: "loading" } | { kind: "error"; message: string } | { kind: "refused"; me: Caller } | { kind: "ready"; me: Caller }>({ kind: "loading" });
  let path = $state(location.pathname);
  // The first part of the path picks the view; the rest is the view's (for
  // example /apps/<name> opens that app).
  let section = $derived(route(path).section);
  let rest = $derived(route(path).rest);
  const admin = $derived(screen.kind === "ready" && screen.me.role === "admin");

  // Counts on the tabs, from the same API the views read.
  let counts = $state<{ apps?: number; machines?: number; waiting?: number; working?: number }>({});
  const guard = newest();
  async function count() {
    try {
      const answer = await guard(
        Promise.all([api.GET("/v0/apps"), api.GET("/v0/nodes"), api.GET("/v0/changes"), admin ? api.GET("/v0/tasks") : null]),
      );
      if (answer === STALE) return;
      const [a, n, c, t] = answer;
      counts = {
        apps: must(a).length,
        machines: must(n).length,
        waiting: must(c).filter((x) => x.phase === "WaitingForApproval").length,
        working: t ? must(t).filter((x) => x.phase === "Pending" || x.phase === "Running").length : undefined,
      };
    } catch {
      counts = {}; // the view on screen says what broke; the tabs just lose their numbers
    }
  }
  $effect(() => {
    if (screen.kind !== "ready") return;
    void path; // a move to another view counts again
    count();
    const t = setInterval(count, 30000);
    return () => clearInterval(t);
  });

  type Tab = { href: string; label: string; icon: IconName; count?: number; live?: boolean };
  const tabs = $derived<Tab[]>([
    { href: "/", label: "Overview", icon: "overview" },
    { href: "/apps", label: "Apps", icon: "apps", count: counts.apps },
    { href: "/machines", label: "Machines", icon: "server", count: counts.machines },
    { href: "/backups", label: "Backups", icon: "backups" },
    { href: "/changes", label: "Changes", icon: "changes", count: counts.waiting || undefined },
    // Viewers read the state of the cell only (E-63): no agents, no people.
    ...(admin ? [{ href: "/agents", label: "Agents", icon: "agents", count: counts.working || undefined, live: !!counts.working } as Tab] : []),
  ]);

  // Leaving for the identity can fail (no login description, no network):
  // then the console says so instead of staying as it was.
  function leave(go: () => Promise<unknown>) {
    go().catch((e: unknown) => (screen = { kind: "error", message: e instanceof Error ? e.message : String(e) }));
  }

  // Addresses of earlier consoles lead to where their content lives now.
  const moved = (p: string) => p.replace(/^\/(approvals|history)(?=\/|$)/, "/changes");
  function navigate(href: string) {
    history.pushState(null, "", moved(href));
    path = location.pathname;
    menu = false;
    scrollTo(0, 0);
  }

  let menu = $state(false);
  onMount(() => {
    if (moved(location.pathname) !== location.pathname) history.replaceState(null, "", moved(location.pathname));
    path = location.pathname;
    addEventListener("popstate", () => (path = moved(location.pathname)));
    // Links inside the console stay in the page instead of loading it again.
    addEventListener("click", (e) => {
      const a = e.target instanceof Element ? e.target.closest("a") : null;
      if (!a || e.defaultPrevented || a.target || a.hasAttribute("download") || a.origin !== location.origin) return;
      if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return;
      e.preventDefault();
      navigate(a.pathname);
    });
    addEventListener("keydown", (e) => e.key === "Escape" && (menu = false));
    (async () => {
      try {
        await finishSignIn();
        path = location.pathname; // the login came back to the page it left
        if (!token()) await signIn();
        const me = must(await api.GET("/v0/whoami"));
        screen = me.role === "admin" || me.role === "viewer" ? { kind: "ready", me } : { kind: "refused", me };
      } catch (e) {
        screen = { kind: "error", message: e instanceof Error ? e.message : String(e) };
      }
    })();
  });

  const current = (href: string) => (href === "/" ? section === "/" : section === href);
</script>

<svelte:document onclick={() => (menu = false)} />

{#if screen.kind === "loading"}
  <Notice title="Signing you in" />
{:else if screen.kind === "error"}
  <Notice title="Something went wrong">
    <p>{screen.message}</p>
    <Button onclick={() => leave(signIn)}>Sign in again</Button>
  </Notice>
{:else if screen.kind === "refused"}
  <Notice title="This console is for the people who run the cell">
    <p>You are signed in as {screen.me.username}, but you are in neither cell-viewers (read) nor cell-admins (run). Ask an admin for access.</p>
    <Button kind="outline" onclick={() => leave(signOut)}>Sign out</Button>
  </Notice>
{:else}
  {@const me = screen.me}
  <header class="bar">
    <div class="in">
      <a class="cell" href="/" aria-label="{cell}, overview"><span class="dot pulse-ok" aria-hidden="true"></span>{cell}</a>
      <nav aria-label="Main">
        <div class="track">
          {#each tabs as t (t.href)}
            <a href={t.href} class:on={current(t.href)} aria-current={current(t.href) ? "page" : undefined}>
              <Icon name={t.icon} />{t.label}{#if t.live}<span class="live pulse" aria-hidden="true"></span>{/if}{#if t.count !== undefined}<span class="n" aria-hidden="true">{t.count}</span>{/if}
            </a>
          {/each}
        </div>
      </nav>
      <div class="me">
        <button class="avatar" aria-label="Account of {me.username}" aria-expanded={menu} onclick={(e) => { e.stopPropagation(); menu = !menu; }}>
          <Avatar name={me.username} />
        </button>
        {#if menu}
          <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
          <div class="menu" onclick={(e) => e.stopPropagation()}>
            <p class="who"><strong>{me.username}</strong><span>{admin ? "Admin, runs the cell" : "Viewer, reads the cell"}</span></p>
            {#if admin}
              <a href="/organization"><Icon name="users" />Organization</a>
            {/if}
            <button onclick={() => tone.toggle()}><Icon name={tone.current === "night" ? "sun" : "moon"} />{tone.current === "night" ? "Paper tone" : "Night tone"}</button>
            <button onclick={() => leave(signOut)}><Icon name="out" />Sign out</button>
          </div>
        {/if}
      </div>
    </div>
  </header>
  <main class="page" class:wide={!rest}>
    {#if !admin}
      <p class="readonly" role="status"><Icon name="eye" size={14} />You see the state of the cell: apps, changes, backups and machines. People, agents and what runs inside the apps are for admins (cell-admins), as are all changes.</p>
    {/if}
    {#if section === "/apps"}
      <Apps open={rest} onnavigate={navigate} {admin} />
    {:else if section === "/changes"}
      <Changes open={rest} onnavigate={navigate} {admin} />
    {:else if section === "/organization" && admin}
      <Organization open={rest} onnavigate={navigate} />
    {:else if section === "/agents" && admin}
      <Agents open={rest} onnavigate={navigate} />
    {:else if section === "/machines"}
      <Machines {cell} {admin} />
    {:else if section === "/backups"}
      <Backups {admin} />
    {:else}
      <Home {admin} {cell} onnavigate={navigate} />
    {/if}
  </main>
{/if}

<style>
  .in {
    max-width: var(--size-page);
    margin: 0 auto;
    padding: 0 32px;
    height: 64px;
    display: flex;
    align-items: center;
    gap: 24px;
  }
  .cell {
    flex: none;
    display: flex;
    align-items: center;
    gap: 9px;
    height: 34px;
    padding: 0 8px;
    border-radius: var(--radius-control);
    font-weight: 600;
    letter-spacing: -0.01em;
  }
  .cell:hover {
    background: var(--color-hover);
  }
  .dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--color-ok);
  }
  nav {
    display: flex;
    flex: 1;
    min-width: 0;
    overflow-x: auto;
    scrollbar-width: none;
  }
  .track {
    display: inline-flex;
    gap: 2px;
    padding: 3px;
    border-radius: var(--radius-tile);
    background: var(--color-track);
  }
  .track a {
    height: 30px;
    display: inline-flex;
    align-items: center;
    gap: 7px;
    padding: 0 12px 0 10px;
    border-radius: 9px;
    color: var(--color-text);
    font-weight: 500;
    white-space: nowrap;
  }
  .track a :global(svg) {
    opacity: 0.75;
  }
  .track a:hover {
    color: var(--color-ink);
  }
  .track a.on {
    background: var(--color-surface);
    box-shadow: var(--shadow-tab);
    color: var(--color-ink);
  }
  .track a.on :global(svg) {
    opacity: 1;
  }
  .n {
    font-size: 12px;
    font-weight: 400;
    color: var(--color-muted);
  }
  .live {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--color-agent);
  }
  .me {
    position: relative;
    flex: none;
  }
  .avatar {
    display: block;
    padding: 0;
    border: 0;
    border-radius: 50%;
    background: none;
  }
  .menu {
    position: absolute;
    z-index: 20;
    right: 0;
    top: 42px;
    width: 240px;
    display: grid;
    padding: 6px;
    border-radius: var(--radius-box);
    background: var(--color-surface);
    box-shadow: var(--shadow-menu);
  }
  .who {
    display: grid;
    margin: 0 0 4px;
    padding: 8px 10px 10px;
    border-bottom: 1px solid var(--color-hairline);
  }
  .who span {
    font-size: 12.5px;
    color: var(--color-muted);
  }
  .menu a,
  .menu button {
    display: flex;
    align-items: center;
    gap: 10px;
    height: 36px;
    padding: 0 10px;
    border: 0;
    border-radius: var(--radius-control);
    background: none;
    text-align: left;
  }
  .menu a:hover,
  .menu button:hover {
    background: var(--color-hover);
  }
  .page {
    max-width: var(--size-page);
    margin: 0 auto;
    padding: 32px 32px 64px;
  }
  .page.wide {
    padding-top: 52px;
  }
  .readonly {
    display: flex;
    align-items: center;
    gap: 9px;
    margin: -20px 0 32px;
    font-size: 12.5px;
    color: var(--color-muted);
  }
  @media (max-width: 720px) {
    .in,
    .page {
      padding-left: 16px;
      padding-right: 16px;
    }
    .in {
      gap: 12px;
    }
    .cell {
      display: none;
    }
  }
</style>
