<script lang="ts">
  import type { Toast } from '../toast.svelte';
  import Icon, { type IconName } from './Icon.svelte';

  interface Props {
    toast: Toast;
    ondismiss: (id: number) => void;
  }
  let { toast, ondismiss }: Props = $props();

  const icons: Record<Toast['kind'], IconName> = { info: 'info', success: 'check', warning: 'alert', error: 'alert' };
</script>

<div class="toast {toast.kind}" role={toast.kind === 'error' ? 'alert' : 'status'}>
  <span class="icon"><Icon name={icons[toast.kind]} /></span>
  <p class="message">{toast.message}</p>
  {#if toast.action}
    <button
      class="action"
      type="button"
      onclick={() => {
        toast.action?.run();
        ondismiss(toast.id);
      }}>{toast.action.label}</button
    >
  {/if}
  <button class="dismiss" type="button" aria-label="Dismiss notification" onclick={() => ondismiss(toast.id)}>
    <Icon name="x" size={16} />
  </button>
</div>

<style>
  .toast {
    --tone: var(--primary);
    display: flex;
    align-items: flex-start;
    gap: var(--space-3);
    width: min(24rem, calc(100vw - 2rem));
    padding: var(--space-3) var(--space-3) var(--space-3) var(--space-4);
    background: var(--panel);
    border: 1px solid var(--border-strong);
    border-left: 3px solid var(--tone);
    border-radius: var(--radius-md);
    box-shadow: var(--shadow-overlay);
    animation: in var(--dur) var(--ease);
    pointer-events: auto;
  }
  .success {
    --tone: var(--ok);
  }
  .warning {
    --tone: var(--warn);
  }
  .error {
    --tone: var(--danger);
  }
  .icon {
    color: var(--tone);
    display: flex;
    padding-top: 2px;
  }
  .message {
    flex: 1;
    font-size: var(--text-sm);
    overflow-wrap: anywhere;
    padding-top: 1px;
  }
  .action {
    flex: none;
    border: 0;
    background: none;
    color: var(--primary);
    font-weight: 700;
    font-size: var(--text-sm);
    cursor: pointer;
    padding: 0 var(--space-1);
  }
  .dismiss {
    flex: none;
    display: grid;
    place-items: center;
    width: 1.75rem;
    height: 1.75rem;
    border: 0;
    border-radius: var(--radius-sm);
    background: none;
    color: var(--text-faint);
    cursor: pointer;
  }
  .dismiss:hover {
    color: var(--text);
    background: var(--surface-hover);
  }
  @keyframes in {
    from {
      opacity: 0;
      transform: translateY(8px);
    }
  }
</style>
