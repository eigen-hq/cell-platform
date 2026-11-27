<script lang="ts">
  // One change waiting for a yes, as a row: who asked, what happens to which
  // app, why; the plan (steps, risk, what protects it) opens below. Only
  // admins decide (E-63): anyone else sees the plan alone.
  import { api, must } from "../lib/api/client";
  import { backupOf, groupsOf, headline, safety, steps, type Change } from "../lib/change";
  import { itemPath } from "../lib/route";
  import { ago } from "../lib/time";
  import Avatar from "../lib/ui/Avatar.svelte";
  import Button from "../lib/ui/Button.svelte";
  import Icon from "../lib/ui/Icon.svelte";
  import { agentIcon } from "../lib/ui/icons";
  import Logo from "../lib/ui/Logo.svelte";
  import Tag from "../lib/ui/Tag.svelte";
  import Tile from "../lib/ui/Tile.svelte";

  let {
    change: c,
    title,
    admin,
    app,
    ondecided,
  }: { change: Change; title: string; admin: boolean; app?: { name: string; spec?: unknown }; ondecided: () => void } = $props();
  let busy = $state(false);
  let declining = $state(false);
  let plan = $state(false);
  let failed = $state("");

  async function decide(verb: "approve" | "reject") {
    busy = true;
    failed = "";
    try {
      const path = { params: { path: { name: c.name } } };
      must(await (verb === "approve" ? api.POST("/v0/changes/{name}/approve", path) : api.POST("/v0/changes/{name}/reject", path)));
      ondecided();
    } catch (e) {
      failed = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
      declining = false;
    }
  }

  const agent = $derived(c.requester.kind === "Agent");
  const who = $derived(agent ? `${c.requester.name[0]!.toUpperCase()}${c.requester.name.slice(1)}` : c.requester.name);
  const groups = $derived(groupsOf(c));
  const version = $derived(c.action === "update" && c.patch && typeof (c.patch as Record<string, unknown>).version === "string" ? String((c.patch as Record<string, unknown>).version) : "");
  const patch = $derived(c.patch && Object.keys(c.patch).length ? JSON.stringify(c.patch, null, 2) : "");
</script>

<article class="slip" aria-label={headline(c, title)}>
  <div class="who">
    {#if agent}<Tile icon={agentIcon(c.requester.name)} tone="agent" size={30} />{:else}<Avatar name={who} size={30} />{/if}
    <span>
      <strong>{agent ? `${who} agent` : who}</strong>
      <small>{admin ? "Waiting for you" : "Waiting for an admin"}</small>
    </span>
  </div>
  <div class="what">
    <Logo app={app ?? { name: c.app }} {title} />
    <span class="txt">
      <span class="head">{headline(c, title).replace(/ to \S+$/, version ? "" : "$&")}{#if version}<span class="mono ver">to {version}</span>{/if}</span>
      <span class="why">{c.reason || `${agent ? "Proposed" : "Asked for"} ${ago(c.createdAt)}.`}</span>
    </span>
  </div>
  <div class="act">
    {#if admin && declining}
      <Button kind="warn" disabled={busy} onclick={() => decide("reject")}>Yes, decline</Button>
      <Button kind="plain" disabled={busy} onclick={() => (declining = false)}>Keep it</Button>
    {:else if admin}
      <Button disabled={busy} onclick={() => decide("approve")}>Approve</Button>
      <Button kind="outline" disabled={busy} onclick={() => (declining = true)}>Decline</Button>
    {/if}
    <Button kind="plain" aria-expanded={plan} onclick={() => (plan = !plan)}>{plan ? "Hide the plan" : "See the plan"}</Button>
  </div>
  {#if failed}<p class="fail" role="alert">{failed}</p>{/if}
  {#if plan}
    <div class="plan">
      <ol aria-label="What happens after a yes">
        {#each steps(c) as s, i (s)}<li><span class="mono">{i + 1}</span>{s}</li>{/each}
      </ol>
      <p class="facts">
        <Tag tone={c.risk === "low" ? "neutral" : "warn"}>{c.risk} risk</Tag>
        <span><Icon name="shield" size={13} stroke={2} />{safety(c)}</span>
        {#if c.action === "restore"}<span><Icon name="restore" size={13} stroke={2} />Back to {backupOf(c)}</span>{/if}
        {#if groups}<span><Icon name="users" size={13} stroke={2} />Open to {groups.length ? groups.join(", ") : "everyone"}</span>{/if}
      </p>
      {#if patch}<pre class="mono">{patch}</pre>{/if}
      <p class="ref">
        <span class="mono">{c.name}</span>, {agent ? "proposed" : "asked"} {ago(c.createdAt)}
        {#if c.task && admin}<a class="go" href={itemPath("/agents", c.task)}>How the agent got there<Icon name="right" size={13} stroke={2} /></a>{/if}
      </p>
    </div>
  {/if}
</article>

<style>
  .slip {
    display: grid;
    grid-template-columns: 200px minmax(0, 1fr) auto;
    align-items: center;
    gap: 14px 24px;
    padding: 20px 22px;
    background: var(--color-surface);
  }
  .who,
  .what {
    display: flex;
    align-items: center;
    gap: 11px;
    min-width: 0;
  }
  .what {
    gap: 14px;
  }
  .who strong,
  .who small {
    display: block;
  }
  .who strong {
    font-weight: 500;
  }
  .who small {
    font-size: 12.5px;
    color: var(--color-agent-ink);
  }
  .txt {
    min-width: 0;
  }
  .head {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 4px 10px;
    font-size: 15px;
    font-weight: 500;
  }
  .ver {
    font-size: 12.5px;
    font-weight: 400;
    color: var(--color-text);
  }
  .why {
    display: block;
    margin-top: 2px;
    color: var(--color-text);
  }
  .act {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }
  .fail,
  .plan {
    grid-column: 2 / -1;
    margin: 0;
  }
  .plan {
    display: grid;
    gap: 14px;
    padding-top: 6px;
  }
  ol {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  ol li {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    height: 30px;
    padding: 0 12px 0 6px;
    border: 1px solid var(--color-chip-line);
    border-radius: var(--radius-control);
    font-size: 13px;
  }
  ol li span {
    width: 20px;
    height: 20px;
    border-radius: 6px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    background: var(--color-tile);
    font-size: 11px;
    color: var(--color-text);
  }
  .facts {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px 18px;
    margin: 0;
    font-size: 13px;
    color: var(--color-text);
  }
  .facts > span {
    display: inline-flex;
    align-items: center;
    gap: 7px;
  }
  pre {
    margin: 0;
    padding: 12px 14px;
    border-radius: var(--radius-tile);
    background: var(--color-surface-2);
    border: 1px solid var(--color-hairline);
    font-size: 12px;
    overflow-x: auto;
  }
  .ref {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 14px;
    margin: 0;
    font-size: 12.5px;
    color: var(--color-muted);
  }
  @media (max-width: 860px) {
    .slip {
      grid-template-columns: minmax(0, 1fr);
    }
    .fail,
    .plan {
      grid-column: 1;
    }
  }
</style>
