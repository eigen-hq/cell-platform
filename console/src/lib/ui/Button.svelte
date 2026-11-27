<script lang="ts">
  // Buttons: primary (ink), outline, plain (text only) and warn (for what
  // removes or undoes something). A link when href is set.
  import type { Snippet } from "svelte";
  import type { HTMLButtonAttributes } from "svelte/elements";
  let {
    kind = "primary",
    size = "md",
    href,
    children,
    ...rest
  }: { kind?: "primary" | "outline" | "plain" | "warn"; size?: "sm" | "md" | "lg"; href?: string; children: Snippet } & HTMLButtonAttributes = $props();
</script>

{#if href}
  <a class="btn {kind} {size}" {href}>{@render children()}</a>
{:else}
  <button class="btn {kind} {size}" type="button" {...rest}>{@render children()}</button>
{/if}

<style>
  .btn {
    flex: none;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 8px;
    height: 34px;
    padding: 0 14px;
    border: 1px solid transparent;
    border-radius: var(--radius-control);
    font-weight: 500;
    white-space: nowrap;
    transition: background-color 0.16s ease;
  }
  .sm {
    height: 30px;
    padding: 0 11px;
    font-size: 13px;
  }
  .lg {
    height: 40px;
    padding: 0 16px 0 18px;
    border-radius: var(--radius-tile);
  }
  .primary {
    background: var(--color-ink);
    color: var(--color-on-ink);
  }
  .primary:hover:not(:disabled) {
    background: color-mix(in srgb, var(--color-ink) 88%, var(--color-bg));
  }
  .outline {
    border-color: var(--color-line);
    background: var(--color-surface);
    color: var(--color-ink);
  }
  .outline:hover:not(:disabled),
  .plain:hover:not(:disabled) {
    background: var(--color-hover);
  }
  .plain {
    padding: 0 8px;
    background: transparent;
    color: var(--color-text);
  }
  .warn {
    border-color: var(--color-warn-edge);
    background: var(--color-warn-bg);
    color: var(--color-warn-text);
  }
</style>
