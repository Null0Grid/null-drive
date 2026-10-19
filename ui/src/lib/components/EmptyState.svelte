<script lang="ts">
  import type { Snippet } from 'svelte';
  import Icon, { type IconName } from './Icon.svelte';

  interface Props {
    icon?: IconName;
    title: string;
    message?: string;
    tone?: 'default' | 'danger';
    children?: Snippet;
  }
  let { icon = 'folder', title, message, tone = 'default', children }: Props = $props();
</script>

<div class="empty {tone}">
  <div class="badge"><Icon name={icon} size={30} strokeWidth={1.5} /></div>
  <h3>{title}</h3>
  {#if message}<p>{message}</p>{/if}
  {#if children}<div class="actions">{@render children()}</div>{/if}
</div>

<style>
  .empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    text-align: center;
    gap: var(--space-3);
    padding: var(--space-7) var(--space-4);
    color: var(--text-muted);
  }
  .badge {
    display: grid;
    place-items: center;
    width: 4rem;
    height: 4rem;
    border-radius: var(--radius-lg);
    border: 1px dashed var(--border-strong);
    background: var(--panel);
    color: var(--text-faint);
    margin-bottom: var(--space-2);
  }
  .danger .badge {
    color: var(--danger);
    border-color: var(--danger);
  }
  h3 {
    font-size: var(--text-md);
    color: var(--text);
    text-transform: uppercase;
  }
  p {
    max-width: 34ch;
    font-size: var(--text-sm);
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    justify-content: center;
    gap: var(--space-2);
    margin-top: var(--space-2);
  }
</style>
