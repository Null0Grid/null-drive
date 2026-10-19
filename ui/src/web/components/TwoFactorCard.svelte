<script lang="ts">
  import Card from '../../lib/components/Card.svelte';
  import Button from '../../lib/components/Button.svelte';
  import Input from '../../lib/components/Input.svelte';
  import Icon from '../../lib/components/Icon.svelte';
  import Modal from '../../lib/components/Modal.svelte';
  import QrCode from '../../lib/components/QrCode.svelte';
  import { toast } from '../../lib/toast.svelte';
  import { saveBlob } from '../../lib/drive';
  import { client } from '../api';
  import { session } from '../session.svelte';
  import { errorMessage } from '../actions';

  const enabled = $derived(!!session.user?.totp_enabled);

  let open = $state(false);
  let step = $state<'scan' | 'codes'>('scan');
  let secret = $state('');
  let uri = $state('');
  let code = $state('');
  let codes = $state<string[]>([]);
  let busy = $state(false);
  let error = $state<string | null>(null);

  let disabling = $state(false);
  let password = $state('');
  let offCode = $state('');

  async function start() {
    error = null;
    code = '';
    busy = true;
    try {
      const r = await client.totpSetup();
      secret = r.secret;
      uri = r.uri;
      step = 'scan';
      open = true;
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      busy = false;
    }
  }

  async function confirm(e: SubmitEvent) {
    e.preventDefault();
    error = null;
    busy = true;
    try {
      const r = await client.totpEnable(code.replace(/\s/g, ''));
      codes = r.recovery_codes;
      step = 'codes';
      if (session.user) session.user = { ...session.user, totp_enabled: true };
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }

  async function turnOff(e: SubmitEvent) {
    e.preventDefault();
    error = null;
    busy = true;
    try {
      await client.totpDisable(password, offCode.trim());
      if (session.user) session.user = { ...session.user, totp_enabled: false };
      disabling = false;
      password = offCode = '';
      toast.success('Two-factor authentication is off.');
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }

  const grouped = $derived(secret.replace(/(.{4})/g, '$1 ').trim());
</script>

<Card title="Two-factor authentication" subtitle="Ask for a code from your phone when signing in to the web app.">
  <div class="stack">
    {#if enabled}
      <p class="status"><Icon name="shield" size={18} /> <strong>On</strong> <span class="muted">· authenticator app</span></p>
      <p class="faint">Desktop apps you've already added keep syncing; adding a new one needs a signed-in session (and so the code).</p>
      <div class="row-end"><Button size="sm" variant="danger" onclick={() => ((disabling = true), (error = null))}>Turn off</Button></div>
    {:else}
      <p class="muted">Off. Anyone with your password can sign in.</p>
      <div class="row-end"><Button variant="primary" icon="shield" loading={busy} onclick={start}>Set up…</Button></div>
    {/if}
  </div>
</Card>

<Modal open={open} title="Set up two-factor authentication" size="md" onclose={() => (open = false)} dismissible={step === 'scan'}>
  {#if step === 'scan'}
    <form class="stack" onsubmit={confirm} novalidate aria-label="Confirm authenticator code">
      <p class="muted">Scan this with an authenticator app (Aegis, 2FAS, Google Authenticator, 1Password…), then enter the 6-digit code it shows.</p>
      <div class="qr">
        <QrCode text={uri} label="QR code for your authenticator app" />
        <div>
          <p class="faint">Can't scan? Enter this key:</p>
          <p class="mono" data-testid="totp-secret">{grouped}</p>
        </div>
      </div>
      <Input label="Code from the app" bind:value={code} autocomplete="one-time-code" inputmode="numeric" />
      {#if error}<p class="error" role="alert"><Icon name="alert" size={16} /> {error}</p>{/if}
      <div class="row-end">
        <Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
        <Button type="submit" variant="primary" loading={busy} disabled={code.replace(/\s/g, '').length !== 6}>Turn on</Button>
      </div>
    </form>
  {:else}
    <div class="stack">
      <p>Two-factor authentication is on. If you lose your phone, sign in with one of these <strong>recovery codes</strong> (each works once). Save them now; they won't be shown again.</p>
      <ol class="codes" data-testid="totp-recovery-codes">
        {#each codes as c (c)}<li>{c}</li>{/each}
      </ol>
      <div class="row-end">
        <Button size="sm" icon="download" onclick={() => saveBlob(new Blob([`Null Drive recovery codes for ${session.user?.username} (${location.origin})\n\n${codes.join('\n')}\n`], { type: 'text/plain' }), 'null-drive-2fa-recovery-codes.txt')}>Download</Button>
        <Button variant="primary" onclick={() => (open = false)}>Done</Button>
      </div>
    </div>
  {/if}
</Modal>

<Modal open={disabling} title="Turn off two-factor authentication" size="sm" onclose={() => (disabling = false)}>
  <form class="stack" onsubmit={turnOff} novalidate aria-label="Turn off two-factor authentication">
    <Input label="Password" type="password" revealable bind:value={password} autocomplete="current-password" />
    <Input label="Code (or a recovery code)" bind:value={offCode} autocomplete="one-time-code" />
    {#if error}<p class="error" role="alert"><Icon name="alert" size={16} /> {error}</p>{/if}
    <div class="row-end">
      <Button type="submit" variant="danger" loading={busy} disabled={!password || !offCode}>Turn off</Button>
    </div>
  </form>
</Modal>

<style>
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
  .qr {
    display: flex;
    gap: var(--space-4);
    align-items: center;
    flex-wrap: wrap;
  }
  .mono {
    font-family: var(--font-mono);
    word-break: break-all;
    color: var(--primary);
  }
  .codes {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--space-2) var(--space-5);
    padding: var(--space-3) var(--space-3) var(--space-3) var(--space-6);
    border: 1px dashed var(--border-strong);
    border-radius: var(--radius-md);
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
