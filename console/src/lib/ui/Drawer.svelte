<script lang="ts">
  // A panel that slides in from the right over the view, on a native modal
  // dialog (focus trap, Escape). Closing goes through onclose, so the caller
  // decides (for example the URL). Knows nothing about the API.
  import type { Snippet } from "svelte";

  let { title, subtitle = "", onclose, children }: { title: string; subtitle?: string; onclose: () => void; children: Snippet } = $props();
  let dialog: HTMLDialogElement;

  $effect(() => {
    dialog.showModal();
  });
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
<dialog
  bind:this={dialog}
  aria-label={title}
  oncancel={(e) => { e.preventDefault(); onclose(); }}
  onclick={(e) => e.target === dialog && onclose()}
>
  <div class="inner">
    <header>
      <div>
        <h2>{title}</h2>
        {#if subtitle}<p>{subtitle}</p>{/if}
      </div>
      <button class="close" onclick={onclose} aria-label="Close">
        <svg viewBox="0 0 24 24"><path d="M6 6l12 12M18 6L6 18" /></svg>
      </button>
    </header>
    {@render children()}
  </div>
</dialog>

<style>
  dialog {
    position: fixed;
    inset: 0 0 0 auto;
    width: min(600px, 100vw);
    height: 100vh;
    max-height: none;
    margin: 0;
    padding: 0;
    overflow-y: auto;
    overscroll-behavior: contain;
    border: 0;
    border-radius: var(--radius-panel) 0 0 var(--radius-panel);
    background: var(--color-panel);
    color: var(--color-ink);
    animation: slide 0.28s cubic-bezier(0.2, 0.8, 0.2, 1);
  }
  dialog::backdrop {
    background: color-mix(in srgb, var(--color-base) 62%, transparent);
  }
  /* One column no content can widen past the panel. */
  .inner {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 26px;
    padding: 22px 22px 40px;
  }
  header {
    display: flex;
    justify-content: space-between;
    gap: 16px;
  }
  h2 {
    margin: 0;
    font: 600 1.6rem / 1.15 var(--font-display);
    letter-spacing: -0.02em;
  }
  p {
    margin: 4px 0 0;
    color: var(--color-muted);
    font-size: 13.5px;
    overflow-wrap: anywhere;
  }
  .close {
    display: grid;
    place-items: center;
    flex: none;
    width: 36px;
    height: 36px;
    border: 0;
    border-radius: 50%;
    background: var(--color-sunk);
  }
  .close svg {
    width: 16px;
    height: 16px;
    fill: none;
    stroke: currentColor;
    stroke-width: 2;
    stroke-linecap: round;
  }
  @keyframes slide {
    from { transform: translateX(40px); opacity: 0; }
  }
</style>
