<script lang="ts">
  // A person: a round tile with their initials.
  let { name, size = 32 }: { name: string; size?: number } = $props();
  const initials = $derived(
    name.split(/[\s._@-]+/).filter(Boolean).map((w) => w[0]).join("").slice(0, 2).toUpperCase() || "?",
  );
</script>

<span class="avatar" style:--s="{size}px" aria-hidden="true">{initials}</span>

<style>
  .avatar {
    flex: none;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: var(--s);
    height: var(--s);
    border-radius: 50%;
    background: var(--color-line-strong);
    color: var(--color-ink);
    font-size: calc(var(--s) * 0.37);
    font-weight: 600;
  }
</style>
