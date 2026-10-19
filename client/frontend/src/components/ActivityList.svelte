<script lang="ts">
  import Icon, { type IconName } from '$lib/components/Icon.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { formatDate, formatDateTime } from '$lib/util/format';
  import type { Skipped, SyncEvent } from '../types';

  interface Props {
    events: SyncEvent[];
    skipped: Skipped[];
    onreveal: (path: string) => void;
    /** Open the file's version history in the web app. */
    onhistory?: (path: string) => void;
  }
  let { events, skipped, onreveal, onhistory }: Props = $props();
  const hasHistory = (k: string) => !k.includes('dir') && k !== 'delete-local' && k !== 'delete-remote';
  let showSkipped = $state(false);

  const labels: Record<string, { icon: IconName; text: string }> = {
    upload: { icon: 'upload', text: 'Uploaded' },
    'restore-remote': { icon: 'upload', text: 'Uploaded (kept your edit)' },
    download: { icon: 'download', text: 'Downloaded' },
    'restore-local': { icon: 'download', text: 'Downloaded (kept their edit)' },
    'create-remote-dir': { icon: 'folder-plus', text: 'Created folder' },
    'create-local-dir': { icon: 'folder-plus', text: 'New folder' },
    'delete-remote': { icon: 'trash', text: 'Deleted on server' },
    'delete-local': { icon: 'trash', text: 'Deleted here' },
    'move-remote': { icon: 'move', text: 'Moved' },
    'move-local': { icon: 'move', text: 'Moved' },
    conflict: { icon: 'alert', text: 'Conflict: kept both' },
  };
  const label = (k: string) => labels[k] ?? { icon: 'refresh' as IconName, text: k };
</script>

{#if skipped.length > 0}
  <div class="notice">
    <button type="button" class="toggle" aria-expanded={showSkipped} onclick={() => (showSkipped = !showSkipped)}>
      <Icon name="alert" size={16} />
      {skipped.length} item{skipped.length === 1 ? '' : 's'} can't be synced
      <Icon name={showSkipped ? 'chevron-down' : 'chevron-right'} size={14} />
    </button>
    {#if showSkipped}
      <ul>
        {#each skipped as s (s.path)}<li><code>{s.path}</code>: {s.reason}</li>{/each}
      </ul>
    {/if}
  </div>
{/if}

{#if events.length === 0}
  <EmptyState icon="refresh" title="No activity yet" message="Changes you make in the folder, or on other devices, show up here." />
{:else}
  <ul class="list" aria-label="Recent activity">
    {#each events as ev, i (i)}
      {@const lb = label(ev.kind)}
      <li class:err={!!ev.error} class:conflict={ev.kind === 'conflict'}>
        <span class="ic"><Icon name={ev.error ? 'alert' : lb.icon} size={16} /></span>
        <div class="body">
          <button type="button" class="path" title="Show in folder" onclick={() => onreveal(ev.kind === 'conflict' && ev.from ? ev.from : ev.path)}>
            {ev.path}
          </button>
          <span class="what">
            {ev.error ? `Failed: ${ev.error}` : lb.text}{#if ev.from && ev.kind !== 'conflict'}&nbsp;from {ev.from}{/if}{#if ev.kind === 'conflict' && ev.from}&nbsp;→ {ev.from}{/if}
          </span>
        </div>
        <time datetime={ev.time} title={formatDateTime(ev.time)}>{formatDate(ev.time)}</time>
        {#if onhistory && !ev.error && hasHistory(ev.kind)}
          <button type="button" class="hist" aria-label="Version history of {ev.path}" title="Version history (opens the web app)" onclick={() => onhistory(ev.path)}>
            <Icon name="refresh" size={14} />
          </button>
        {/if}
      </li>
    {/each}
  </ul>
{/if}

<style>
  .hist {
    display: grid;
    place-items: center;
    width: 1.75rem;
    height: 1.75rem;
    border: 1px solid transparent;
    border-radius: var(--radius-sm);
    background: none;
    color: var(--text-faint);
    cursor: pointer;
  }
  .hist:hover,
  .hist:focus-visible {
    color: var(--primary);
    border-color: var(--border);
    outline: none;
  }
  .notice {
    margin-bottom: var(--space-3);
    padding: var(--space-3);
    border: 1px solid var(--warn);
    border-radius: var(--radius-md);
    background: var(--warn-soft);
    color: var(--warn);
    font-size: var(--text-sm);
  }
  .toggle {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    border: 0;
    background: none;
    color: inherit;
    cursor: pointer;
    padding: 0;
  }
  .notice ul {
    margin: var(--space-2) 0 0;
    padding-left: var(--space-5);
    color: var(--text);
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .list li {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto auto;
    align-items: center;
    gap: var(--space-3);
    padding: var(--space-2) var(--space-1);
    border-bottom: 1px solid var(--border);
  }
  .ic {
    display: flex;
    color: var(--primary);
  }
  .conflict .ic {
    color: var(--warn);
  }
  .err .ic {
    color: var(--danger);
  }
  .body {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .path {
    border: 0;
    background: none;
    padding: 0;
    color: var(--text);
    font-size: var(--text-sm);
    text-align: left;
    cursor: pointer;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .path:hover {
    color: var(--primary);
  }
  .what {
    font-size: var(--text-xs);
    color: var(--text-faint);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .err .what {
    color: var(--danger);
  }
  time {
    font-size: var(--text-xs);
    color: var(--text-faint);
    white-space: nowrap;
  }
</style>
