<script lang="ts">
  // A small rounded square with a line icon: neutral, an agent's, the
  // operator's, healthy or needing a look.
  import Icon from "./Icon.svelte";
  import type { IconName } from "./icons";
  let {
    icon,
    tone = "neutral",
    size = 28,
    pulse = false,
  }: { icon: IconName; tone?: "neutral" | "agent" | "operator" | "ok" | "warn"; size?: number; pulse?: boolean } = $props();
</script>

<span class="tile {tone}" class:pulse style:--s="{size}px"><Icon name={icon} size={Math.round(size * 0.55)} /></span>

<style>
  .tile {
    flex: none;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: var(--s);
    height: var(--s);
    border-radius: calc(var(--s) * 0.33);
    background: var(--color-tile);
    color: var(--color-ink);
  }
  .agent {
    background: var(--color-agent-tile);
    color: var(--color-agent-ink);
  }
  .operator {
    color: var(--color-text);
  }
  .ok {
    background: var(--color-ok-tile);
    color: var(--color-ok);
  }
  .warn {
    background: var(--color-warn-bg);
    color: var(--color-warn-ink);
  }
</style>
