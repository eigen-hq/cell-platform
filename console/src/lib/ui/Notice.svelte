<script lang="ts">
  // A whole-page state: signing in, refused, something broke.
  import type { Snippet } from "svelte";
  import Tile from "./Tile.svelte";

  let { title, children }: { title: string; children?: Snippet } = $props();
</script>

<main>
  <Tile icon="cell" size={40} />
  <h1>{title}</h1>
  {@render children?.()}
</main>

<style>
  main {
    display: grid;
    justify-items: start;
    gap: 18px;
    max-width: 560px;
    margin: 18vh auto 0;
    padding: 0 24px;
  }
  h1 {
    margin: 0;
    font-size: 40px;
    line-height: 1.05;
    font-weight: 500;
    letter-spacing: -0.035em;
    text-wrap: balance;
  }
  main :global(p) {
    margin: 0;
    color: var(--color-text);
    font-size: 16px;
  }
</style>
