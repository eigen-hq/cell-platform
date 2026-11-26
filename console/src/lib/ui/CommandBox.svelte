<script lang="ts">
  // A shell command (or another value, named by label) to copy. Knows
  // nothing about the API.
  let { command, label = "the command" }: { command: string; label?: string } = $props();
  let copied = $state("");

  async function copy() {
    try {
      await navigator.clipboard.writeText(command);
      copied = "Copied";
    } catch {
      copied = "Copy failed";
    }
    setTimeout(() => (copied = ""), 1600);
  }
</script>

<div class="box">
  <code>{command}</code>
  <button onclick={copy} aria-label={`Copy ${label}`}>{copied || "Copy"}</button>
</div>

<style>
  .box {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 10px 10px 10px 18px;
    border-radius: var(--radius-part);
    background: var(--color-sunk);
    border: 1px solid var(--color-line);
  }
  code {
    flex: 1;
    min-width: 0;
    overflow-wrap: anywhere;
    font: 500 14px/1.5 ui-monospace, "SF Mono", Menlo, monospace;
  }
  button {
    flex: none;
    border: 0;
    border-radius: var(--radius-pill);
    padding: 8px 16px;
    font-weight: 600;
    background: var(--button-primary-bg);
    color: var(--button-primary-ink);
  }
</style>
