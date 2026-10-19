<script lang="ts">
  import Modal from '../../lib/components/Modal.svelte';
  import Button from '../../lib/components/Button.svelte';

  export type Resolution = 'replace' | 'keep' | 'skip';

  interface Props {
    open: boolean;
    names: string[];
    onresolve: (r: Resolution) => void;
  }
  let { open, names, onresolve }: Props = $props();
  const shown = $derived(names.slice(0, 5));
</script>

<Modal
  {open}
  title={names.length === 1 ? 'File already exists' : `${names.length} files already exist`}
  description="What should happen to the existing {names.length === 1 ? 'file' : 'files'}?"
  size="md"
  onclose={() => onresolve('skip')}
>
  <ul>
    {#each shown as n (n)}<li><code>{n}</code></li>{/each}
    {#if names.length > shown.length}<li class="more">and {names.length - shown.length} more…</li>{/if}
  </ul>
  {#snippet footer()}
    <Button variant="ghost" onclick={() => onresolve('skip')}>Skip</Button>
    <Button onclick={() => onresolve('replace')}>Replace</Button>
    <Button variant="primary" onclick={() => onresolve('keep')} data-autofocus>Keep both</Button>
  {/snippet}
</Modal>

<style>
  ul {
    margin: 0;
    padding-left: var(--space-4);
    display: flex;
    flex-direction: column;
    gap: var(--space-1);
    font-size: var(--text-sm);
  }
  code {
    overflow-wrap: anywhere;
  }
  .more {
    color: var(--text-muted);
  }
</style>
