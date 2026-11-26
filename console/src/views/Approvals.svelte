<script lang="ts">
  // Changes waiting for a yes (Figma: 02 Approvals): each as a ticket with
  // what happens, for whom and how safely; beside them who decides and
  // what needs a yes. Every change of an app needs an admin's approval
  // today (A-10); a policy for what may run by itself is still open.
  import { api, must } from "../lib/api/client";
  import { open, type Change } from "../lib/change";
  import type { components } from "../lib/api/schema";
  import Avatar from "../lib/ui/Avatar.svelte";
  import Risk from "../lib/ui/Risk.svelte";
  import StatStrip, { type Stat } from "../lib/ui/StatStrip.svelte";
  import Slip from "./Slip.svelte";

  type Group = components["schemas"]["Group"];

  let changes = $state<Change[] | null>(null);
  let titles = $state<Map<string, string>>(new Map());
  let { admin }: { admin: boolean } = $props();
  let admins = $state<string[]>([]);
  let failed = $state("");

  async function load() {
    try {
      // Viewers read no groups (E-63): who the admins are stays with them.
      const [c, a, k, g] = await Promise.all([api.GET("/v0/changes"), api.GET("/v0/apps"), api.GET("/v0/catalog"), admin ? api.GET("/v0/groups") : null]);
      changes = must(c);
      titles = new Map([...must(k).map((e) => [e.name, e.title] as const), ...must(a).map((x) => [x.name, x.title || x.name] as const)]);
      admins = g ? [...new Set(must(g).find((x: Group) => x.name === "cell-admins")?.members ?? [])] : [];
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

  const week = Date.now() - 7 * 86400_000;
  let waiting = $derived(changes?.filter((c) => c.phase === "WaitingForApproval") ?? []);
  let running = $derived(changes?.filter((c) => open(c) && c.phase !== "WaitingForApproval") ?? []);
  let stats = $derived.by((): Stat[] => {
    if (!changes) return [];
    const decided = changes.filter((c) => c.approval.at && new Date(c.approval.at).getTime() > week);
    const agents = waiting.filter((c) => c.requester.kind === "Agent").length;
    return [
      { label: "Waiting", value: String(waiting.length), tone: waiting.length ? "wait" : "ok" },
      { label: "From people", value: String(waiting.length - agents), tone: "ok" },
      { label: "From agents", value: String(agents), tone: "ok" },
      { label: "Under way", value: String(running.length), tone: running.length ? "wait" : "none" },
      { label: "Decided, 7 days", value: `${decided.filter((c) => c.approval.decision === "Approved").length} yes, ${decided.filter((c) => c.approval.decision === "Rejected").length} no`, tone: "none" },
    ];
  });
  const title = (app: string) => titles.get(app) ?? app;
</script>

<section class="view-head">
  <h1>Approvals</h1>
  <p>Every change to an app waits here for a yes or a no from an admin. Each one says what happens, for whom, and what protects the app while it runs.</p>
</section>

{#if failed}
  <p class="fail">{failed}</p>
{:else if changes}
  <StatStrip items={stats} />

  <div class="band">
    <section class="waiting">
      <h2 class="sec-title">Waiting <small>newest first</small></h2>
      {#each waiting as c (c.name)}
        <Slip change={c} title={title(c.app)} ondecided={load} />
      {:else}
        <div class="calm">
          <svg viewBox="0 0 120 80" aria-hidden="true">
            <path class="t" d="M10 14a8 8 0 0 1 8-8h84a8 8 0 0 1 8 8v52a8 8 0 0 1-8 8H18a8 8 0 0 1-8-8z" />
            <path class="d" d="M80 8v64" />
            <path class="k" d="M30 40l10 10 22-22" />
          </svg>
          <p><strong>Nothing waits for a yes.</strong> New requests from people and agents show up here.</p>
        </div>
      {/each}
    </section>

    <aside>
      <h2 class="sec-title">Who decides</h2>
      <div class="role">
        <div class="faces">{#each admins.slice(0, 5) as a (a)}<Avatar name={a} size={24} />{/each}</div>
        <strong>Admins</strong>
        <p>Members of cell-admins{admins.length ? ` (${admins.length})` : ""}. They approve or decline every change.</p>
      </div>

      <h2 class="sec-title">What needs a yes</h2>
      <ul class="rules">
        <li><strong>Who can open an app</strong><span class="l1"><Risk level="low" /></span><span class="who">Admin</span></li>
        <li><strong>A new version</strong><span class="l2"><Risk level="medium" /></span><span class="who">Admin</span></li>
        <li><strong>Installing, restoring, removing, other settings</strong><span class="l3"><Risk level="high" /></span><span class="who">Admin</span></li>
      </ul>
      <p class="note">Rules for what may run without a yes are not decided yet; until then every change waits here.</p>
    </aside>
  </div>
{/if}

<style>
  .band {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 360px;
    gap: 40px;
    margin-top: 44px;
  }
  .waiting {
    display: grid;
    align-content: start;
    gap: 18px;
  }
  .calm {
    display: flex;
    align-items: center;
    gap: 20px;
    padding: 26px;
    border-radius: var(--radius-panel);
    background: var(--color-panel);
  }
  .calm svg {
    flex: none;
    width: 110px;
    fill: none;
    stroke-width: 3;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .calm .t { stroke: var(--color-wait); }
  .calm .d { stroke: var(--color-wait); stroke-dasharray: 4 5; }
  .calm .k { stroke: var(--color-ok); }
  .calm p {
    margin: 0;
    color: var(--color-muted);
  }
  .calm strong {
    display: block;
    color: var(--color-ink);
    font: 600 1.25rem var(--font-display);
  }
  aside {
    display: grid;
    align-content: start;
    gap: 14px;
  }
  aside .sec-title:not(:first-child) {
    margin-top: 22px;
  }
  .role {
    display: grid;
    gap: 6px;
    padding: 16px;
    border-radius: var(--radius-part);
    background: var(--color-panel);
    outline: 2px solid color-mix(in srgb, var(--color-ok) 70%, transparent);
  }
  .faces {
    display: flex;
    gap: 4px;
  }
  .role p {
    margin: 0;
    color: var(--color-muted);
    font-size: 13.5px;
  }
  .rules {
    display: grid;
    gap: 14px;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .rules li {
    display: grid;
    grid-template-columns: 1fr auto;
    align-items: center;
    gap: 12px;
  }
  .rules li > span:not(.who) {
    grid-column: 1;
    font-size: 13px;
  }
  .rules .who {
    grid-column: 2;
    grid-row: 1 / 3;
  }
  .l1 { color: var(--color-ok); }
  .l2 { color: var(--color-wait); }
  .l3 { color: var(--color-bad); }
  .who {
    padding: 3px 10px;
    border-radius: var(--radius-pill);
    background: var(--paint-attention);
    color: var(--slip-ink);
    font-size: 12.5px;
    font-weight: 700;
  }
  .note {
    margin: 0;
    color: var(--color-muted);
    font-size: 13.5px;
  }
  @media (max-width: 1000px) {
    .band {
      grid-template-columns: 1fr;
    }
  }
</style>
