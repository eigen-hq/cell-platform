<script lang="ts">
  // The machines of the cell: each one with its load, its apps and its
  // facts, a few key figures, and adding one more (E-44). Shows only what
  // the API delivers.
  import { api, must } from "../lib/api/client";
  import type { components } from "../lib/api/schema";
  import { follow } from "../lib/live";
  import { newest, STALE } from "../lib/newest";
  import { itemPath } from "../lib/route";
  import { machineName } from "../lib/text";
  import { ago } from "../lib/time";
  import Button from "../lib/ui/Button.svelte";
  import Drawer from "../lib/ui/Drawer.svelte";
  import Icon from "../lib/ui/Icon.svelte";
  import type { IconName } from "../lib/ui/icons";
  import Logo from "../lib/ui/Logo.svelte";
  import Tag from "../lib/ui/Tag.svelte";
  import Spans from "../lib/ui/Spans.svelte";
  import Tile from "../lib/ui/Tile.svelte";
  import UsageChart from "../lib/ui/UsageChart.svelte";
  import { series, spanMs, type Span, type Usage } from "../lib/usage";

  type App = components["schemas"]["App"];
  type Node = components["schemas"]["Node"];
  type Join = components["schemas"]["Join"];

  let { cell, admin }: { cell: string; admin: boolean } = $props();
  let nodes = $state<Node[] | null>(null);
  let apps = $state<Map<string, App>>(new Map());
  let failed = $state("");
  let join = $state<Join | null>(null);
  let joinError = $state("");
  let joining = $state(false);

  const guard = newest();
  async function load() {
    try {
      const answer = await guard(Promise.all([api.GET("/v0/nodes"), api.GET("/v0/apps")]));
      if (answer === STALE) return;
      const [n, a] = answer;
      nodes = must(n);
      apps = new Map(must(a).map((x) => [x.name, x]));
      failed = "";
    } catch (e) {
      failed = e instanceof Error ? e.message : String(e);
    }
  }

  async function addMachine() {
    if (joining) return;
    joining = true;
    joinError = "";
    try {
      join = must(await api.POST("/v0/nodes/joins"));
    } catch (e) {
      joinError = e instanceof Error ? e.message : String(e);
    } finally {
      joining = false;
    }
  }

  // One copy button per value; the label says which value it copied.
  let copied = $state<{ label: string; text: string } | null>(null);
  async function copy(value: string, label: string) {
    try {
      await navigator.clipboard.writeText(value);
      copied = { label, text: "Copied" };
    } catch {
      copied = { label, text: "Copy failed" };
    }
    setTimeout(() => (copied = null), 1600);
  }

  // Live events reload at once (a burst counts once). Machines are not on
  // the stream: the poll finds them, quickly while a join key is out.
  $effect(() => {
    load();
    const t = setInterval(load, join ? 5000 : 15000);
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

  // What each machine used over the span, read again every minute.
  let span = $state<Span>("24h");
  let history = $state<Map<string, { end: number; usage?: Usage; failed?: string }>>(new Map());
  const names = $derived(nodes?.map((m) => m.name).join(" ") ?? "");
  $effect(() => {
    const list = names ? names.split(" ") : [];
    const range = span;
    let gone = false;
    async function read() {
      const end = Date.now();
      const got = await Promise.all(
        list.map(async (name): Promise<[string, { end: number; usage?: Usage; failed?: string }]> => {
          try {
            return [name, { end, usage: must(await api.GET("/v0/nodes/{name}/usage", { params: { path: { name }, query: { range } } })) }];
          } catch (e) {
            return [name, { end, failed: e instanceof Error ? e.message : String(e) }];
          }
        }),
      );
      if (!gone) history = new Map(got);
    }
    read();
    const t = setInterval(read, 60000);
    return () => {
      gone = true;
      clearInterval(t);
    };
  });

  const gb = (b: number) => {
    const v = b / 2 ** 30;
    return v >= 1000 ? `${(v / 1024).toFixed(2)} TB` : `${v < 10 ? v.toFixed(1) : Math.round(v)} GB`;
  };
  const num = (v: number) => (v < 10 ? v.toFixed(1) : String(Math.round(v)));
  const pct = (used: number | undefined, size: number) => (used === undefined || size === 0 ? undefined : Math.round((100 * used) / size));
  const until = (t: string) => new Date(t).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  const short = machineName;
  const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;
  const title = (name: string) => apps.get(name)?.title || name;
  // The same rule as the overview: not ready, or a disk at 85% or more.
  const look = (m: Node) => !m.ready || (pct(m.diskUsed, m.disk) ?? 0) >= 85;

  type Meter = { label: string; icon: IconName; p?: number; used: string; all: string };
  const meters = (m: Node): Meter[] => [
    { label: "Processor", icon: "cpu", p: pct(m.cpuUsed, m.cpu), used: m.cpuUsed === undefined ? "" : num(m.cpuUsed), all: `${m.cpu} cores` },
    { label: "Memory", icon: "mem", p: pct(m.memoryUsed, m.memory), used: m.memoryUsed === undefined ? "" : gb(m.memoryUsed), all: gb(m.memory) },
    { label: "Disk", icon: "disk", p: pct(m.diskUsed, m.disk), used: m.diskUsed === undefined ? "" : gb(m.diskUsed), all: gb(m.disk) },
  ];

  let server = $derived(nodes?.find((m) => m.role === "server"));
  let ready = $derived(nodes?.filter((m) => m.ready).length ?? 0);
  let needs = $derived(nodes?.filter(look).length ?? 0);
  let lede = $derived.by(() => {
    if (!nodes) return "";
    if (!nodes.length) return "No machine reports yet.";
    const state = needs ? `${needs === 1 ? "one needs" : `${needs} need`} a look` : nodes.length === 1 ? "ready" : "all ready";
    return `${nodes.length === 1 ? "One machine" : `${nodes.length} machines`}, ${state}. Apps and the console answer at *.${cell}.`;
  });
  let figures = $derived.by(() => {
    if (!nodes) return null;
    const cores = nodes.reduce((s, m) => s + m.cpu, 0);
    const memory = nodes.reduce((s, m) => s + m.memory, 0);
    const measured = nodes.length > 0 && nodes.every((m) => m.cpuUsed !== undefined && m.memoryUsed !== undefined);
    const usedCores = nodes.reduce((s, m) => s + (m.cpuUsed ?? 0), 0);
    const usedMemory = nodes.reduce((s, m) => s + (m.memoryUsed ?? 0), 0);
    const fullest = nodes
      .map((m) => ({ m, p: pct(m.diskUsed, m.disk) }))
      .filter((x) => x.p !== undefined)
      .sort((x, y) => y.p! - x.p!)[0];
    return { cores, memory, measured, usedCores, usedMemory, fullest };
  });
</script>

{#snippet copybox(value: string, label: string)}
  <div class="copy">
    <code class="mono">{value}</code>
    <Button kind="outline" size="sm" aria-label={`Copy ${label}`} onclick={() => copy(value, label)}>
      <Icon name="copy" size={13} stroke={2} />{copied?.label === label ? copied.text : "Copy"}
    </Button>
  </div>
{/snippet}

<header class="page-head">
  <div>
    <h1>Machines</h1>
    {#if lede}<p>{lede}</p>{/if}
  </div>
  <!-- A join key is for admins only (E-63). -->
  {#if admin && nodes}
    <Button kind="outline" size="lg" onclick={addMachine} disabled={joining}><Icon name="plus" size={14} stroke={2} />Add a machine</Button>
  {/if}
</header>

{#if failed}<p class="fail" role="alert">{failed}</p>{/if}
{#if joinError}<p class="fail" role="alert">{joinError}</p>{/if}

{#if nodes && figures}
  <section class="sec" aria-labelledby="h-where">
    <div class="sec-head">
      <h2 id="h-where">What runs where</h2>
      <span>Each machine, its load and its apps.</span>
      <span class="end"><Spans bind:value={span} /></span>
    </div>
    <div class="board">
      <div class="map">
        {#each nodes as m (m.name)}
          {@const hot = look(m)}
          <article class="plate" class:look={hot} aria-labelledby="m-{m.name}">
            <header class="ph">
              <Tile icon="server" size={36} />
              <span class="pn">
                <strong id="m-{m.name}">{short(m.name)}</strong>
                <small>{m.role === "server" ? "runs the cell" : "runs apps"}, {plural(m.apps.length, "app")}</small>
              </span>
              {#if !m.ready}<Tag tone="warn">not ready</Tag>{:else if hot}<Tag tone="warn">needs a look</Tag>{:else}<span class="okdot pulse-ok" role="img" aria-label="ready"></span>{/if}
            </header>

            <div class="meters">
              {#each meters(m) as x (x.label)}
                {@const full = x.p !== undefined && x.p >= 85}
                <div class="meter">
                  <span class="ml">
                    <span><Icon name={x.icon} size={12} stroke={2} />{x.label}</span>
                    {#if x.p === undefined}
                      <span class="mono quiet">{x.all}, not measured</span>
                    {:else}
                      <span class="mono" class:hot={full}>{x.used} of {x.all} <b>{x.p}%</b></span>
                    {/if}
                  </span>
                  <span class="track" class:none={x.p === undefined}><span class:hot={full} style:width="{x.p ?? 0}%"></span></span>
                </div>
              {/each}
            </div>

            {#if history.get(m.name)}
              {@const h = history.get(m.name)!}
              {#if h.usage}
                <div class="history">
                  <UsageChart label="Processor" points={series(h.usage, "cpu")} step={h.usage.step} end={h.end} span={spanMs(span)} max={m.cpu} format={(v) => `${num(v)} cores`} />
                  <UsageChart label="Memory" points={series(h.usage, "memory")} step={h.usage.step} end={h.end} span={spanMs(span)} max={m.memory} format={gb} />
                </div>
              {:else}
                <p class="quiet-note">No history: {h.failed}</p>
              {/if}
            {/if}

            {#if m.unmeasured}
              <p class="hint">
                No load from this machine: its collector sends nothing to the cell's store. It starts with the machine; if it does not run, the machine lacks room or cannot reach {server ? short(server.name) : "the server"}.
              </p>
            {/if}

            <div class="apps">
              {#if m.apps.length}
                <ul class="tiles">
                  {#each m.apps as name (name)}
                    <li>
                      <a class="t" href={itemPath("/apps", name)}>
                        <Logo app={apps.get(name) ?? { name }} title={title(name)} size={42} />
                        <small>{title(name)}</small>
                      </a>
                    </li>
                  {/each}
                </ul>
              {:else if !m.system}
                <span class="none">No app runs here yet.</span>
              {/if}
              {#if m.system}<Tag>cell services</Tag>{/if}
            </div>

            <dl class="facts">
              <div><dt>Address</dt><dd class="mono">{m.address}</dd></div>
              <div><dt>System</dt><dd>{m.os}</dd></div>
              <div><dt>Kubernetes</dt><dd class="mono">{m.version}</dd></div>
              <div><dt>Joined</dt><dd><time datetime={m.joined}>{ago(m.joined)}</time></dd></div>
              <div><dt>{m.ready ? "Ready since" : "Not ready since"}</dt><dd><time datetime={m.since}>{ago(m.since)}</time></dd></div>
            </dl>
          </article>
        {:else}
          <p class="empty">No machine reports yet.</p>
        {/each}
      </div>

      <div class="stats">
        <div class="stat">
          <span class="sl"><Icon name="server" size={14} stroke={2} />Machines</span>
          <span class="big"><span>{ready}</span>ready</span>
          <span class="sub" class:hot={ready < nodes.length}>{ready === nodes.length ? `${plural(nodes.length, "machine")} in all` : `${nodes.length - ready} not ready`}</span>
        </div>
        <div class="stat">
          <span class="sl"><Icon name="cpu" size={14} stroke={2} />Processor</span>
          <span class="big"><span>{figures.measured ? num(figures.usedCores) : figures.cores}</span>{figures.measured ? `of ${figures.cores} cores` : "cores"}</span>
          <span class="sub">{figures.measured ? "in use now" : "Not every machine reports its load"}</span>
        </div>
        <div class="stat">
          <span class="sl"><Icon name="mem" size={14} stroke={2} />Memory</span>
          <span class="big"><span>{figures.measured ? gb(figures.usedMemory) : gb(figures.memory)}</span>{figures.measured ? `of ${gb(figures.memory)}` : ""}</span>
          <span class="sub">{figures.measured ? "in use now" : "Not every machine reports its load"}</span>
        </div>
        <div class="stat">
          <span class="sl"><Icon name="disk" size={14} stroke={2} />Fullest disk</span>
          {#if figures.fullest}
            <span class="big"><span>{figures.fullest.p}%</span>full</span>
            <span class="sub" class:hot={figures.fullest.p! >= 85}>on {short(figures.fullest.m.name)}</span>
          {:else}
            <span class="big"><span>none</span></span>
            <span class="sub">No disk measured</span>
          {/if}
        </div>
      </div>
    </div>
  </section>
{:else if !failed}
  <p class="empty plain">Reading the machines</p>
{/if}

{#if join}
  <Drawer title="Add a machine" subtitle="More room for apps, on a machine you choose." onclose={() => (join = null)}>
    <ol class="steps">
      <li>
        <p>Run this as root on an Ubuntu machine that can reach {server ? short(server.name) : "the cell's server"}. It joins, links up and shows up on this page.</p>
        {@render copybox(join.command, "the command")}
      </li>
      <li>
        <p>Paste this join key when the command asks for it. It works for one machine, until {until(join.expiresAt)}: add machines one after another.</p>
        {@render copybox(join.key, "the join key")}
      </li>
    </ol>
    <div><Button onclick={() => (join = null)}>Done</Button></div>
  </Drawer>
{/if}

<style>
  .board {
    margin-top: 4px;
    border-radius: var(--radius-hero);
    border: 1px solid var(--color-line);
    overflow: hidden;
    background: var(--color-surface);
  }
  .map {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(min(360px, 100%), 1fr));
    gap: 22px;
    padding: 28px 24px;
    background-color: var(--color-surface-2);
    background-image: radial-gradient(var(--color-line) 1px, transparent 1.3px);
    background-size: 16px 16px;
  }
  .plate {
    display: flex;
    flex-direction: column;
    gap: 18px;
    min-width: 0;
    padding: 20px 22px;
    border-radius: var(--radius-panel);
    background: var(--color-surface);
    box-shadow: 0 0 0 1px var(--color-line), var(--shadow-raised);
  }
  @supports (corner-shape: squircle) {
    .plate {
      border-radius: 40px;
      corner-shape: squircle;
    }
  }
  .plate.look {
    box-shadow: 0 0 0 1px var(--color-warn-edge), var(--shadow-raised);
  }
  .ph {
    display: flex;
    align-items: center;
    gap: 12px;
  }
  .pn {
    flex: 1;
    min-width: 0;
  }
  .pn strong,
  .pn small {
    display: block;
  }
  .pn strong {
    font-size: 16px;
    font-weight: 500;
    overflow-wrap: anywhere;
  }
  .pn small {
    font-size: 12.5px;
    color: var(--color-muted);
  }
  .okdot {
    flex: none;
    width: 9px;
    height: 9px;
    border-radius: 50%;
    background: var(--color-ok);
  }
  .meters {
    display: grid;
    gap: 14px;
  }
  .ml {
    display: flex;
    justify-content: space-between;
    gap: 12px;
    font-size: 12.5px;
    color: var(--color-text);
  }
  .ml > span:first-child {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
  .ml .mono {
    font-size: 12px;
    color: var(--color-ink);
    text-align: right;
  }
  .ml .mono b {
    margin-left: 8px;
    font-weight: 400;
    color: var(--color-muted);
  }
  .ml .quiet {
    color: var(--color-muted);
  }
  .ml .hot,
  .ml .hot b {
    color: var(--color-warn-ink);
  }
  .track {
    display: block;
    margin-top: 7px;
    height: 5px;
    border-radius: 999px;
    background: var(--color-track);
    overflow: hidden;
  }
  .track.none {
    background: repeating-linear-gradient(90deg, var(--color-hairline) 0 4px, transparent 4px 8px);
  }
  .track span {
    display: block;
    height: 100%;
    border-radius: inherit;
    background: var(--color-ok-dot);
  }
  .track span.hot {
    background: var(--color-warn);
  }
  .history {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
    gap: 14px 18px;
  }
  .quiet-note {
    margin: 0;
    font-size: 12.5px;
    color: var(--color-muted);
  }
  .hint {
    margin: 0;
    padding: 10px 12px;
    border-radius: var(--radius-control);
    background: var(--color-warn-bg);
    color: var(--color-warn-text);
    font-size: 13px;
  }
  .apps {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 10px;
  }
  .tiles {
    flex: 1 1 100%;
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(72px, 1fr));
    gap: 12px 8px;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .t {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 6px;
    min-width: 0;
    padding: 6px 2px;
    border-radius: var(--radius-tile);
  }
  .t:hover {
    background: var(--color-hover);
  }
  .t small {
    max-width: 100%;
    font-size: 11.5px;
    color: var(--color-text);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .none {
    color: var(--color-muted);
    font-size: 13px;
  }
  .facts {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
    gap: 10px 16px;
    margin: auto 0 0;
    padding-top: 16px;
    border-top: 1px solid var(--color-hairline);
  }
  .facts div {
    min-width: 0;
  }
  .facts dt {
    font-size: 11.5px;
    color: var(--color-muted);
  }
  .facts dd {
    margin: 2px 0 0;
    font-size: 13px;
    overflow-wrap: anywhere;
  }
  .facts dd.mono {
    font-size: 12px;
  }
  .stats {
    display: flex;
    flex-wrap: wrap;
    border-top: 1px solid var(--color-hairline);
  }
  .stat {
    flex: 1 1 220px;
    padding: 18px 24px 20px;
    box-shadow: -1px 0 0 var(--color-hairline);
  }
  .sl {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
    font-weight: 500;
    color: var(--color-text);
  }
  .big {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 4px 10px;
    margin-top: 4px;
    color: var(--color-text);
  }
  .big span {
    font-size: 48px;
    line-height: 1.05;
    letter-spacing: -0.045em;
    font-variant-numeric: tabular-nums;
    color: var(--color-ink);
  }
  .sub {
    display: block;
    margin-top: 4px;
    font-size: 12.5px;
    color: var(--color-text);
  }
  .sub.hot {
    color: var(--color-warn-ink);
  }
  .empty.plain {
    padding: 0;
    margin-top: 32px;
  }
  .steps {
    display: grid;
    gap: 22px;
    margin: 0;
    padding: 0 0 0 20px;
  }
  .steps li::marker {
    color: var(--color-muted);
  }
  .steps p {
    margin: 0 0 10px;
    color: var(--color-text);
  }
  .copy {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 10px 10px 10px 14px;
    border-radius: var(--radius-box);
    background: var(--color-surface-2);
    border: 1px solid var(--color-line);
  }
  .copy code {
    flex: 1;
    min-width: 0;
    overflow-wrap: anywhere;
    font-size: 13px;
    line-height: 1.5;
  }
  @media (max-width: 860px) {
    .map {
      padding: 18px 14px;
      gap: 16px;
    }
    .plate {
      padding: 18px;
    }
    .copy {
      flex-direction: column;
      align-items: stretch;
    }
  }
</style>
