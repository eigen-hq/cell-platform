<script lang="ts">
  // The ask box: an admin tells Ops in plain words what to look at or do,
  // about the cell or about one app (E-85). Ops answers in a task and
  // proposes changes, which wait for approval like every change. The ring
  // turns while the box may hand work to an agent. With reply, the box
  // answers the agent's question or follows up on its answer in that task
  // (E-99): the agent goes on from the conversation.
  import { api, must } from "../lib/api/client";
  import { itemPath } from "../lib/route";
  import { MAX_ASK } from "../lib/task";
  import Icon from "../lib/ui/Icon.svelte";
  import type { IconName } from "../lib/ui/icons";

  let {
    app,
    title,
    suggestions = [],
    big = true,
    reply,
    onsent,
  }: {
    app?: string;
    title?: string;
    suggestions?: { text: string; icon: IconName }[];
    big?: boolean;
    reply?: { task: string; asked: boolean };
    onsent?: () => void;
  } = $props();
  let input = $state("");
  let busy = $state(false);
  let note = $state<{ text: string; bad?: boolean; task?: string } | null>(null);
  let box: HTMLTextAreaElement;

  // Characters as the API counts them (code points, not UTF-16 units).
  const long = $derived([...input].length > MAX_ASK);
  const placeholder = $derived(
    reply
      ? reply.asked
        ? "Answer Ops."
        : "Follow up: ask Ops more, or tell it what to do next."
      : app
        ? `Ask Ops about ${title ?? app}, or hand off a task.`
        : "Ask anything about the cell or hand off a task.",
  );
  const label = $derived(reply ? "Reply to Ops" : app ? `Ask Ops about ${title ?? app}` : "Ask Ops about the cell");

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (busy || !input.trim() || long) return;
    busy = true;
    note = null;
    try {
      if (reply) {
        must(await api.POST("/v0/tasks/{name}/reply", { params: { path: { name: reply.task } }, body: { text: input.trim() } }));
        note = { text: "Sent. Ops goes on from the conversation." };
        input = "";
        onsent?.();
        return;
      }
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
  function pick(text: string) {
    input = text;
    box.focus();
  }
</script>

<div class="wrap" class:big>
  {#if big}<div class="glow" aria-hidden="true"></div>{/if}
  <form class="ring" class:ring-fast={busy} onsubmit={submit} aria-label={reply || app ? label : "Hand off to an agent"}>
    <div class="ring-spin" aria-hidden="true"></div>
    <div class="face">
      <label>
        <span class="vh">{label}</span>
        <textarea bind:this={box} bind:value={input} onkeydown={key} rows="1" {placeholder} aria-label="What should Ops do?" aria-invalid={long}></textarea>
      </label>
      <div class="foot">
        {#each suggestions as s (s.text)}
          <button type="button" class="chip" onclick={() => pick(s.text)}><span class="ic"><Icon name={s.icon} size={13} stroke={2} /></span>{s.text}</button>
        {/each}
        <span class="space"></span>
        <button type="submit" class="go-btn" disabled={busy || !input.trim() || long}>
          {reply ? (busy ? "Sending" : "Reply") : busy ? "Handing off" : "Hand off"}<Icon name="arrow" size={14} stroke={2.2} />
        </button>
      </div>
    </div>
  </form>
</div>
<p class="hint" class:warn={long || note?.bad} role={note ? "status" : undefined}>
  {#if long}
    At most {MAX_ASK} characters.
  {:else if note}
    {note.text}
    {#if note.task}<a class="go" href={itemPath("/agents", note.task)}>Follow Ops<Icon name="right" size={13} stroke={2} /></a>{/if}
  {:else}
    <Icon name="shield" size={13} stroke={2} />
    {reply
      ? "Ops goes on from this conversation. Every change it proposes waits for your approval. No keys or passwords here."
      : app
      ? `Ops reads ${title ?? app}: its state, pods, logs and changes. Every change it proposes waits for your approval. No keys or passwords here.`
      : "Ops reads the cell through the platform: apps, machines and changes. Every change it proposes waits for your approval. No keys or passwords here."}
  {/if}
</p>

<style>
  .wrap {
    position: relative;
    isolation: isolate;
  }
  form {
    padding: 1.5px;
    border-radius: var(--radius-hero);
    background: var(--color-line);
    box-shadow: var(--shadow-raised);
  }
  .face {
    position: relative;
    z-index: 1;
    border-radius: 20.5px;
    background: var(--color-surface);
  }
  label {
    display: block;
    padding: 20px 22px 10px;
  }
  textarea {
    display: block;
    width: 100%;
    min-height: 40px;
    max-height: 240px;
    field-sizing: content;
    resize: none;
    border: 0;
    outline: 0;
    padding: 8px 0;
    background: transparent;
    font-size: 15px;
    line-height: 1.5;
    color: var(--color-ink);
  }
  .big textarea {
    font-size: 18px;
    letter-spacing: -0.01em;
  }
  textarea::placeholder {
    color: var(--color-muted);
  }
  .foot {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    padding: 6px 14px 14px 20px;
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    height: 32px;
    padding: 0 12px 0 5px;
    border: 1px solid var(--color-chip-line);
    border-radius: var(--radius-control);
    background: transparent;
    font-size: 13px;
    transition: background-color 0.16s ease;
  }
  .chip:hover {
    background: var(--color-hover);
  }
  .ic {
    width: 22px;
    height: 22px;
    border-radius: 7px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    background: var(--color-tile);
    box-shadow: 0 0 0 1px var(--color-line);
  }
  .space {
    flex: 1;
  }
  .go-btn {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    height: 40px;
    padding: 0 16px 0 18px;
    border: 0;
    border-radius: var(--radius-tile);
    background: var(--color-ink);
    color: var(--color-on-ink);
    font-weight: 500;
  }
  .hint {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 7px;
    margin: 14px 0 0 4px;
    font-size: 12.5px;
    color: var(--color-muted);
  }
  .hint.warn {
    color: var(--color-warn-text);
  }
</style>
