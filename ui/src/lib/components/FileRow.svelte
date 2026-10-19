<script lang="ts" module>
  /** What a FileRow/FileTile displays. Matches util/paths Item. */
  export interface FileItem {
    path: string;
    name: string;
    isDir: boolean;
    size: number;
    count?: number;
    modifiedAt: string;
  }
</script>

<script lang="ts">
  import { formatBytes, formatDate, formatDateTime } from '../util/format';
  import FileIcon from './FileIcon.svelte';
  import Menu, { type MenuItem } from './Menu.svelte';

  interface Props {
    item: FileItem;
    actions: MenuItem[];
    /** Primary action: open a folder / download a file. */
    onopen: (item: FileItem) => void;
    onaction: (action: string, item: FileItem) => void;
    /** Highlight text matches of the current filter. */
    highlight?: string;
  }
  let { item, actions, onopen, onaction, highlight = '' }: Props = $props();

  const parts = $derived.by(() => {
    const q = highlight.trim().toLowerCase();
    const i = q ? item.name.toLowerCase().indexOf(q) : -1;
    if (i < 0) return null;
    return [item.name.slice(0, i), item.name.slice(i, i + q.length), item.name.slice(i + q.length)];
  });
  const meta = $derived(
    item.isDir ? `${item.count ?? 0} file${item.count === 1 ? '' : 's'}` : formatBytes(item.size),
  );
</script>

<div class="row" class:dir={item.isDir} data-path={item.path}>
  <button
    type="button"
    class="main"
    onclick={() => onopen(item)}
    aria-label={item.name}
    title={item.isDir ? `Open ${item.name}` : `Download ${item.name}`}
  >
    <FileIcon name={item.name} isDir={item.isDir} />
    <span class="name">
      {#if parts}{parts[0]}<mark>{parts[1]}</mark>{parts[2]}{:else}{item.name}{/if}
    </span>
    <span class="sub">{meta} · {formatDate(item.modifiedAt)}</span>
  </button>
  <span class="size">{item.isDir ? (item.size ? formatBytes(item.size) : '—') : formatBytes(item.size)}</span>
  <time class="date" datetime={item.modifiedAt} title={formatDateTime(item.modifiedAt)}>{formatDate(item.modifiedAt)}</time>
  <span class="menu"><Menu items={actions} label="Actions for {item.name}" onselect={(id) => onaction(id, item)} /></span>
</div>

<style>
  .row {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 7rem 8rem var(--tap);
    align-items: center;
    gap: var(--space-3);
    padding: 0 var(--space-2) 0 0;
    border-bottom: 1px solid var(--border);
    transition: background var(--dur-fast) var(--ease);
  }
  .row:hover,
  .row:focus-within {
    background: var(--surface-hover);
  }
  .main {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    align-items: center;
    column-gap: var(--space-3);
    min-height: 3rem;
    padding: var(--space-2) var(--space-3);
    border: 0;
    background: none;
    color: inherit;
    text-align: left;
    cursor: pointer;
    min-width: 0;
  }
  .main:focus-visible {
    outline: 2px solid var(--primary);
    outline-offset: -2px;
  }
  .name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: var(--text-md);
  }
  .dir .name {
    font-weight: 500;
  }
  .main:hover .name {
    color: var(--primary);
  }
  mark {
    background: var(--accent-soft);
    color: var(--accent);
    border-radius: 2px;
  }
  .sub {
    display: none;
    grid-column: 2;
    font-size: var(--text-xs);
    color: var(--text-faint);
  }
  .size,
  .date {
    font-size: var(--text-sm);
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }
  .size {
    text-align: right;
  }
  .menu {
    display: flex;
    justify-content: flex-end;
  }

  @media (max-width: 720px) {
    .row {
      grid-template-columns: minmax(0, 1fr) var(--tap);
    }
    .size,
    .date {
      display: none;
    }
    .sub {
      display: block;
    }
  }
</style>
