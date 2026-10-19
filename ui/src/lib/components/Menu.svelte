<script lang="ts" module>
  import type { IconName } from './Icon.svelte';
  export interface MenuItem {
    id: string;
    label: string;
    icon?: IconName;
    danger?: boolean;
    disabled?: boolean;
  }
</script>

<script lang="ts">
  import { tick } from 'svelte';
  import Icon from './Icon.svelte';

  interface Props {
    items: MenuItem[];
    /** Accessible label of the trigger button, e.g. "Actions for report.pdf". */
    label: string;
    onselect: (id: string) => void;
  }
  let { items, label, onselect }: Props = $props();

  let open = $state(false);
  let root = $state<HTMLDivElement>();
  let trigger = $state<HTMLButtonElement>();
  let list = $state<HTMLUListElement>();
  let up = $state(false);
  const uid = $props.id();

  async function show() {
    open = true;
    await tick();
    // Open upwards when there is no room below (e.g. last rows on a phone).
    const r = trigger?.getBoundingClientRect();
    up = !!r && window.innerHeight - r.bottom < 220 && r.top > 220;
    list?.querySelector<HTMLButtonElement>('button:not([disabled])')?.focus();
  }

  function hide(focusTrigger = true) {
    open = false;
    if (focusTrigger) trigger?.focus();
  }

  function onkeydown(e: KeyboardEvent) {
    if (!open || !list) return;
    const buttons = [...list.querySelectorAll<HTMLButtonElement>('button:not([disabled])')];
    const i = buttons.indexOf(document.activeElement as HTMLButtonElement);
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      hide();
    } else if (e.key === 'ArrowDown') {
      e.preventDefault();
      buttons[(i + 1) % buttons.length]?.focus();
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      buttons[(i - 1 + buttons.length) % buttons.length]?.focus();
    } else if (e.key === 'Tab') {
      hide(false);
    }
  }

  function onwindowclick(e: MouseEvent) {
    if (open && root && !root.contains(e.target as Node)) hide(false);
  }
</script>

<svelte:window onclick={onwindowclick} />

<div class="menu" bind:this={root} {onkeydown} role="presentation">
  <button
    bind:this={trigger}
    type="button"
    class="trigger"
    aria-label={label}
    title={label}
    aria-haspopup="menu"
    aria-expanded={open}
    aria-controls={open ? `menu-${uid}` : undefined}
    onclick={(e) => {
      e.stopPropagation();
      if (open) hide();
      else show();
    }}
  >
    <Icon name="more" size={20} strokeWidth={3} />
  </button>
  {#if open}
    <ul bind:this={list} id="menu-{uid}" role="menu" class:up>
      {#each items as item (item.id)}
        <li role="none">
          <button
            type="button"
            role="menuitem"
            class:danger={item.danger}
            disabled={item.disabled}
            onclick={(e) => {
              e.stopPropagation();
              hide();
              onselect(item.id);
            }}
          >
            {#if item.icon}<Icon name={item.icon} size={16} />{/if}
            {item.label}
          </button>
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .menu {
    position: relative;
    display: inline-flex;
  }
  .trigger {
    display: grid;
    place-items: center;
    width: var(--tap);
    height: var(--tap);
    border: 1px solid transparent;
    border-radius: var(--radius-md);
    background: none;
    color: var(--text-muted);
    cursor: pointer;
  }
  .trigger:hover,
  .trigger[aria-expanded='true'] {
    color: var(--text);
    background: var(--surface-hover);
    border-color: var(--border);
  }
  ul {
    position: absolute;
    right: 0;
    top: calc(100% + 4px);
    z-index: 20;
    min-width: 11rem;
    margin: 0;
    padding: var(--space-1);
    list-style: none;
    background: var(--panel);
    border: 1px solid var(--border-strong);
    border-radius: var(--radius-md);
    box-shadow: var(--shadow-overlay);
    animation: pop var(--dur-fast) var(--ease);
  }
  ul.up {
    top: auto;
    bottom: calc(100% + 4px);
  }
  button[role='menuitem'] {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    width: 100%;
    padding: var(--space-2) var(--space-3);
    border: 0;
    border-radius: var(--radius-sm);
    background: none;
    color: var(--text);
    font-size: var(--text-sm);
    text-align: left;
    cursor: pointer;
  }
  button[role='menuitem']:hover,
  button[role='menuitem']:focus-visible {
    outline: none;
    background: var(--primary-soft);
    color: var(--primary);
  }
  button[role='menuitem'].danger {
    color: var(--danger);
  }
  button[role='menuitem'].danger:hover,
  button[role='menuitem'].danger:focus-visible {
    background: var(--danger-soft);
    color: var(--danger);
  }
  button[role='menuitem']:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }
  @keyframes pop {
    from {
      opacity: 0;
      transform: translateY(-4px);
    }
  }
</style>
