<script lang="ts">
  import Button from '$lib/components/Button.svelte';
  import Input from '$lib/components/Input.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { api, errText } from '../bridge';

  // Shown instead of the status card when the account is end-to-end
  // encrypted and this computer doesn't have the key yet (new device,
  // paired device, or the account was just switched to encryption).
  interface Props {
    mode: 'locked' | 'migrating';
    onunlocked: () => void;
  }
  let { mode, onunlocked }: Props = $props();

  let secret = $state('');
  let recovery = $state(false);
  let busy = $state(false);
  let error = $state<string | null>(null);

  async function unlock(e: SubmitEvent) {
    e.preventDefault();
    if (!secret.trim()) return;
    busy = true;
    error = null;
    try {
      await api.unlockE2EE(secret, recovery);
      secret = '';
      onunlocked();
    } catch (err) {
      error = errText(err);
    } finally {
      busy = false;
    }
  }

  async function retry() {
    busy = true;
    try {
      await api.retrySync();
      onunlocked();
    } finally {
      busy = false;
    }
  }
</script>

<section class="card" aria-labelledby="unlock-title">
  {#if mode === 'migrating'}
    <h2 id="unlock-title"><Icon name="lock" size={18} /> Encryption is being turned on</h2>
    <p class="muted">Your files are being encrypted from another device or browser. Syncing resumes when that has finished.</p>
    <div class="row"><Button icon="refresh" loading={busy} onclick={retry}>Check again</Button></div>
  {:else}
    <form onsubmit={unlock} novalidate>
      <h2 id="unlock-title"><Icon name="lock" size={18} /> Unlock end-to-end encryption</h2>
      <p class="muted">
        Your files are encrypted before they leave your devices. Enter your {recovery ? 'recovery key' : 'encryption passphrase'} once;
        this computer keeps the key in the system keychain.
      </p>
      {#if recovery}
        <Input label="Recovery key" bind:value={secret} autocomplete="off" spellcheck="false" />
      {:else}
        <Input label="Encryption passphrase" type="password" revealable bind:value={secret} autocomplete="current-password" />
      {/if}
      {#if error}<p class="error" role="alert"><Icon name="alert" size={16} /> {error}</p>{/if}
      <div class="row">
        <button type="button" class="link" onclick={() => ((recovery = !recovery), (secret = ''), (error = null))}>
          {recovery ? 'Use the passphrase' : 'Use the recovery key'}
        </button>
        <Button type="submit" variant="primary" icon="unlock" loading={busy}>Unlock and sync</Button>
      </div>
    </form>
  {/if}
</section>

<style>
  .card {
    padding: var(--space-5);
    background: var(--panel);
    border: 1px solid var(--border);
    border-top: 2px solid var(--primary);
    border-radius: var(--radius-lg);
  }
  form {
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
  }
  h2 {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    font-size: var(--text-lg);
  }
  .muted {
    color: var(--text-muted);
    font-size: var(--text-sm);
    margin: var(--space-2) 0;
  }
  .row {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: var(--space-2);
  }
  .link {
    background: none;
    border: 0;
    color: var(--primary);
    font-size: var(--text-sm);
    cursor: pointer;
  }
  .error {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    color: var(--danger);
    font-size: var(--text-sm);
  }
</style>
