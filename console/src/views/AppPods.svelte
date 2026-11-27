<script lang="ts">
  // What runs inside an app: its pods as rows of containers (a dot per
  // container: ready, waiting or stopped), restarts and the last exit, and
  // the end of a container's log one click away. Only admins see it (E-63).
  import { api, must } from "../lib/api/client";
  import type { components } from "../lib/api/schema";
  import { newest, STALE } from "../lib/newest";
  import { textOf } from "../lib/text";
  import { ago } from "../lib/time";
  import Button from "../lib/ui/Button.svelte";

  type Pod = components["schemas"]["Pod"];

  let { app }: { app: string } = $props();
  let pods = $state<Pod[] | null>(null);
  let failed = $state("");
  let log = $state<{ pod: string; container: string; previous: boolean; text: string } | null>(null);
  let logFailed = $state("");
  let reading = $state(false);

  const guard = newest();
  async function load() {
    try {
      const answer = await guard(api.GET("/v0/apps/{name}/pods", { params: { path: { name: app } } }));
      if (answer === STALE) return;
      pods = must(answer);
      failed = "";
    } catch (e) {
      failed = e instanceof Error ? e.message : String(e);
    }
  }
  $effect(() => {
    load();
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  });

  async function readLog(pod: string, container: string, previous: boolean) {
    reading = true;
    logFailed = "";
    try {
      const r = await api.GET("/v0/apps/{name}/logs", { params: { path: { name: app }, query: { pod, container, previous, tail: 300 } }, parseAs: "text" });
      log = { pod, container, previous, text: textOf(r) };
    } catch (e) {
      logFailed = e instanceof Error ? e.message : String(e);
    } finally {
      reading = false;
    }
  }

  // A container that is not ready and has crashed before counts as failing.
  const dot = (c: Pod["containers"][number]) => (c.ready ? "ok" : c.state === "terminated" || c.restarts > 0 ? "bad" : "wait");
  const word = (c: Pod["containers"][number]) =>
    c.ready ? "ready" : c.reason ? c.reason : c.state === "terminated" ? "stopped" : c.state === "waiting" ? "waiting" : "starting";

  // Use next to the most the container may take: memory past the limit
  // ends it (OOMKilled), processor past it is only slowed.
  const mb = (b: number) => (b >= 1 << 30 ? `${(b / (1 << 30)).toFixed(1)} GB` : `${Math.round(b / (1 << 20))} MB`);
  const cores = (v: number) => (v < 0.1 ? `${Math.round(v * 1000)}m` : v.toFixed(2).replace(/\.?0+$/, ""));
  function usage(c: Pod["containers"][number]) {
    const part = (used: string | undefined, limit: string | undefined, unit: string) =>
      used ? `${used}${limit ? ` of ${limit}` : ""} ${unit}` : limit ? `at most ${limit} ${unit}` : "";
    const mem = part(c.memoryUsed === undefined ? undefined : mb(c.memoryUsed), c.memoryLimit === undefined ? undefined : mb(c.memoryLimit), "memory");
    const cpu = part(c.cpuUsed === undefined ? undefined : cores(c.cpuUsed), c.cpuLimit === undefined ? undefined : cores(c.cpuLimit), "cores");
    return [mem, cpu].filter(Boolean).join(", ");
  }
  const tight = (c: Pod["containers"][number]) => c.memoryUsed !== undefined && c.memoryLimit !== undefined && c.memoryUsed >= 0.9 * c.memoryLimit;
</script>

{#if failed}<p class="fail" role="alert">{failed}</p>{/if}
{#if pods}
  {#if pods.length}
    <ul class="pods">
      {#each pods as p (p.name)}
        <li>
          <div class="head">
            <strong title={p.name}>{p.name}</strong>
            <small>{p.phase.toLowerCase()}, started {ago(p.createdAt)}</small>
          </div>
          <ul class="containers">
            {#each p.containers as c (c.name)}
              <li>
                <span class="dot {dot(c)}" aria-hidden="true"></span>
                <span class="what">
                  <strong>{c.name}</strong> <small>{word(c)}{c.restarts ? `, ${c.restarts} ${c.restarts === 1 ? "restart" : "restarts"}` : ""}</small>
                  {#if c.lastExit}<small class="exit">last exit {ago(c.lastExit.at)}: {c.lastExit.reason}, code {c.lastExit.exitCode}</small>{/if}
                  {#if c.message}<small class="exit">{c.message}</small>{/if}
                  {#if usage(c)}<small class="use" class:tight={tight(c)}>{usage(c)}{tight(c) ? ", close to its memory limit" : ""}</small>{/if}
                </span>
                <span class="read">
                  <Button kind="quiet" disabled={reading} onclick={() => readLog(p.name, c.name, false)}>Log</Button>
                  {#if c.restarts}<Button kind="quiet" disabled={reading} onclick={() => readLog(p.name, c.name, true)}>Before the restart</Button>{/if}
                </span>
              </li>
            {/each}
          </ul>
        </li>
      {/each}
    </ul>
  {:else}
    <p class="hint">Nothing runs right now.</p>
  {/if}
{/if}
{#if logFailed}<p class="fail" role="alert">{logFailed}</p>{/if}
{#if log}
  <div class="logbox">
    <p>
      <span>{log.container} in {log.pod}{log.previous ? ", before the restart" : ""}, the last 300 lines</span>
      <button class="link" disabled={reading} onclick={() => log && readLog(log.pod, log.container, log.previous)}>reload</button>
      <button class="link" onclick={() => (log = null)}>close</button>
    </p>
    <pre>{log.text || "The log is empty."}</pre>
  </div>
{/if}

<style>
  ul {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .pods {
    display: grid;
    gap: 10px;
  }
  .pods > li {
    padding: 12px 14px;
    border: 1px solid var(--color-line);
    border-radius: var(--radius-part);
  }
  .head {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 4px 10px;
    margin-bottom: 8px;
  }
  .head strong {
    font: 600 13px ui-monospace, SFMono-Regular, Menlo, monospace;
  }
  small {
    color: var(--color-muted);
    font-size: 0.82rem;
  }
  .containers li {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    align-items: start;
    gap: 10px;
    padding: 6px 0;
  }
  .dot {
    width: 12px;
    height: 12px;
    margin-top: 5px;
    border-radius: 50%;
    background: var(--color-muted);
  }
  .dot.ok {
    background: var(--color-ok);
  }
  .dot.wait {
    background: var(--color-wait);
  }
  .dot.bad {
    background: var(--color-bad);
    box-shadow: 0 0 0 4px color-mix(in srgb, var(--color-bad) 25%, transparent);
  }
  .exit,
  .use {
    display: block;
  }
  .use.tight {
    color: var(--color-wait);
  }
  .read {
    display: flex;
    flex-wrap: wrap;
    justify-content: end;
    gap: 6px;
  }
  .logbox {
    margin-top: 12px;
  }
  .logbox p {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 12px;
    margin: 0 0 6px;
    color: var(--color-muted);
    font-size: 0.85rem;
  }
  .logbox p span {
    flex: 1;
  }
  .link {
    padding: 0;
    border: 0;
    background: none;
    color: var(--color-ink);
    font-size: 0.85rem;
    text-decoration: underline;
  }
  pre {
    max-height: 340px;
    margin: 0;
    padding: 14px;
    overflow: auto;
    border-radius: var(--radius-part);
    background: var(--color-sunk);
    font: 12.5px/1.55 ui-monospace, SFMono-Regular, Menlo, monospace;
    white-space: pre-wrap;
    word-break: break-word;
  }
  .hint {
    margin: 0;
    color: var(--color-muted);
  }
</style>
