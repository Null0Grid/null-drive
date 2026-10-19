<script lang="ts">
  import { onMount } from 'svelte';
  import Logo from '$lib/components/Logo.svelte';
  import Button from '$lib/components/Button.svelte';
  import Input from '$lib/components/Input.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { api, errText } from '../bridge';
  import type { DiscoveredServer, ServerInfo } from '../types';

  // First run: server → (trust certificate) → how to add this computer
  // (password or pairing code) → this computer. The Go side then stores
  // the device token in the OS keychain and starts syncing.

  interface Props {
    ondone: () => void;
  }
  let { ondone }: Props = $props();

  type Step = 'server' | 'trust' | 'method' | 'account' | 'code' | 'device';
  type Method = 'password' | 'code';
  let step = $state<Step>('server');
  let method = $state<Method>('password');
  let busy = $state(false);
  let error = $state<string | null>(null);

  let scanning = $state(true);
  let found = $state<DiscoveredServer[]>([]);
  let serverInput = $state('');
  let server = $state<ServerInfo | null>(null);
  let pin = $state('');
  let username = $state('');
  let password = $state('');
  let code = $state('');
  let deviceName = $state('');
  let folder = $state('');

  onMount(async () => {
    deviceName = await api.defaultDeviceName();
    folder = await api.defaultFolder();
    await scan();
  });

  async function scan() {
    scanning = true;
    try {
      found = await api.discover();
    } catch {
      found = [];
    } finally {
      scanning = false;
    }
  }

  async function check(url: string) {
    error = null;
    busy = true;
    pin = '';
    try {
      const info = await api.testServer(url);
      server = info;
      serverInput = info.url;
      step = info.cert && !info.cert.trusted ? 'trust' : 'method';
    } catch (err) {
      error = errText(err);
    } finally {
      busy = false;
    }
  }

  function checkManual(e: SubmitEvent) {
    e.preventDefault();
    void check(serverInput);
  }

  async function trust() {
    if (!server?.cert) return;
    error = null;
    busy = true;
    try {
      const fp = server.cert.fingerprint;
      const info = await api.testServer(server.url, fp);
      server = { ...info, cert: server.cert };
      pin = fp;
      step = 'method';
    } catch (err) {
      error = errText(err);
    } finally {
      busy = false;
    }
  }

  function choose(m: Method) {
    method = m;
    error = null;
    step = m === 'password' ? 'account' : 'code';
  }

  function toDevice(e: SubmitEvent) {
    e.preventDefault();
    if (method === 'password') {
      error = username && password ? null : 'Enter your username and password.';
    } else {
      error = code.trim() ? null : 'Enter the pairing code shown in the web app.';
    }
    if (!error) step = 'device';
  }

  // A pasted nulldrive://pair link carries the server address too.
  async function codeChanged() {
    const link = await api.parsePairLink(code).catch(() => ({}) as { server?: string; code?: string });
    if (link.server && link.code) {
      code = link.code;
      if (link.server !== server?.url) await check(link.server).then(() => (step = 'code'));
    }
  }

  async function browse() {
    try {
      const picked = await api.chooseFolder(folder);
      if (picked) folder = picked;
    } catch (err) {
      error = errText(err);
    }
  }

  async function connect(e: SubmitEvent) {
    e.preventDefault();
    error = null;
    busy = true;
    try {
      const common = { server_url: server!.url, device_name: deviceName, folder, cert_sha256: pin };
      if (method === 'password') {
        await api.setup({ ...common, username, password });
        password = '';
      } else {
        await api.pairWithCode({ ...common, code });
      }
      ondone();
    } catch (err) {
      error = errText(err);
      if (/password/i.test(error)) step = 'account';
      else if (/code|attempts/i.test(error)) step = 'code';
    } finally {
      busy = false;
    }
  }

  function back(to: Step) {
    error = null;
    step = to;
  }

  const order: Step[] = ['server', 'method', 'account', 'device'];
  const index = $derived(order.indexOf(step === 'trust' ? 'server' : step === 'code' ? 'account' : step));
</script>

<div class="setup">
  <div class="frame">
    <header>
      <Logo size={30} />
      <ol class="dots" aria-label="Setup progress">
        {#each order as s, i (s)}
          <li class:on={i <= index} aria-current={i === index ? 'step' : undefined}><span class="sr-only">Step {i + 1}</span></li>
        {/each}
      </ol>
    </header>

    <section class="panel">
      {#if step === 'server'}
        <div class="stack">
          <p class="eyebrow">Connect</p>
          <h1>Find your server</h1>
          <div class="lan" aria-busy={scanning}>
            <div class="lan-head">
              <h2>On this network</h2>
              <Button variant="ghost" size="sm" icon="refresh" onclick={scan} disabled={scanning}>{scanning ? 'Scanning…' : 'Scan again'}</Button>
            </div>
            {#if scanning && found.length === 0}
              <p class="scan"><span class="pulse" aria-hidden="true"></span> Looking for Null Drive servers…</p>
            {:else if found.length === 0}
              <p class="none">
                No servers answered. Discovery only works on the same network segment (it doesn't cross VLANs or VPNs, and a server in
                Docker needs host networking), so enter the address below.
              </p>
            {:else}
              <ul class="found">
                {#each found as f (f.url)}
                  <li>
                    <button type="button" class="server" onclick={() => check(f.url)} disabled={busy}>
                      <Icon name="disk" size={22} />
                      <span class="who">
                        <strong>{f.name}</strong>
                        <span class="where">{f.address} · {f.version}</span>
                      </span>
                      {#if !f.setup_done}<span class="tag">Not set up</span>{/if}
                      <Icon name="chevron-right" size={18} />
                    </button>
                  </li>
                {/each}
              </ul>
            {/if}
          </div>
          <form onsubmit={checkManual} novalidate class="stack">
            <Input label="Or enter an address" bind:value={serverInput} placeholder="192.168.1.20:8080 or https://drive.example.com" icon="link" autocomplete="url" spellcheck="false" />
            {#if error}<p class="error" role="alert"><Icon name="alert" size={16} /> {error}</p>{/if}
            <div class="actions">
              <Button type="submit" variant="primary" icon="arrow-right" loading={busy}>Continue</Button>
            </div>
          </form>
        </div>
      {:else if step === 'trust' && server?.cert}
        <div class="stack">
          <p class="eyebrow warn">Certificate</p>
          <h1>Trust this server's certificate?</h1>
          <p class="lead">
            <code>{server.url}</code> uses a certificate your computer can't verify{server.cert.self_signed ? ' (it is self-signed)' : ''}.
            That's normal for a homelab server. Before trusting it, compare the fingerprint with the one shown in your server's
            settings or by <code>openssl x509 -fingerprint -sha256</code>.
          </p>
          <dl class="cert">
            <dt>SHA-256 fingerprint</dt>
            <dd class="fp">{server.cert.fingerprint}</dd>
            <dt>Issued to</dt>
            <dd>{server.cert.subject || '—'}</dd>
            <dt>Expires</dt>
            <dd>{new Date(server.cert.not_after).toLocaleDateString()}</dd>
          </dl>
          <p class="note">Null Drive will only accept this exact certificate from now on and will warn you if it ever changes.</p>
          {#if error}<p class="error" role="alert"><Icon name="alert" size={16} /> {error}</p>{/if}
          <div class="actions split">
            <Button variant="ghost" icon="chevron-left" onclick={() => back('server')}>Back</Button>
            <Button variant="primary" icon="key" loading={busy} onclick={trust}>Trust this certificate</Button>
          </div>
        </div>
      {:else if step === 'method'}
        <div class="stack">
          <p class="eyebrow">Add this computer</p>
          <h1>How do you want to sign in?</h1>
          <p class="lead">
            Connected to <code>{server?.url}</code> ({server?.version}){#if pin}, certificate pinned{/if}.
          </p>
          {#if server?.warning}
            <p class="warning" role="alert"><Icon name="alert" size={16} /> <span>{server.warning}</span></p>
          {/if}
          <div class="methods">
            <button type="button" class="method" onclick={() => choose('code')}>
              <Icon name="device" size={24} />
              <strong>Pair with a code</strong>
              <span>No password on this computer. In the web app open Settings → Devices → <em>Add device</em>.</span>
            </button>
            <button type="button" class="method" onclick={() => choose('password')}>
              <Icon name="key" size={24} />
              <strong>Sign in with password</strong>
              <span>Used once to register this computer, never stored.</span>
            </button>
          </div>
          <div class="actions split">
            <Button variant="ghost" icon="chevron-left" onclick={() => back('server')}>Back</Button>
          </div>
        </div>
      {:else if step === 'account'}
        <form onsubmit={toDevice} novalidate>
          <p class="eyebrow">Sign in</p>
          <h1>Your Null Drive account</h1>
          <p class="lead">Your password is used once to register this computer and is never stored.</p>
          <div class="fields">
            <Input label="Username" bind:value={username} autocomplete="username" autocapitalize="none" spellcheck="false" />
            <Input label="Password" type="password" revealable bind:value={password} autocomplete="current-password" />
          </div>
          {#if error}<p class="error" role="alert"><Icon name="alert" size={16} /> {error}</p>{/if}
          <div class="actions split">
            <Button variant="ghost" icon="chevron-left" onclick={() => back('method')}>Back</Button>
            <Button type="submit" variant="primary" icon="arrow-right">Continue</Button>
          </div>
        </form>
      {:else if step === 'code'}
        <form onsubmit={toDevice} novalidate>
          <p class="eyebrow">Pair</p>
          <h1>Enter the pairing code</h1>
          <p class="lead">
            In the web app on <code>{server?.url}</code>, open Settings → Devices → <em>Add device</em>. Codes work once and expire after
            5 minutes. You can also paste the pairing link.
          </p>
          <div class="fields">
            <Input
              label="Pairing code"
              bind:value={code}
              placeholder="NULL-XXXX-XX"
              autocomplete="one-time-code"
              autocapitalize="characters"
              spellcheck="false"
              onchange={codeChanged}
              class="code-input"
            />
          </div>
          {#if error}<p class="error" role="alert"><Icon name="alert" size={16} /> {error}</p>{/if}
          <div class="actions split">
            <Button variant="ghost" icon="chevron-left" onclick={() => back('method')}>Back</Button>
            <Button type="submit" variant="primary" icon="arrow-right">Continue</Button>
          </div>
        </form>
      {:else}
        <form onsubmit={connect} novalidate>
          <p class="eyebrow">This computer</p>
          <h1>Name it and pick a folder</h1>
          <p class="lead">Everything in this folder stays in sync with your server. Existing files in it are uploaded.</p>
          <div class="fields">
            <Input label="Computer name" bind:value={deviceName} hint="Shown in the web app's device list and in conflict copies." />
            <div class="folder">
              <Input label="Folder to sync" bind:value={folder} spellcheck="false" />
              <Button icon="folder" onclick={browse}>Browse…</Button>
            </div>
          </div>
          {#if error}<p class="error" role="alert"><Icon name="alert" size={16} /> {error}</p>{/if}
          <div class="actions split">
            <Button variant="ghost" icon="chevron-left" onclick={() => back(method === 'password' ? 'account' : 'code')} disabled={busy}>Back</Button>
            <Button type="submit" variant="primary" icon="link" loading={busy}>{method === 'password' ? 'Connect and sync' : 'Pair and sync'}</Button>
          </div>
        </form>
      {/if}
    </section>
  </div>
</div>

<style>
  .setup {
    min-height: 100dvh;
    display: grid;
    place-items: center;
    padding: var(--space-5) var(--space-4);
  }
  .frame {
    width: 100%;
    max-width: 34rem;
    display: flex;
    flex-direction: column;
    gap: var(--space-5);
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  .dots {
    display: flex;
    gap: var(--space-2);
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .dots li {
    width: 2rem;
    height: 4px;
    border-radius: 2px;
    background: var(--border-strong);
  }
  .dots li.on {
    background: var(--primary);
    box-shadow: var(--glow-primary);
  }
  .panel {
    background: var(--panel);
    border: 1px solid var(--border);
    border-top: 2px solid var(--primary);
    border-radius: var(--radius-lg);
    padding: var(--space-6);
    box-shadow: var(--shadow-overlay);
  }
  form {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }
  .eyebrow {
    font-family: var(--font-term);
    font-size: 1.25rem;
    color: var(--accent);
  }
  h1 {
    font-size: var(--text-xl);
  }
  .lead {
    color: var(--text-muted);
    font-size: var(--text-sm);
  }
  .fields {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }
  .folder {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: end;
    gap: var(--space-2);
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--space-2);
    margin-top: var(--space-2);
  }
  .actions.split {
    justify-content: space-between;
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
  .stack {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }
  .eyebrow.warn {
    color: var(--warn);
  }
  .lan {
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    padding: var(--space-3);
    background: var(--bg);
  }
  .lan-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: var(--space-2);
  }
  .lan h2 {
    font-family: var(--font-body);
    font-size: var(--text-sm);
    font-weight: 600;
    color: var(--text-muted);
    text-transform: none;
  }
  .scan,
  .none {
    color: var(--text-muted);
    font-size: var(--text-sm);
    display: flex;
    align-items: center;
    gap: var(--space-2);
    padding: var(--space-2) 0;
  }
  .pulse {
    width: 0.6rem;
    height: 0.6rem;
    border-radius: 50%;
    background: var(--primary);
    box-shadow: var(--glow-primary);
    animation: pulse 1.2s ease-in-out infinite;
  }
  @keyframes pulse {
    50% {
      opacity: 0.25;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .pulse {
      animation: none;
    }
  }
  .found {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
  }
  .server,
  .method {
    width: 100%;
    font: inherit;
    color: var(--text);
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    cursor: pointer;
    text-align: left;
  }
  .server {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto auto;
    align-items: center;
    gap: var(--space-3);
    padding: var(--space-3);
  }
  .server:hover,
  .server:focus-visible,
  .method:hover,
  .method:focus-visible {
    border-color: var(--primary);
    outline: none;
    box-shadow: var(--glow-primary);
  }
  .server:disabled {
    opacity: 0.6;
    cursor: wait;
  }
  .who {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .where {
    color: var(--text-muted);
    font-size: var(--text-sm);
    font-family: var(--font-mono);
  }
  .tag {
    font-size: var(--text-xs);
    color: var(--warn);
    border: 1px solid var(--warn);
    border-radius: 999px;
    padding: 0 var(--space-2);
  }
  .methods {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--space-3);
  }
  .method {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    padding: var(--space-4);
  }
  .method span {
    color: var(--text-muted);
    font-size: var(--text-sm);
  }
  .cert {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    gap: var(--space-2) var(--space-4);
    margin: 0;
    font-size: var(--text-sm);
  }
  .cert dt {
    color: var(--text-muted);
  }
  .cert dd {
    margin: 0;
    overflow-wrap: anywhere;
  }
  .fp {
    font-family: var(--font-mono);
    color: var(--warn);
    line-height: 1.5;
  }
  .note {
    font-size: var(--text-sm);
    color: var(--text-muted);
  }
  .warning {
    display: flex;
    align-items: flex-start;
    gap: var(--space-2);
    padding: var(--space-2) var(--space-3);
    border: 1px solid var(--warn);
    border-radius: var(--radius-md);
    background: var(--warn-soft, transparent);
    color: var(--warn);
    font-size: var(--text-sm);
  }
  :global(.code-input input) {
    font-family: var(--font-mono);
    letter-spacing: 0.12em;
    text-transform: uppercase;
  }
  @media (max-width: 480px) {
    .methods {
      grid-template-columns: 1fr;
    }
  }
  @media (max-width: 480px) {
    .panel {
      padding: var(--space-5) var(--space-4);
    }
  }
</style>
