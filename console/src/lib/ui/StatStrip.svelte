<script module lang="ts">
  export type Stat = { label: string; value: string; tone?: "ok" | "wait" | "bad" | "none" };
</script>

<script lang="ts">
  // A row of key figures, each with a status light. Knows nothing about the API.
  let { items }: { items: Stat[] } = $props();
</script>

<dl class="strip">
  {#each items as s (s.label)}
    <div>
      <dt>{s.label}</dt>
      <dd><span class="led {s.tone ?? 'ok'}"></span>{s.value}</dd>
    </div>
  {/each}
</dl>

<style>
  .strip {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(170px, 1fr));
    gap: 14px 24px;
    margin: 0;
    padding: 18px 22px;
    border-radius: var(--radius-panel);
    background: var(--color-panel);
  }
  dt {
    margin-bottom: 4px;
    color: var(--color-muted);
    font-size: 13px;
    font-weight: 600;
  }
  dd {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    font-size: 15.5px;
    font-weight: 700;
  }
  .led {
    flex: none;
    width: 9px;
    height: 9px;
    border-radius: 50%;
  }
  .ok { background: var(--color-ok); }
  .wait { background: var(--color-wait); }
  .bad { background: var(--color-bad); }
  .none { background: var(--color-dim); }
</style>
