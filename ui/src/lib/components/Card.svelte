<script lang="ts">
  import type { Snippet } from 'svelte';

  interface Props {
    title?: string;
    subtitle?: string;
    /** Neon edge: use for the one panel that is "live" or needs attention. */
    glow?: boolean;
    padded?: boolean;
    tone?: 'default' | 'danger';
    actions?: Snippet;
    children?: Snippet;
    class?: string;
  }
  let { title, subtitle, glow = false, padded = true, tone = 'default', actions, children, class: className = '' }: Props =
    $props();
  const uid = $props.id();
</script>

<section class="card {tone} {className}" class:glow class:padded aria-labelledby={title ? `card-${uid}` : undefined}>
  {#if title || actions}
    <header>
      <div class="titles">
        {#if title}<h2 id="card-{uid}">{title}</h2>{/if}
        {#if subtitle}<p class="subtitle">{subtitle}</p>{/if}
      </div>
      {#if actions}<div class="actions">{@render actions()}</div>{/if}
    </header>
  {/if}
  {@render children?.()}
</section>

<style>
  .card {
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    min-width: 0;
  }
  .padded {
    padding: var(--space-5);
  }
  .glow {
    border-color: var(--primary-line);
    box-shadow: var(--glow-primary);
  }
  .danger {
    border-color: var(--danger-soft);
  }
  header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--space-3);
    margin-bottom: var(--space-4);
  }
  .titles {
    min-width: 0;
  }
  h2 {
    font-size: var(--text-md);
    color: var(--text);
    text-transform: uppercase;
  }
  .danger h2 {
    color: var(--danger);
  }
  .subtitle {
    margin-top: var(--space-1);
    color: var(--text-muted);
    font-size: var(--text-sm);
  }
  .actions {
    display: flex;
    gap: var(--space-2);
    flex: none;
  }
</style>
