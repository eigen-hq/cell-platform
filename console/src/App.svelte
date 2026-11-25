<script lang="ts">
  // The console: login, then the shell with the views the platform has.
  import { onMount } from "svelte";
  import { finishSignIn, signIn, signOut, token } from "./lib/api/auth";
  import { api, must, type Caller } from "./lib/api/client";
  import { tone } from "./lib/tone.svelte";
  import TopBar from "./lib/ui/TopBar.svelte";
  import IconButton from "./lib/ui/IconButton.svelte";
  import Avatar from "./lib/ui/Avatar.svelte";
  import Button from "./lib/ui/Button.svelte";
  import Notice from "./lib/ui/Notice.svelte";
  import { route } from "./lib/route";
  import Home from "./views/Home.svelte";
  import Machines from "./views/Machines.svelte";
  import Backups from "./views/Backups.svelte";
  import Apps from "./views/Apps.svelte";
  import Approvals from "./views/Approvals.svelte";
  import History from "./views/History.svelte";
  import Agents from "./views/Agents.svelte";
  import Organization from "./views/Organization.svelte";

  const cell = location.hostname.replace(/^console\./, "");
  const views = [
    { href: "/", label: "Home" },
    { href: "/approvals", label: "Approvals" },
    { href: "/apps", label: "Apps" },
    { href: "/machines", label: "Machines" },
    { href: "/backups", label: "Backups" },
    { href: "/organization", label: "Organization" },
    { href: "/agents", label: "Agents" },
    { href: "/history", label: "History" },
  ];

  let screen = $state<{ kind: "loading" } | { kind: "error"; message: string } | { kind: "refused"; me: Caller } | { kind: "ready"; me: Caller }>({ kind: "loading" });
  let path = $state(location.pathname);
  // The first part of the path picks the view; the rest is the view's (for
  // example /apps/<name> opens that app).
  let section = $derived(route(path).section);
  let rest = $derived(route(path).rest);

  // Leaving for the identity can fail (no login description, no network):
  // then the console says so instead of staying as it was.
  function leave(go: () => Promise<unknown>) {
    go().catch((e: unknown) => (screen = { kind: "error", message: e instanceof Error ? e.message : String(e) }));
  }

  function navigate(href: string) {
    history.pushState(null, "", href);
    path = location.pathname;
  }

  onMount(() => {
    addEventListener("popstate", () => (path = location.pathname));
    // Links inside the console stay in the page instead of loading it again.
    addEventListener("click", (e) => {
      const a = e.target instanceof Element ? e.target.closest("a") : null;
      if (!a || e.defaultPrevented || a.target || a.hasAttribute("download") || a.origin !== location.origin) return;
      if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return;
      e.preventDefault();
      navigate(a.pathname);
    });
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
</script>

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
    <Button kind="quiet" onclick={() => leave(signOut)}>Sign out</Button>
  </Notice>
{:else}
  {@const me = screen.me}
  <div class="page">
    <TopBar title="Console" subtitle={cell} items={views} current={section} onnavigate={navigate}>
      {#snippet tools()}
        <IconButton label={tone.current === "night" ? "Paper tone" : "Night tone"} onclick={() => tone.toggle()}>
          {#if tone.current === "night"}
            <svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="4" /><path d="M12 2.5v2M12 19.5v2M2.5 12h2M19.5 12h2M5.3 5.3l1.4 1.4M17.3 17.3l1.4 1.4M5.3 18.7l1.4-1.4M17.3 6.7l1.4-1.4" /></svg>
          {:else}
            <svg viewBox="0 0 24 24"><path d="M20 14.5A8 8 0 1 1 9.5 4a6.5 6.5 0 0 0 10.5 10.5z" /></svg>
          {/if}
        </IconButton>
        <button class="me" onclick={() => leave(signOut)} title="Sign out" aria-label="Sign out {me.username}">
          <span>{me.username}</span><Avatar name={me.username} />
        </button>
      {/snippet}
    </TopBar>
    {#if me.role === "viewer"}
      <p class="readonly" role="status">You can look at everything here. Changes need an admin (cell-admins).</p>
    {/if}
    {#if section === "/approvals"}
      <Approvals />
    {:else if section === "/apps"}
      <Apps open={rest} onnavigate={navigate} />
    {:else if section === "/history"}
      <History open={rest} onnavigate={navigate} />
    {:else if section === "/organization"}
      <Organization open={rest} onnavigate={navigate} />
    {:else if section === "/agents"}
      <Agents open={rest} onnavigate={navigate} />
    {:else if section === "/machines"}
      <Machines {cell} />
    {:else if section === "/backups"}
      <Backups admin={me.role === "admin"} />
    {:else}
      <Home />
    {/if}
  </div>
{/if}

<style>
  .page {
    max-width: 1240px;
    margin-inline: auto;
    padding: 14px max(16px, env(safe-area-inset-left, 0px)) 72px;
  }
  .readonly {
    margin: 12px 0 0;
    padding: 10px 16px;
    border-radius: var(--radius-part);
    background: var(--color-sunk);
    color: var(--color-muted);
    border-left: 4px solid var(--shelf-sun);
  }
  .me {
    display: flex;
    align-items: center;
    gap: 10px;
    border: 0;
    padding: 0;
    background: none;
    font-weight: 600;
  }
  @media (max-width: 600px) {
    .me span {
      display: none;
    }
  }
</style>
