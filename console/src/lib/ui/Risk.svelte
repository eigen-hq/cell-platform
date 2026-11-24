<script lang="ts">
  // Risk as three rising bars and a word. Knows nothing about the API.
  let { level, short = false }: { level: "low" | "medium" | "high"; short?: boolean } = $props();
  const lit = $derived({ low: 1, medium: 2, high: 3 }[level]);
</script>

<span class="risk"><span class="bars" aria-hidden="true">{#each [1, 2, 3] as i (i)}<i class:on={i <= lit}></i>{/each}</span>{short ? level : `${level[0]!.toUpperCase()}${level.slice(1)} risk`}</span>

<style>
  .risk {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    font-weight: 700;
  }
  .bars {
    display: inline-flex;
    align-items: flex-end;
    gap: 2px;
    height: 14px;
  }
  i {
    width: 4px;
    border-radius: 2px;
    background: currentColor;
    opacity: 0.28;
  }
  i:nth-child(1) { height: 6px; }
  i:nth-child(2) { height: 10px; }
  i:nth-child(3) { height: 14px; }
  i.on { opacity: 1; }
</style>
