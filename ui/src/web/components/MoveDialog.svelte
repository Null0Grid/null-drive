<script lang="ts">
  import Modal from '../../lib/components/Modal.svelte';
  import Button from '../../lib/components/Button.svelte';
  import Input from '../../lib/components/Input.svelte';
  import Icon from '../../lib/components/Icon.svelte';
  import { basename, dirname, isWithin } from '../../lib/util/paths';

  interface Props {
    open: boolean;
    /** The file or folder being moved. */
    source: { path: string; name: string; isDir: boolean } | null;
    folders: string[];
    onmove: (destFolder: string) => Promise<void>;
    onclose: () => void;
  }
  let { open, source, folders, onmove, onclose }: Props = $props();

  let filter = $state('');
  let selected = $state<string | null>(null);
  let busy = $state(false);
  let error = $state<string | null>(null);

  $effect(() => {
    if (open) {
      filter = '';
      selected = null;
      error = null;
      busy = false;
    }
  });

  const currentParent = $derived(source ? dirname(source.path) : '');
  const options = $derived.by(() => {
    const q = filter.trim().toLowerCase();
    return ['', ...folders]
      .filter((f) => !source || !(source.isDir && (f === source.path || isWithin(f, source.path))))
      .filter((f) => !q || f.toLowerCase().includes(q))
      .map((f) => ({ path: f, depth: f ? f.split('/').length : 0, disabled: f === currentParent }));
  });

  async function confirm() {
    if (selected === null) return;
    busy = true;
    error = null;
    try {
      await onmove(selected);
      onclose();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }
</script>

<Modal {open} title="Move “{source?.name ?? ''}”" description="Choose a destination folder." size="md" {onclose} dismissible={!busy}>
  <Input label="Filter folders" hideLabel placeholder="Filter folders…" icon="search" bind:value={filter} data-autofocus />
  <div class="list" role="listbox" aria-label="Destination folder" tabindex="-1">
    {#each options as o (o.path)}
      <button
        type="button"
        role="option"
        aria-selected={selected === o.path}
        class:selected={selected === o.path}
        disabled={o.disabled}
        style:padding-left="calc({filter ? 0 : o.depth} * 1.1rem + 0.75rem)"
        onclick={() => (selected = o.path)}
        ondblclick={() => {
          selected = o.path;
          confirm();
        }}
      >
        <Icon name={o.path ? 'folder' : 'home'} size={16} />
        <span class="name">{o.path ? (filter ? o.path : basename(o.path)) : 'My Drive'}</span>
        {#if o.disabled}<span class="tag">current</span>{/if}
      </button>
    {:else}
      <p class="none">No folders match.</p>
    {/each}
  </div>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#snippet footer()}
    <Button variant="ghost" onclick={onclose} disabled={busy}>Cancel</Button>
    <Button variant="primary" icon="move" loading={busy} disabled={selected === null} onclick={confirm}>Move here</Button>
  {/snippet}
</Modal>

<style>
  .list {
    margin-top: var(--space-3);
    max-height: min(22rem, 50dvh);
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    background: var(--bg-sunken);
    padding: var(--space-1);
  }
  button {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    width: 100%;
    min-height: 2.25rem;
    padding: var(--space-1) var(--space-3);
    border: 1px solid transparent;
    border-radius: var(--radius-sm);
    background: none;
    color: var(--text);
    text-align: left;
    font-size: var(--text-sm);
    cursor: pointer;
  }
  button :global(.icon) {
    color: var(--primary);
  }
  button:hover:not(:disabled) {
    background: var(--surface-hover);
  }
  button.selected {
    background: var(--primary-soft);
    border-color: var(--primary-line);
    color: var(--primary);
  }
  button:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .tag {
    margin-left: auto;
    font-size: var(--text-xs);
    color: var(--text-faint);
  }
  .none {
    padding: var(--space-3);
    color: var(--text-faint);
    font-size: var(--text-sm);
  }
  .error {
    margin-top: var(--space-3);
    color: var(--danger);
    font-size: var(--text-sm);
  }
</style>
