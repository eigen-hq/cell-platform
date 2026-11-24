<script lang="ts">
  import type { Snippet } from "svelte";
  import type { HTMLButtonAttributes } from "svelte/elements";

  let { label, children, ...rest }: { label: string; children: Snippet } & HTMLButtonAttributes = $props();
</script>

<button aria-label={label} title={label} {...rest}>{@render children()}</button>

<style>
  button {
    display: grid;
    place-items: center;
    flex: none;
    width: 36px;
    height: 36px;
    border: 0;
    border-radius: 50%;
    background: var(--nav-bg);
    color: var(--nav-ink);
    transition: background-color 0.2s, color 0.2s;
  }
  button:hover {
    background: var(--color-panel);
    color: var(--color-ink);
  }
  button :global(svg) {
    width: 18px;
    height: 18px;
    fill: none;
    stroke: currentColor;
    stroke-width: 1.9;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
</style>
