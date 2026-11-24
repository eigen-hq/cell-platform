<script lang="ts">
  // A person as a round face, or an app as a small tile: initials on a
  // pastel picked from the name, so the same name always gets the same color.
  let { name, size = 30, tile = false }: { name: string; size?: 22 | 24 | 30 | 40; tile?: boolean } = $props();

  const fills = ["blue", "lavender", "rose", "sun", "leaf"];
  const parts = $derived(name.split(/[\s._-]+/).filter(Boolean));
  // An app with a one-word name gets two letters, so Nextcloud and Notes differ.
  const initials = $derived(
    tile && parts.length === 1 ? parts[0]!.slice(0, 2).replace(/^./, (c) => c.toUpperCase()) : parts.slice(0, 2).map((p) => p[0]!.toUpperCase()).join(""),
  );
  const fill = $derived(fills[[...name].reduce((h, c) => (h * 31 + c.charCodeAt(0)) >>> 0, 7) % fills.length]);
</script>

<span class="face s{size} {fill}" class:tile aria-hidden="true">{initials}</span>

<style>
  .face {
    display: grid;
    place-items: center;
    flex: none;
    border-radius: 50%;
    color: var(--avatar-ink);
    font: 700 12px/1 var(--font-sans);
  }
  .tile { border-radius: 6px; }
  .s22 { width: 22px; height: 22px; font-size: 9px; }
  .s24 { width: 24px; height: 24px; font-size: 10px; }
  .s30 { width: 30px; height: 30px; }
  .s40 { width: 40px; height: 40px; font-size: 15px; }
  .blue { background: var(--avatar-blue); }
  .lavender { background: var(--avatar-lavender); }
  .rose { background: var(--avatar-rose); }
  .sun { background: var(--avatar-sun); }
  .leaf { background: var(--avatar-leaf); }
</style>
