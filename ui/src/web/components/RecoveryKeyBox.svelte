<script lang="ts">
  import Button from '../../lib/components/Button.svelte';
  import { toast } from '../../lib/toast.svelte';
  import { saveBlob } from '../../lib/drive';

  // Shows a recovery key once, with the three ways to keep it: download a
  // text file, print it, or copy it. Printing shows only this sheet.

  interface Props {
    recoveryKey: string;
    username?: string;
  }
  let { recoveryKey, username = '' }: Props = $props();

  const sheet = $derived(
    [
      'NULL DRIVE · RECOVERY KEY',
      '',
      `Account: ${username}`,
      `Server:  ${location.origin}`,
      `Created: ${new Date().toLocaleString()}`,
      '',
      `    ${recoveryKey}`,
      '',
      'This key unlocks your end-to-end encrypted files if you forget your',
      'encryption passphrase. Keep it somewhere safe and offline (printed in a',
      'drawer, in a password manager). Anyone with this key and your login can',
      'read your files.',
      '',
      'If you lose BOTH this key and your passphrase, your files cannot be',
      'recovered by anyone, including the server administrator.',
      '',
    ].join('\n'),
  );

  function download() {
    saveBlob(new Blob([sheet], { type: 'text/plain' }), 'null-drive-recovery-key.txt');
  }

  async function copy() {
    try {
      await navigator.clipboard.writeText(recoveryKey);
      toast.success('Recovery key copied. Paste it somewhere safe, then clear your clipboard.');
    } catch {
      toast.error("Couldn't copy. Select the key and copy it manually.");
    }
  }

  function print() {
    document.body.classList.add('print-recovery');
    const done = () => {
      document.body.classList.remove('print-recovery');
      removeEventListener('afterprint', done);
    };
    addEventListener('afterprint', done);
    window.print();
    setTimeout(done, 1000);
  }
</script>

<div class="recovery-sheet">
  <p class="label">Recovery key</p>
  <p class="key" data-testid="recovery-key">{recoveryKey}</p>
  <pre class="print-only">{sheet}</pre>
  <div class="acts no-print">
    <Button size="sm" icon="download" onclick={download}>Download</Button>
    <Button size="sm" icon="print" onclick={print}>Print</Button>
    <Button size="sm" icon="copy" onclick={copy}>Copy</Button>
  </div>
</div>

<style>
  .recovery-sheet {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    padding: var(--space-4);
    border: 1px dashed var(--primary);
    border-radius: var(--radius-md);
    background: var(--surface);
  }
  .label {
    font-size: var(--text-xs);
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-muted);
  }
  .key {
    font-family: var(--font-mono);
    font-size: clamp(0.95rem, 2.6vw, 1.2rem);
    letter-spacing: 0.04em;
    color: var(--primary);
    word-break: break-word;
    user-select: all;
  }
  .print-only {
    display: none;
  }
  .acts {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
  }
  @media print {
    .print-only {
      display: block;
      font-size: 12pt;
    }
    .key,
    .label {
      display: none;
    }
  }
</style>
