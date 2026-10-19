<script lang="ts" module>
  import type { IconName } from './Icon.svelte';
  export interface NavItem {
    id: string;
    label: string;
    icon: IconName;
  }
</script>

<script lang="ts">
  import type { Snippet } from 'svelte';
  import Icon from './Icon.svelte';

  interface Props {
    items: NavItem[];
    active: string;
    onselect: (id: string) => void;
    header?: Snippet;
    footer?: Snippet;
  }
  let { items, active, onselect, header, footer }: Props = $props();
</script>

<!--
  Desktop: a fixed column on the left. Phones (≤720px): the header and
  footer move to a top bar, and the nav becomes a bottom tab bar (thumb
  reach). Same component, same markup, CSS only.
-->
<aside class="sidebar">
  {#if header}<div class="header">{@render header()}</div>{/if}
  <nav aria-label="Main">
    <ul>
      {#each items as item (item.id)}
        <li>
          <button
            type="button"
            class:active={item.id === active}
            aria-current={item.id === active ? 'page' : undefined}
            onclick={() => onselect(item.id)}
          >
            <Icon name={item.icon} size={20} />
            <span>{item.label}</span>
          </button>
        </li>
      {/each}
    </ul>
  </nav>
  {#if footer}<div class="footer">{@render footer()}</div>{/if}
</aside>

<style>
  .sidebar {
    display: flex;
    flex-direction: column;
    gap: var(--space-5);
    width: var(--sidebar-w);
    height: 100dvh;
    position: sticky;
    top: 0;
    padding: var(--space-5) var(--space-3);
    background: var(--panel);
    border-right: 1px solid var(--border);
  }
  .header {
    padding: 0 var(--space-2);
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--space-1);
  }
  nav {
    flex: 1;
  }
  nav button {
    position: relative;
    display: flex;
    align-items: center;
    gap: var(--space-3);
    width: 100%;
    height: var(--tap);
    padding: 0 var(--space-3);
    border: 1px solid transparent;
    border-radius: var(--radius-md);
    background: none;
    color: var(--text-muted);
    font-size: var(--text-md);
    text-align: left;
    cursor: pointer;
  }
  nav button:hover {
    background: var(--surface-hover);
    color: var(--text);
  }
  /* The active page is "lit": the one place neon marks where you are. */
  nav button.active {
    background: var(--primary-soft);
    border-color: var(--primary-line);
    color: var(--primary);
  }
  nav button.active::before {
    content: '';
    position: absolute;
    left: -0.8rem;
    top: 20%;
    bottom: 20%;
    width: 3px;
    border-radius: 2px;
    background: var(--primary);
    box-shadow: var(--glow-primary);
  }
  .footer {
    border-top: 1px solid var(--border);
    padding-top: var(--space-4);
  }

  @media (max-width: 720px) {
    .sidebar {
      position: fixed;
      inset: auto 0 0 0;
      z-index: 30;
      width: auto;
      height: auto;
      padding: 0 var(--space-2) env(safe-area-inset-bottom);
      flex-direction: row;
      border-right: 0;
      border-top: 1px solid var(--border);
      background: color-mix(in srgb, var(--panel) 92%, transparent);
      backdrop-filter: blur(10px);
    }
    .header,
    .footer {
      display: none;
    }
    ul {
      flex-direction: row;
      justify-content: space-around;
    }
    nav button {
      flex-direction: column;
      gap: 2px;
      height: 3.75rem;
      padding: 0 var(--space-4);
      font-size: var(--text-xs);
      border: 0;
    }
    nav button.active {
      background: none;
    }
    nav button.active::before {
      left: 25%;
      right: 25%;
      top: 0;
      bottom: auto;
      width: auto;
      height: 2px;
    }
  }
</style>
