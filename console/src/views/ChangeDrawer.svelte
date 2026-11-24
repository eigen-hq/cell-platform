<script lang="ts">
  // One change from the history in a drawer (Figma: 26 Drawer - change
  // rolled back): who asked and decided, what it did, how it ended step by
  // step, and the patch on demand.
  import { backupOf, ended, groupsOf, headline, people, safety, trail, type Change } from "../lib/change";
  import { ago } from "../lib/time";
  import Avatar from "../lib/ui/Avatar.svelte";
  import Drawer from "../lib/ui/Drawer.svelte";
  import Risk from "../lib/ui/Risk.svelte";

  let { change: c, title, onclose }: { change: Change; title: string; onclose: () => void } = $props();
  const end = $derived(ended(c));
  const groups = $derived(groupsOf(c));
  const patch = $derived(c.patch && Object.keys(c.patch).length ? JSON.stringify(c.patch, null, 2) : "");
  const when = $derived(new Date(c.finishedAt ?? c.createdAt).toLocaleString([], { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" }));
</script>

<Drawer title="Change request" subtitle="nothing happens before a yes" {onclose}>
  <article class="card">
    <p class="by">
      <Avatar name={c.requester.name} size={22} />
      <span>{people(c)}</span>
      <span class="badge {end.tone}">{end.label}</span>
      <small>{when}</small>
    </p>
    <h3><Avatar name={title} tile size={40} />{headline(c, title)}</h3>
    {#if c.message}<p class="text">{c.message}</p>{/if}
    {#if c.reason}<p class="text quiet">Why: {c.reason}</p>{/if}
    {#if c.action === "restore"}<p class="text quiet">Backup: {backupOf(c)}</p>{/if}
    {#if groups}<p class="text quiet">Open to: {groups.length ? groups.join(", ") : "everyone"}</p>{/if}
    <ul class="facts">
      <li><Risk level={c.risk} /></li>
      <li>{safety(c)}</li>
    </ul>
    {#if patch}
      <details><summary>Show the details</summary><pre>{patch}</pre></details>
    {/if}

    <div class="tear">
      <h4>{end.label}</h4>
      <p>{c.finishedAt ? `ended ${ago(c.finishedAt)}` : c.startedAt ? `started ${ago(c.startedAt)}` : `asked ${ago(c.createdAt)}`}{c.backup && c.backup !== "none" ? `, backup ${c.backup}` : ""}</p>
      <ol class="trail">
        {#each trail(c) as s (s.step)}<li class={s.state}>{s.step}{s.state === "skipped" ? ": nothing to back up" : ""}</li>{/each}
      </ol>
      {#if c.task}<a class="trail" href="/agents/{c.task}">How the agent got there</a>{/if}
      <small class="ref">{c.name}</small>
      <span class="orb" aria-hidden="true"></span>
    </div>
  </article>
</Drawer>

<style>
  .card {
    position: relative;
    display: grid;
    gap: 14px;
    overflow: hidden;
    padding: 24px 24px 0;
    border-radius: 28px;
    background: var(--slip-bg);
    color: var(--slip-ink);
  }
  .by {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px 10px;
    margin: 0;
    font-size: 15px;
  }
  .by small {
    margin-left: auto;
    font-size: 13px;
    font-weight: 600;
  }
  .badge {
    padding: 3px 10px;
    border-radius: var(--radius-pill);
    background: var(--slip-ink);
    color: var(--slip-key);
    font-size: 12.5px;
    font-weight: 700;
  }
  h3 {
    display: flex;
    gap: 14px;
    align-items: flex-start;
    margin: 0;
    font: 600 1.9rem / 1.08 var(--font-display);
    letter-spacing: -0.025em;
  }
  .text {
    margin: 0;
    font-size: 16px;
    line-height: 1.5;
  }
  .quiet {
    font-size: 14px;
    opacity: 0.8;
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
    padding: 6px 14px;
    border-radius: var(--radius-pill);
    background: rgb(255 255 255 / 0.22);
    font-size: 13.5px;
    font-weight: 700;
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
  .tear {
    position: relative;
    display: grid;
    justify-items: center;
    gap: 6px;
    margin: 8px -24px 0;
    padding: 22px 24px 24px;
    background: repeating-linear-gradient(90deg, currentColor 0 5px, transparent 5px 10px) top / 100% 2px no-repeat;
    text-align: center;
  }
  h4 {
    margin: 0;
    font: 600 1.2rem var(--font-display);
  }
  .tear p {
    margin: 0;
    font-size: 13.5px;
  }
  .trail {
    position: relative;
    z-index: 1;
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px 24px;
    width: 100%;
    margin: 10px 0 4px;
    padding: 0;
    list-style: none;
    text-align: left;
    font-size: 14px;
    font-weight: 700;
  }
  .trail li {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .trail li::before {
    content: "";
    width: 12px;
    height: 12px;
    border: 2px solid currentColor;
    border-radius: 50%;
  }
  .trail .done::before { background: currentColor; }
  .trail .failed::before { border-color: var(--brand-pastel-poppy); background: var(--brand-pastel-poppy); }
  .trail .open,
  .trail .skipped { opacity: 0.55; }
  .trail {
    position: relative;
    z-index: 1;
    font-size: 13px;
    font-weight: 700;
  }
  .ref {
    position: relative;
    z-index: 1;
    font-size: 13px;
    opacity: 0.7;
  }
  .orb {
    position: absolute;
    right: 24px;
    bottom: -90px;
    width: 156px;
    height: 156px;
    border-radius: 50%;
    background: var(--slip-orb);
  }
</style>
