<script lang="ts">
  // One figure over a span as a line (E-87): the axis runs from 0 to max,
  // a gap in the figures is a gap in the line. Pointer or arrow keys read
  // one point.
  let {
    label,
    points,
    step,
    end,
    span,
    max,
    format,
  }: {
    label: string;
    points: { at: string; v: number }[];
    step: number; // seconds between points
    end: number; // ms, the right edge
    span: number; // ms
    max: number;
    format: (v: number) => string;
  } = $props();

  const H = 52;
  let w = $state(0);
  let pick = $state<number | null>(null);

  type Pt = { at: string; v: number; t: number; x: number; y: number };
  const pts = $derived(
    points.map((p): Pt => {
      const t = Date.parse(p.at);
      return { ...p, t, x: (w * (t - (end - span))) / span, y: H - 1 - ((H - 3) * Math.min(p.v, max)) / (max || 1) };
    }),
  );
  // A pause of more than one and a half steps breaks the line.
  const runs = $derived.by(() => {
    const out: Pt[][] = [];
    let run: Pt[] = [];
    for (const p of pts) {
      const prev = run[run.length - 1];
      if (!prev || p.t - prev.t > 1500 * step) out.push((run = []));
      run.push(p);
    }
    return out;
  });
  const xy = (p: Pt) => `${p.x.toFixed(1)} ${p.y.toFixed(1)}`;
  const line = $derived(runs.map((r) => r.map((p, k) => `${k ? "L" : "M"}${xy(p)}`).join("")).join(""));
  const area = $derived(runs.map((r) => `M${r[0]!.x.toFixed(1)} ${H - 1}${r.map((p) => `L${xy(p)}`).join("")}L${r[r.length - 1]!.x.toFixed(1)} ${H - 1}Z`).join(""));
  const peak = $derived(pts.reduce((m, p) => Math.max(m, p.v), 0));
  const last = $derived(pts[pts.length - 1]);
  const sel = $derived(pick === null ? undefined : pts[pick]);

  const when = (iso: string) =>
    new Date(iso).toLocaleString([], span > 864e5 ? { weekday: "short", hour: "2-digit", minute: "2-digit" } : { hour: "2-digit", minute: "2-digit" });

  function near(e: PointerEvent) {
    const x = e.clientX - (e.currentTarget as HTMLElement).getBoundingClientRect().left;
    let best: number | null = null;
    pts.forEach((p, i) => (best === null || Math.abs(p.x - x) < Math.abs(pts[best]!.x - x)) && (best = i));
    pick = best;
  }
  function keys(e: KeyboardEvent) {
    if (!pts.length) return;
    const n = pts.length - 1;
    const to = { ArrowLeft: (pick ?? n + 1) - 1, ArrowRight: (pick ?? -1) + 1, Home: 0, End: n }[e.key];
    if (to === undefined) return;
    e.preventDefault();
    pick = Math.max(0, Math.min(n, to));
  }
  const summary = $derived(last ? `${label}: peak ${format(peak)}, last ${format(last.v)}` : `${label}: no figures`);
</script>

<div class="chart">
  <p class="head">
    <span>{label}</span>
    {#if sel}
      <span class="mono"><b>{format(sel.v)}</b> {when(sel.at)}</span>
    {:else if last}
      <span class="mono">peak <b>{format(peak)}</b></span>
    {/if}
  </p>
  <div
    class="plot"
    bind:clientWidth={w}
    role="slider"
    aria-label={summary}
    aria-valuemin={0}
    aria-valuemax={Math.max(0, pts.length - 1)}
    aria-valuenow={pick ?? Math.max(0, pts.length - 1)}
    aria-valuetext={sel ? `${format(sel.v)} at ${when(sel.at)}` : summary}
    tabindex="0"
    onpointermove={near}
    onpointerleave={() => (pick = null)}
    onkeydown={keys}
    onblur={() => (pick = null)}
  >
    {#if w && pts.length}
      <svg width={w} height={H} aria-hidden="true">
        <line class="grid" x1="0" x2={w} y1="0.5" y2="0.5" />
        <line class="grid" x1="0" x2={w} y1={H - 0.5} y2={H - 0.5} />
        <path class="area" d={area} />
        <path class="line" d={line} />
        {#if sel}
          <line class="cross" x1={sel.x} x2={sel.x} y1="0" y2={H} />
          <circle class="dot" cx={sel.x} cy={sel.y} r="4" />
        {/if}
      </svg>
    {:else if w}
      <span class="none">No figures in this span yet.</span>
    {/if}
  </div>
</div>

<style>
  .chart {
    min-width: 0;
  }
  .head {
    display: flex;
    justify-content: space-between;
    gap: 8px;
    margin: 0 0 6px;
    font-size: 12.5px;
    color: var(--color-text);
  }
  .head .mono {
    font-size: 12px;
    color: var(--color-muted);
    white-space: nowrap;
  }
  .head b {
    font-weight: 500;
    color: var(--color-ink);
  }
  .plot {
    position: relative;
    height: 52px;
    border-radius: 4px;
    outline-offset: 3px;
    touch-action: pan-y;
  }
  svg {
    display: block;
    overflow: visible;
  }
  .grid {
    stroke: var(--color-hairline);
    stroke-width: 1;
  }
  .area {
    fill: color-mix(in srgb, var(--color-ok) 12%, transparent);
  }
  .line {
    fill: none;
    stroke: var(--color-ok);
    stroke-width: 2;
    stroke-linejoin: round;
    stroke-linecap: round;
  }
  .cross {
    stroke: var(--color-line-strong);
    stroke-width: 1;
  }
  .dot {
    fill: var(--color-ok);
    stroke: var(--color-surface);
    stroke-width: 2;
  }
  .none {
    display: grid;
    place-items: center;
    height: 100%;
    border-radius: inherit;
    background: repeating-linear-gradient(90deg, var(--color-hairline) 0 4px, transparent 4px 8px) bottom / 100% 1px no-repeat;
    color: var(--color-muted);
    font-size: 12px;
  }
</style>
