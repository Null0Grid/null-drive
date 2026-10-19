<script lang="ts">
  import Button from '$lib/components/Button.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import { formatDateTime } from '$lib/util/format';
  import type { ConflictRecord, Resolution } from '../types';

  interface Props {
    conflicts: ConflictRecord[];
    onresolve: (id: number, how: Resolution) => Promise<void>;
    onreveal: (path: string) => void;
  }
  let { conflicts, onresolve, onreveal }: Props = $props();

  let confirm = $state<{ c: ConflictRecord; how: Resolution } | null>(null);
  let busy = $state(false);

  async function apply(c: ConflictRecord, how: Resolution) {
    busy = true;
    try {
      await onresolve(c.id, how);
      confirm = null;
    } finally {
      busy = false;
    }
  }
</script>

{#if conflicts.length === 0}
  <EmptyState icon="check" title="No conflicts" message="When the same file is changed in two places, Null Drive keeps both versions and lists them here." />
{:else}
  <p class="intro">
    These files were changed on this computer and somewhere else at the same time. Nothing was lost: the other version is in the
    original file, and this computer's version is in the copy.
  </p>
  <ul class="list" aria-label="Conflicts">
    {#each conflicts as c (c.id)}
      <li>
        <div class="names">
          <button type="button" class="file" onclick={() => onreveal(c.path)} title="Show in folder">{c.path}</button>
          <button type="button" class="copy" onclick={() => onreveal(c.conflict_path)} title="Show in folder">{c.conflict_path}</button>
          <span class="when">{c.device} · {formatDateTime(c.at)}</span>
        </div>
        <div class="actions">
          <Button size="sm" onclick={() => apply(c, 'keep-both')} disabled={busy}>Keep both</Button>
          <Button size="sm" onclick={() => (confirm = { c, how: 'keep-mine' })} disabled={busy}>Use my copy</Button>
          <Button size="sm" variant="ghost" onclick={() => (confirm = { c, how: 'keep-server' })} disabled={busy}>Discard my copy</Button>
        </div>
      </li>
    {/each}
  </ul>
{/if}

<Modal
  open={confirm !== null}
  title={confirm?.how === 'keep-mine' ? 'Use your copy?' : 'Discard your copy?'}
  description={confirm?.how === 'keep-mine'
    ? `“${confirm?.c.path}” will be replaced by this computer's version everywhere.`
    : `“${confirm?.c.conflict_path}” will be deleted everywhere. The other version stays.`}
  onclose={() => (confirm = null)}
  dismissible={!busy}
>
  {#snippet footer()}
    <Button variant="ghost" onclick={() => (confirm = null)} disabled={busy}>Cancel</Button>
    <Button variant={confirm?.how === 'keep-server' ? 'danger' : 'primary'} loading={busy} onclick={() => confirm && apply(confirm.c, confirm.how)}>
      {confirm?.how === 'keep-mine' ? 'Use my copy' : 'Discard'}
    </Button>
  {/snippet}
</Modal>

<style>
  .intro {
    color: var(--text-muted);
    font-size: var(--text-sm);
    margin-bottom: var(--space-3);
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
  }
  li {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-3);
    padding: var(--space-3);
    border: 1px solid var(--border);
    border-left: 3px solid var(--warn);
    border-radius: var(--radius-md);
    background: var(--panel);
  }
  .names {
    display: flex;
    flex-direction: column;
    min-width: 0;
    flex: 1 1 16rem;
  }
  .file,
  .copy {
    border: 0;
    background: none;
    padding: 0;
    text-align: left;
    cursor: pointer;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .file {
    color: var(--text);
    font-size: var(--text-sm);
    font-weight: 500;
  }
  .copy {
    color: var(--warn);
    font-size: var(--text-xs);
  }
  .file:hover,
  .copy:hover {
    text-decoration: underline;
  }
  .when {
    font-size: var(--text-xs);
    color: var(--text-faint);
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-1);
  }
</style>
