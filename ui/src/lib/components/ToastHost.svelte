<script lang="ts">
  import { toast } from '../toast.svelte';
  import Toast from './Toast.svelte';
</script>

<!-- One live region for all notifications; mount once near the app root. -->
<div class="host" aria-live="polite" aria-relevant="additions">
  {#each toast.items as t (t.id)}
    <Toast toast={t} ondismiss={(id) => toast.dismiss(id)} />
  {/each}
</div>

<style>
  /* Bottom-centre on wide screens: clear of the page's primary actions
     (top right) and of panels docked in the corners. */
  .host {
    position: fixed;
    left: 50%;
    bottom: var(--space-5);
    transform: translateX(-50%);
    z-index: var(--z-toast);
    display: flex;
    flex-direction: column-reverse;
    gap: var(--space-2);
    align-items: center;
    pointer-events: none;
  }
  @media (max-width: 720px) {
    /* Phones: top, because the tab bar and upload panel own the bottom. */
    .host {
      left: var(--space-3);
      right: var(--space-3);
      bottom: auto;
      top: calc(var(--space-3) + env(safe-area-inset-top));
      transform: none;
      flex-direction: column;
      align-items: stretch;
    }
  }
</style>
