<script lang="ts">
  // Any open-source project with a Compose file becomes an app: Packer
  // turns the file into a manifest and proposes the install, which waits
  // for approval like every change (Figma: 06 Apps, "Add an app").
  import { api, must } from "../lib/api/client";
  import { MAX_CHARS, packageProblem } from "../lib/package";
  import { itemPath } from "../lib/route";
  import Button from "../lib/ui/Button.svelte";


  let { taken }: { taken: Set<string> } = $props();
  let name = $state("");
  let compose = $state("");
  let busy = $state(false);
  let note = $state<{ text: string; bad?: boolean; task?: string } | null>(null);

  const problem = $derived(packageProblem(name, compose, taken));

  async function pick(e: Event) {
    const file = (e.currentTarget as HTMLInputElement).files?.[0];
    if (!file) return;
    // A character takes at least one byte: anything this large is too long.
    if (file.size > 4 * MAX_CHARS) {
      note = { text: `${file.name} is too large for a Compose file.`, bad: true };
      return;
    }
    try {
      compose = await file.text();
      if (!name) name = file.name.replace(/\.(ya?ml)$/i, "").replace(/^(docker-)?compose$/i, "").toLowerCase().replace(/[^a-z0-9-]+/g, "-").replace(/^-+|-+$/g, "");
      note = null;
    } catch (err) {
      note = { text: err instanceof Error ? err.message : String(err), bad: true };
    }
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (busy || problem) return;
    busy = true;
    note = null;
    try {
      const t = must(await api.POST("/v0/tasks", { body: { kind: "package", app: name, input: compose } }));
      note = { text: `Packer is on ${name}. It proposes the install; you approve it like every change.`, task: t.name };
      name = "";
      compose = "";
    } catch (err) {
      note = { text: err instanceof Error ? err.message : String(err), bad: true };
    } finally {
      busy = false;
    }
  }
</script>

<form class="package" onsubmit={submit}>
  <div class="art" aria-hidden="true">
    <svg viewBox="0 0 120 90">
      <rect class="sheet" x="10" y="12" width="46" height="62" rx="8" />
      <path class="lines" d="M19 28h22M19 38h28M19 48h18M19 58h24" />
      <path class="arrow" d="M62 43h18m-6-6 6 6-6 6" />
      <rect class="box" x="86" y="24" width="28" height="38" rx="14" />
    </svg>
  </div>
  <div class="fields">
    <label>Name of the app <input bind:value={name} autocomplete="off" spellcheck="false" placeholder="wiki" aria-invalid={!!problem} /></label>
    <label class="file">Compose file <input type="file" accept=".yml,.yaml,text/yaml" onchange={pick} /></label>
    <label class="text">or paste it <textarea bind:value={compose} rows="6" spellcheck="false" placeholder={"services:\n  wiki:\n    image: ..."}></textarea></label>
    {#if problem}<small class="fail">{problem}</small>{/if}
    <div class="row">
      <Button type="submit" disabled={busy || !name || !compose.trim() || !!problem}>{busy ? "Asking Packer" : "Let Packer package it"}</Button>
    </div>
    {#if note}
      <p class="note" class:fail={note.bad} role="status">
        {note.text}
        {#if note.task}<a href={itemPath("/agents", note.task)}>Follow Packer</a>{/if}
      </p>
    {/if}
  </div>
</form>

<style>
  .package {
    display: grid;
    grid-template-columns: 160px minmax(0, 1fr);
    gap: 28px;
    padding: 24px;
    border-radius: var(--radius-panel);
    background: var(--color-panel);
  }
  @media (max-width: 720px) {
    .package {
      grid-template-columns: 1fr;
    }
  }
  .art svg {
    width: 100%;
    height: auto;
  }
  .sheet {
    fill: var(--color-sunk);
    stroke: var(--color-line);
  }
  .lines {
    stroke: var(--color-muted);
    stroke-width: 3;
    stroke-linecap: round;
  }
  .arrow {
    fill: none;
    stroke: var(--color-ink);
    stroke-width: 3;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .box {
    fill: var(--color-info);
  }
  .fields {
    display: grid;
    gap: 12px;
    max-width: 560px;
  }
  label {
    display: grid;
    gap: 4px;
    color: var(--color-muted);
    font-size: 0.85rem;
  }
  input:not([type="file"]),
  textarea {
    padding: 9px 12px;
    border: 1px solid var(--color-line);
    border-radius: var(--radius-part);
    background: var(--color-sunk);
    color: var(--color-ink);
    font: 14px ui-monospace, SFMono-Regular, Menlo, monospace;
  }
  input[aria-invalid="true"] {
    border-color: var(--color-bad);
  }
  textarea {
    resize: vertical;
  }
  input[type="file"] {
    color: var(--color-muted);
    font-size: 0.85rem;
  }
  input[type="file"]::file-selector-button {
    margin-right: 10px;
    padding: 7px 14px;
    border: 0;
    border-radius: var(--radius-pill);
    background: var(--button-quiet-bg);
    color: var(--color-ink);
    font: 600 0.85rem var(--font-sans);
    cursor: pointer;
  }
  .note {
    margin: 0;
  }
  .note a {
    margin-left: 6px;
  }
</style>
