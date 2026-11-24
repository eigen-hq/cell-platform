<script lang="ts">
  // An agent as a small soft creature with two eyes; the color follows the
  // agent's name. Knows nothing about the API.
  let { name, size = 40 }: { name: string; size?: number } = $props();
  const fills = ["lavender", "rose", "sun", "blue", "leaf"];
  const fill = $derived(fills[[...name.toLowerCase()].reduce((h, c) => (h * 31 + c.charCodeAt(0)) >>> 0, 3) % fills.length]);
</script>

<svg class={fill} width={size} height={size} viewBox="0 0 40 40" aria-hidden="true">
  <path class="body" d="M8 12c0-5 3.5-8 12-8s12 3 12 8v14c0 6-4.5 10-12 10S8 32 8 26z" />
  <path class="shine" d="M12 11c1-3 4-4.5 8-4.5" />
  <rect class="eye" x="14" y="17" width="4" height="7" rx="2" />
  <rect class="eye" x="22" y="17" width="4" height="7" rx="2" />
</svg>

<style>
  svg {
    flex: none;
  }
  .body { fill: var(--fill); }
  .shine { fill: none; stroke: rgb(255 255 255 / 0.55); stroke-width: 2; stroke-linecap: round; }
  .eye { fill: var(--brand-paper-ink); }
  .lavender { --fill: var(--avatar-lavender); }
  .rose { --fill: var(--avatar-rose); }
  .sun { --fill: var(--avatar-sun); }
  .blue { --fill: var(--avatar-blue); }
  .leaf { --fill: var(--avatar-leaf); }
</style>
