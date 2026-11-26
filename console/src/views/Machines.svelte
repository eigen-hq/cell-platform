<script lang="ts">
  // The machines of the cell (Figma: 16 Cell): key figures, how the machines
  // link up and how people reach them, what runs where, and adding one more
  // (E-44). Shows only what the API delivers.
  import { onDestroy, onMount } from "svelte";
  import { api, must } from "../lib/api/client";
  import type { components } from "../lib/api/schema";
  import Avatar from "../lib/ui/Avatar.svelte";
  import Button from "../lib/ui/Button.svelte";
  import CommandBox from "../lib/ui/CommandBox.svelte";
  import Meter from "../lib/ui/Meter.svelte";
  import StatStrip, { type Stat } from "../lib/ui/StatStrip.svelte";

  type Node = components["schemas"]["Node"];
  type Join = components["schemas"]["Join"];

  let { cell, admin }: { cell: string; admin: boolean } = $props();
  let nodes = $state<Node[] | null>(null);
  let failed = $state("");
  let join = $state<Join | null>(null);
  let joinError = $state("");
  let timer: ReturnType<typeof setInterval> | undefined;

  async function load() {
    try {
      nodes = must(await api.GET("/v0/nodes"));
      failed = "";
    } catch (e) {
      failed = e instanceof Error ? e.message : String(e);
    }
  }

  async function addMachine() {
    joinError = "";
    try {
      join = must(await api.POST("/v0/nodes/joins"));
    } catch (e) {
      joinError = e instanceof Error ? e.message : String(e);
    }
  }

  onMount(() => {
    load();
    // New machines show up while the join command runs.
    timer = setInterval(load, 5000);
  });
  onDestroy(() => clearInterval(timer));

  const gb = (b: number) => {
    const v = b / 2 ** 30;
    return v >= 1000 ? `${(v / 1024).toFixed(2)} TB` : `${v < 10 ? v.toFixed(1) : Math.round(v)} GB`;
  };
  const num = (v: number) => (v < 10 ? v.toFixed(1) : String(Math.round(v)));
  const pct = (used: number | undefined, size: number) => (used === undefined || size === 0 ? null : used / size);
  const until = (t: string) => new Date(t).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  const short = (name: string) => name.replace(/^lima-|^k3d-/, "");

  let server = $derived(nodes?.find((m) => m.role === "server"));
  let agents = $derived(nodes?.filter((m) => m.role !== "server") ?? []);
  let stats = $derived.by((): Stat[] => {
    if (!nodes) return [];
    const up = nodes.filter((m) => m.ready).length;
    const cores = nodes.reduce((s, m) => s + m.cpu, 0);
    const memory = nodes.reduce((s, m) => s + m.memory, 0);
    const measured = nodes.every((m) => m.cpuUsed !== undefined);
    const usedCores = nodes.reduce((s, m) => s + (m.cpuUsed ?? 0), 0);
    const usedMemory = nodes.reduce((s, m) => s + (m.memoryUsed ?? 0), 0);
    const disks = nodes.filter((m) => m.diskUsed !== undefined).map((m) => ({ m, p: m.diskUsed! / m.disk }));
    const fullest = disks.sort((a, b) => b.p - a.p)[0];
    return [
      { label: "Machines", value: `${up} of ${nodes.length} up`, tone: up === nodes.length ? "ok" : "bad" },
      { label: "Links", value: nodes.length > 1 ? "direct, encrypted" : "one machine", tone: "ok" },
      { label: "Processor", value: measured ? `${num(usedCores)} of ${cores} cores` : `${cores} cores`, tone: measured && usedCores / cores >= 0.8 ? "wait" : "ok" },
      { label: "Memory", value: measured ? `${gb(usedMemory)} of ${gb(memory)}` : gb(memory), tone: measured && usedMemory / memory >= 0.8 ? "wait" : "ok" },
      fullest
        ? { label: "Fullest disk", value: `${short(fullest.m.name)}, ${Math.round(fullest.p * 100)}%`, tone: fullest.p >= 0.8 ? "wait" : "ok" }
        : { label: "Disk", value: gb(nodes.reduce((s, m) => s + m.disk, 0)), tone: "none" },
    ];
  });
</script>

{#snippet box(m: Node)}
  <div class="mbox" class:down={!m.ready}>
    <div class="mname"><span class="sq"></span>{short(m.name)}</div>
    <div class="mspec">{m.cpu} cores, {gb(m.memory)}, {gb(m.disk)}</div>
    <div class="minis">
      {#each [["cpu", pct(m.cpuUsed, m.cpu)], ["mem", pct(m.memoryUsed, m.memory)], ["disk", pct(m.diskUsed, m.disk)]] as [label, p] (label)}
        <div class="mini">
          <span class="track"><span class:full={p !== null && Number(p) >= 0.8} style:width="{p === null ? 0 : Math.max(4, Number(p) * 100)}%"></span></span>
          <span>{label} {p === null ? "–" : `${Math.round(Number(p) * 100)}%`}</span>
        </div>
      {/each}
    </div>
  </div>
{/snippet}

<section class="view-head">
  <h1>Machines</h1>
  <p>
    {#if nodes}
      {nodes.length === 1 ? "One cell on one machine." : `One cell on ${nodes.length} machines, linked directly and encrypted.`}
    {/if}
    Where each app runs, how loaded the hardware is, and how to add more.
  </p>
</section>

{#if failed}
  <p class="error">{failed}</p>
{:else if nodes && server}
  <StatStrip items={stats} />

  <section class="map">
    <div class="cap">
      <span>Machines and links</span>
      <span class="legend"><i class="people"></i>people <i class="mesh"></i>between machines, WireGuard</span>
    </div>
    <div class="topo" >
      <div class="who">
        <strong>People</strong>
        <small>open *.{cell}</small>
      </div>
      <div class="wire people"><span>https</span></div>
      {@render box(server)}
      {#if agents.length}
        <div class="wire mesh"><span>encrypted</span></div>
        <div class="agents" class:many={agents.length > 1}>
          {#each agents as m (m.name)}
            <div class="agent">{@render box(m)}</div>
          {/each}
        </div>
      {:else}
        <div class="wire mesh ghostwire"></div>
        <div class="ghost">room for the next machine</div>
      {/if}
    </div>
  </section>

  <section class="where">
    <h2 class="sec-title">What runs where <small>each machine, its load and its apps</small></h2>
    <ul class="cards">
      {#each nodes as m (m.name)}
        <li>
          <div class="top">
            <div>
              <strong>{short(m.name)}</strong>
              <span class="sub">{m.address} · {m.role === "server" ? "runs the cell" : "runs apps"}</span>
            </div>
            <span class="stat" class:bad={!m.ready}><span class="led"></span>{m.ready ? "Up" : "Away"}</span>
          </div>
          <div class="kit">
            {#each m.apps as app (app)}<span title={app}><Avatar name={app} size={24} tile /></span>{/each}
            {#if m.system}<span class="chip">cell services</span>{/if}
            {#if !m.system && m.apps.length === 0}<span class="chip quiet">no apps yet</span>{/if}
          </div>
          <Meter label="Processor" value={m.cpuUsed === undefined ? `${m.cpu} cores` : `${num(m.cpuUsed)} / ${m.cpu} cores`} part={pct(m.cpuUsed, m.cpu)} />
          <Meter label="Memory" value={m.memoryUsed === undefined ? gb(m.memory) : `${gb(m.memoryUsed)} / ${gb(m.memory)}`} part={pct(m.memoryUsed, m.memory)} />
          <Meter label="Disk" value={m.diskUsed === undefined ? gb(m.disk) : `${gb(m.diskUsed)} / ${gb(m.disk)}`} part={pct(m.diskUsed, m.disk)} />
        </li>
      {/each}
    </ul>
  </section>

  <section class="band">
    <div class="card reach">
      <h2 class="sec-title">How it is reached</h2>
      <ul>
        <li><span class="ico people"><svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="8.5" /><path d="M3.5 12h17M12 3.5c2.6 2.4 3.8 5.2 3.8 8.5s-1.2 6.1-3.8 8.5c-2.6-2.4-3.8-5.2-3.8-8.5S9.4 5.9 12 3.5z" /></svg></span><div><strong>Apps and console</strong><small>https://*.{cell}, through {short(server.name)}</small></div></li>
        <li><span class="ico mesh"><svg viewBox="0 0 24 24"><rect x="3" y="4" width="7" height="6" rx="1.5" /><rect x="14" y="4" width="7" height="6" rx="1.5" /><rect x="8.5" y="15" width="7" height="6" rx="1.5" /><path d="M10 7h4M6.5 10l3.5 5M17.5 10 14 15" /></svg></span><div><strong>Between the machines</strong><small>WireGuard, machine to machine, no hub in between</small></div></li>
        <li><span class="ico soon"><svg viewBox="0 0 24 24"><path d="M4 20V8l6-4v16M10 20V10h10v10M2.5 20h19M13 13h1.5M16.5 13H18M13 16.5h1.5M16.5 16.5H18M6.5 10H7.5M6.5 13.5H7.5" /></svg></span><div><strong>Company network</strong><small>private access through the cell's own mesh, coming next</small></div></li>
      </ul>
    </div>

    <!-- A join token is for admins only (E-63). -->
    {#if admin}
    <div class="card grow">
      <div class="pic" aria-hidden="true">
        {#each nodes.slice(0, 3) as m (m.name)}<span class="chip">{short(m.name)}</span>{/each}
        {#if nodes.length > 3}<span class="chip">+{nodes.length - 3}</span>{/if}
        <span class="dash"></span>
        <span class="plus">+1</span>
      </div>
      {#if join}
        <h3>Add a machine</h3>
        <p>Run this as root on an Ubuntu machine that can reach {short(server.name)}. It joins, links up and shows up above. Works until {until(join.expiresAt)}.</p>
        <CommandBox command={join.command} />
        <Button kind="quiet" onclick={() => (join = null)}>Done</Button>
      {:else}
        <strong class="big">+1</strong>
        <p>More room for apps, on a machine you choose. It joins with one command and links up by itself.</p>
        <Button onclick={addMachine}><span class="plusicon" aria-hidden="true">+</span>Add machine</Button>
        {#if joinError}<p class="error">{joinError}</p>{/if}
      {/if}
    </div>
    {/if}
  </section>
{/if}

<style>

  /* map */
  .map {
    position: relative;
    margin-top: 44px;
    padding: 16px 20px 28px;
    border-radius: var(--radius-panel);
    background: var(--color-panel);
  }
  .map::before {
    content: "";
    position: absolute;
    z-index: -1;
    top: -60px;
    right: 6%;
    width: 480px;
    height: 220px;
    border-radius: 50%;
    background: var(--paint-glow);
    filter: blur(40px);
    opacity: 0.45;
  }
  .cap {
    display: flex;
    flex-wrap: wrap;
    justify-content: space-between;
    gap: 8px;
    color: var(--color-muted);
    font-size: 13px;
    font-weight: 600;
  }
  .legend {
    display: flex;
    align-items: center;
    gap: 8px;
    color: var(--color-dim);
    font-weight: 400;
  }
  .legend i {
    display: inline-block;
    width: 14px;
    height: 2px;
    margin-left: 6px;
  }
  .legend .people { background: var(--color-ok); }
  .legend .mesh { background: var(--color-info); }
  .topo {
    display: grid;
    grid-template-columns: auto minmax(70px, 1fr) auto minmax(70px, 1fr) auto;
    align-items: center;
    margin-top: 34px;
  }
  .ghost {
    padding: 22px 18px;
    border: 2px dashed color-mix(in srgb, var(--color-info) 55%, transparent);
    border-radius: 12px;
    color: var(--color-dim);
    font-size: 12.6px;
    font-weight: 600;
  }
  .ghostwire {
    opacity: 0.45;
  }
  .who {
    display: grid;
    gap: 2px;
    padding: 10px 14px;
    border-radius: 12px;
    background: var(--color-sunk);
    font-size: 12.6px;
  }
  .who small,
  .mspec {
    color: var(--color-dim);
    font-size: 11.5px;
    font-weight: 500;
  }
  .wire {
    position: relative;
    height: 2px;
    background-size: 10px 2px;
    animation: flow 1.6s linear infinite;
  }
  .wire.people { background-image: linear-gradient(90deg, var(--color-ok) 0 5px, transparent 5px); }
  .wire.mesh { background-image: linear-gradient(90deg, var(--color-info) 0 5px, transparent 5px); }
  .wire span {
    position: absolute;
    bottom: 7px;
    left: 50%;
    translate: -50%;
    color: var(--color-muted);
    font-size: 11.5px;
    font-weight: 600;
    white-space: nowrap;
  }
  @keyframes flow {
    to { background-position: 10px 0; }
  }
  .mbox {
    display: grid;
    gap: 3px;
    padding: 11px 13px 10px;
    border-radius: 12px;
    background: var(--color-sunk);
  }
  .mname {
    display: flex;
    align-items: center;
    gap: 7px;
    font-size: 12.6px;
    font-weight: 600;
  }
  .sq {
    width: 7px;
    height: 7px;
    border-radius: 2px;
    background: var(--color-ok);
  }
  .down .sq { background: var(--color-bad); }
  .minis {
    display: flex;
    gap: 12px;
    margin-top: 6px;
  }
  .mini {
    display: grid;
    gap: 5px;
    color: var(--color-muted);
    font-size: 11.5px;
    font-weight: 600;
    white-space: nowrap;
  }
  .track {
    width: 56px;
    height: 5px;
    border-radius: 3px;
    background: color-mix(in srgb, var(--color-ink) 16%, transparent);
    overflow: hidden;
  }
  .track span {
    display: block;
    height: 100%;
    background: var(--color-info);
  }
  .track span.full { background: var(--color-wait); }
  .agents {
    display: grid;
    gap: 14px;
  }
  .agents.many {
    position: relative;
    padding-left: 22px;
  }
  /* one spine from the link to every further machine */
  .agents.many::before {
    content: "";
    position: absolute;
    top: 36px;
    bottom: 36px;
    left: 0;
    width: 2px;
    background: linear-gradient(var(--color-info) 0 5px, transparent 5px) 0 0 / 2px 10px;
  }
  .agents.many .agent {
    position: relative;
  }
  .agents.many .agent::before {
    content: "";
    position: absolute;
    top: 50%;
    left: -22px;
    width: 22px;
    height: 2px;
    background: linear-gradient(90deg, var(--color-info) 0 5px, transparent 5px) 0 0 / 10px 2px;
  }

  /* what runs where */
  .where {
    margin-top: 52px;
  }
  .cards {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
    gap: 12px;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .cards li {
    display: grid;
    gap: 14px;
    padding: 16px 16px 20px;
    border-radius: var(--radius-panel);
    background: var(--color-panel);
  }
  .top {
    display: flex;
    justify-content: space-between;
    gap: 12px;
  }
  .top strong {
    display: block;
    font-size: 16px;
    font-weight: 600;
  }
  .sub {
    color: var(--color-dim);
    font-size: 13px;
  }
  .stat {
    display: flex;
    align-items: center;
    gap: 6px;
    height: 20px;
    color: var(--color-ok);
    font-size: 13px;
    font-weight: 700;
  }
  .stat .led {
    width: 9px;
    height: 9px;
    border-radius: 50%;
    background: currentColor;
  }
  .stat.bad { color: var(--color-bad); }
  .kit {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px;
    min-height: 28px;
  }
  .chip {
    padding: 5px 11px;
    border-radius: var(--radius-pill);
    background: color-mix(in srgb, var(--color-ink) 11%, transparent);
    color: var(--color-muted);
    font-size: 13px;
    font-weight: 600;
  }
  .chip.quiet {
    color: var(--color-dim);
    font-weight: 500;
  }

  /* bottom band */
  .band {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
    margin-top: 52px;
  }
  .band > :only-child {
    grid-column: 1 / -1;
  }
  .card {
    display: grid;
    align-content: start;
    justify-items: start;
    gap: 12px;
    padding: 18px;
    border-radius: var(--radius-panel);
    background: var(--color-panel);
  }
  .reach ul {
    display: grid;
    gap: 18px;
    margin: 4px 0 0;
    padding: 0;
    list-style: none;
  }
  .reach li {
    display: flex;
    gap: 14px;
    align-items: center;
  }
  .reach li div {
    display: grid;
  }
  .reach small {
    color: var(--color-dim);
    font-size: 13px;
  }
  .ico {
    display: grid;
    place-items: center;
    flex: none;
    width: 40px;
    height: 40px;
    border-radius: 12px;
    background: color-mix(in srgb, var(--color-ok) 18%, transparent);
    color: var(--color-ok);
  }
  .ico svg {
    width: 22px;
    height: 22px;
    fill: none;
    stroke: currentColor;
    stroke-width: 1.6;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .ico.mesh {
    background: color-mix(in srgb, var(--color-info) 18%, transparent);
    color: var(--color-info);
  }
  .ico.soon {
    background: transparent;
    outline: 1.5px dashed var(--color-dim);
    outline-offset: -1.5px;
    color: var(--color-dim);
  }
  .grow .pic {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 18px 16px;
    border-radius: 16px;
    background: var(--color-sunk);
  }
  .pic .dash {
    flex: 1;
    height: 2px;
    background: linear-gradient(90deg, var(--color-wait) 0 6px, transparent 6px) 0 0 / 11px 2px;
    animation: flow 1.6s linear infinite;
  }
  .pic .plus {
    color: var(--color-wait);
    font-weight: 700;
  }
  .big {
    font: 600 2rem / 1 var(--font-display);
  }
  .grow p,
  .grow h3 {
    margin: 0;
  }
  .grow p {
    max-width: 46ch;
    color: var(--color-muted);
  }
  .grow h3 {
    font: 600 1.25rem var(--font-display);
  }
  .grow :global(.box) {
    width: 100%;
  }
  .plusicon {
    display: grid;
    place-items: center;
    width: 22px;
    height: 22px;
    border-radius: 7px;
    background: var(--avatar-lavender);
    color: var(--avatar-ink);
    font-weight: 700;
  }
  .error {
    color: var(--color-bad);
  }
  @media (max-width: 900px) {
    .topo {
      grid-template-columns: 1fr;
      justify-items: start;
      gap: 0;
    }
    .wire {
      width: 2px;
      height: 34px;
      margin-left: 24px;
      background-size: 2px 10px;
    }
    .wire.people { background-image: linear-gradient(var(--color-ok) 0 5px, transparent 5px); }
    .wire.mesh { background-image: linear-gradient(var(--color-info) 0 5px, transparent 5px); }
    .wire span {
      bottom: auto;
      top: 50%;
      left: 12px;
      translate: 0 -50%;
    }
    .band {
      grid-template-columns: 1fr;
    }
  }
</style>
