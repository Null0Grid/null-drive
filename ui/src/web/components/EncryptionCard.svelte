<script lang="ts">
  import Card from '../../lib/components/Card.svelte';
  import Button from '../../lib/components/Button.svelte';
  import Input from '../../lib/components/Input.svelte';
  import Icon from '../../lib/components/Icon.svelte';
  import Modal from '../../lib/components/Modal.svelte';
  import { toast } from '../../lib/toast.svelte';
  import { checkPassphrase, cryptoAvailable, INSECURE_CONTEXT_MESSAGE, withNewRecoveryKey, withPassphrase } from '../../lib/crypto/keys';
  import { formatDate } from '../../lib/util/format';
  import { client, drive } from '../api';
  import { session } from '../session.svelte';
  import { vault } from '../vault.svelte';
  import { errorMessage } from '../actions';
  import EncryptionSetup from './EncryptionSetup.svelte';
  import RecoveryKeyBox from './RecoveryKeyBox.svelte';

  const encState = $derived(session.user?.e2ee ?? 'off');
  let setupOpen = $state(false);

  // Change passphrase
  let changing = $state(false);
  let newPass = $state('');
  let newConfirm = $state('');
  let busy = $state(false);
  let error = $state<string | null>(null);
  const passError = $derived(checkPassphrase(newPass));

  // New recovery key
  let rotating = $state(false);
  let newRecovery = $state<string | null>(null);

  $effect(() => {
    if (encState !== 'off' && !vault.status) void vault.refresh().catch(() => {});
  });

  async function changePassphrase(e: SubmitEvent) {
    e.preventDefault();
    if (passError || newPass !== newConfirm || !drive.keys) return;
    busy = true;
    error = null;
    try {
      const st = vault.status?.bundle ? vault.status : await vault.refresh();
      const nb = await withPassphrase(st.bundle!, drive.keys, newPass);
      vault.status = await client.e2eeUpdateBundle(nb, drive.keys.authKey);
      toast.success('Encryption passphrase changed. Other devices keep working; new ones will ask for the new passphrase.');
      changing = false;
      newPass = newConfirm = '';
    } catch (err) {
      error = errorMessage(err);
    } finally {
      busy = false;
    }
  }

  async function rotate() {
    if (!drive.keys) return;
    busy = true;
    error = null;
    try {
      const st = vault.status?.bundle ? vault.status : await vault.refresh();
      const r = await withNewRecoveryKey(st.bundle!, drive.keys);
      vault.status = await client.e2eeUpdateBundle(r.bundle, drive.keys.authKey);
      newRecovery = r.recoveryKey;
    } catch (err) {
      error = errorMessage(err);
    } finally {
      busy = false;
    }
  }

  function lockNow() {
    vault.lock();
    toast.info('Locked. Enter your passphrase to see your files again.');
  }
</script>

<Card title="End-to-end encryption" subtitle="Encrypt files and names on your devices, so the server can't read them." glow={encState === 'on'}>
  {#if encState === 'off'}
    <div class="stack">
      <p class="muted">
        Off. The server can read your files (as with most self-hosted storage). Turn this on if the server's disk, its
        administrator or a backup of it shouldn't be able to.
      </p>
      {#if !cryptoAvailable()}<p class="error" role="alert"><Icon name="alert" size={16} /> {INSECURE_CONTEXT_MESSAGE}</p>{/if}
      <div class="row-end">
        <Button variant="primary" icon="lock" disabled={!cryptoAvailable()} onclick={() => (setupOpen = true)}>Turn on…</Button>
      </div>
    </div>
  {:else}
    <div class="stack">
      <p class="status"><Icon name="shield" size={18} /> <strong>On</strong>
        {#if vault.status?.enabled_at}<span class="muted">since {formatDate(vault.status.enabled_at)}</span>{/if}
      </p>
      <dl>
        <dt>Key fingerprint</dt>
        <dd class="mono">{vault.status?.fingerprint ?? '…'}</dd>
      </dl>
      <p class="faint">Only you have the key. The web app decrypts in this tab and forgets the key when you sign out, lock, or close the tab.</p>
      {#if error}<p class="error" role="alert"><Icon name="alert" size={16} /> {error}</p>{/if}
      <div class="actions">
        <Button size="sm" icon="key" onclick={() => ((changing = true), (error = null))}>Change passphrase</Button>
        <Button size="sm" icon="refresh" onclick={() => ((rotating = true), (error = null), (newRecovery = null))}>New recovery key</Button>
        <Button size="sm" variant="ghost" icon="lock" onclick={lockNow}>Lock now</Button>
      </div>
    </div>
  {/if}
</Card>

<Modal open={setupOpen} title="Turn on end-to-end encryption" size="md" onclose={() => (setupOpen = false)} dismissible={false}>
  {#if setupOpen}
    <EncryptionSetup oncancel={() => (setupOpen = false)} ondone={() => (setupOpen = false)} />
  {/if}
</Modal>

<Modal open={changing} title="Change encryption passphrase" size="sm" onclose={() => (changing = false)}>
  <form class="stack" onsubmit={changePassphrase} novalidate aria-label="Change encryption passphrase">
    <p class="muted">Your files aren't re-encrypted: the key is re-wrapped under the new passphrase. Your recovery key keeps working.</p>
    <Input label="New passphrase" type="password" revealable bind:value={newPass} autocomplete="new-password" error={newPass ? passError : null} />
    <Input label="Repeat it" type="password" revealable bind:value={newConfirm} autocomplete="new-password" error={newConfirm && newConfirm !== newPass ? "Passphrases don't match." : null} />
    {#if error}<p class="error" role="alert"><Icon name="alert" size={16} /> {error}</p>{/if}
    <div class="row-end">
      <Button type="submit" variant="primary" loading={busy} disabled={!!passError || newPass !== newConfirm}>Change passphrase</Button>
    </div>
  </form>
</Modal>

<Modal open={rotating} title="New recovery key" size="md" onclose={() => (rotating = false)} dismissible={!busy}>
  <div class="stack">
    {#if newRecovery}
      <p>Your new recovery key. The old one no longer works. Save this one now: it won't be shown again.</p>
      <RecoveryKeyBox recoveryKey={newRecovery} username={session.user?.username} />
      <div class="row-end"><Button variant="primary" onclick={() => (rotating = false)}>I've saved it</Button></div>
    {:else}
      <p class="muted">Use this if your recovery key may have been seen by someone else. The current one stops working immediately.</p>
      {#if error}<p class="error" role="alert"><Icon name="alert" size={16} /> {error}</p>{/if}
      <div class="row-end">
        <Button variant="ghost" onclick={() => (rotating = false)}>Cancel</Button>
        <Button variant="primary" loading={busy} onclick={rotate}>Create new recovery key</Button>
      </div>
    {/if}
  </div>
</Modal>

<style>
  .error {
    display: flex;
    align-items: flex-start;
    gap: var(--space-2);
    padding: var(--space-2) var(--space-3);
    border: 1px solid var(--danger);
    border-radius: var(--radius-md);
    background: var(--danger-soft);
    color: var(--danger);
    font-size: var(--text-sm);
  }
  .stack {
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
  }
  .row-end {
    display: flex;
    justify-content: flex-end;
    gap: var(--space-2);
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
  }
  .status {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    color: var(--ok);
  }
  .muted {
    color: var(--text-muted);
    font-size: var(--text-sm);
  }
  .faint {
    color: var(--text-faint);
    font-size: var(--text-sm);
  }
  dl {
    display: flex;
    gap: var(--space-3);
    font-size: var(--text-sm);
  }
  dt {
    color: var(--text-muted);
  }
  .mono {
    font-family: var(--font-mono);
  }
  .error {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    color: var(--danger);
    font-size: var(--text-sm);
  }
</style>
