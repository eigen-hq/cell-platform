<script lang="ts">
  import type { Snippet } from "svelte";
  import type { HTMLButtonAttributes } from "svelte/elements";

  let { kind = "primary", children, ...rest }: { kind?: "primary" | "quiet" | "danger"; children: Snippet } & HTMLButtonAttributes = $props();
</script>

<button class={kind} {...rest}>{@render children()}</button>

<style>
  button {
    display: inline-flex;
    align-items: center;
    gap: 9px;
    border: 0;
    border-radius: var(--radius-pill);
    padding: 11px 20px;
    font-weight: 600;
    transition: transform 0.15s;
  }
  button:active {
    transform: translateY(1px);
  }
  .primary {
    background: var(--button-primary-bg);
    color: var(--button-primary-ink);
  }
  .quiet,
  .danger {
    background: var(--button-quiet-bg);
    color: var(--button-quiet-ink);
  }
  .danger {
    color: var(--color-bad);
  }
  button:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }
</style>
