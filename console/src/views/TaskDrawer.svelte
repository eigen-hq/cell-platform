<script lang="ts">
  // One agent task in a drawer (Figma: 21 Agents, "every step the agent
  // took"): what the agent was asked, each API call it made, its answer and
  // the changes it proposed.
  import { api, must } from "../lib/api/client";
  import { taskState, task as line, agents, type Task } from "../lib/task";
  import { ago, stamp } from "../lib/time";
  import AgentFace from "../lib/ui/AgentFace.svelte";
  import Drawer from "../lib/ui/Drawer.svelte";
  import Status from "../lib/ui/Status.svelte";

  let { name, title, onclose, onnavigate }: { name: string; title: (app: string) => string; onclose: () => void; onnavigate: (href: string) => void } = $props();
  let t = $state<Task | null>(null);
  let failed = $state("");

  $effect(() => {
    const n = name;
    const run = async () => {
      try {
        t = must(await api.GET("/v0/tasks/{name}", { params: { path: { name: n } } }));
        failed = "";
      } catch (e) {
        failed = e instanceof Error ? e.message : String(e);
      }
    };
    run();
    const i = setInterval(run, 5000);
    return () => clearInterval(i);
  });

  const go = (e: MouseEvent, href: string) => {
    if (e.metaKey || e.ctrlKey) return;
    e.preventDefault();
    onnavigate(href);
  };
</script>

<Drawer title={t ? agents[t.kind].name : "Agent task"} subtitle={t ? `${t.assignee || "no agent yet"} / ${t.name}` : name} {onclose}>
  {#if failed}
    <p class="fail">{failed}</p>
  {:else if t}
    {@const s = taskState(t)}
    <div class="head">
      <AgentFace name={agents[t.kind].name} size={52} />
      <div><strong>{line(t, title(t.app))}</strong><small>asked by {t.requester.kind === "Operator" ? "the platform" : t.requester.name}, {ago(t.createdAt)}</small></div>
      <Status tone={s.tone} label={s.label} />
    </div>

    <section>
      <h3>What it was given</h3>
      <!-- a request is prose; a Compose file keeps its lines -->
      <pre class:wrap={t.kind === "ask"}>{t.input || "nothing"}</pre>
    </section>

    <section>
      <h3>Every step <small>API calls, recorded before they were served</small></h3>
      {#if t.steps.length}
        <ol class="steps">
          {#each t.steps as st, i (i)}<li><time>{stamp(st.at)}</time><code>{st.call}</code></li>{/each}
        </ol>
      {:else}
        <p class="hint">No calls yet.</p>
      {/if}
    </section>

    {#if t.result}
      <section>
        <h3>Its answer</h3>
        <p class="answer">{t.result}</p>
      </section>
    {/if}

    {#if t.changes?.length}
      <section>
        <h3>Changes it proposed</h3>
        <ul class="links">
          {#each t.changes as c (c)}<li><a href="/history/{c}" onclick={(e) => go(e, `/history/${c}`)}>{c}</a></li>{/each}
        </ul>
      </section>
    {/if}
  {/if}
</Drawer>

<style>
  .head {
    display: flex;
    align-items: center;
    gap: 14px;
  }
  .head div {
    flex: 1;
  }
  .head strong,
  .head small {
    display: block;
  }
  .head strong {
    font-size: 1.05rem;
  }
  .head small,
  .hint {
    color: var(--color-muted);
    font-size: 13.5px;
  }
  section {
    display: grid;
    gap: 10px;
  }
  h3 {
    display: flex;
    align-items: baseline;
    gap: 10px;
    margin: 0;
    font: 600 1.25rem / 1.2 var(--font-display);
  }
  h3 small {
    color: var(--color-muted);
    font: 600 13px var(--font-sans);
  }
  pre,
  .steps {
    overflow: auto;
    max-height: 320px;
    margin: 0;
    padding: 14px;
    border-radius: var(--radius-part);
    background: var(--color-sunk);
    font: 13px/1.6 ui-monospace, SFMono-Regular, Menlo, monospace;
  }
  .steps {
    list-style: none;
  }
  .steps li {
    display: flex;
    gap: 14px;
  }
  .steps time {
    flex: none;
    color: var(--color-muted);
  }
  .steps code {
    font: inherit;
    overflow-wrap: anywhere;
  }
  .wrap {
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .answer {
    margin: 0;
    white-space: pre-line;
    padding: 14px 16px;
    border-radius: var(--radius-part);
    background: color-mix(in srgb, var(--color-info) 14%, transparent);
    line-height: 1.5;
  }
  .links {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .links a {
    display: inline-block;
    padding: 6px 12px;
    border-radius: var(--radius-pill);
    background: var(--color-sunk);
    font-weight: 600;
    text-decoration: none;
  }
</style>
