<script lang="ts">
  import { tick } from 'svelte';
  import Modal from '../../lib/components/Modal.svelte';
  import Button from '../../lib/components/Button.svelte';
  import Input from '../../lib/components/Input.svelte';
  import { validateName, splitExt } from '../../lib/util/names';

  // Used for "New folder" and "Rename".
  interface Props {
    open: boolean;
    title: string;
    label?: string;
    initial?: string;
    confirmLabel: string;
    /** Names already used in the folder (case-insensitive check). */
    taken: Set<string>;
    /** For rename: select only the part before the extension. */
    selectStem?: boolean;
    onsubmit: (name: string) => Promise<void>;
    onclose: () => void;
  }
  let { open, title, label = 'Name', initial = '', confirmLabel, taken, selectStem = false, onsubmit, onclose }: Props =
    $props();

  let name = $state('');
  let error = $state<string | null>(null);
  let busy = $state(false);
  let input = $state<HTMLInputElement>();

  $effect(() => {
    if (!open) return;
    name = initial;
    error = null;
    busy = false;
    tick().then(() => {
      if (!input) return;
      input.focus();
      const end = selectStem ? splitExt(initial)[0].length : initial.length;
      input.setSelectionRange(0, end);
    });
  });

  const lowerTaken = $derived(new Set([...taken].map((n) => n.toLowerCase())));

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    const n = name.trim();
    error = validateName(n);
    if (!error && n !== initial && lowerTaken.has(n.toLowerCase()) && n.toLowerCase() !== initial.toLowerCase()) {
      error = `"${n}" already exists here`;
    }
    if (error) return;
    if (n === initial) return onclose();
    busy = true;
    try {
      await onsubmit(n);
      onclose();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      busy = false;
    }
  }
</script>

<Modal {open} {title} {onclose} dismissible={!busy}>
  <form id="name-form" onsubmit={submit} novalidate>
    <Input {label} bind:value={name} bind:element={input} {error} autocomplete="off" spellcheck="false" data-autofocus />
  </form>
  {#snippet footer()}
    <Button variant="ghost" onclick={onclose} disabled={busy}>Cancel</Button>
    <Button type="submit" form="name-form" variant="primary" loading={busy}>{confirmLabel}</Button>
  {/snippet}
</Modal>
