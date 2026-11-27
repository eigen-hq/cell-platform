<script lang="ts">
  // One line icon from icons.ts, in the color of the text around it.
  import { icons, type IconName } from "./icons";
  let { name, size = 15, stroke = 1.9 }: { name: IconName; size?: number; stroke?: number } = $props();
</script>

<svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width={stroke} stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
  <path d={icons[name]} />
</svg>

<style>
  svg {
    flex: none;
    display: block;
  }
</style>
