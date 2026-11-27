<script lang="ts">
  // Any open-source project with a Compose file becomes an app: Packer
  // turns the file into a manifest and proposes the install, which waits
  // for approval like every change. A manifest someone already wrote goes
  // to the API as it is; the API checks it and answers with what is wrong.
  import { api, must } from "../lib/api/client";
  import { MAX_CHARS, packageProblem } from "../lib/package";
  import { itemPath } from "../lib/route";
  import Button from "../lib/ui/Button.svelte";
  import Icon from "../lib/ui/Icon.svelte";

  let { taken }: { taken: Set<string> } = $props();
  let kind = $state<"compose" | "manifest">("compose");
  let name = $state("");
  let compose = $state("");
  let busy = $state(false);
  let note = $state<{ text: string; bad?: boolean; task?: string; approvals?: boolean } | null>(null);

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
      if (!name && kind === "compose") name = file.name.replace(/\.(ya?ml)$/i, "").replace(/^(docker-)?compose$/i, "").toLowerCase().replace(/[^a-z0-9-]+/g, "-").replace(/^-+|-+$/g, "");
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
      if (kind === "manifest") {
        const c = must(await api.POST("/v0/apps", { body: { manifestYaml: compose, ...(name ? { name } : {}), reason: "installed from a manifest in the console" } }));
        note = { text: `The install of ${c.app} waits for your approval.`, approvals: true };
      } else {
        const t = must(await api.POST("/v0/tasks", { body: { kind: "package", app: name, input: compose } }));
        note = { text: `Packer is on ${name}. It proposes the install; you approve it like every change.`, task: t.name };
      }
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
  <fieldset class="kind">
    <legend>What you have</legend>
    <label class:on={kind === "compose"}><input type="radio" bind:group={kind} value="compose" /><span>A Compose file<small>Packer makes the manifest</small></span></label>
    <label class:on={kind === "manifest"}><input type="radio" bind:group={kind} value="manifest" /><span>An app manifest<small>installs as written</small></span></label>
  </fieldset>
  <label class="field">Name of the app{kind === "manifest" ? " (optional)" : ""}
    <input class="mono" bind:value={name} autocomplete="off" spellcheck="false" placeholder={kind === "manifest" ? "from metadata.name" : "wiki"} aria-invalid={!!problem} /></label>
  <label class="file"><span>{kind === "manifest" ? "Manifest file" : "Compose file"}</span><input type="file" accept=".yml,.yaml,text/yaml" onchange={pick} /></label>
  <label class="field">or paste it
    <textarea bind:value={compose} rows="7" spellcheck="false"
      placeholder={kind === "manifest" ? "apiVersion: cell.internal/v1alpha1\nkind: App\nmetadata:\n  name: wiki\nspec: ..." : "services:\n  wiki:\n    image: ..."}></textarea></label>
  {#if problem}<p class="problem">{problem}</p>{/if}
  <div>
    {#if kind === "manifest"}
      <Button type="submit" disabled={busy || !compose.trim() || !!problem}>{busy ? "Checking" : "Propose the install"}</Button>
    {:else}
      <Button type="submit" disabled={busy || !name || !compose.trim() || !!problem}>{busy ? "Asking Packer" : "Let Packer package it"}</Button>
    {/if}
  </div>
  {#if note}
    <p class={note.bad ? "fail" : "note"} role={note.bad ? "alert" : "status"}>
      {note.text}
      {#if note.task}<a class="go" href={itemPath("/agents", note.task)}>Follow Packer<Icon name="right" size={13} stroke={2} /></a>{/if}
      {#if note.approvals}<a class="go" href="/changes">Open the change log<Icon name="right" size={13} stroke={2} /></a>{/if}
    </p>
  {/if}
</form>

<style>
  .package {
    display: grid;
    gap: 14px;
  }
  .kind {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 8px;
    margin: 0;
    padding: 0;
    border: 0;
  }
  .kind legend {
    margin-bottom: 6px;
    padding: 0;
    font-size: 13px;
    color: var(--color-text);
  }
  .kind label {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 12px 14px;
    border: 1px solid var(--color-line);
    border-radius: var(--radius-tile);
    background: var(--color-surface);
    cursor: pointer;
  }
  .kind label.on {
    border-color: var(--color-line-strong);
    background: var(--color-surface-2);
  }
  .kind input {
    margin: 3px 0 0;
    accent-color: var(--color-ink);
  }
  .kind span {
    font-weight: 500;
    color: var(--color-ink);
  }
  .kind small {
    display: block;
    font-weight: 400;
    font-size: 12.5px;
    color: var(--color-muted);
  }
  input[aria-invalid="true"] {
    border-color: var(--color-warn);
  }
  .file {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 12px;
    font-size: 13px;
    color: var(--color-text);
  }
  .file input {
    min-width: 0;
    font-size: 13px;
    color: var(--color-muted);
  }
  .file input::file-selector-button {
    margin-right: 10px;
    height: 32px;
    padding: 0 12px;
    border: 1px solid var(--color-line-strong);
    border-radius: var(--radius-control);
    background: var(--color-surface);
    color: var(--color-ink);
    font: 500 13px var(--font-sans);
    cursor: pointer;
  }
  .problem {
    margin: 0;
    font-size: 13px;
    color: var(--color-warn-ink);
  }
  .note {
    margin: 0;
    padding: 10px 14px;
    border-radius: var(--radius-control);
    background: var(--color-surface-2);
    border: 1px solid var(--color-line);
  }
  .fail {
    margin: 0;
  }
  .note .go,
  .fail .go {
    margin-left: 8px;
  }
  @media (max-width: 860px) {
    .kind {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>
