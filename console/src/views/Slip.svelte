<script lang="ts">
  // One change waiting for a yes, as a ticket (Figma: 02 Approvals, cr.slip):
  // who asked, what happens to which app and for whom, why, how risky and
  // what protects it; the stub holds the decision and the steps that follow.
  import { api, must } from "../lib/api/client";
  import { backupOf, groupsOf, headline, safety, steps, type Change } from "../lib/change";
  import { ago } from "../lib/time";
  import Avatar from "../lib/ui/Avatar.svelte";
  import Risk from "../lib/ui/Risk.svelte";

  // Only admins decide (E-63): for anyone else the stub shows the plan alone.
  let { change: c, title, admin, ondecided }: { change: Change; title: string; admin: boolean; ondecided: () => void } = $props();
  let busy = $state(false);
  let declining = $state(false);
  let failed = $state("");

  async function decide(verb: "approve" | "reject") {
    busy = true;
    failed = "";
    try {
      const path = { params: { path: { name: c.name } } };
      must(await (verb === "approve" ? api.POST("/v0/changes/{name}/approve", path) : api.POST("/v0/changes/{name}/reject", path)));
      ondecided();
    } catch (e) {
      failed = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
      declining = false;
    }
  }

  const agent = $derived(c.requester.kind === "Agent");
  const who = $derived(agent ? `${c.requester.name[0]!.toUpperCase()}${c.requester.name.slice(1)}` : c.requester.name);
  const groups = $derived(groupsOf(c));
  const verb = $derived({ install: "install", update: "change", restore: "bring back", remove: c.deleteData ? "remove, delete data" : "remove" }[c.action]);
  const patch = $derived(c.patch && Object.keys(c.patch).length ? JSON.stringify(c.patch, null, 2) : "");
</script>

<article class="slip" aria-label={headline(c, title)}>
  <div class="main">
    <p class="by">
      <Avatar name={who} size={22} />
      <span><strong>{who}</strong> {agent ? "proposes this" : "asked for this"}</span>
      <small>{ago(c.createdAt)}</small>
    </p>
    <h3><Avatar name={title} tile size={40} />{headline(c, title)}</h3>

    <div class="plan">
      <div class="line">
        <span class="stn"><Avatar name={who} size={24} /><span><b>{who}</b><small>{agent ? "agent" : "person"}</small></span></span>
        <span class="wire"><small>{verb}</small></span>
        <span class="stn" class:new={c.action === "install"}><Avatar name={title} tile size={24} /><span><b>{title}</b><small>{c.app}</small></span></span>
      </div>
      {#if c.action === "restore"}
        <div class="line">
          <span class="wire"><small>back to</small></span>
          <span class="stn small"><b>{backupOf(c)}</b></span>
        </div>
      {/if}
      {#if groups}
        <div class="line">
          <span class="wire"><small>open to</small></span>
          {#each groups.length ? groups : ["everyone"] as g (g)}<span class="stn small"><b>{g}</b></span>{/each}
        </div>
      {/if}
    </div>

    {#if c.reason}<p class="why">{c.reason}</p>{/if}

    <ul class="facts">
      <li><Risk level={c.risk} /></li>
      <li>
        <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3l7 3v5c0 4.5-3 8.2-7 10-4-1.8-7-5.5-7-10V6z" /></svg>
        {safety(c)}
      </li>
    </ul>
    {#if patch}
      <details>
        <summary>Show the details</summary>
        <pre>{patch}</pre>
      </details>
    {/if}
    <span class="orb" aria-hidden="true"></span>
  </div>

  <div class="stub">
    {#if admin && declining}
      <button class="key" disabled={busy} onclick={() => decide("reject")}>Yes, decline</button>
      <button class="ghost" disabled={busy} onclick={() => (declining = false)}>Keep it</button>
    {:else if admin}
      <button class="key" disabled={busy} onclick={() => decide("approve")}>
        <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5 12.5l4.5 4.5L19 7.5" /></svg>Approve
      </button>
      <button class="ghost" disabled={busy} onclick={() => (declining = true)}>Decline</button>
    {/if}
    {#if failed}<p class="failed" role="alert">{failed}</p>{/if}
    <ol class="steps" aria-label="What happens after a yes">
      {#each steps(c) as s (s)}<li>{s}</li>{/each}
    </ol>
    {#if c.task && admin}<a class="trail" href="/agents/{c.task}">How the agent got there</a>{/if}
    <small class="ref">{c.name}</small>
  </div>
</article>

<style>
  .slip {
    --cut: calc(100% - 234px);
    position: relative;
    display: grid;
    grid-template-columns: 1fr 234px;
    overflow: hidden;
    border-radius: 28px;
    background: var(--slip-bg);
    color: var(--slip-ink);
    /* the two notches where the stub tears off */
    mask-image: radial-gradient(circle 13px at var(--cut) 0, #0000 97%, #000), radial-gradient(circle 13px at var(--cut) 100%, #0000 97%, #000);
    mask-composite: intersect;
  }
  .main {
    position: relative;
    display: grid;
    align-content: start;
    gap: 18px;
    padding: 24px 28px 26px;
  }
  .by {
    display: flex;
    align-items: center;
    gap: 9px;
    margin: 0;
    font-size: 15px;
  }
  .by small {
    margin-left: auto;
    font-size: 13px;
    font-weight: 600;
  }
  h3 {
    display: flex;
    align-items: flex-start;
    gap: 14px;
    margin: 0;
    max-width: 22ch;
    font: 600 clamp(1.6rem, 2.6vw, 2.05rem) / 1.06 var(--font-display);
    letter-spacing: -0.025em;
    text-wrap: balance;
  }
  h3 :global(.face) {
    margin-top: 2px;
  }
  .plan {
    display: grid;
    gap: 12px;
    padding: 16px;
    border-radius: 12px;
    background: rgb(255 255 255 / 0.3);
  }
  .line {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 0;
  }
  .stn {
    display: inline-flex;
    align-items: center;
    gap: 9px;
    padding: 8px 12px 8px 9px;
    border-radius: 12px;
    background: rgb(255 255 255 / 0.56);
    font-size: 13.5px;
  }
  .stn.small {
    margin-right: 6px;
    padding: 6px 12px;
  }
  .stn.new {
    outline: 2px dashed currentColor;
    outline-offset: -2px;
  }
  .stn b,
  .stn small {
    display: block;
    line-height: 1.25;
  }
  .stn small {
    opacity: 0.68;
    font-size: 13px;
  }
  .wire {
    display: inline-grid;
    min-width: 74px;
    padding: 0 8px 8px;
    background: repeating-linear-gradient(90deg, currentColor 0 6px, transparent 6px 10px) bottom / 100% 2px no-repeat;
    font-size: 13px;
    text-align: center;
    opacity: 0.85;
  }
  .why {
    margin: 0;
    max-width: 60ch;
    font-size: 16px;
    line-height: 1.5;
  }
  .facts {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .facts li {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    padding: 6px 14px 6px 10px;
    border-radius: var(--radius-pill);
    background: rgb(255 255 255 / 0.22);
    font-size: 13.5px;
    font-weight: 700;
  }
  .facts svg {
    width: 15px;
    height: 15px;
    fill: none;
    stroke: currentColor;
    stroke-width: 2;
    stroke-linejoin: round;
  }
  details {
    font-size: 13px;
    font-weight: 600;
  }
  summary {
    cursor: pointer;
  }
  pre {
    overflow: auto;
    max-height: 260px;
    margin: 10px 0 0;
    padding: 12px;
    border-radius: 12px;
    background: rgb(255 255 255 / 0.4);
    font: 12.5px/1.5 ui-monospace, SFMono-Regular, Menlo, monospace;
  }
  .main > :not(.orb) {
    position: relative;
    z-index: 1;
  }
  .orb {
    position: absolute;
    z-index: 0;
    right: -40px;
    bottom: -84px;
    width: 156px;
    height: 156px;
    border-radius: 50%;
    background: var(--slip-orb);
    pointer-events: none;
  }
  .stub {
    display: grid;
    align-content: center;
    gap: 10px;
    padding: 24px 22px;
    background: repeating-linear-gradient(180deg, currentColor 0 5px, transparent 5px 10px) left / 2px 100% no-repeat;
  }
  .stub button {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 8px;
    min-height: 48px;
    border-radius: var(--radius-pill);
    font-size: 15.5px;
    font-weight: 700;
  }
  .stub button:disabled {
    opacity: 0.6;
    cursor: progress;
  }
  .key {
    border: 0;
    background: var(--slip-ink);
    color: var(--slip-key) !important;
  }
  .key svg {
    width: 16px;
    height: 16px;
    fill: none;
    stroke: currentColor;
    stroke-width: 2.4;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .ghost {
    border: 2px solid currentColor;
    background: none;
    color: var(--slip-ink) !important;
  }
  .failed {
    margin: 0;
    font-size: 13px;
    font-weight: 700;
  }
  .steps {
    display: grid;
    gap: 12px;
    margin: 12px 0 0;
    padding: 0;
    list-style: none;
    font-size: 13.5px;
    font-weight: 700;
    opacity: 0.55;
  }
  .steps li {
    display: flex;
    align-items: center;
    gap: 9px;
  }
  .steps li::before {
    content: "";
    width: 10px;
    height: 10px;
    border: 2px solid currentColor;
    border-radius: 50%;
  }
  .trail {
    font-size: 13px;
    font-weight: 700;
  }
  .ref {
    font-size: 13px;
    opacity: 0.7;
  }
  @media (max-width: 760px) {
    .slip {
      grid-template-columns: 1fr;
      mask-image: none;
    }
    .stub {
      background: repeating-linear-gradient(90deg, currentColor 0 5px, transparent 5px 10px) top / 100% 2px no-repeat;
    }
  }
</style>
