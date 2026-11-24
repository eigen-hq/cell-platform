<script lang="ts">
  import Avatar from "../lib/ui/Avatar.svelte";
  import type { Caller } from "../lib/api/client";

  let { me, cell }: { me: Caller; cell: string } = $props();
</script>

<section>
  <Avatar name={me.username} size={40} />
  <h1>Hello, {me.username}.<br /><span>You run {cell}.</span></h1>
  <p>Signed in as {me.role}. The views of the console arrive here one by one, each with the part of the platform it shows.</p>
</section>

<style>
  section {
    position: relative;
    isolation: isolate; /* keeps the glow above the page paint */
    display: grid;
    justify-items: start;
    gap: 18px;
    padding-top: 9vh;
  }
  /* the one soft glow of the brand, behind the greeting */
  section::before {
    content: "";
    position: absolute;
    z-index: -1;
    top: 40px;
    right: 4%;
    width: min(520px, 70vw);
    height: 300px;
    border-radius: 50%;
    background: var(--paint-glow);
    filter: blur(30px);
    opacity: 0.55;
    pointer-events: none;
  }
  h1 {
    margin: 0;
    font: 600 clamp(2.4rem, 5.2vw, 4.3rem) / 1 var(--font-display);
    letter-spacing: -0.038em;
    text-wrap: balance;
  }
  h1 span {
    color: var(--color-muted);
  }
  p {
    margin: 0;
    max-width: 60ch;
    color: var(--color-muted);
    font-size: 1.15rem;
  }
</style>
