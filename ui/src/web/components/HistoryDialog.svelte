<script lang="ts">
  import { untrack } from 'svelte';
  import Modal from '../../lib/components/Modal.svelte';
  import Button from '../../lib/components/Button.svelte';
  import Icon from '../../lib/components/Icon.svelte';
  import ProgressBar from '../../lib/components/ProgressBar.svelte';
  import type { FileVersion, VersionList } from '../../lib/api/types';
  import { formatBytes, formatDate, formatDateTime } from '../../lib/util/format';
  import { drive } from '../api';
  import { errorMessage } from '../actions';
  import { toast } from '../../lib/toast.svelte';

  // Version history of one file: who changed it when, download any
  // version, restore one. Restoring adds a new version on top; nothing in
  // the history is ever rewritten, so a restore can itself be undone.

  interface Props {
    path: string | null;
    onclose: () => void;
    onrestored?: () => void;
  }
  let { path, onclose, onrestored }: Props = $props();

  let list = $state<VersionList | null>(null);
  let error = $state<string | null>(null);
  let confirming = $state<FileVersion | null>(null);
  let busy = $state(false);

  const name = $derived(path?.split('/').pop() ?? '');

  async function load(p: string) {
    list = null;
    error = null;
    try {
      list = await drive.listVersions(p);
    } catch (e) {
      error = errorMessage(e);
    }
  }

  $effect(() => {
    const p = path;
    if (p) untrack(() => void load(p));
  });

  async function restore() {
    const v = confirming;
    if (!v || !path) return;
    busy = true;
    try {
      await drive.restoreVersion(path, v.version);
      toast.success(list?.deleted ? `Restored “${name}”.` : `Restored the version from ${formatDateTime(v.created_at)}.`);
      confirming = null;
      onrestored?.();
      await load(path);
    } catch (e) {
      toast.error(`Couldn't restore: ${errorMessage(e)}`);
    } finally {
      busy = false;
    }
  }

  function download(v: FileVersion) {
    if (!path) return;
    drive.download(path, name, v.version).catch((e) => toast.error(`Couldn't download: ${errorMessage(e)}`));
  }

  function who(v: FileVersion): string {
    if (v.device) return v.device;
    return v.origin === 'migrated' ? 'Before history' : 'Web';
  }
</script>

<Modal open={path !== null} title="History · {name}" size="lg" {onclose}>
  {#if error}
    <p class="error" role="alert"><Icon name="alert" size={16} /> {error}</p>
  {:else if !list}
    <ProgressBar label="Loading history" />
  {:else if confirming}
    <div class="confirm" role="alertdialog" aria-label="Confirm restore">
      <p>
        {#if list.deleted}
          Bring back <strong>{name}</strong> as it was on <strong>{formatDateTime(confirming.created_at)}</strong>?
        {:else}
          Replace the current version with the one from <strong>{formatDateTime(confirming.created_at)}</strong>?
        {/if}
        It's added as a new version: nothing in the history is lost, and synced devices get it like any other change.
      </p>
      <div class="row">
        <Button variant="ghost" onclick={() => (confirming = null)} disabled={busy}>Cancel</Button>
        <Button variant="primary" icon="refresh" loading={busy} onclick={restore}>Restore</Button>
      </div>
    </div>
  {:else}
    {#if list.deleted}
      <p class="deleted"><Icon name="trash" size={16} /> This file is deleted. Restore any version to bring it back.</p>
    {/if}
    <ol class="versions" aria-label="Versions, newest first">
      {#each list.versions as v (v.version)}
        <li class:current={v.current}>
          <span class="dot" aria-hidden="true"></span>
          <div class="info">
            <span class="when" title={formatDateTime(v.created_at)}>
              {formatDate(v.created_at)}
              {#if v.current}<span class="tag cur">Current</span>{/if}
              {#if v.origin === 'restore'}<span class="tag">Restored</span>{/if}
            </span>
            <span class="meta">{who(v)} · {formatBytes(v.size)}</span>
          </div>
          <div class="acts">
            <button
              type="button"
              class="btn-link"
              onclick={() => download(v)}
              aria-label="Download version from {formatDateTime(v.created_at)}"
            >
              <Icon name="download" size={16} />
            </button>
            {#if !v.current}
              <Button size="sm" onclick={() => (confirming = v)} aria-label="Restore version from {formatDateTime(v.created_at)}">Restore</Button>
            {/if}
          </div>
        </li>
      {/each}
    </ol>
    <p class="muted small">
      Kept per the server's retention policy (Settings → Storage). Unchanged parts of a file are stored only once across all versions.
    </p>
  {/if}
  {#snippet footer()}
    <Button variant="ghost" onclick={onclose}>Close</Button>
  {/snippet}
</Modal>

<style>
  .versions {
    list-style: none;
    margin: 0;
    padding: 0;
    position: relative;
  }
  .versions::before {
    content: '';
    position: absolute;
    left: 0.45rem;
    top: 1.1rem;
    bottom: 1.1rem;
    width: 1px;
    background: var(--border);
  }
  li {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    align-items: center;
    gap: var(--space-3);
    padding: var(--space-3) 0;
  }
  .dot {
    position: relative;
    width: 0.95rem;
    height: 0.95rem;
    border-radius: 50%;
    background: var(--panel);
    border: 2px solid var(--border-strong);
  }
  li.current .dot {
    border-color: var(--primary);
    background: var(--primary);
    box-shadow: var(--glow-primary);
  }
  .info {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .when {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    font-weight: 500;
  }
  .meta {
    color: var(--text-muted);
    font-size: var(--text-sm);
  }
  .tag {
    font-family: var(--font-term);
    font-size: 0.95rem;
    letter-spacing: 0.04em;
    color: var(--text-muted);
    border: 1px solid var(--border-strong);
    border-radius: 999px;
    padding: 0 var(--space-2);
    line-height: 1.3;
  }
  .tag.cur {
    color: var(--primary);
    border-color: var(--primary-line, var(--primary));
  }
  .acts {
    display: flex;
    align-items: center;
    gap: var(--space-2);
  }
  .btn-link {
    background: none;
    cursor: pointer;
    display: grid;
    place-items: center;
    width: 2rem;
    height: 2rem;
    border-radius: var(--radius-sm);
    color: var(--text-muted);
    border: 1px solid var(--border);
  }
  .btn-link:hover,
  .btn-link:focus-visible {
    color: var(--primary);
    border-color: var(--primary);
    outline: none;
  }
  .deleted,
  .error {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    margin-bottom: var(--space-3);
    color: var(--warn);
  }
  .error {
    color: var(--danger);
  }
  .confirm p {
    line-height: 1.6;
  }
  .row {
    display: flex;
    justify-content: flex-end;
    gap: var(--space-2);
    margin-top: var(--space-4);
  }
  .muted {
    color: var(--text-muted);
  }
  .small {
    font-size: var(--text-sm);
    margin-top: var(--space-3);
  }
</style>
