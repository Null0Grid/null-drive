<script lang="ts">
  import { onMount } from 'svelte';
  import Button from '../../lib/components/Button.svelte';
  import Icon from '../../lib/components/Icon.svelte';
  import EmptyState from '../../lib/components/EmptyState.svelte';
  import FileIcon from '../../lib/components/FileIcon.svelte';
  import ProgressBar from '../../lib/components/ProgressBar.svelte';
  import type { TrashItem } from '../../lib/api/types';
  import { formatBytes, formatDate, formatDateTime } from '../../lib/util/format';
  import { toast } from '../../lib/toast.svelte';
  import { drive } from '../api';
  import { errorMessage } from '../actions';
  import { folderHref, router } from '../router.svelte';
  import { live } from '../live.svelte';
  import HistoryDialog from '../components/HistoryDialog.svelte';

  // Recently deleted: files whose content is still retained. Restoring
  // recreates the file (and its folders) as a new version.

  let items = $state<TrashItem[] | null>(null);
  let error = $state<string | null>(null);
  let filter = $state('');
  let restoring = $state<string | null>(null);
  let historyOf = $state<string | null>(null);

  async function load() {
    try {
      items = await drive.trash();
      error = null;
    } catch (e) {
      error = errorMessage(e);
    }
  }

  onMount(() => {
    void load();
    let t: ReturnType<typeof setTimeout> | undefined;
    const off = live.on((m) => {
      if (m.type === 'changed') {
        clearTimeout(t);
        t = setTimeout(load, 300);
      }
    });
    return () => {
      off();
      clearTimeout(t);
    };
  });

  const shown = $derived(
    (items ?? []).filter((i) => !filter.trim() || i.path.toLowerCase().includes(filter.trim().toLowerCase())),
  );
  const name = (p: string) => p.split('/').pop() ?? p;
  const dir = (p: string) => (p.includes('/') ? p.slice(0, p.lastIndexOf('/')) : '');

  async function restore(it: TrashItem) {
    restoring = it.path;
    try {
      await drive.restoreVersion(it.path, it.version);
      toast.success(`Restored “${name(it.path)}”.`, {
        action: { label: 'Show', run: () => router.navigate(folderHref(dir(it.path))) },
      });
      await load();
    } catch (e) {
      toast.error(`Couldn't restore “${name(it.path)}”: ${errorMessage(e)}`);
    } finally {
      restoring = null;
    }
  }

  function daysLeft(iso: string): number {
    return Math.max(0, Math.ceil((new Date(iso).getTime() - Date.now()) / 86_400_000));
  }
</script>

<svelte:head><title>Recently deleted · Null Drive</title></svelte:head>

<div class="trash">
  <header>
    <div>
      <h1>Recently deleted</h1>
      <p class="sub">Deleted files stay restorable for the retention period, on every device.</p>
    </div>
    {#if items && items.length > 6}
      <input class="filter" type="search" placeholder="Filter by name" aria-label="Filter deleted files" bind:value={filter} />
    {/if}
  </header>

  {#if error}
    <p class="error" role="alert"><Icon name="alert" size={16} /> {error}</p>
  {:else if items === null}
    <ProgressBar label="Loading deleted files" />
  {:else if items.length === 0}
    <EmptyState icon="trash" title="Nothing here" message="Files you delete, from the web or any device, show up here and can be restored." />
  {:else}
    <ul class="list" aria-label="Deleted files">
      {#each shown as it (it.path)}
        <li>
          <FileIcon name={name(it.path)} />
          <div class="info">
            <strong>{name(it.path)}</strong>
            <span class="meta">
              {dir(it.path) || 'My Drive'} · {formatBytes(it.size)} ·
              <span title={formatDateTime(it.deleted_at)}>deleted {formatDate(it.deleted_at)}</span>
            </span>
          </div>
          <span class="left" title="Removed for good on {formatDateTime(it.purge_at)}">{daysLeft(it.purge_at)} days left</span>
          <div class="acts">
            {#if it.versions > 1}
              <Button size="sm" variant="ghost" onclick={() => (historyOf = it.path)} aria-label="Versions of {name(it.path)}">
                {it.versions} versions
              </Button>
            {/if}
            <Button size="sm" icon="refresh" loading={restoring === it.path} onclick={() => restore(it)} aria-label="Restore {name(it.path)}">
              Restore
            </Button>
          </div>
        </li>
      {/each}
    </ul>
  {/if}
</div>

<HistoryDialog path={historyOf} onclose={() => (historyOf = null)} onrestored={load} />

<style>
  .trash {
    max-width: 64rem;
    display: flex;
    flex-direction: column;
    gap: var(--space-5);
  }
  header {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: var(--space-4);
    flex-wrap: wrap;
  }
  h1 {
    font-size: var(--text-xl);
  }
  .sub {
    color: var(--text-muted);
    font-size: var(--text-sm);
    margin-top: var(--space-1);
  }
  .filter {
    font: inherit;
    color: var(--text);
    background: var(--bg-sunken);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    padding: var(--space-2) var(--space-3);
    min-width: 14rem;
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    background: var(--panel);
  }
  li {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto auto;
    align-items: center;
    gap: var(--space-3);
    padding: var(--space-3) var(--space-4);
    border-bottom: 1px solid var(--border);
  }
  li:last-child {
    border-bottom: 0;
  }
  .info {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .info strong {
    font-weight: 500;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .meta {
    color: var(--text-muted);
    font-size: var(--text-sm);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .left {
    font-family: var(--font-term);
    font-size: 1.05rem;
    color: var(--text-faint);
    white-space: nowrap;
  }
  .acts {
    display: flex;
    gap: var(--space-2);
  }
  .error {
    display: flex;
    gap: var(--space-2);
    align-items: center;
    color: var(--danger);
  }
  @media (max-width: 640px) {
    li {
      grid-template-columns: auto minmax(0, 1fr) auto;
    }
    .left {
      display: none;
    }
    .acts :global(.btn.ghost) {
      display: none;
    }
  }
</style>
