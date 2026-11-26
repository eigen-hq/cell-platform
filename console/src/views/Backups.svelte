<script lang="ts">
  // Where the backups go (E-47): the path out of the cell, the last check
  // of a target, and, for admins, setting an S3 target. The cell checks a
  // new target before it uses it. Shows only what the API delivers.
  import { onMount } from "svelte";
  import { api, must } from "../lib/api/client";
  import type { components } from "../lib/api/schema";
  import { checking, hetznerEndpoint, hetznerRegions, plainHttp, requestOf, shown, type TargetForm } from "../lib/backup";
  import { newest, STALE } from "../lib/newest";
  import { ago } from "../lib/time";
  import Button from "../lib/ui/Button.svelte";
  import StatStrip, { type Stat } from "../lib/ui/StatStrip.svelte";
  import Status from "../lib/ui/Status.svelte";

  type Target = components["schemas"]["BackupTarget"];

  let { admin }: { admin: boolean } = $props();
  let polled = $state<Target | null>(null);
  // The answer to the admin's request, until a poll shows that check or a later one.
  let asked = $state<Target | null>(null);
  let failed = $state("");

  const guard = newest();
  async function load() {
    try {
      const answer = await guard(api.GET("/v0/backup-target"));
      if (answer === STALE) return;
      polled = must(answer);
      failed = "";
    } catch (e) {
      failed = e instanceof Error ? e.message : String(e);
    }
  }

  let target = $derived(shown(polled, asked));
  let busy = $derived(checking(target));
  onMount(() => void load());
  // Quick while a check runs, slow otherwise.
  $effect(() => {
    const t = setInterval(load, busy ? 2500 : 15000);
    return () => clearInterval(t);
  });

  let form = $state<TargetForm>({ provider: "hetzner", region: "fsn1", endpoint: "", otherRegion: "", bucket: "", accessKey: "", secretKey: "", prefix: "", privateNetwork: false });
  let sending = $state(false);
  let formError = $state("");

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (sending) return;
    sending = true;
    formError = "";
    try {
      asked = must(await api.PUT("/v0/backup-target", { body: requestOf(form) }));
      form.secretKey = "";
    } catch (err) {
      formError = err instanceof Error ? err.message : String(err);
    } finally {
      sending = false;
    }
  }

  const kinds = { s3: "S3 bucket", local: "directory on the first machine", other: "rclone remote" } as const;
  let check = $derived(target?.check);
  let wire = $derived(busy ? "wait" : check?.state === "failed" ? "bad" : target?.configured ? "ok" : "none");
  let stats = $derived.by((): Stat[] => {
    if (!target) return [];
    return [
      { label: "Backups", value: target.configured ? "leave the cell" : "have nowhere to go", tone: target.configured ? "ok" : "bad" },
      { label: "Target", value: target.kind ? kinds[target.kind] : "not set", tone: target.configured ? "ok" : "none" },
      {
        label: "Last check",
        value: !check ? "none yet" : check.state === "checking" ? "running" : `${check.state} ${ago(check.at)}`,
        tone: !check ? "none" : check.state === "passed" ? "ok" : check.state === "failed" ? "bad" : "wait",
      },
    ];
  });
</script>

<section class="view-head">
  <h1>Backups</h1>
  <p>The cell copies the files and databases of every app out of the cell, to one target. Apps never see its keys: each gets its own, for its own corner.</p>
</section>

{#if failed}<p class="fail" role="alert">{failed}</p>{/if}
{#if target}
  <StatStrip items={stats} />

  <section class="map" aria-label="The path of the backups">
    <div class="cap">
      <span>Out of the cell</span>
      <span class="legend"><i class="ok"></i>in use <i class="wait"></i>being checked <i class="bad"></i>no target or failed</span>
    </div>
    <div class="topo">
      <div class="node">
        <span class="ico"><svg viewBox="0 0 24 24"><rect x="3.5" y="4" width="7" height="7" rx="2" /><rect x="13.5" y="4" width="7" height="7" rx="2" /><rect x="3.5" y="14" width="7" height="6" rx="2" /><ellipse cx="17" cy="15.2" rx="3.5" ry="1.6" /><path d="M13.5 15.2v3.4c0 .9 1.6 1.6 3.5 1.6s3.5-.7 3.5-1.6v-3.4" /></svg></span>
        <strong>Apps</strong><small>files and databases</small>
      </div>
      <div class="wire ok"><span>a key per app</span></div>
      <div class="node">
        <span class="ico"><svg viewBox="0 0 24 24"><path d="M12 3.5 19 6v5.5c0 4.2-3 7.7-7 9-4-1.3-7-4.8-7-9V6z" /><path d="M9.2 12.2 11.2 14l3.8-4" /></svg></span>
        <strong>Backup gateway</strong><small>holds the target's keys</small>
      </div>
      <div class="wire {wire}" class:still={wire === "none"}><span>{busy ? "checking" : target.configured ? "encrypted" : "nowhere"}</span></div>
      {#if target.configured || busy}
        <div class="node far {wire}">
          <span class="ico"><svg viewBox="0 0 24 24"><ellipse cx="12" cy="6" rx="7" ry="2.5" /><path d="M5 6v12c0 1.4 3.1 2.5 7 2.5s7-1.1 7-2.5V6M5 12c0 1.4 3.1 2.5 7 2.5s7-1.1 7-2.5" /></svg></span>
          <strong>{busy ? "New target" : target.kind ? kinds[target.kind] : "Target"}</strong>
          <small class="where">{busy ? check?.target : target.where}</small>
        </div>
      {:else}
        <div class="ghost">no target yet</div>
      {/if}
    </div>
  </section>

  <section class="band">
    <div class="card">
      <h2 class="sec-title">Last check</h2>
      {#if !check}
        <div class="verdict none">
          <svg viewBox="0 0 48 48" aria-hidden="true"><circle cx="24" cy="24" r="19" /><path d="M24 15v10" /><circle cx="24" cy="32" r="1.4" /></svg>
          <p>{target.configured ? "This target was set at the install; nobody checked another one here yet." : "Without a target nothing leaves the cell. Set one, and the cell checks it before it uses it."}</p>
        </div>
      {:else}
        <div class="verdict {check.state}">
          {#if check.state === "passed"}
            <svg viewBox="0 0 48 48" aria-hidden="true"><circle cx="24" cy="24" r="19" /><path d="M15.5 24.5l6 6 11-12" /></svg>
          {:else if check.state === "failed"}
            <svg viewBox="0 0 48 48" aria-hidden="true"><circle cx="24" cy="24" r="19" /><path d="M17 17l14 14M31 17 17 31" /></svg>
          {:else}
            <svg class="spin" viewBox="0 0 48 48" aria-hidden="true"><circle cx="24" cy="24" r="19" /><path d="M24 5a19 19 0 0 1 19 19" /></svg>
          {/if}
          <div>
            <Status tone={check.state === "passed" ? "ok" : check.state === "failed" ? "bad" : "wait"} label={{ checking: "Checking", passed: check.target === target.where ? "Passed, in use" : "Passed", failed: "Failed, nothing changed" }[check.state]} />
            <p class="tried">{check.target}</p>
            {#if check.state === "checking"}
              <p>{check.message || "Writing a small test file and deleting it again. This takes a few seconds, at most two minutes."}</p>
              <span class="progress" aria-hidden="true"><span></span></span>
            {:else if check.message}
              <p class="why">{check.message}</p>
            {/if}
            <small>asked by {check.by}, {ago(check.at)}</small>
          </div>
        </div>
      {/if}
    </div>

    <div class="card">
      <h2 class="sec-title">{target.configured ? "Move the backups" : "Set a target"} <small>an S3 bucket</small></h2>
      {#if admin}
        <form onsubmit={submit}>
          <fieldset class="providers">
            <legend class="vh">Provider</legend>
            <label class="tile" class:on={form.provider === "hetzner"}>
              <input type="radio" bind:group={form.provider} value="hetzner" />
              <strong>Hetzner Object Storage</strong><small>Germany and Finland</small>
            </label>
            <label class="tile" class:on={form.provider === "other"}>
              <input type="radio" bind:group={form.provider} value="other" />
              <strong>Another S3 service</strong><small>endpoint and region</small>
            </label>
          </fieldset>
          {#if form.provider === "hetzner"}
            <fieldset class="regions">
              <legend>Location</legend>
              {#each hetznerRegions as r (r.id)}
                <label class="pick" class:on={form.region === r.id}><input type="radio" bind:group={form.region} value={r.id} />{r.city} <small>{r.id}</small></label>
              {/each}
              <code class="endpoint">{hetznerEndpoint(form.region)}</code>
            </fieldset>
          {:else}
            <label>Endpoint<input type="url" bind:value={form.endpoint} required placeholder="https://s3.example.com" autocomplete="off" spellcheck="false" /></label>
            <label>Region <small>optional</small><input bind:value={form.otherRegion} placeholder="us-east-1" autocomplete="off" spellcheck="false" /></label>
            {#if plainHttp(form)}
              <label class="pick"><input type="checkbox" bind:checked={form.privateNetwork} />Plain http on a private network, on purpose <small>only to a private IP address</small></label>
            {/if}
          {/if}
          <label>Bucket<input bind:value={form.bucket} required placeholder="cell-backups" autocomplete="off" spellcheck="false" /></label>
          <label>Folder in the bucket <small>optional</small><input bind:value={form.prefix} placeholder={target.prefix || "the cell's domain"} autocomplete="off" spellcheck="false" /></label>
          <label>Access key<input bind:value={form.accessKey} required autocomplete="off" spellcheck="false" /></label>
          <label>Secret key<input type="password" bind:value={form.secretKey} required autocomplete="new-password" /></label>
          {#if target.configured}
            <p class="note">Backups made so far stay in the old target, and the cell restores only from the new one. Once the check passed, the next backups go to the new bucket.</p>
          {/if}
          <div class="row">
            <Button type="submit" disabled={sending || busy}>{busy ? "Checking..." : "Check and use"}</Button>
            {#if formError}<p class="fail" role="alert">{formError}</p>{/if}
          </div>
        </form>
      {:else}
        <p class="note">Admins (cell-admins) set the target. The cell checks it before it uses it.</p>
      {/if}
    </div>
  </section>
{/if}

<style>
  .map {
    position: relative;
    margin-top: 44px;
    padding: 16px 20px 30px;
    border-radius: var(--radius-panel);
    background: var(--color-panel);
  }
  .map::before {
    content: "";
    position: absolute;
    z-index: -1;
    top: -60px;
    right: 6%;
    width: 480px;
    height: 220px;
    border-radius: 50%;
    background: var(--paint-glow);
    filter: blur(40px);
    opacity: 0.45;
  }
  .cap {
    display: flex;
    flex-wrap: wrap;
    justify-content: space-between;
    gap: 8px;
    color: var(--color-muted);
    font-size: 13px;
    font-weight: 600;
  }
  .legend {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    color: var(--color-dim);
    font-weight: 400;
  }
  .legend i {
    display: inline-block;
    width: 14px;
    height: 2px;
    margin-left: 6px;
  }
  .legend .ok { background: var(--color-ok); }
  .legend .wait { background: var(--color-wait); }
  .legend .bad { background: var(--color-bad); }
  .topo {
    display: grid;
    grid-template-columns: auto minmax(70px, 1fr) auto minmax(70px, 1fr) minmax(0, auto);
    align-items: center;
    margin-top: 34px;
  }
  .node {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    column-gap: 12px;
    align-items: center;
    padding: 12px 16px 12px 12px;
    border-radius: 12px;
    background: var(--color-sunk);
  }
  .node .ico {
    grid-row: span 2;
  }
  .node strong {
    font-size: 14px;
    font-weight: 600;
  }
  .node small {
    color: var(--color-dim);
    font-size: 12px;
  }
  .node .where {
    max-width: 30ch;
    overflow-wrap: anywhere;
  }
  .far.ok { outline: 2px solid color-mix(in srgb, var(--color-ok) 55%, transparent); }
  .far.wait { outline: 2px dashed var(--color-wait); }
  .far.bad { outline: 2px solid color-mix(in srgb, var(--color-bad) 65%, transparent); }
  .ico {
    display: grid;
    place-items: center;
    width: 40px;
    height: 40px;
    border-radius: 12px;
    background: color-mix(in srgb, var(--color-info) 18%, transparent);
    color: var(--color-info);
  }
  .far .ico {
    background: color-mix(in srgb, var(--color-wait) 20%, transparent);
    color: var(--color-wait);
  }
  .ico svg {
    width: 22px;
    height: 22px;
    fill: none;
    stroke: currentColor;
    stroke-width: 1.6;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .ghost {
    padding: 20px 18px;
    border: 2px dashed color-mix(in srgb, var(--color-bad) 60%, transparent);
    border-radius: 12px;
    color: var(--color-dim);
    font-size: 12.6px;
    font-weight: 600;
  }
  .wire {
    --c: var(--color-dim);
    position: relative;
    height: 2px;
    background: linear-gradient(90deg, var(--c) 0 5px, transparent 5px) 0 0 / 10px 2px;
    animation: flow 1.6s linear infinite;
  }
  .wire.ok { --c: var(--color-ok); }
  .wire.wait { --c: var(--color-wait); animation-duration: 0.6s; }
  .wire.bad { --c: var(--color-bad); }
  .wire.still { animation: none; opacity: 0.6; }
  .wire span {
    position: absolute;
    bottom: 7px;
    left: 50%;
    translate: -50%;
    color: var(--color-muted);
    font-size: 11.5px;
    font-weight: 600;
    white-space: nowrap;
  }
  @keyframes flow {
    to { background-position: 10px 0; }
  }

  .band {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1.2fr);
    gap: 12px;
    margin-top: 52px;
  }
  .card {
    display: grid;
    align-content: start;
    gap: 14px;
    padding: 18px;
    border-radius: var(--radius-panel);
    background: var(--color-panel);
  }
  .card .sec-title {
    margin: 0;
  }
  .verdict {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    gap: 18px;
    align-items: start;
    padding: 18px;
    border-radius: 16px;
    background: var(--color-sunk);
  }
  .verdict svg {
    width: 52px;
    height: 52px;
    fill: none;
    stroke: currentColor;
    stroke-width: 3;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .verdict.passed { color: var(--color-ok); }
  .verdict.failed { color: var(--color-bad); }
  .verdict.checking { color: var(--color-wait); }
  .verdict.none { color: var(--color-dim); }
  .verdict div {
    display: grid;
    gap: 6px;
    color: var(--color-ink);
  }
  .verdict p {
    margin: 0;
    color: var(--color-muted);
  }
  .verdict .tried {
    color: var(--color-ink);
    font-weight: 600;
    overflow-wrap: anywhere;
  }
  .verdict .why {
    padding: 8px 12px;
    border-left: 3px solid var(--color-bad);
    border-radius: 6px;
    background: color-mix(in srgb, var(--color-bad) 10%, transparent);
    color: var(--color-ink);
    font-size: 14px;
    overflow-wrap: anywhere;
  }
  .verdict small {
    color: var(--color-dim);
    font-size: 12.5px;
  }
  .spin {
    animation: turn 1.1s linear infinite;
  }
  .spin circle {
    opacity: 0.25;
  }
  @keyframes turn {
    to { rotate: 360deg; }
  }
  .progress {
    display: block;
    height: 5px;
    border-radius: 3px;
    background: color-mix(in srgb, var(--color-ink) 14%, transparent);
    overflow: hidden;
  }
  .progress span {
    display: block;
    width: 35%;
    height: 100%;
    border-radius: inherit;
    background: var(--color-wait);
    animation: slide 1.4s ease-in-out infinite;
  }
  @keyframes slide {
    from { translate: -100% 0; }
    to { translate: 300% 0; }
  }

  form {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
    gap: 12px;
  }
  fieldset {
    grid-column: 1 / -1;
    margin: 0;
    padding: 0;
    border: 0;
  }
  .providers {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }
  .tile {
    display: grid;
    gap: 2px;
    padding: 14px 16px;
    border-radius: 16px;
    background: var(--color-sunk);
    outline: 2px solid transparent;
    cursor: pointer;
  }
  .tile.on {
    outline-color: var(--color-ink);
  }
  .tile input,
  .pick input {
    position: absolute;
    opacity: 0;
    pointer-events: none;
  }
  .tile:has(input:focus-visible),
  .pick:has(input:focus-visible) {
    outline: 2px solid var(--color-focus);
    outline-offset: 2px;
  }
  .tile small {
    color: var(--color-dim);
    font-size: 12.5px;
  }
  .regions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
  }
  legend {
    width: 100%;
    margin-bottom: 6px;
    font-size: 13.5px;
    font-weight: 600;
  }
  .pick {
    position: relative;
    display: inline-flex;
    align-items: baseline;
    gap: 6px;
    padding: 6px 14px;
    border-radius: var(--radius-pill);
    background: var(--color-sunk);
    font-size: 14px;
    font-weight: 600;
    cursor: pointer;
  }
  .pick small {
    color: var(--color-dim);
    font-weight: 500;
  }
  .pick.on {
    background: var(--color-selected);
    color: var(--color-on-selected);
  }
  .pick.on small {
    color: inherit;
    opacity: 0.7;
  }
  .endpoint {
    color: var(--color-muted);
    font-size: 12.5px;
    overflow-wrap: anywhere;
  }
  form > label {
    display: grid;
    gap: 6px;
    font-size: 13.5px;
    font-weight: 600;
  }
  form > label small {
    color: var(--color-dim);
    font-weight: 500;
  }
  input {
    width: 100%;
    padding: 9px 12px;
    border: 2px solid var(--color-line);
    border-radius: var(--radius-part);
    background: var(--color-sunk);
    color: inherit;
    font: inherit;
  }
  .note {
    grid-column: 1 / -1;
    margin: 0;
    padding: 10px 14px;
    border-left: 4px solid var(--shelf-sun);
    border-radius: var(--radius-part);
    background: var(--color-sunk);
    color: var(--color-muted);
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 12px;
    grid-column: 1 / -1;
  }
  .row .fail {
    margin: 0;
  }
  @media (max-width: 900px) {
    .topo {
      grid-template-columns: 1fr;
      justify-items: start;
    }
    .wire {
      width: 2px;
      height: 34px;
      margin-left: 30px;
      background: linear-gradient(var(--c) 0 5px, transparent 5px) 0 0 / 2px 10px;
    }
    .wire span {
      bottom: auto;
      top: 50%;
      left: 12px;
      translate: 0 -50%;
    }
    .band,
    .providers {
      grid-template-columns: 1fr;
    }
  }
</style>
