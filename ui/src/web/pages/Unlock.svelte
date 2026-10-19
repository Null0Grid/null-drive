<script lang="ts">
  import Logo from '../../lib/components/Logo.svelte';
  import Button from '../../lib/components/Button.svelte';
  import Input from '../../lib/components/Input.svelte';
  import Icon from '../../lib/components/Icon.svelte';
  import { WrongKeyError, checkPassphrase, withPassphrase, type Keys } from '../../lib/crypto/keys';
  import { client } from '../api';
  import { session } from '../session.svelte';
  import { vault } from '../vault.svelte';
  import { errorMessage, signOut } from '../actions';
  import EncryptionSetup from '../components/EncryptionSetup.svelte';
  import { toast } from '../../lib/toast.svelte';

  // Shown after sign-in for an end-to-end encrypted account: the browser
  // needs the key before it can show a single file name. The key is
  // derived here (Argon2id, ~1 s) and kept in memory for this tab only.

  let secret = $state('');
  let recovery = $state(false);
  let busy = $state(false);
  let error = $state<string | null>(null);

  // After unlocking with the recovery key, offer to set a new passphrase
  // (the keys are held here until then, so the app doesn't open yet).
  let resetting = $state(false);
  let recovered: Keys | null = null;
  let newPass = $state('');
  let newConfirm = $state('');

  async function unlock(e: SubmitEvent) {
    e.preventDefault();
    error = null;
    if (!secret.trim()) {
      error = recovery ? 'Enter your recovery key.' : 'Enter your encryption passphrase.';
      return;
    }
    busy = true;
    try {
      if (recovery) {
        recovered = await vault.open(secret, true);
        resetting = true;
      } else {
        await vault.unlock(secret, false);
      }
      secret = '';
    } catch (err) {
      if (err instanceof WrongKeyError) {
        error = recovery ? "That recovery key doesn't match." : "That passphrase doesn't match.";
        secret = '';
      } else error = errorMessage(err);
    } finally {
      busy = false;
    }
  }

  const newError = $derived(checkPassphrase(newPass));
  async function setPassphrase(e: SubmitEvent) {
    e.preventDefault();
    if (newError || newPass !== newConfirm || !recovered) return;
    busy = true;
    try {
      const st = vault.status ?? (await vault.refresh());
      const nb = await withPassphrase(st.bundle!, recovered, newPass);
      vault.status = await client.e2eeUpdateBundle(nb, recovered.authKey);
      toast.success('New encryption passphrase set. Your recovery key still works.');
      done();
    } catch (err) {
      error = errorMessage(err);
    } finally {
      busy = false;
    }
  }

  function done() {
    resetting = false;
    if (recovered) vault.use(recovered);
    recovered = null;
  }

  const migrating = $derived(session.user?.e2ee === 'migrating');
</script>

<div class="unlock">
  <div class="card">
    <Logo size={40} />
    {#if resetting}
      <form class="stack" onsubmit={setPassphrase} novalidate aria-label="Set a new encryption passphrase">
        <h1>Set a new passphrase</h1>
        <p class="muted">You unlocked with your recovery key. Choose a new passphrase for next time (your recovery key keeps working).</p>
        <Input label="New encryption passphrase" type="password" revealable bind:value={newPass} autocomplete="new-password" error={newPass ? newError : null} />
        <Input label="Repeat it" type="password" revealable bind:value={newConfirm} autocomplete="new-password" error={newConfirm && newConfirm !== newPass ? "Passphrases don't match." : null} />
        {#if error}<p class="error" role="alert"><Icon name="alert" size={16} /> {error}</p>{/if}
        <div class="row">
          <Button variant="ghost" onclick={done}>Skip</Button>
          <Button type="submit" variant="primary" icon="key" loading={busy} disabled={!!newError || newPass !== newConfirm}>Save passphrase</Button>
        </div>
      </form>
    {:else if vault.unlocked && migrating}
      <h1>Finish encrypting your files</h1>
      <EncryptionSetup resume />
    {:else}
      <form class="stack" onsubmit={unlock} novalidate aria-labelledby="unlock-title">
        <div>
          <h1 id="unlock-title"><Icon name="lock" size={22} /> Unlock your files</h1>
          <p class="muted">
            {session.user?.username}'s files are end-to-end encrypted. Enter your
            {recovery ? 'recovery key' : 'encryption passphrase'} to decrypt them in this browser.
          </p>
        </div>
        {#if recovery}
          <Input label="Recovery key" bind:value={secret} autocomplete="off" spellcheck="false" placeholder="XXXX-XXXX-XXXX-XXXX-XXXX-XXXX-XXXX-XXXX" />
        {:else}
          <Input label="Encryption passphrase" type="password" revealable bind:value={secret} autocomplete="current-password" />
        {/if}
        {#if error}<p class="error" role="alert"><Icon name="alert" size={16} /> {error}</p>{/if}
        <Button type="submit" variant="primary" size="lg" full loading={busy} icon="unlock">{busy ? 'Unlocking…' : 'Unlock'}</Button>
        <div class="links">
          <button type="button" class="link" onclick={() => ((recovery = !recovery), (secret = ''), (error = null))}>
            {recovery ? 'Use my passphrase instead' : 'Forgot it? Use the recovery key'}
          </button>
          <button type="button" class="link" onclick={signOut}>Sign out</button>
        </div>
      </form>
    {/if}
  </div>
</div>

<style>
  .unlock {
    min-height: 100dvh;
    display: grid;
    place-items: center;
    padding: var(--space-4);
  }
  .card {
    width: 100%;
    max-width: 28rem;
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
    padding: var(--space-6);
    background: var(--panel);
    border: 1px solid var(--border);
    border-top: 2px solid var(--primary);
    border-radius: var(--radius-lg);
    box-shadow: var(--shadow-overlay);
  }
  .stack {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }
  h1 {
    font-size: var(--text-xl);
    display: flex;
    align-items: center;
    gap: var(--space-2);
  }
  .muted {
    margin-top: var(--space-1);
    color: var(--text-muted);
    font-size: var(--text-sm);
  }
  .error {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    padding: var(--space-2) var(--space-3);
    border: 1px solid var(--danger);
    border-radius: var(--radius-md);
    background: var(--danger-soft);
    color: var(--danger);
    font-size: var(--text-sm);
  }
  .row {
    display: flex;
    justify-content: flex-end;
    gap: var(--space-2);
  }
  .links {
    display: flex;
    justify-content: space-between;
    gap: var(--space-2);
    flex-wrap: wrap;
  }
  .link {
    background: none;
    border: 0;
    padding: 0;
    color: var(--primary);
    font-size: var(--text-sm);
    cursor: pointer;
  }
  .link:hover {
    text-decoration: underline;
  }
  @media (max-width: 480px) {
    .card {
      padding: var(--space-5) var(--space-4);
    }
  }
</style>
