<script lang="ts">
  // Where the backups go (E-47) and what was backed up: the target and its
  // last check, each app's last copy, and the copies newest first. Admins
  // set an S3 target; the cell checks a new target before it uses it.
  // Shows only what the API delivers.
  import { onMount } from "svelte";
  import { api, must } from "../lib/api/client";
  import type { components } from "../lib/api/schema";
  import { checking, hetznerEndpoint, hetznerRegions, plainHttp, requestOf, shown, type TargetForm } from "../lib/backup";
  import { lastBackup } from "../lib/home";
  import { follow } from "../lib/live";
  import { newest, STALE } from "../lib/newest";
  import { itemPath } from "../lib/route";
  import { ago } from "../lib/time";
  import Button from "../lib/ui/Button.svelte";
  import Drawer from "../lib/ui/Drawer.svelte";
  import Logo from "../lib/ui/Logo.svelte";
  import Tag from "../lib/ui/Tag.svelte";
  import Tile from "../lib/ui/Tile.svelte";

  type App = components["schemas"]["App"];
  type Backup = components["schemas"]["Backup"];
  type Target = components["schemas"]["BackupTarget"];

  let { admin }: { admin: boolean } = $props();
  let polled = $state<Target | null>(null);
  // The answer to the admin's request, until a poll shows that check or a later one.
  let asked = $state<Target | null>(null);
  let apps = $state<App[] | null>(null);
  let backups = $state<Backup[]>([]);
  let failed = $state("");
  let failedCopies = $state("");

  // Two loaders, each with its own guard, so one never drops the other's answer.
  const guardTarget = newest();
  async function loadTarget() {
    try {
      const answer = await guardTarget(api.GET("/v0/backup-target"));
      if (answer === STALE) return;
      polled = must(answer);
      failed = "";
    } catch (e) {
      failed = e instanceof Error ? e.message : String(e);
    }
  }
  const guardCopies = newest();
  async function loadCopies() {
    try {
      const answer = await guardCopies(
        (async () => {
          const list = must(await api.GET("/v0/apps"));
          const b = await Promise.all(list.map((x) => api.GET("/v0/apps/{name}/backups", { params: { path: { name: x.name } } })));
          return { list, b: b.flatMap((r) => must(r)) };
        })(),
      );
      if (answer === STALE) return;
      apps = answer.list.sort((x, y) => (x.title || x.name).localeCompare(y.title || y.name));
      backups = answer.b.sort((x, y) => y.createdAt.localeCompare(x.createdAt));
      failedCopies = "";
    } catch (e) {
      failedCopies = e instanceof Error ? e.message : String(e);
    }
  }

  let target = $derived(shown(polled, asked));
  let busy = $derived(checking(target));
  const running = (b: Backup) => b.phase === "" || b.phase === "Pending" || b.phase === "Running";
  let copying = $derived(backups.some(running));
  onMount(() => {
    void loadTarget();
    void loadCopies();
  });
  // Quick while a check or a backup runs, slow otherwise.
  $effect(() => {
    const t = setInterval(loadTarget, busy ? 2500 : 15000);
    return () => clearInterval(t);
  });
  $effect(() => {
    const t = setInterval(loadCopies, copying ? 5000 : 15000);
    return () => clearInterval(t);
  });
  // A backup or restore that changes reloads the copies at once.
  $effect(() => {
    let soon: ReturnType<typeof setTimeout> | undefined;
    const stop = follow(
      (signal) => api.GET("/v0/events", { parseAs: "stream", signal }),
      () => {
        clearTimeout(soon);
        soon = setTimeout(loadCopies, 300);
      },
      () => {},
    );
    return () => {
      clearTimeout(soon);
      stop();
    };
  });

  let form = $state<TargetForm>({ provider: "hetzner", region: "fsn1", endpoint: "", otherRegion: "", bucket: "", accessKey: "", secretKey: "", prefix: "", privateNetwork: false });
  let editing = $state(false);
  let sending = $state(false);
  let formError = $state("");

  function close() {
    editing = false;
    form.secretKey = "";
    formError = "";
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (sending) return;
    sending = true;
    formError = "";
    try {
      asked = must(await api.PUT("/v0/backup-target", { body: requestOf(form) }));
      close();
    } catch (err) {
      formError = err instanceof Error ? err.message : String(err);
    } finally {
      sending = false;
    }
  }

  const kinds = { s3: "S3 bucket", local: "Directory on the first machine", other: "rclone remote" } as const;
  const phases = { "": "Waiting to start", Pending: "Waiting to start", Running: "Running", Completed: "Done", Failed: "Failed" } as const;
  const tone = (b: Backup) => (b.phase === "Completed" ? "ok" : b.phase === "Failed" ? "warn" : "neutral");
  const at = (b: Backup) => b.completedAt ?? b.createdAt;
  let check = $derived(target?.check);
  let last = $derived(lastBackup(backups));
  let titles = $derived(new Map((apps ?? []).map((a) => [a.name, a.title || a.name])));
  let byName = $derived(new Map((apps ?? []).map((a) => [a.name, a])));
  const title = (name: string) => titles.get(name) ?? name;

  // Per app: the newest backup (it may still run) and the newest that ended.
  let perApp = $derived(
    (apps ?? []).map((a) => {
      const own = backups.filter((b) => b.app === a.name);
      return { app: a, count: own.length, now: own.find(running), ended: own.find((b) => !running(b)) };
    }),
  );

  let lede = $derived.by(() => {
    if (!target) return "";
    const copy = last ? (last.ok ? `Last copy ${ago(last.at)}.` : `The last backup failed, ${ago(last.at)}.`) : apps ? "No copy yet." : "";
    const where = target.configured ? (target.where ? `Copies go to ${target.where}.` : "") : "Without a target nothing leaves the cell.";
    return [copy, where].filter(Boolean).join(" ");
  });
  const HISTORY = 30;
</script>

<header class="page-head">
  <div>
    <h1>Backups</h1>
    {#if lede}<p>{lede}</p>{/if}
  </div>
  {#if admin && target}
    <Button kind="outline" size="lg" onclick={() => (editing = true)}>{target.configured ? "Move the backups" : "Set a target"}</Button>
  {/if}
</header>

{#if failed}<p class="fail" role="alert">{failed}</p>{/if}
{#if failedCopies}<p class="fail" role="alert">{failedCopies}</p>{/if}

{#if target}
  <section class="sec" aria-labelledby="h-target">
    <div class="sec-head">
      <h2 id="h-target">Where the copies go</h2>
      <span>One target for the files and databases of every app.</span>
    </div>
    <div class="target">
      <div class="where">
        <Tile icon="backups" tone={target.configured ? "ok" : "warn"} size={40} />
        <span class="wn">
          <strong>{target.configured ? (target.kind ? kinds[target.kind] : "Target set") : "No target yet"}</strong>
          <small>{target.configured ? target.where : "Backups have nowhere to go, so no app is backed up."}</small>
        </span>
        {#if target.configured}<Tag tone="ok">in use</Tag>{:else}<Tag tone="warn">not set</Tag>{/if}
      </div>

      <div class="check">
        <span class="cl">Last check</span>
        {#if !check}
          <p>{target.configured ? "This target was set at the install; nobody checked another one here yet." : "Set one, and the cell checks it before it uses it."}</p>
        {:else}
          <div class="ch">
            {#if check.state === "passed"}
              <Tag tone="ok">{check.target === target.where ? "Passed, in use" : "Passed"}</Tag>
            {:else if check.state === "failed"}
              <Tag tone="warn">Failed, nothing changed</Tag>
            {:else}
              <Tag>Checking</Tag>
            {/if}
            <span class="tried mono">{check.target}</span>
          </div>
          {#if check.state === "checking"}
            <p>{check.message || "Writing a small test file and deleting it again. This takes a few seconds, at most two minutes."}</p>
          {:else if check.message}
            <p class:why={check.state === "failed"}>{check.message}</p>
          {/if}
          <small>Asked by {check.by}, {ago(check.at)}</small>
        {/if}
      </div>
    </div>
    {#if !admin}
      <p class="note">Admins (cell-admins) set the target. The cell checks it before it uses it.</p>
    {/if}
  </section>
{/if}

{#if apps}
  <section class="sec" aria-labelledby="h-apps">
    <div class="sec-head">
      <h2 id="h-apps">By app</h2>
      <span>{apps.length}</span>
    </div>
    <ul class="list">
      {#each perApp as p (p.app.name)}
        <li>
          <a class="app row" href={itemPath("/apps", p.app.name)}>
            <Logo app={p.app} title={title(p.app.name)} size={32} />
            <span class="an">
              <strong>{title(p.app.name)}</strong>
              <small>{p.count === 0 ? "No copy yet" : `${p.count} ${p.count === 1 ? "backup" : "backups"}`}</small>
            </span>
            {#if p.now}
              <Tag>{phases[p.now.phase]}</Tag>
            {/if}
            {#if p.ended}
              <Tag tone={tone(p.ended)}>{p.ended.phase === "Completed" ? "Backed up" : "Failed"}</Tag>
              <time class="mono" datetime={at(p.ended)}>{ago(at(p.ended))}</time>
            {:else if !p.now}
              <span class="never">never</span>
            {/if}
          </a>
        </li>
      {:else}
        <li class="empty">No app is installed yet, so there is nothing to back up.</li>
      {/each}
    </ul>
  </section>

  <section class="sec" aria-labelledby="h-history">
    <div class="sec-head">
      <h2 id="h-history">History</h2>
      <span>{backups.length > HISTORY ? `newest ${HISTORY} of ${backups.length}` : backups.length}</span>
    </div>
    {#if backups.length}
      <ul class="list">
        {#each backups.slice(0, HISTORY) as b (`${b.app}/${b.name}`)}
          <li class="hist">
            <time class="mono" datetime={at(b)}>{ago(at(b))}</time>
            <Logo app={byName.get(b.app) ?? { name: b.app }} title={title(b.app)} size={24} />
            <span class="ht">
              <span>{title(b.app)}{#if b.reason}<small>, {b.reason}</small>{/if}</span>
              {#if b.message && b.phase === "Failed"}<small class="msg">{b.message}</small>{/if}
            </span>
            <Tag tone={tone(b)}>{phases[b.phase]}</Tag>
          </li>
        {/each}
      </ul>
    {:else}
      <p class="empty plain">No backup ran yet.</p>
    {/if}
  </section>
{/if}

{#if !target && !apps && !failed && !failedCopies}
  <p class="empty plain">Reading the backups</p>
{/if}

{#if editing && target}
  <Drawer
    title={target.configured ? "Move the backups" : "Set a target"}
    subtitle="An S3 bucket. The cell checks it first: only if that works does the target change."
    onclose={close}
  >
    <form onsubmit={submit}>
      <fieldset class="choice two">
        <legend>Provider</legend>
        <label class:on={form.provider === "hetzner"}>
          <input type="radio" bind:group={form.provider} value="hetzner" />
          <strong>Hetzner Object Storage</strong><small>Germany and Finland</small>
        </label>
        <label class:on={form.provider === "other"}>
          <input type="radio" bind:group={form.provider} value="other" />
          <strong>Another S3 service</strong><small>endpoint and region</small>
        </label>
      </fieldset>
      {#if form.provider === "hetzner"}
        <fieldset class="choice pills">
          <legend>Location</legend>
          {#each hetznerRegions as r (r.id)}
            <label class:on={form.region === r.id}><input type="radio" bind:group={form.region} value={r.id} />{r.city} <small>{r.id}</small></label>
          {/each}
          <code class="endpoint mono">{hetznerEndpoint(form.region)}</code>
        </fieldset>
      {:else}
        <label class="field">Endpoint<input type="url" bind:value={form.endpoint} required placeholder="https://s3.example.com" autocomplete="off" spellcheck="false" /></label>
        <label class="field">Region, optional<input bind:value={form.otherRegion} placeholder="us-east-1" autocomplete="off" spellcheck="false" /></label>
        {#if plainHttp(form)}
          <label class="tick"><input type="checkbox" bind:checked={form.privateNetwork} />Plain http on a private network, on purpose <small>only to a private IP address</small></label>
        {/if}
      {/if}
      <label class="field">Bucket<input bind:value={form.bucket} required placeholder="cell-backups" autocomplete="off" spellcheck="false" /></label>
      <label class="field">Folder in the bucket, optional<input bind:value={form.prefix} placeholder={target.prefix || "the cell's domain"} autocomplete="off" spellcheck="false" /></label>
      <label class="field">Access key<input bind:value={form.accessKey} required autocomplete="off" spellcheck="false" /></label>
      <label class="field">Secret key<input type="password" bind:value={form.secretKey} required autocomplete="new-password" /></label>
      {#if target.configured}
        <p class="note">Backups made so far stay in the old target, and the cell restores only from the new one. Once the check passed, the next backups go to the new bucket.</p>
      {/if}
      {#if formError}<p class="fail" role="alert">{formError}</p>{/if}
      <div><Button type="submit" size="lg" disabled={sending || busy}>{busy ? "Checking..." : "Check and use"}</Button></div>
    </form>
  </Drawer>
{/if}

<style>
  .target {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1.2fr);
    border-radius: var(--radius-panel);
    border: 1px solid var(--color-line);
    background: var(--color-surface);
    overflow: hidden;
  }
  .where {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 22px 24px;
    background: var(--color-surface-2);
  }
  .wn {
    flex: 1;
    min-width: 0;
  }
  .wn strong,
  .wn small {
    display: block;
  }
  .wn strong {
    font-size: 16px;
    font-weight: 500;
  }
  .wn small {
    margin-top: 2px;
    font-size: 13px;
    color: var(--color-text);
    overflow-wrap: anywhere;
  }
  .check {
    display: grid;
    align-content: start;
    gap: 8px;
    padding: 22px 24px;
    border-left: 1px solid var(--color-hairline);
  }
  .cl {
    font-size: 13px;
    font-weight: 500;
    color: var(--color-text);
  }
  .ch {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px 12px;
  }
  .tried {
    font-size: 12.5px;
    overflow-wrap: anywhere;
  }
  .check p {
    margin: 0;
    color: var(--color-text);
  }
  .check p.why {
    padding: 8px 12px;
    border-radius: var(--radius-control);
    background: var(--color-warn-bg);
    color: var(--color-warn-text);
    overflow-wrap: anywhere;
  }
  .check small {
    font-size: 12.5px;
    color: var(--color-muted);
  }
  .note {
    margin: 12px 0 0;
    color: var(--color-text);
    font-size: 13px;
  }
  .app {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 14px 22px;
  }
  .an {
    flex: 1;
    min-width: 0;
  }
  .an strong,
  .an small {
    display: block;
  }
  .an strong {
    font-weight: 500;
  }
  .an small {
    font-size: 12.5px;
    color: var(--color-muted);
  }
  .app time,
  .never {
    flex: none;
    min-width: 88px;
    text-align: right;
    font-size: 12px;
    color: var(--color-muted);
  }
  .hist {
    display: flex;
    align-items: center;
    gap: 12px;
    min-height: 48px;
    padding: 10px 22px;
    font-size: 13px;
  }
  .hist time {
    flex: none;
    width: 110px;
    font-size: 12px;
    color: var(--color-muted);
  }
  .ht {
    flex: 1;
    min-width: 0;
  }
  .ht small {
    color: var(--color-muted);
  }
  .ht .msg {
    display: block;
    color: var(--color-warn-text);
    overflow-wrap: anywhere;
  }
  .empty.plain {
    padding: 0;
    margin-top: 24px;
  }
  form {
    display: grid;
    gap: 16px;
  }
  fieldset {
    margin: 0;
    padding: 0;
    border: 0;
  }
  legend {
    margin-bottom: 8px;
    font-size: 13px;
    color: var(--color-text);
  }
  .choice label {
    position: relative;
    cursor: pointer;
    border-radius: var(--radius-box);
    background: var(--color-surface-2);
    box-shadow: 0 0 0 1px var(--color-line);
  }
  .choice label.on {
    box-shadow: 0 0 0 2px var(--color-ink);
  }
  .choice input {
    position: absolute;
    opacity: 0;
    pointer-events: none;
  }
  .choice label:has(input:focus-visible) {
    outline: 2px solid var(--color-agent);
    outline-offset: 2px;
  }
  .two {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }
  .two legend {
    grid-column: 1 / -1;
  }
  .two label {
    display: grid;
    gap: 2px;
    padding: 14px 16px;
  }
  .two strong {
    font-weight: 500;
  }
  .two small {
    font-size: 12.5px;
    color: var(--color-muted);
  }
  .pills {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
  }
  .pills legend {
    width: 100%;
  }
  .pills label {
    display: inline-flex;
    align-items: baseline;
    gap: 6px;
    padding: 6px 12px;
    border-radius: var(--radius-control);
  }
  .pills small {
    color: var(--color-muted);
  }
  .endpoint {
    flex-basis: 100%;
    margin-top: 4px;
    font-size: 12px;
    color: var(--color-muted);
    overflow-wrap: anywhere;
  }
  .tick {
    display: flex;
    align-items: baseline;
    gap: 8px;
    font-size: 13px;
  }
  .tick small {
    color: var(--color-muted);
  }
  form .note {
    margin: 0;
    padding: 10px 14px;
    border-radius: var(--radius-control);
    background: var(--color-surface-2);
    border: 1px solid var(--color-line);
  }
  form .fail {
    margin: 0;
  }
  @media (max-width: 860px) {
    .target,
    .two {
      grid-template-columns: minmax(0, 1fr);
    }
    .check {
      border-left: 0;
      border-top: 1px solid var(--color-hairline);
    }
    .app,
    .hist {
      flex-wrap: wrap;
      padding: 12px 16px;
    }
    .hist time {
      width: auto;
    }
  }
</style>
