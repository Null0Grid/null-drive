<script lang="ts">
  import Button from '$lib/components/Button.svelte';
  import Card from '$lib/components/Card.svelte';
  import Input from '$lib/components/Input.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import { toast } from '$lib/toast.svelte';
  import { api, errText } from '../bridge';
  import type { AppState } from '../types';

  interface Props {
    app: AppState;
    onchange: () => void;
  }
  let { app, onchange }: Props = $props();

  // Editable copies of the saved settings ($state.snapshot-like init).
  let startOnLogin = $state(false);
  let up = $state('0');
  let down = $state('0');
  let poll = $state('30');
  let saving = $state(false);
  let unlinking = $state(false);
  let confirmUnlink = $state(false);

  $effect.pre(() => {
    startOnLogin = app.config.start_on_login;
    up = String(app.config.up_kbps);
    down = String(app.config.down_kbps);
    poll = String(app.config.poll_seconds);
  });

  const num = (s: string) => (/^\d+$/.test(s.trim()) ? Number(s) : NaN);
  const invalid = $derived(Number.isNaN(num(up)) || Number.isNaN(num(down)) || Number.isNaN(num(poll)));

  async function save(e: SubmitEvent) {
    e.preventDefault();
    if (invalid) return;
    saving = true;
    try {
      await api.saveSettings({ start_on_login: startOnLogin, up_kbps: num(up), down_kbps: num(down), poll_seconds: num(poll) });
      toast.success('Settings saved.');
      onchange();
    } catch (err) {
      toast.error(errText(err));
    } finally {
      saving = false;
    }
  }

  async function changeFolder() {
    try {
      const picked = await api.chooseFolder(app.config.folder);
      if (!picked || picked === app.config.folder) return;
      await api.changeFolder(picked);
      toast.success('Now syncing the new folder. The old one was left as it is.');
      onchange();
    } catch (err) {
      toast.error(errText(err));
    }
  }

  async function unlink() {
    unlinking = true;
    try {
      await api.unlink();
      confirmUnlink = false;
      onchange();
    } catch (err) {
      toast.error(errText(err));
    } finally {
      unlinking = false;
    }
  }
</script>

<div class="grid">
  <Card title="Account">
    <dl>
      <dt>Server</dt>
      <dd><code>{app.config.server_url}</code></dd>
      <dt>User</dt>
      <dd>{app.config.username}</dd>
      <dt>This computer</dt>
      <dd>{app.config.device_name}</dd>
    </dl>
    <div class="row">
      <Button variant="danger" onclick={() => (confirmUnlink = true)}>Sign out of this computer</Button>
    </div>
  </Card>

  <Card title="Folder">
    <p class="path"><code>{app.config.folder}</code></p>
    <div class="row">
      <Button icon="folder" onclick={() => api.openFolder()}>Open</Button>
      <Button onclick={changeFolder}>Change…</Button>
    </div>
  </Card>

  <Card title="Sync">
    <form class="stack" onsubmit={save} novalidate>
      {#if app.packaged}
        <p class="muted">Starting at sign-in is managed by Windows: <em>Settings › Apps › Startup</em>.</p>
      {:else}
        <label class="toggle">
          <input type="checkbox" bind:checked={startOnLogin} />
          <span class="switch" aria-hidden="true"></span>
          Start Null Drive when I sign in
        </label>
      {/if}
      <div class="two">
        <Input label="Upload limit (KB/s)" bind:value={up} inputmode="numeric" hint="0 = unlimited" />
        <Input label="Download limit (KB/s)" bind:value={down} inputmode="numeric" hint="0 = unlimited" />
      </div>
      <Input label="Check the server every (seconds)" bind:value={poll} inputmode="numeric" hint="Changes on this computer sync right away." />
      {#if invalid}<p class="error" role="alert">Use whole numbers.</p>{/if}
      <div class="row">
        <Button type="submit" variant="primary" loading={saving} disabled={invalid}>Save</Button>
      </div>
    </form>
  </Card>

  <Card title="History and deleted files">
    <p class="muted">
      Every change is kept as a version on the server. Browse a file's history, restore old versions or bring back deleted files in
      the web app. In the activity list, the history button next to a file opens its history directly.
    </p>
    <div class="row">
      <Button icon="trash" onclick={() => api.openWebApp('trash')}>Recently deleted</Button>
      <Button variant="ghost" icon="arrow-right" onclick={() => api.openWebApp('')}>Open web app</Button>
    </div>
  </Card>

  <Card title="Help">
    <dl>
      <dt>Version</dt>
      <dd><code>{app.version}</code></dd>
    </dl>
    <p class="muted">Logs help with bug reports. They contain file names but never file contents or passwords.</p>
    <div class="row">
      <Button icon="text" onclick={() => api.openLogs()}>Open logs</Button>
    </div>
  </Card>
</div>

<Modal
  open={confirmUnlink}
  title="Sign out of this computer?"
  description="Syncing stops and the saved credentials are removed. Files in your folder stay where they are. To fully revoke this device, also remove it under Settings → Devices in the web app."
  onclose={() => (confirmUnlink = false)}
  dismissible={!unlinking}
>
  {#snippet footer()}
    <Button variant="ghost" onclick={() => (confirmUnlink = false)} disabled={unlinking}>Cancel</Button>
    <Button variant="danger" loading={unlinking} onclick={unlink}>Sign out</Button>
  {/snippet}
</Modal>

<style>
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(min(100%, 22rem), 1fr));
    gap: var(--space-4);
    align-items: start;
  }
  dl {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    gap: var(--space-2) var(--space-4);
    margin: 0 0 var(--space-4);
    font-size: var(--text-sm);
  }
  dt {
    color: var(--text-muted);
  }
  dd {
    margin: 0;
    overflow-wrap: anywhere;
  }
  .path {
    font-size: var(--text-sm);
    overflow-wrap: anywhere;
    margin-bottom: var(--space-4);
  }
  .row {
    display: flex;
    gap: var(--space-2);
    flex-wrap: wrap;
  }
  .stack {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }
  .two {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--space-3);
  }
  .muted {
    color: var(--text-muted);
    font-size: var(--text-sm);
    margin-bottom: var(--space-3);
  }
  .error {
    color: var(--danger);
    font-size: var(--text-sm);
  }
  .toggle {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    font-size: var(--text-sm);
    cursor: pointer;
  }
  .toggle input {
    position: absolute;
    opacity: 0;
    width: 1px;
    height: 1px;
  }
  .switch {
    position: relative;
    flex: none;
    width: 2.5rem;
    height: 1.375rem;
    border-radius: 999px;
    background: var(--bg-sunken);
    border: 1px solid var(--border-strong);
  }
  .switch::after {
    content: '';
    position: absolute;
    top: 2px;
    left: 2px;
    width: 1rem;
    height: 1rem;
    border-radius: 50%;
    background: var(--text-faint);
    transition: transform var(--dur) var(--ease);
  }
  .toggle input:checked + .switch {
    background: var(--primary-soft);
    border-color: var(--primary);
  }
  .toggle input:checked + .switch::after {
    transform: translateX(1.1rem);
    background: var(--primary);
  }
  .toggle input:focus-visible + .switch {
    outline: 2px solid var(--primary);
    outline-offset: 2px;
  }
  @media (max-width: 520px) {
    .two {
      grid-template-columns: 1fr;
    }
  }
</style>
