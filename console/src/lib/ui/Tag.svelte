<script lang="ts">
  // A short label next to a name: neutral, an agent's, or "needs a look".
  import type { Snippet } from "svelte";
  let { tone = "neutral", mono = false, children }: { tone?: "neutral" | "agent" | "warn" | "ok"; mono?: boolean; children: Snippet } = $props();
</script>

<span class="tag {tone}" class:mono>{@render children()}</span>

<style>
  .tag {
    flex: none;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 22px;
    padding: 0 8px;
    border-radius: var(--radius-chip);
    background: var(--color-tile);
    color: var(--color-text);
    font-size: 11.5px;
    font-weight: 500;
    white-space: nowrap;
  }
  .mono {
    font-family: var(--font-mono);
    font-weight: 400;
    background: var(--color-track);
  }
  .agent {
    background: var(--color-agent-tile);
    color: var(--color-agent-ink);
  }
  .warn {
    background: var(--color-warn-bg);
    color: var(--color-warn-text);
  }
  .ok {
    background: var(--color-ok-tile);
    color: var(--color-ok);
  }
</style>
