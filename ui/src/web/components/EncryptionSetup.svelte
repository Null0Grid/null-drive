<script lang="ts">
  import { untrack } from 'svelte';
  import Button from '../../lib/components/Button.svelte';
  import Input from '../../lib/components/Input.svelte';
  import Icon from '../../lib/components/Icon.svelte';
  import ProgressBar from '../../lib/components/ProgressBar.svelte';
  import PasswordStrength from '../../lib/components/PasswordStrength.svelte';
  import { checkPassphrase, createKeys, MIN_PASSPHRASE, type Keys } from '../../lib/crypto/keys';
  import type { KeyBundle } from '../../lib/api/types';
  import { client, drive } from '../api';
  import { session } from '../session.svelte';
  import { vault } from '../vault.svelte';
  import { errorMessage } from '../actions';
  import RecoveryKeyBox from './RecoveryKeyBox.svelte';

  // Turning on end-to-end encryption, in order:
  //   1. choose a passphrase (separate from the login password)
  //   2. keys are created here in the browser; the recovery key is shown
  //      and must be confirmed BEFORE anything is sent to the server, so an
  //      account can't end up encrypted with an unsaved recovery key
  //   3. the wrapped keys go to the server
  //   4. existing files are re-uploaded encrypted, then the server deletes
  //      the plaintext (resumable if interrupted)

  interface Props {
    /** Called when encryption is fully on. */
    ondone?: () => void;
    /** Called when the user backs out before anything was sent. */
    oncancel?: () => void;
    /** Start straight at the migration (resuming an interrupted one). */
    resume?: boolean;
  }
  let { ondone, oncancel, resume = false }: Props = $props();

  type Step = 'passphrase' | 'creating' | 'recovery' | 'enabling' | 'migrating' | 'done';
  let step = $state<Step>(untrack(() => (resume ? 'migrating' : 'passphrase')));
  let pass = $state('');
  let confirm = $state('');
  let understood = $state(false);
  let submitted = $state(false);
  let error = $state<string | null>(null);

  let created: { bundle: KeyBundle; keys: Keys; recoveryKey: string } | null = null;
  let recoveryKey = $state('');
  let typedGroup = $state('');
  let saved = $state(false);
  let progress = $state({ done: 0, total: 0, path: '' });

  const passError = $derived(checkPassphrase(pass));
  const confirmError = $derived(confirm !== pass ? "Passphrases don't match." : null);
  const lastGroup = $derived(recoveryKey.split('-').pop() ?? '');
  const groupOK = $derived(typedGroup.trim().toUpperCase() === lastGroup);

  async function choose(e: SubmitEvent) {
    e.preventDefault();
    submitted = true;
    error = null;
    if (passError || confirmError || !understood) return;
    step = 'creating';
    try {
      created = await createKeys(pass);
      recoveryKey = created.recoveryKey;
      step = 'recovery';
    } catch (err) {
      error = errorMessage(err);
      step = 'passphrase';
    }
  }

  async function enable() {
    if (!created || !groupOK || !saved) return;
    step = 'enabling';
    error = null;
    try {
      const st = await client.e2eeEnable(created.bundle, created.keys.authKey);
      vault.use(created.keys);
      vault.status = st;
      // session.user.e2ee is updated when everything is done, so the app
      // doesn't switch screens under a running migration.
      pass = confirm = '';
      if (st.state === 'migrating') await migrate();
      else finish();
    } catch (err) {
      error = errorMessage(err);
      step = 'recovery';
    }
  }

  async function migrate() {
    step = 'migrating';
    error = null;
    try {
      await drive.migrate((p) => (progress = p));
      finish();
    } catch (err) {
      error = errorMessage(err);
    }
  }

  function finish() {
    step = 'done';
    if (session.user) session.user = { ...session.user, e2ee: 'on' };
    void vault.refresh().catch(() => {});
  }

  $effect(() => {
    if (resume && step === 'migrating' && progress.total === 0 && !error) void migrate();
  });
</script>

<div class="enc">
  {#if step === 'passphrase' || step === 'creating'}
    <form class="stack" onsubmit={choose} novalidate aria-label="Choose an encryption passphrase">
      <div class="explain">
        <p>
          Files and folder names are encrypted <strong>on your devices</strong> before they're uploaded. The server stores only
          ciphertext; whoever runs it, or steals its disk, can't read your files.
        </p>
        <ul>
          <li><Icon name="key" size={16} /> The passphrase is separate from your login password and never leaves your devices.</li>
          <li><Icon name="laptop" size={16} /> Each new device and browser asks for it once.</li>
          <li><Icon name="alert" size={16} /> Encrypting existing files removes their unencrypted history.</li>
        </ul>
      </div>
      <Input
        label="Encryption passphrase"
        type="password"
        revealable
        bind:value={pass}
        autocomplete="new-password"
        hint="At least {MIN_PASSPHRASE} characters. A few random words work well."
        error={submitted ? passError : null}
      >
        {#snippet after()}<PasswordStrength password={pass} username={session.user?.username} />{/snippet}
      </Input>
      <Input label="Repeat it" type="password" revealable bind:value={confirm} autocomplete="new-password" error={submitted ? confirmError : null} />
      <label class="check" class:invalid={submitted && !understood}>
        <input type="checkbox" bind:checked={understood} />
        <span>I understand that if I lose both this passphrase and the recovery key, my files <strong>can't be recovered</strong> by anyone.</span>
      </label>
      {#if error}<p class="error" role="alert"><Icon name="alert" size={16} /> {error}</p>{/if}
      <div class="row">
        {#if oncancel}<Button variant="ghost" onclick={oncancel}>Not now</Button>{/if}
        <Button type="submit" variant="primary" icon="lock" loading={step === 'creating'}>Create keys</Button>
      </div>
    </form>
  {:else if step === 'recovery' || step === 'enabling'}
    <div class="stack">
      <p>
        This is your <strong>recovery key</strong>. It unlocks your files if you forget the passphrase. It's shown only now: download
        or print it and keep it somewhere safe and offline.
      </p>
      <RecoveryKeyBox {recoveryKey} username={session.user?.username} />
      <Input label="Type the last group of the key to confirm ({lastGroup.length} characters)" bind:value={typedGroup} autocomplete="off" spellcheck="false" />
      <label class="check">
        <input type="checkbox" bind:checked={saved} />
        <span>I've saved my recovery key.</span>
      </label>
      {#if error}<p class="error" role="alert"><Icon name="alert" size={16} /> {error}</p>{/if}
      <div class="row">
        {#if oncancel && step === 'recovery'}<Button variant="ghost" onclick={oncancel}>Cancel</Button>{/if}
        <Button variant="primary" icon="shield" disabled={!groupOK || !saved} loading={step === 'enabling'} onclick={enable}>
          Turn on encryption
        </Button>
      </div>
    </div>
  {:else if step === 'migrating'}
    <div class="stack" aria-live="polite">
      <p>Encrypting your existing files. Keep this tab open; if it's interrupted, you can continue later from where it stopped.</p>
      <ProgressBar
        value={progress.total ? progress.done / progress.total : undefined}
        label={progress.total ? `${progress.done} of ${progress.total}` : 'Preparing'}
        showLabel
      />
      {#if progress.path}<p class="faint path">{progress.path}</p>{/if}
      {#if error}
        <p class="error" role="alert"><Icon name="alert" size={16} /> {error}</p>
        <div class="row"><Button variant="primary" icon="refresh" onclick={migrate}>Try again</Button></div>
      {/if}
    </div>
  {:else}
    <div class="stack done">
      <p class="ok"><Icon name="shield" size={20} /> End-to-end encryption is on.</p>
      <p class="faint">Other devices will ask for the passphrase the next time they connect.</p>
      <div class="row"><Button variant="primary" onclick={() => ondone?.()}>Done</Button></div>
    </div>
  {/if}
</div>

<style>
  .stack {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }
  .explain p {
    color: var(--text-muted);
    font-size: var(--text-sm);
  }
  .explain ul {
    list-style: none;
    margin-top: var(--space-3);
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    font-size: var(--text-sm);
  }
  .explain li {
    display: flex;
    gap: var(--space-2);
    align-items: flex-start;
  }
  .check {
    display: flex;
    gap: var(--space-2);
    align-items: flex-start;
    font-size: var(--text-sm);
  }
  .check input {
    margin-top: 0.2rem;
    accent-color: var(--primary);
  }
  .check.invalid {
    color: var(--danger);
  }
  .row {
    display: flex;
    justify-content: flex-end;
    gap: var(--space-2);
    flex-wrap: wrap;
  }
  .error {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    color: var(--danger);
    font-size: var(--text-sm);
  }
  .faint {
    color: var(--text-faint);
    font-size: var(--text-sm);
  }
  .path {
    font-family: var(--font-mono);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .ok {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    color: var(--ok);
    font-weight: 600;
  }
</style>
