<script lang="ts">
  // The console's top bar: mark and cell, the views as a pill row, tools on
  // the right. Knows nothing about the API; the caller passes the items.
  import type { Snippet } from "svelte";
  import Mark from "./Mark.svelte";

  type Item = { href: string; label: string };
  let {
    title,
    subtitle = "",
    items,
    current,
    onnavigate,
    tools,
  }: { title: string; subtitle?: string; items: Item[]; current: string; onnavigate: (href: string) => void; tools?: Snippet } = $props();

  function go(e: MouseEvent, href: string) {
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
    e.preventDefault();
    onnavigate(href);
  }
</script>

<header>
  <a class="brand" href="/" onclick={(e) => go(e, "/")}><Mark />{title}<small>{subtitle}</small></a>
  <nav aria-label="Main">
    {#each items as item (item.href)}
      <a href={item.href} aria-current={item.href === current ? "page" : undefined} onclick={(e) => go(e, item.href)}>{item.label}</a>
    {/each}
  </nav>
  <div class="tools">{@render tools?.()}</div>
</header>

<style>
  header {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 12px 16px;
  }
  .brand {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 10px 0;
    font: 600 17px var(--font-display);
    letter-spacing: -0.01em;
    text-decoration: none;
  }
  .brand small {
    font: 400 12.5px var(--font-sans);
    color: var(--color-muted);
  }
  nav {
    display: flex;
    gap: 2px;
    margin-inline: auto;
    padding: 4px;
    overflow-x: auto;
    min-width: 0;
    border-radius: var(--radius-pill);
    background: var(--nav-bg);
  }
  nav a {
    padding: 8px 15px;
    border-radius: var(--radius-pill);
    color: var(--nav-ink);
    font-weight: 600;
    font-size: 14.5px;
    text-decoration: none;
    white-space: nowrap;
    transition: background-color 0.2s, color 0.2s;
  }
  nav a:hover {
    color: var(--color-ink);
  }
  nav a[aria-current] {
    background: var(--nav-current-bg);
    color: var(--nav-current-ink);
  }
  .tools {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  @media (max-width: 600px) {
    nav {
      order: 3;
      flex-basis: 100%;
      margin-inline: 0;
    }
    .tools {
      margin-left: auto;
    }
  }
</style>
