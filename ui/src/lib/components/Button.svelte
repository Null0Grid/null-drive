<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLButtonAttributes } from 'svelte/elements';
  import Icon, { type IconName } from './Icon.svelte';

  interface Props extends HTMLButtonAttributes {
    variant?: 'primary' | 'secondary' | 'ghost' | 'danger';
    size?: 'sm' | 'md' | 'lg';
    icon?: IconName;
    /** Icon-only button: requires `label` for screen readers. */
    iconOnly?: boolean;
    label?: string;
    loading?: boolean;
    full?: boolean;
    children?: Snippet;
  }

  let {
    variant = 'secondary',
    size = 'md',
    icon,
    iconOnly = false,
    label,
    loading = false,
    full = false,
    type = 'button',
    disabled = false,
    children,
    class: className = '',
    ...rest
  }: Props = $props();
</script>

<button
  {type}
  class="btn {variant} {size} {className}"
  class:icon-only={iconOnly}
  class:full
  class:loading
  disabled={disabled || loading}
  aria-busy={loading || undefined}
  aria-label={iconOnly ? label : rest['aria-label']}
  title={iconOnly ? label : rest.title}
  {...rest}
>
  {#if loading}
    <span class="spinner" aria-hidden="true"></span>
  {:else if icon}
    <Icon name={icon} size={size === 'sm' ? 16 : 18} />
  {/if}
  {#if !iconOnly}
    <span class="text">{#if children}{@render children()}{:else}{label}{/if}</span>
  {/if}
</button>

<style>
  .btn {
    --h: 2.375rem;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: var(--space-2);
    height: var(--h);
    min-width: var(--h);
    padding: 0 var(--space-4);
    border: 1px solid var(--border-strong);
    border-radius: var(--radius-md);
    background: var(--surface);
    color: var(--text);
    font-size: var(--text-sm);
    font-weight: 500;
    letter-spacing: 0.01em;
    white-space: nowrap;
    cursor: pointer;
    user-select: none;
    transition:
      background var(--dur-fast) var(--ease),
      border-color var(--dur-fast) var(--ease),
      box-shadow var(--dur) var(--ease),
      color var(--dur-fast) var(--ease);
  }
  .sm {
    --h: 2rem;
    padding: 0 var(--space-3);
    font-size: var(--text-xs);
  }
  .lg {
    --h: 2.875rem;
    padding: 0 var(--space-5);
    font-size: var(--text-md);
  }
  .icon-only {
    padding: 0;
    width: var(--h);
  }
  .full {
    width: 100%;
  }
  .btn:hover:not(:disabled) {
    background: var(--surface-hover);
    border-color: var(--primary-line);
  }
  .btn:active:not(:disabled) {
    background: var(--surface-active);
  }
  .btn:focus-visible {
    outline: none;
    box-shadow: var(--glow-primary);
    border-color: var(--primary);
  }
  .btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .btn.loading {
    opacity: 0.85;
    cursor: progress;
  }

  .primary {
    background: var(--primary);
    border-color: var(--primary);
    color: var(--primary-ink);
    font-weight: 700;
  }
  .primary:hover:not(:disabled) {
    background: var(--primary-hover);
    border-color: var(--primary-hover);
    box-shadow: var(--glow-primary);
  }
  .primary:active:not(:disabled) {
    background: var(--primary);
  }

  .ghost {
    background: transparent;
    border-color: transparent;
    color: var(--text-muted);
  }
  .ghost:hover:not(:disabled) {
    background: var(--surface-hover);
    border-color: transparent;
    color: var(--text);
  }

  .danger {
    background: var(--danger-soft);
    border-color: var(--danger);
    color: var(--danger);
  }
  .danger:hover:not(:disabled) {
    background: var(--danger);
    border-color: var(--danger);
    color: var(--danger-ink);
  }

  .spinner {
    width: 1em;
    height: 1em;
    border: 2px solid currentColor;
    border-right-color: transparent;
    border-radius: 50%;
    animation: spin 0.7s linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
</style>
