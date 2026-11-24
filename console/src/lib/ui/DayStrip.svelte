<script lang="ts">
  // Rows of days, one square each, colored by what happened that day
  // (oldest left). Knows nothing about the API.
  type Tone = "ok" | "wait" | "bad" | "none" | "info";
  let { rows, days }: { rows: { label: string; days: (Tone | null)[] }[]; days: Date[] } = $props();
  const fmt = (d: Date) => d.toLocaleDateString([], { day: "numeric", month: "short" });
</script>

<div class="strip" style:--n={days.length}>
  {#each rows as r (r.label)}
    <span class="label">{r.label}</span>
    {#each r.days as t, i (i)}<span class="d {t ?? ''}" title="{fmt(days[i]!)}: {t ? r.label.toLowerCase() : 'nothing'}"></span>{/each}
  {/each}
  <span></span>
  {#each days as d, i (i)}<span class="tick">{(days.length - 1 - i) % 7 === 0 ? d.getDate() : ""}</span>{/each}
</div>

<style>
  .strip {
    display: grid;
    grid-template-columns: 96px repeat(var(--n), minmax(0, 1fr));
    gap: 4px;
    align-items: center;
    padding: 16px 18px 12px;
    border-radius: var(--radius-panel);
    background: var(--color-panel);
  }
  .label {
    color: var(--color-muted);
    font-size: 13px;
    font-weight: 600;
  }
  .d {
    height: 16px;
    border-radius: 4px;
    background: color-mix(in srgb, var(--color-ink) 8%, transparent);
  }
  .ok { background: var(--color-ok); }
  .wait { background: var(--color-wait); }
  .bad { background: var(--color-bad); }
  .none { background: var(--color-dim); }
  .info { background: var(--avatar-lavender); }
  .tick {
    color: var(--color-muted);
    font-size: 12px;
    text-align: center;
  }
</style>
