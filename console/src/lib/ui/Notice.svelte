<script lang="ts">
  // A whole-page state: signing in, refused, something broke.
  import type { Snippet } from "svelte";
  import Mark from "./Mark.svelte";

  let { title, children }: { title: string; children?: Snippet } = $props();
</script>

<main>
  <Mark />
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
    font: 600 clamp(2rem, 4vw, 3.1rem) / 1.02 var(--font-display);
    letter-spacing: -0.035em;
    text-wrap: balance;
  }
  main :global(p) {
    margin: 0;
    color: var(--color-muted);
    font-size: 1.1rem;
  }
</style>
