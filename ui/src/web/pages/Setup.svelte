<script lang="ts">
  import PairDialog from '../components/PairDialog.svelte';
  import Logo from '../../lib/components/Logo.svelte';
  import Button from '../../lib/components/Button.svelte';
  import Input from '../../lib/components/Input.svelte';
  import Icon from '../../lib/components/Icon.svelte';
  import ProgressBar from '../../lib/components/ProgressBar.svelte';

  // Where the desktop apps are published (the last step links to them).
  const STORE_URL = 'https://apps.microsoft.com/detail/9P3NQGJ8ZZMG';
  const RELEASES_URL = 'https://github.com/Null0Grid/null-drive/releases/latest';
  import PasswordStrength from '../../lib/components/PasswordStrength.svelte';
  import { ApiError } from '../../lib/api/client';
  import type { SystemInfo } from '../../lib/api/types';
  import { formatBytes } from '../../lib/util/format';
  import { client } from '../api';
  import { session } from '../session.svelte';
  import { router } from '../router.svelte';
  import { errorMessage } from '../actions';
  import EncryptionSetup from '../components/EncryptionSetup.svelte';

  type Step = 'welcome' | 'account' | 'storage' | 'privacy' | 'done';
  const steps: { id: Step; label: string }[] = [
    { id: 'welcome', label: 'Welcome' },
    { id: 'account', label: 'Account' },
    { id: 'storage', label: 'Storage' },
    { id: 'privacy', label: 'Privacy' },
    { id: 'done', label: 'Done' },
  ];

  // Resume where a reload interrupted us (after the account exists).
  let step = $state<Step>((session.wizardStep as Step | null) ?? 'welcome');
  const index = $derived(steps.findIndex((s) => s.id === step));

  // ---- account ----
  let username = $state('admin');
  let password = $state('');
  let confirm = $state('');
  let submitted = $state(false);
  let busy = $state(false);
  let formError = $state<string | null>(null);

  const usernameError = $derived(
    !/^[A-Za-z0-9._-]{1,64}$/.test(username) ? 'Use 1–64 letters, digits, dots, dashes or underscores.' : null,
  );
  const passwordError = $derived(password.length < 8 ? 'At least 8 characters.' : null);
  const confirmError = $derived(confirm !== password ? "Passwords don't match." : null);

  async function createAccount(e: SubmitEvent) {
    e.preventDefault();
    submitted = true;
    formError = null;
    if (usernameError || passwordError || confirmError) return;
    busy = true;
    try {
      const res = await client.setup(username, password);
      password = confirm = '';
      session.setWizardStep('storage');
      session.setUser(res.user);
      go('storage');
    } catch (err) {
      if (err instanceof ApiError && err.code === 'setup_complete') {
        formError = 'This server was already set up. Sign in instead.';
        setTimeout(() => location.assign('/login'), 1500);
      } else {
        formError = errorMessage(err);
      }
    } finally {
      busy = false;
    }
  }

  // ---- storage ----
  let info = $state<SystemInfo | null>(null);
  let infoError = $state<string | null>(null);
  $effect(() => {
    if (step === 'storage' && !info) {
      client.systemInfo().then(
        (i) => (info = i),
        (e) => (infoError = errorMessage(e)),
      );
    }
  });
  const used = $derived(info && info.disk_total_bytes ? 1 - info.disk_free_bytes / info.disk_total_bytes : null);
  const lowSpace = $derived(!!info && !info.disk_error && info.disk_free_bytes < 5 * 1024 ** 3);

  function go(s: Step) {
    step = s;
    if (s === 'storage' || s === 'privacy' || s === 'done') session.setWizardStep(s);
    // Move focus to the new step's heading for screen-reader and keyboard users.
    queueMicrotask(() => document.getElementById('step-title')?.focus());
  }

  function finish() {
    session.setWizardStep(null);
    router.navigate('/files', { replace: true });
  }

  let pairOpen = $state(false);
  let encrypting = $state(false);
</script>

<div class="wizard">
  <div class="frame">
    <header class="top">
      <Logo size={30} />
      <ol class="steps" aria-label="Setup progress">
        {#each steps as s, i (s.id)}
          <li class:done={i < index} class:current={i === index} aria-current={i === index ? 'step' : undefined}>
            <span class="dot">{#if i < index}<Icon name="check" size={12} strokeWidth={3} />{:else}{i + 1}{/if}</span>
            <span class="label">{s.label}</span>
          </li>
        {/each}
      </ol>
    </header>

    <section class="panel" aria-labelledby="step-title">
      {#if step === 'welcome'}
        <div class="hero">
          <p class="eyebrow">First-run setup</p>
          <h1 id="step-title" tabindex="-1">Your files.<br /><span class="neon">Your server.</span></h1>
          <p class="lead">
            Null Drive keeps a folder on your computers in sync with this server. Nothing leaves your network unless you
            want it to.
          </p>
          <ul class="points">
            <li><Icon name="disk" /> Files are stored on this machine, in a folder you control.</li>
            <li><Icon name="refresh" /> Edits from two devices never silently overwrite each other.</li>
            <li><Icon name="laptop" /> Use it in the browser, and sync folders with the desktop app for Windows and Linux.</li>
          </ul>
          <div class="actions">
            <Button variant="primary" size="lg" icon="arrow-right" onclick={() => go('account')}>Get started</Button>
          </div>
          <p class="foot">Setup takes about a minute.</p>
        </div>
      {:else if step === 'account'}
        <form onsubmit={createAccount} novalidate>
          <p class="eyebrow">Step 2 of 5</p>
          <h1 id="step-title" tabindex="-1">Create the admin account</h1>
          <p class="lead">This is the account you'll sign in with. You can change the password later in Settings.</p>
          <div class="fields">
            <Input
              label="Username"
              bind:value={username}
              autocomplete="username"
              autocapitalize="none"
              spellcheck="false"
              error={submitted ? usernameError : null}
              required
            />
            <Input
              label="Password"
              type="password"
              revealable
              bind:value={password}
              autocomplete="new-password"
              error={submitted ? passwordError : null}
              data-autofocus
              required
            >
              {#snippet after()}
                <PasswordStrength {password} {username} />
              {/snippet}
            </Input>
            <Input
              label="Confirm password"
              type="password"
              revealable
              bind:value={confirm}
              autocomplete="new-password"
              error={submitted || (confirm.length >= password.length && confirm.length > 0) ? confirmError : null}
              required
            />
          </div>
          {#if formError}<p class="form-error" role="alert"><Icon name="alert" size={16} /> {formError}</p>{/if}
          <div class="actions split">
            <Button variant="ghost" icon="chevron-left" onclick={() => go('welcome')} disabled={busy}>Back</Button>
            <Button type="submit" variant="primary" loading={busy}>Create account</Button>
          </div>
        </form>
      {:else if step === 'storage'}
        <div>
          <p class="eyebrow">Step 3 of 5</p>
          <h1 id="step-title" tabindex="-1">Confirm storage location</h1>
          <p class="lead">Everything you upload is stored here, together with the Null Drive database.</p>
          {#if infoError}
            <p class="form-error" role="alert"><Icon name="alert" size={16} /> {infoError}</p>
          {:else if !info}
            <ProgressBar label="Checking storage" />
          {:else}
            <div class="storage">
              <div class="path-row">
                <Icon name="disk" size={22} />
                <code class="path" data-testid="data-dir">{info.data_dir}</code>
              </div>
              {#if info.disk_error}
                <p class="note warn"><Icon name="alert" size={16} /> Free space could not be determined ({info.disk_error}).</p>
              {:else}
                <ProgressBar
                  value={used}
                  label="Disk usage"
                  showLabel
                  tone={lowSpace ? 'warn' : 'primary'}
                  detail="{formatBytes(info.disk_free_bytes)} free of {formatBytes(info.disk_total_bytes)}"
                />
                {#if lowSpace}
                  <p class="note warn"><Icon name="alert" size={16} /> Less than 5 GB free. Consider a bigger disk.</p>
                {/if}
              {/if}
              <p class="note">
                <Icon name="info" size={16} />
                <span>To use a different disk, stop the server and set <code>NULLDRIVE_DATA_DIR</code> (or change the volume in
                  Docker), then start it again.</span>
              </p>
            </div>
          {/if}
          <div class="actions">
            <Button variant="primary" icon="arrow-right" onclick={() => go('privacy')} disabled={!info && !infoError}>Looks good</Button>
          </div>
        </div>
      {:else if step === 'privacy'}
        <div>
          <p class="eyebrow">Step 4 of 5 · optional</p>
          <h1 id="step-title" tabindex="-1">End-to-end encryption</h1>
          {#if session.user?.e2ee === 'on'}
            <p class="lead">Encryption is on for {session.user.username}. Files are encrypted on your devices before upload.</p>
            <div class="actions"><Button variant="primary" icon="arrow-right" onclick={() => go('done')}>Continue</Button></div>
          {:else if encrypting}
            <EncryptionSetup oncancel={() => (encrypting = false)} ondone={() => go('done')} />
          {:else}
            <p class="lead">
              Encrypt files and their names on your devices, so this server (and anyone who gets hold of its disk) can't read
              them. You can also turn it on later in Settings.
            </p>
            <ul class="points">
              <li><Icon name="lock" /> Needs a separate encryption passphrase, typed once on each device.</li>
              <li><Icon name="key" /> You get a recovery key to print or store. Lose both, and the files are gone.</li>
              <li><Icon name="info" /> Thumbnails in the web app are off for encrypted files.</li>
            </ul>
            <div class="actions">
              <Button variant="ghost" onclick={() => go('done')}>Skip for now</Button>
              <Button variant="primary" icon="lock" onclick={() => (encrypting = true)}>Turn on encryption</Button>
            </div>
          {/if}
        </div>
      {:else}
        <div>
          <p class="eyebrow">All set</p>
          <h1 id="step-title" tabindex="-1">Install the desktop app</h1>
          <p class="lead">
            Your server is ready. Install Null Drive on each computer to keep a folder in sync. Until then, the web app
            does everything.
          </p>
          <ul class="platforms">
            <li>
              <Icon name="window" size={22} />
              <div><strong>Windows</strong><span>Microsoft Store · installer</span></div>
              <Button size="sm" icon="download" onclick={() => open(STORE_URL, '_blank', 'noopener')}>Get it</Button>
            </li>
            <li>
              <Icon name="laptop" size={22} />
              <div><strong>macOS</strong><span>Universal app</span></div>
              <Button size="sm" disabled title="Available in a future release">Coming soon</Button>
            </li>
            <li>
              <Icon name="terminal" size={22} />
              <div><strong>Linux</strong><span>AppImage · Flatpak · .deb · .rpm</span></div>
              <Button size="sm" icon="download" onclick={() => open(RELEASES_URL, '_blank', 'noopener')}>Download</Button>
            </li>
          </ul>
          <div class="pairing">
            <div>
              <strong><Icon name="link" size={16} /> Pair a device</strong>
              <p>Connect a computer with a short one-time code: no password typed on it. On the same network the app finds this server by itself.</p>
            </div>
            <Button icon="device" class="pair-btn" onclick={() => (pairOpen = true)}>Show a code</Button>
          </div>
          <div class="actions">
            <Button variant="primary" size="lg" icon="folder" onclick={finish}>Open Null Drive</Button>
          </div>
        </div>
      {/if}
    </section>
  </div>
</div>

<PairDialog open={pairOpen} onclose={() => (pairOpen = false)} />

<style>
  .wizard {
    min-height: 100dvh;
    display: grid;
    place-items: center;
    padding: var(--space-6) var(--space-4);
  }
  .frame {
    width: 100%;
    max-width: 38rem;
    display: flex;
    flex-direction: column;
    gap: var(--space-5);
  }
  .top {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-4);
    flex-wrap: wrap;
  }
  .steps {
    display: flex;
    gap: var(--space-3);
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .steps li {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    color: var(--text-faint);
    font-size: var(--text-xs);
    text-transform: uppercase;
    letter-spacing: 0.08em;
  }
  .dot {
    display: grid;
    place-items: center;
    width: 1.5rem;
    height: 1.5rem;
    border-radius: 50%;
    border: 1px solid var(--border-strong);
    font-size: var(--text-xs);
  }
  .steps li.current {
    color: var(--primary);
  }
  .steps li.current .dot {
    border-color: var(--primary);
    box-shadow: var(--glow-primary);
  }
  .steps li.done {
    color: var(--text-muted);
  }
  .steps li.done .dot {
    background: var(--primary);
    border-color: var(--primary);
    color: var(--primary-ink);
  }
  .panel {
    background: var(--panel);
    border: 1px solid var(--border);
    border-top: 2px solid var(--primary);
    border-radius: var(--radius-lg);
    padding: var(--space-6);
    box-shadow: var(--shadow-overlay);
  }
  .eyebrow {
    font-family: var(--font-term);
    font-size: 1.25rem;
    color: var(--accent);
    letter-spacing: 0.04em;
    margin-bottom: var(--space-2);
  }
  h1 {
    font-size: var(--text-xl);
  }
  h1:focus {
    outline: none;
  }
  .hero h1 {
    font-size: clamp(1.75rem, 6vw, 2.5rem);
  }
  .neon {
    color: var(--primary);
    text-shadow: var(--glow-text);
  }
  .lead {
    margin-top: var(--space-3);
    color: var(--text-muted);
    max-width: 60ch;
  }
  .points {
    list-style: none;
    margin: var(--space-5) 0 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
    font-size: var(--text-sm);
  }
  .points li {
    display: flex;
    gap: var(--space-3);
    align-items: flex-start;
  }
  .points :global(.icon) {
    color: var(--primary);
    margin-top: 2px;
  }
  .fields {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
    margin-top: var(--space-5);
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--space-2);
    margin-top: var(--space-6);
  }
  .hero .actions {
    justify-content: flex-start;
  }
  .actions.split {
    justify-content: space-between;
  }
  .foot {
    margin-top: var(--space-3);
    font-size: var(--text-xs);
    color: var(--text-faint);
  }
  .form-error {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    margin-top: var(--space-4);
    padding: var(--space-3);
    border: 1px solid var(--danger);
    border-radius: var(--radius-md);
    background: var(--danger-soft);
    color: var(--danger);
    font-size: var(--text-sm);
  }
  .storage {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
    margin-top: var(--space-5);
  }
  .path-row {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    padding: var(--space-3) var(--space-4);
    border: 1px solid var(--primary-line);
    border-radius: var(--radius-md);
    background: var(--bg-sunken);
    color: var(--primary);
  }
  .path {
    background: none;
    border: 0;
    padding: 0;
    color: var(--text);
    overflow-wrap: anywhere;
  }
  .note {
    display: flex;
    gap: var(--space-2);
    align-items: flex-start;
    font-size: var(--text-sm);
    color: var(--text-muted);
  }
  .note :global(.icon) {
    margin-top: 2px;
  }
  .note.warn {
    color: var(--warn);
  }
  .platforms {
    list-style: none;
    margin: var(--space-5) 0 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
  }
  .platforms li {
    display: grid;
    grid-template-columns: auto 1fr auto;
    align-items: center;
    gap: var(--space-3);
    padding: var(--space-3) var(--space-4);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    background: var(--surface);
    color: var(--text-muted);
  }
  .platforms div {
    display: flex;
    flex-direction: column;
  }
  .platforms strong {
    color: var(--text);
    font-weight: 500;
  }
  .platforms span {
    font-size: var(--text-xs);
  }
  .pairing {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-4);
    margin-top: var(--space-4);
    padding: var(--space-4);
    border: 1px solid var(--border-strong);
    border-radius: var(--radius-md);
    color: var(--text-muted);
  }
  .pairing :global(.pair-btn) {
    flex: none;
  }
  .pairing strong {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    color: var(--text);
    font-weight: 500;
  }
  .pairing p {
    margin-top: var(--space-1);
    font-size: var(--text-xs);
  }
  @media (max-width: 560px) {
    .panel {
      padding: var(--space-5) var(--space-4);
    }
    .steps .label {
      display: none;
    }
    .pairing {
      flex-direction: column;
      align-items: flex-start;
    }
  }
</style>
