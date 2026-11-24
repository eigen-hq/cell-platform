<script lang="ts">
  // A labelled bar: how much of something is used. Turns amber when nearly
  // full. Knows nothing about the API.
  let { label, value, part }: { label: string; value: string; part: number | null } = $props();
  const full = $derived(part !== null && part >= 0.8);
</script>

<div class="meter">
  <span class="label">{label}</span>
  <span class="value">{value}</span>
  <span class="bar" class:none={part === null}>
    {#if part !== null}<span class="fill" class:full style:width="{Math.min(100, Math.max(2, part * 100))}%"></span>{/if}
  </span>
</div>

<style>
  .meter {
    display: grid;
    grid-template-columns: 1fr auto;
    gap: 7px 12px;
    font-size: 13px;
    color: var(--color-muted);
  }
  .label {
    font-weight: 700;
  }
  .bar {
    grid-column: 1 / -1;
    height: 10px;
    border-radius: 5px;
    background: color-mix(in srgb, var(--color-ink) 16%, transparent);
    overflow: hidden;
  }
  .bar.none {
    background: repeating-linear-gradient(90deg, color-mix(in srgb, var(--color-ink) 12%, transparent) 0 6px, transparent 6px 10px);
  }
  .fill {
    display: block;
    height: 100%;
    border-radius: inherit;
    background: var(--color-info);
    transition: width 0.6s cubic-bezier(0.2, 0.8, 0.2, 1);
  }
  .fill.full {
    background: var(--color-wait);
  }
</style>
