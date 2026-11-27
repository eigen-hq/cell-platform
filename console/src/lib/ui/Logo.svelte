<script lang="ts">
  // An app's tile: its logo on a light tile in both tones, or its initials.
  // An agent at work on the app turns its edge into the moving ring.
  import { logo } from "../logos";
  let {
    app,
    title = "",
    size = 36,
    busy = false,
  }: { app: { name: string; spec?: unknown }; title?: string; size?: number; busy?: boolean } = $props();
  const url = $derived(logo(app));
  const initials = $derived((title || app.name).replace(/[^\p{L}\p{N} ]/gu, " ").trim().split(/\s+/).map((w) => w[0]).join("").slice(0, 2).toUpperCase());
</script>

<span class="logo" class:ring={busy} class:ring-fast={busy} style:--s="{size}px">
  {#if busy}<span class="ring-spin" aria-hidden="true"></span>{/if}
  <span class="face">
    {#if url}<img src={url} alt="" />{:else}<span class="ini" aria-hidden="true">{initials}</span>{/if}
  </span>
</span>

<style>
  .logo {
    flex: none;
    display: inline-flex;
    width: var(--s);
    height: var(--s);
    padding: 0;
    border-radius: calc(var(--s) * 0.28);
    box-shadow: 0 0 0 1px var(--color-line);
  }
  .logo.ring {
    padding: 1.5px;
    box-shadow: none;
    background: var(--color-agent-edge);
  }
  .face {
    position: relative;
    z-index: 1;
    flex: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    border-radius: inherit;
    background: var(--color-logo);
  }
  img {
    width: 61%;
    height: 61%;
    object-fit: contain;
  }
  .ini {
    font-size: calc(var(--s) * 0.33);
    font-weight: 600;
    letter-spacing: -0.02em;
    color: #6b6658;
  }
</style>
