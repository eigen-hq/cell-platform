<script lang="ts">
  // A shell command to copy. Knows nothing about the API.
  let { command }: { command: string } = $props();
  let copied = $state(false);

  async function copy() {
    await navigator.clipboard.writeText(command);
    copied = true;
    setTimeout(() => (copied = false), 1600);
  }
</script>

<div class="box">
  <code>{command}</code>
  <button onclick={copy} aria-label="Copy the command">{copied ? "Copied" : "Copy"}</button>
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
