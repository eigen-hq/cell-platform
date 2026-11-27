<script lang="ts">
  // Picks the span a usage history covers.
  import { spans, type Span } from "../usage";
  let { value = $bindable() }: { value: Span } = $props();
</script>

<div class="spans" role="group" aria-label="Span">
  {#each spans as [key, label] (key)}
    <button type="button" aria-pressed={value === key} aria-label={label} onclick={() => (value = key)}>{key}</button>
  {/each}
</div>

<style>
  .spans {
    display: inline-flex;
    gap: 2px;
    padding: 3px;
    border-radius: var(--radius-control);
    background: var(--color-tile);
  }
  button {
    height: 26px;
    padding: 0 10px;
    border: 0;
    border-radius: calc(var(--radius-control) - 2px);
    background: none;
    color: var(--color-text);
    font-size: 12.5px;
    font-variant-numeric: tabular-nums;
  }
  button:hover {
    color: var(--color-ink);
  }
  [aria-pressed="true"] {
    background: var(--color-surface);
    color: var(--color-ink);
    box-shadow: var(--shadow-tab);
  }
</style>
