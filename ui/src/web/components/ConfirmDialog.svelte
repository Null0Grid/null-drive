<script lang="ts">
  import Modal from '../../lib/components/Modal.svelte';
  import Button from '../../lib/components/Button.svelte';

  interface Props {
    open: boolean;
    title: string;
    message: string;
    confirmLabel: string;
    danger?: boolean;
    onconfirm: () => Promise<void>;
    onclose: () => void;
  }
  let { open, title, message, confirmLabel, danger = false, onconfirm, onclose }: Props = $props();
  let busy = $state(false);

  async function confirm() {
    busy = true;
    try {
      await onconfirm();
      onclose();
    } finally {
      busy = false;
    }
  }
</script>

<Modal {open} {title} description={message} {onclose} dismissible={!busy}>
  {#snippet footer()}
    <Button variant="ghost" onclick={onclose} disabled={busy}>Cancel</Button>
    <Button variant={danger ? 'danger' : 'primary'} loading={busy} onclick={confirm} data-autofocus>{confirmLabel}</Button>
  {/snippet}
</Modal>
