<script lang="ts">
  // One group in a drawer: its members (tick to change) and the apps that
  // are attached to it. Changing members takes effect at once in the
  // identity; it is not an app change, so it needs no approval.
  import { api, must } from "../lib/api/client";
  import { untrack } from "svelte";
  import { begin, changes, saved, snapshot, startOver, stateOf, toggle, type Edit } from "../lib/draft";
  import type { components } from "../lib/api/schema";
  import Avatar from "../lib/ui/Avatar.svelte";
  import Button from "../lib/ui/Button.svelte";
  import Drawer from "../lib/ui/Drawer.svelte";

  type Group = components["schemas"]["Group"];
  type User = components["schemas"]["User"];

  let { group, people, apps, onclose, onchange }: { group: Group; people: User[]; apps: string[]; onclose: () => void; onchange: (saved?: Group) => void } = $props();
  // The edit and the membership it builds on. Each load hands in a new
  // group object; a failed or dropped reload hands in none.
  // svelte-ignore state_referenced_locally
  let e = $state<Edit>(begin(group.members));
  let note = $state<{ text: string; bad?: boolean } | null>(null);
  let busy = $state(false);
  const members = $derived(e.draft);
  const edit = $derived(stateOf(e));
  const changed = $derived(edit !== "clean");
  // svelte-ignore state_referenced_locally
  let last = group;
  $effect(() => {
    const g = group;
    if (g !== last) {
      last = g;
      e = snapshot(untrack(() => e), g.members);
    }
  });

  async function save() {
    busy = true;
    note = null;
    try {
      const g = must(await api.PATCH("/v0/groups/{name}/members", { params: { path: { name: group.name } }, body: changes(e) }));
      e = saved(g.members);
      note = { text: "Saved. The members get the group's apps at their next sign-in." };
      onchange(g);
    } catch (err) {
      note = { text: err instanceof Error ? err.message : String(err), bad: true };
      // Someone may have changed the group: load it, so the edit shows the conflict.
      onchange();
    } finally {
      busy = false;
    }
  }
</script>

<Drawer title={group.name} subtitle="{members.length} {members.length === 1 ? 'person' : 'people'}{apps.length ? `, gets ${apps.join(', ')}` : ''}" {onclose}>
  {#if note}<p class="note" class:fail={note.bad} role="status">{note.text}</p>{/if}
  <section aria-labelledby="g-members">
    <h3 id="g-members">Members <span>tick who belongs</span></h3>
    <ul class="people">
      {#each people as p (p.username)}
        <li>
          <label class="row" class:on={members.includes(p.username)}>
            <input type="checkbox" checked={members.includes(p.username)} disabled={busy} onchange={() => (e = toggle(e, p.username))} />
            <Avatar name={p.name || p.username} size={26} />
            <span><strong>{p.name || p.username}</strong><small>{p.username}</small></span>
          </label>
        </li>
      {:else}
        <li class="empty">No person yet.</li>
      {/each}
    </ul>
    {#if edit === "conflict"}
      <p class="note warn" role="status">Someone else changed this group meanwhile. Saving applies only your ticks; start over to see who is in it now.</p>
    {/if}
    <div class="buttons">
      <Button onclick={save} disabled={!changed || busy}>{busy ? "Saving" : "Save members"}</Button>
      {#if edit !== "clean"}<Button kind="plain" onclick={() => (e = startOver(e))}>Start over</Button>{/if}
    </div>
  </section>
</Drawer>

<style>
  section {
    display: grid;
    gap: 12px;
  }
  h3 {
    display: flex;
    align-items: baseline;
    gap: 10px;
    margin: 0;
    font-size: 15px;
    font-weight: 500;
  }
  h3 span {
    font-size: 13px;
    font-weight: 400;
    color: var(--color-muted);
  }
  .people {
    margin: 0;
    padding: 0;
    list-style: none;
    border-radius: var(--radius-panel);
    background: var(--color-surface-2);
    border: 1px solid var(--color-line);
    overflow: hidden;
  }
  .people > li + li {
    border-top: 1px solid var(--color-hairline);
  }
  label {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 10px 16px;
    cursor: pointer;
  }
  label.on {
    background: var(--color-ok-tile);
  }
  input {
    accent-color: var(--color-ok);
  }
  label strong,
  label small {
    display: block;
  }
  label strong {
    font-weight: 500;
  }
  label small {
    color: var(--color-text);
    font-size: 12.5px;
  }
  .buttons {
    display: flex;
    gap: 8px;
  }
  .note {
    margin: 0 0 16px;
    padding: 10px 14px;
    border-radius: var(--radius-control);
    background: var(--color-ok-tile);
  }
  .note.fail,
  .note.warn {
    background: var(--color-warn-bg);
    color: var(--color-warn-text);
  }
  .note.warn {
    margin: 0;
  }
</style>
