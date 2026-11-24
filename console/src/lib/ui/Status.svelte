<script lang="ts">
  // A status light with its word. Knows nothing about the API.
  let { tone, label }: { tone: "ok" | "wait" | "bad" | "none"; label: string } = $props();
</script>

<span class="status {tone}"><span class="led"></span>{label}</span>

<style>
  .status {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    font-size: 13.5px;
    font-weight: 600;
    white-space: nowrap;
  }
  .led {
    width: 9px;
    height: 9px;
    border-radius: 50%;
    background: currentColor;
  }
  .ok { color: var(--color-ok); }
  .wait { color: var(--color-wait); }
  .bad { color: var(--color-bad); }
  .none { color: var(--color-dim); }
  .wait .led { animation: pulse 1.6s ease-in-out infinite; }
  @keyframes pulse { 50% { opacity: 0.35; } }
</style>
