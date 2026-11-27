<script lang="ts">
  // The ask box: an admin tells Ops in plain words what to look at or do,
  // about the cell or about one app (E-85). Ops answers in a task and
  // proposes changes, which wait for approval like every change.
  import { api, must } from "../lib/api/client";
  import { itemPath } from "../lib/route";
  import { MAX_ASK } from "../lib/task";
  import AgentFace from "../lib/ui/AgentFace.svelte";
  import Button from "../lib/ui/Button.svelte";

  let { app, title }: { app?: string; title?: string } = $props();
  let input = $state("");
  let busy = $state(false);
  let note = $state<{ text: string; bad?: boolean; task?: string } | null>(null);

  // Characters as the API counts them (code points, not UTF-16 units).
  const long = $derived([...input].length > MAX_ASK);
  const examples = $derived(
    app ? `Why did ${title ?? app} restart this morning? Let only admins open it.` : "Which machine is fullest, and which apps run on it?",
  );

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (busy || !input.trim() || long) return;
    busy = true;
    note = null;
    try {
      const t = must(await api.POST("/v0/tasks", { body: { kind: "ask", ...(app ? { app } : {}), input: input.trim() } }));
      note = { text: "Ops is on it. What it wants changed waits for your approval.", task: t.name };
      input = "";
    } catch (err) {
      note = { text: err instanceof Error ? err.message : String(err), bad: true };
    } finally {
      busy = false;
    }
  }

  // Enter sends, Shift+Enter makes a new line.
  function key(e: KeyboardEvent) {
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      (e.currentTarget as HTMLTextAreaElement).form?.requestSubmit();
    }
  }
</script>

<form class="ask" onsubmit={submit}>
  <AgentFace name="Ops" size={34} />
  <label>
    <span class="sr">{app ? `Ask Ops about ${title ?? app}` : "Ask Ops about the cell"}</span>
    <textarea bind:value={input} onkeydown={key} rows="2" placeholder={examples} aria-invalid={long}></textarea>
  </label>
  <Button type="submit" disabled={busy || !input.trim() || long}>{busy ? "Asking" : "Ask Ops"}</Button>
  <small class="hint" class:fail={long}>
    {#if long}
      At most {MAX_ASK} characters.
    {:else if app}
      Ops reads {title ?? app}: its state, pods, logs and changes. No keys or passwords here.
    {:else}
      Ops reads the cell: apps, machines and changes. Ask about one app from its page to let it look inside. No keys or passwords here.
    {/if}
  </small>
  {#if note}
    <p class="note" class:fail={note.bad} role="status">
      {note.text}
      {#if note.task}<a href={itemPath("/agents", note.task)}>Follow Ops</a>{/if}
    </p>
  {/if}
</form>

<style>
  .ask {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    align-items: start;
    gap: 8px 14px;
    padding: 16px 18px;
    border-radius: var(--radius-panel);
    background: var(--color-panel);
    border: 1px solid var(--color-line);
  }
  label {
    display: block;
  }
  textarea {
    width: 100%;
    box-sizing: border-box;
    min-height: 44px;
    padding: 10px 12px;
    border: 1px solid var(--color-line);
    border-radius: var(--radius-part);
    background: var(--color-sunk);
    color: var(--color-ink);
    font: 15px/1.45 var(--font-sans);
    resize: vertical;
  }
  textarea[aria-invalid="true"] {
    border-color: var(--color-bad);
  }
  .hint,
  .note {
    grid-column: 2 / 4;
    margin: 0;
    color: var(--color-muted);
    font-size: 0.85rem;
  }
  .note {
    color: var(--color-ink);
  }
  .fail {
    color: var(--color-bad);
  }
  .note a {
    margin-left: 6px;
  }
  .sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
  }
  @media (max-width: 640px) {
    .ask {
      grid-template-columns: auto minmax(0, 1fr);
    }
    .ask > :global(button) {
      grid-column: 2;
      justify-self: start;
    }
    .hint,
    .note {
      grid-column: 1 / -1;
    }
  }
</style>
