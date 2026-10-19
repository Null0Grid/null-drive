<script lang="ts">
  import { formatBytes } from '../util/format';
  import FileIcon from './FileIcon.svelte';
  import type { FileItem } from './FileRow.svelte';
  import Menu, { type MenuItem } from './Menu.svelte';

  interface Props {
    item: FileItem;
    actions: MenuItem[];
    onopen: (item: FileItem) => void;
    onaction: (action: string, item: FileItem) => void;
    /** Image URL for a thumbnail; the icon is shown if absent or it fails. */
    thumbnail?: string;
  }
  let { item, actions, onopen, onaction, thumbnail }: Props = $props();
  let failed = $state(false);
</script>

<div class="tile" class:dir={item.isDir} data-path={item.path}>
  <button type="button" class="main" onclick={() => onopen(item)} aria-label={item.name} title={item.isDir ? `Open ${item.name}` : `Download ${item.name}`}>
    <span class="preview">
      {#if thumbnail && !failed}
        <img src={thumbnail} alt="" loading="lazy" decoding="async" onerror={() => (failed = true)} />
      {:else}
        <FileIcon name={item.name} isDir={item.isDir} size={40} />
      {/if}
    </span>
    <span class="name">{item.name}</span>
    <span class="meta">{item.isDir ? `${item.count ?? 0} files` : formatBytes(item.size)}</span>
  </button>
  <span class="menu"><Menu items={actions} label="Actions for {item.name}" onselect={(id) => onaction(id, item)} /></span>
</div>

<style>
  .tile {
    position: relative;
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    background: var(--panel);
    transition:
      border-color var(--dur-fast) var(--ease),
      box-shadow var(--dur) var(--ease);
    min-width: 0;
  }
  .tile:hover,
  .tile:focus-within {
    border-color: var(--primary-line);
    box-shadow: var(--glow-primary);
  }
  .main {
    display: flex;
    flex-direction: column;
    align-items: stretch;
    gap: var(--space-1);
    width: 100%;
    padding: var(--space-2) var(--space-2) var(--space-3);
    border: 0;
    background: none;
    color: inherit;
    text-align: left;
    cursor: pointer;
    border-radius: inherit;
  }
  .main:focus-visible {
    outline: 2px solid var(--primary);
    outline-offset: -2px;
  }
  .preview {
    display: grid;
    place-items: center;
    aspect-ratio: 4 / 3;
    margin-bottom: var(--space-2);
    border-radius: var(--radius-md);
    background: var(--bg-sunken);
    overflow: hidden;
  }
  img {
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
  .name {
    padding: 0 var(--space-1);
    padding-right: 2rem;
    font-size: var(--text-sm);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .dir .name {
    font-weight: 500;
  }
  .meta {
    padding: 0 var(--space-1);
    font-size: var(--text-xs);
    color: var(--text-faint);
  }
  .menu {
    position: absolute;
    right: var(--space-1);
    bottom: var(--space-1);
  }
</style>
