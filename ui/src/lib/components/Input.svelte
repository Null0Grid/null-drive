<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLInputAttributes } from 'svelte/elements';
  import Icon, { type IconName } from './Icon.svelte';

  interface Props extends Omit<HTMLInputAttributes, 'value'> {
    label?: string;
    value?: string;
    error?: string | null;
    hint?: string;
    icon?: IconName;
    /** Hide the visible label (still announced to screen readers). */
    hideLabel?: boolean;
    /** Show/hide toggle for password fields. */
    revealable?: boolean;
    element?: HTMLInputElement;
    after?: Snippet;
  }

  let {
    label,
    value = $bindable(''),
    error = null,
    hint,
    icon,
    hideLabel = false,
    revealable = false,
    type = 'text',
    id,
    element = $bindable(),
    after,
    class: className = '',
    ...rest
  }: Props = $props();

  const uid = $props.id();
  const inputId = $derived(id ?? `in-${uid}`);
  const describedBy = $derived(error ? `${inputId}-err` : hint ? `${inputId}-hint` : undefined);
  let revealed = $state(false);
  const actualType = $derived(type === 'password' && revealed ? 'text' : type);
</script>

<div class="field {className}" class:has-error={!!error}>
  {#if label}
    <label for={inputId} class:sr-only={hideLabel}>{label}</label>
  {/if}
  <div class="control">
    {#if icon}<span class="lead"><Icon name={icon} size={16} /></span>{/if}
    <input
      bind:this={element}
      bind:value
      id={inputId}
      type={actualType}
      class:with-icon={!!icon}
      class:with-trail={revealable}
      aria-invalid={error ? 'true' : undefined}
      aria-describedby={describedBy}
      {...rest}
    />
    {#if revealable && type === 'password'}
      <button
        type="button"
        class="reveal"
        aria-label={revealed ? 'Hide password' : 'Show password'}
        aria-pressed={revealed}
        onclick={() => (revealed = !revealed)}
      >
        <Icon name={revealed ? 'eye-off' : 'eye'} size={16} />
      </button>
    {/if}
  </div>
  {#if error}
    <p class="msg error" id="{inputId}-err" role="alert">{error}</p>
  {:else if hint}
    <p class="msg" id="{inputId}-hint">{hint}</p>
  {/if}
  {@render after?.()}
</div>

<style>
  .field {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    min-width: 0;
  }
  label {
    font-size: var(--text-xs);
    font-weight: 500;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.08em;
  }
  .control {
    position: relative;
    display: flex;
    align-items: center;
  }
  input {
    width: 100%;
    height: 2.5rem;
    padding: 0 var(--space-3);
    background: var(--bg-sunken);
    border: 1px solid var(--border-strong);
    border-radius: var(--radius-md);
    color: var(--text);
    font-size: var(--text-md);
    transition:
      border-color var(--dur-fast) var(--ease),
      box-shadow var(--dur) var(--ease);
  }
  input::placeholder {
    color: var(--text-faint);
  }
  input:hover {
    border-color: var(--text-faint);
  }
  input:focus {
    outline: none;
    border-color: var(--primary);
    box-shadow: var(--glow-primary);
  }
  input.with-icon {
    padding-left: 2.25rem;
  }
  input.with-trail {
    padding-right: 2.5rem;
  }
  .lead {
    position: absolute;
    left: 0.7rem;
    color: var(--text-faint);
    pointer-events: none;
    display: flex;
  }
  .reveal {
    position: absolute;
    right: 0.25rem;
    width: 2rem;
    height: 2rem;
    display: grid;
    place-items: center;
    border: 0;
    border-radius: var(--radius-sm);
    background: none;
    color: var(--text-faint);
    cursor: pointer;
  }
  .reveal:hover {
    color: var(--text);
  }
  .has-error input {
    border-color: var(--danger);
  }
  .has-error input:focus {
    box-shadow: 0 0 0 1px var(--danger);
  }
  .msg {
    font-size: var(--text-xs);
    color: var(--text-muted);
  }
  .msg.error {
    color: var(--danger);
  }
</style>
