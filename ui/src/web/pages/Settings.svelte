<script lang="ts">
  import { onMount } from 'svelte';
  import Card from '../../lib/components/Card.svelte';
  import Button from '../../lib/components/Button.svelte';
  import Input from '../../lib/components/Input.svelte';
  import Icon from '../../lib/components/Icon.svelte';
  import ProgressBar from '../../lib/components/ProgressBar.svelte';
  import PasswordStrength from '../../lib/components/PasswordStrength.svelte';
  import Modal from '../../lib/components/Modal.svelte';
  import { toast } from '../../lib/toast.svelte';
  import { ApiError } from '../../lib/api/client';
  import type { Device, ServerSettings, Stats, SystemInfo, TokenScope } from '../../lib/api/types';
  import { formatBytes, formatDate, formatDateTime } from '../../lib/util/format';
  import { getScanlines, getTheme, setScanlines, setTheme, type Theme } from '../../lib/theme';
  import { client } from '../api';
  import { session } from '../session.svelte';
  import { errorMessage, signOut } from '../actions';
  import ConfirmDialog from '../components/ConfirmDialog.svelte';
  import PairDialog from '../components/PairDialog.svelte';
  import EncryptionCard from '../components/EncryptionCard.svelte';
  import TwoFactorCard from '../components/TwoFactorCard.svelte';
  import AuditCard from '../components/AuditCard.svelte';
  import { live } from '../live.svelte';
  import { router } from '../router.svelte';

  // ---------- password ----------
  let current = $state('');
  let next = $state('');
  let confirm = $state('');
  let pwBusy = $state(false);
  let pwSubmitted = $state(false);
  let pwError = $state<string | null>(null);
  const nextError = $derived(next.length < 8 ? 'At least 8 characters.' : next === current ? 'Choose a different password.' : null);
  const confirmError = $derived(confirm !== next ? "Passwords don't match." : null);

  async function changePassword(e: SubmitEvent) {
    e.preventDefault();
    pwSubmitted = true;
    pwError = null;
    if (!current) {
      pwError = 'Enter your current password.';
      return;
    }
    if (nextError || confirmError) return;
    pwBusy = true;
    try {
      await client.changePassword(current, next);
      current = next = confirm = '';
      pwSubmitted = false;
      toast.success('Password changed. Other browser sessions were signed out.');
    } catch (err) {
      pwError = err instanceof ApiError && err.status === 401 ? 'Current password is incorrect.' : errorMessage(err);
    } finally {
      pwBusy = false;
    }
  }

  // ---------- devices ----------
  let devices = $state<Device[] | null>(null);
  let devicesError = $state<string | null>(null);
  let showRevoked = $state(false);
  let revoking = $state<Device | null>(null);
  const activeDevices = $derived((devices ?? []).filter((d) => !d.revoked_at));
  const revokedDevices = $derived((devices ?? []).filter((d) => d.revoked_at));

  async function loadDevices() {
    try {
      devices = await client.listDevices();
      devicesError = null;
    } catch (e) {
      devicesError = errorMessage(e);
    }
  }

  async function revoke() {
    const d = revoking!;
    try {
      await client.revokeDevice(d.id);
      toast.success(`“${d.name}” can no longer sync.`);
    } catch (e) {
      toast.error(`Couldn't revoke “${d.name}”: ${errorMessage(e)}`);
    }
    await loadDevices();
  }

  // Pairing is the normal way to add a device (no password typed on it).
  let pairing = $state(false);

  // Manual device registration for scripts and the CLI. The token is shown
  // exactly once.
  let adding = $state(false);
  let newName = $state('');
  let addBusy = $state(false);
  let addError = $state<string | null>(null);
  let newToken = $state<string | null>(null);
  let newScope = $state<TokenScope>('full');
  let copied = $state(false);
  let copiedURL = $state(false);
  const serverURL = window.location.origin;

  // Null Search calls GET /search from the browser with a token it keeps;
  // a search-only token can't read, change or delete files.
  function connectSearch() {
    newName = 'Null Search';
    newScope = 'search';
    adding = true;
  }

  async function addDevice(e: SubmitEvent) {
    e.preventDefault();
    addError = newName.trim() ? null : 'Give the device a name.';
    if (addError) return;
    addBusy = true;
    try {
      const res = await client.createDevice(newName.trim(), newScope);
      newToken = res.token;
      await loadDevices();
    } catch (err) {
      addError = errorMessage(err);
    } finally {
      addBusy = false;
    }
  }
  function closeAdd() {
    adding = false;
    newName = '';
    newToken = null;
    newScope = 'full';
    addError = null;
    copied = false;
    copiedURL = false;
  }
  async function copyURL() {
    try {
      await navigator.clipboard.writeText(serverURL);
      copiedURL = true;
    } catch {
      /* visible to copy by hand */
    }
  }
  async function copyToken() {
    if (!newToken) return;
    try {
      await navigator.clipboard.writeText(newToken);
      copied = true;
    } catch {
      toast.warning('Copy failed. Select the token and copy it manually.');
    }
  }

  // ---------- appearance ----------
  let theme = $state<Theme>(getTheme());
  let scanlines = $state(getScanlines());

  // ---------- system ----------
  let info = $state<SystemInfo | null>(null);
  const used = $derived(info && info.disk_total_bytes ? 1 - info.disk_free_bytes / info.disk_total_bytes : null);

  // ---------- storage & history ----------
  let stats = $state<Stats | null>(null);
  let fullDays = $state('');
  let dailyDays = $state('');
  let trashDays = $state('');
  let retBusy = $state(false);
  let gcBusy = $state(false);
  const retInvalid = $derived(
    !/^\d{1,4}$/.test(fullDays) || !/^\d{1,4}$/.test(dailyDays) || !/^\d{1,4}$/.test(trashDays) || Number(trashDays) < 1,
  );
  // Dedup savings: what a store without chunking would hold (every
  // retained version in full) vs. the distinct chunks actually stored.
  const saved = $derived(stats && stats.history_bytes > 0 ? Math.max(0, 1 - stats.stored_bytes / stats.history_bytes) : 0);

  async function loadStats() {
    try {
      stats = await client.stats();
      fullDays = String(stats.retention_full_days);
      dailyDays = String(stats.retention_daily_days);
      trashDays = String(stats.trash_days);
    } catch {
      stats = null;
    }
  }
  async function saveRetention(e: SubmitEvent) {
    e.preventDefault();
    if (retInvalid) return;
    retBusy = true;
    try {
      await client.updateSettings({
        retention_full_days: Number(fullDays),
        retention_daily_days: Number(dailyDays),
        trash_days: Number(trashDays),
      });
      toast.success('Retention policy saved. It applies at the next cleanup (hourly).');
      await loadStats();
    } catch (err) {
      toast.error(errorMessage(err));
    } finally {
      retBusy = false;
    }
  }
  async function cleanup() {
    gcBusy = true;
    try {
      const r = await client.runGC();
      toast.success(
        `Cleanup done: ${r.versions_pruned} old version${r.versions_pruned === 1 ? '' : 's'} removed, ${formatBytes(r.bytes_freed)} freed.`,
      );
      await loadStats();
    } catch (err) {
      toast.error(errorMessage(err));
    } finally {
      gcBusy = false;
    }
  }

  // ---------- LAN discovery (admin) ----------
  let srv = $state<ServerSettings | null>(null);
  let srvName = $state('');
  let srvBusy = $state(false);
  let srvError = $state<string | null>(null);

  async function loadSettings() {
    try {
      srv = await client.getSettings();
      srvName = srv.server_name;
    } catch {
      srv = null;
    }
  }
  async function saveSettings(patch: Partial<Pick<ServerSettings, 'server_name' | 'mdns_enabled'>>) {
    srvBusy = true;
    srvError = null;
    try {
      srv = await client.updateSettings(patch);
      srvName = srv.server_name;
      toast.success('Server settings saved.');
    } catch (e) {
      srvError = errorMessage(e);
    } finally {
      srvBusy = false;
    }
  }
  function saveName(e: SubmitEvent) {
    e.preventDefault();
    if (srvName.trim() && srvName.trim() !== srv?.server_name) void saveSettings({ server_name: srvName.trim() });
  }

  function clientLabel(v?: string) {
    // "NullDrive/0.5.0 (windows/amd64)" -> "Desktop 0.5.0 · Windows"
    const m = v?.match(/^NullDrive\/(\S+) \((\w+)\//);
    if (!m) return '';
    const os: Record<string, string> = { windows: 'Windows', darwin: 'macOS', linux: 'Linux' };
    return `Desktop ${m[1]} · ${os[m[2]!] ?? m[2]}`;
  }

  onMount(() => {
    void loadDevices();
    void loadStats();
    if (session.user?.is_admin) void loadSettings();
    // Online dots follow the devices' realtime connections.
    const off = live.on((m) => {
      if (m.type === 'presence' && devices) {
        devices = devices.map((d) => (d.id === m.device_id ? { ...d, online: !!m.online, last_seen_at: m.online ? new Date().toISOString() : d.last_seen_at } : d));
      }
    });
    client.systemInfo().then(
      (i) => (info = i),
      () => (info = null),
    );
    return off;
  });
</script>

<svelte:head><title>Settings · Null Drive</title></svelte:head>

<div class="settings">
  <h1>Settings</h1>

  <div class="grid">
    <Card title="Account" subtitle="Signed in as {session.user?.username}{session.user?.is_admin ? ' · administrator' : ''}">
      <form class="stack" onsubmit={changePassword} novalidate aria-label="Change password">
        <Input label="Current password" type="password" revealable bind:value={current} autocomplete="current-password" />
        <Input
          label="New password"
          type="password"
          revealable
          bind:value={next}
          autocomplete="new-password"
          error={pwSubmitted ? nextError : null}
        >
          {#snippet after()}<PasswordStrength password={next} username={session.user?.username} />{/snippet}
        </Input>
        <Input
          label="Confirm new password"
          type="password"
          revealable
          bind:value={confirm}
          autocomplete="new-password"
          error={pwSubmitted ? confirmError : null}
        />
        {#if pwError}<p class="error" role="alert"><Icon name="alert" size={16} /> {pwError}</p>{/if}
        <div class="row-end">
          <Button type="submit" variant="primary" icon="key" loading={pwBusy}>Change password</Button>
        </div>
      </form>
    </Card>

    <EncryptionCard />

    <TwoFactorCard />

    <Card title="Devices" subtitle="Computers and apps allowed to sync with this server.">
      {#snippet actions()}
        <Button size="sm" variant="ghost" icon="search" onclick={connectSearch}>Connect Null Search</Button>
        <Button size="sm" icon="plus" onclick={() => (pairing = true)}>Add device</Button>
      {/snippet}
      {#if devicesError}
        <p class="error" role="alert"><Icon name="alert" size={16} /> {devicesError}</p>
      {:else if devices === null}
        <ProgressBar label="Loading devices" />
      {:else if activeDevices.length === 0}
        <p class="muted">No devices yet. Install the desktop app and choose <em>Add device</em> to pair it with a code.</p>
      {:else}
        <ul class="devices" aria-label="Active devices">
          {#each activeDevices as d (d.id)}
            <li>
              <span class="dev-icon" class:live={d.online}>
                <Icon name={d.scope === 'search' ? 'search' : 'laptop'} />
                <span class="presence" aria-hidden="true"></span>
              </span>
              <div class="dev-info">
                <strong
                  >{d.name}
                  {#if d.scope === 'search'}<span class="scope" title="Can only search your files: no downloads or changes">Search only</span
                    >{:else}<span class="state" class:on={d.online}>{d.online ? 'Online' : 'Offline'}</span>{/if}</strong
                >
                <span title={d.last_seen_at ? formatDateTime(d.last_seen_at) : undefined}>
                  {d.online ? 'Connected now' : d.last_seen_at ? `Last seen ${formatDate(d.last_seen_at)}` : 'Never connected'}{d.last_ip ? ` · ${d.last_ip}` : ''}
                </span>
                <span class="faint">
                  {clientLabel(d.client_version) || 'Token / script'} · {d.added_via === 'pairing' ? 'paired' : 'signed in'} {formatDate(d.created_at)}
                </span>
              </div>
              <Button size="sm" variant="danger" onclick={() => (revoking = d)}>Revoke</Button>
            </li>
          {/each}
        </ul>
      {/if}
      {#if revokedDevices.length > 0}
        <button type="button" class="link" onclick={() => (showRevoked = !showRevoked)} aria-expanded={showRevoked}>
          {showRevoked ? 'Hide' : 'Show'} {revokedDevices.length} revoked
        </button>
        {#if showRevoked}
          <ul class="devices revoked" aria-label="Revoked devices">
            {#each revokedDevices as d (d.id)}
              <li>
                <span class="dev-icon"><Icon name="laptop" /></span>
                <div class="dev-info">
                  <strong>{d.name}</strong>
                  <span>Revoked {formatDate(d.revoked_at!)}</span>
                </div>
              </li>
            {/each}
          </ul>
        {/if}
      {/if}
    </Card>

    <Card title="Storage" subtitle="Your files, their history, and what that actually takes on disk.">
      {#if stats}
        <div class="stat-grid">
          <div class="stat">
            <span class="num">{formatBytes(stats.logical_bytes)}</span>
            <span class="lbl">{stats.files.toLocaleString()} file{stats.files === 1 ? '' : 's'}</span>
          </div>
          <div class="stat">
            <span class="num">{formatBytes(stats.history_bytes)}</span>
            <span class="lbl">with {stats.versions.toLocaleString()} versions</span>
          </div>
          <div class="stat accent">
            <span class="num" data-testid="stored-bytes">{formatBytes(stats.stored_bytes)}</span>
            <span class="lbl">actually stored</span>
          </div>
        </div>
        <ProgressBar
          value={stats.history_bytes ? stats.stored_bytes / stats.history_bytes : 0}
          label="Stored vs. full size"
          showLabel
          detail={saved > 0.005 ? `${Math.round(saved * 100)}% saved by chunking and dedup` : 'No duplicate data yet'}
        />
        {#if stats.server_stored_bytes !== undefined && session.user?.is_admin}
          <p class="muted small">
            Server total: {formatBytes(stats.server_stored_bytes)} in {stats.server_chunks?.toLocaleString()} chunks{#if stats.server_reclaimable_bytes}, {formatBytes(stats.server_reclaimable_bytes)} awaiting cleanup{/if}.
          </p>
        {/if}
        <p class="muted small">
          History: every version kept {stats.retention_full_days} days, then one per day for {stats.retention_daily_days} more.
          Deleted files can be restored for {stats.trash_days} days:
          <a href="/trash" onclick={(e) => (e.preventDefault(), router.navigate('/trash'))}>{stats.trash_files} recently deleted</a>.
        </p>
        {#if session.user?.is_admin}
          <form class="ret" onsubmit={saveRetention} novalidate aria-label="Retention policy">
            <Input label="Keep every version (days)" bind:value={fullDays} inputmode="numeric" />
            <Input label="Then one per day (days)" bind:value={dailyDays} inputmode="numeric" />
            <Input label="Keep deleted files (days)" bind:value={trashDays} inputmode="numeric" />
            <div class="ret-actions">
              <Button
                type="submit"
                size="sm"
                loading={retBusy}
                disabled={retInvalid ||
                  (Number(fullDays) === stats.retention_full_days &&
                    Number(dailyDays) === stats.retention_daily_days &&
                    Number(trashDays) === stats.trash_days)}>Save</Button
              >
              <Button size="sm" variant="ghost" icon="trash" loading={gcBusy} onclick={cleanup}>Clean up now</Button>
            </div>
          </form>
          {#if retInvalid}<p class="error small" role="alert">Use whole numbers of days (0–9999; deleted files at least 1).</p>{/if}
        {/if}
      {:else}
        <ProgressBar label="Loading storage stats" />
      {/if}
    </Card>

    {#if session.user?.is_admin && srv}
      <Card title="LAN discovery" subtitle="Lets desktop apps on this network find the server without typing an address.">
        <label class="toggle">
          <input
            type="checkbox"
            checked={srv.mdns_enabled && !srv.mdns_forced_off}
            disabled={srv.mdns_forced_off || srvBusy}
            onchange={(e) => saveSettings({ mdns_enabled: e.currentTarget.checked })}
          />
          <span class="switch" aria-hidden="true"></span>
          <span>Announce on the local network <small>(mDNS · _nulldrive._tcp)</small></span>
        </label>
        {#if srv.mdns_forced_off}
          <p class="muted small">Turned off by <code>NULLDRIVE_MDNS=off</code> on the server.</p>
        {:else if srv.mdns_active}
          <p class="disc-ok small"><span class="dot" aria-hidden="true"></span> Announcing <span data-testid="mdns-addrs">{srv.mdns_addrs?.join(', ')}</span></p>
        {/if}
        {#if srv.mdns_error}<p class="warnline small" role="status"><Icon name="alert" size={14} /> {srv.mdns_error}</p>{/if}
        <form class="name-row" onsubmit={saveName} novalidate>
          <Input label="Server name" bind:value={srvName} hint="Shown as “Null Drive on {srvName || '…'}”." maxlength={63} />
          <Button type="submit" size="sm" disabled={srvBusy || !srvName.trim() || srvName.trim() === srv.server_name}>Save</Button>
        </form>
        {#if srvError}<p class="error" role="alert"><Icon name="alert" size={16} /> {srvError}</p>{/if}
        <p class="muted small">Discovery doesn't cross subnets or VLANs. Off-LAN devices pair with a code instead.</p>
      </Card>
    {/if}

    {#if session.user?.is_admin}
      <AuditCard />
    {/if}

    <Card title="Appearance">
      <fieldset class="themes">
        <legend class="sr-only">Theme</legend>
        {#each [{ v: 'dark', label: 'Dark', icon: 'moon' }, { v: 'light', label: 'Light', icon: 'sun' }] as const as o (o.v)}
          <label class:checked={theme === o.v}>
            <input
              type="radio"
              name="theme"
              value={o.v}
              checked={theme === o.v}
              onchange={() => {
                theme = o.v;
                setTheme(o.v);
              }}
            />
            <Icon name={o.icon} />
            {o.label}
          </label>
        {/each}
      </fieldset>
      <label class="toggle">
        <input
          type="checkbox"
          checked={scanlines}
          onchange={(e) => {
            scanlines = e.currentTarget.checked;
            setScanlines(scanlines);
          }}
        />
        <span class="switch" aria-hidden="true"></span>
        <span>CRT scanlines <small>(cosmetic)</small></span>
      </label>
      <p class="muted small">Saved in this browser only.</p>
    </Card>

    <Card title="Server">
      {#if info}
        <dl>
          <dt>Version</dt>
          <dd><code>{info.version}</code></dd>
          <dt>Data folder</dt>
          <dd><code class="path" data-testid="data-dir">{info.data_dir}</code></dd>
        </dl>
        {#if !info.disk_error}
          <ProgressBar
            value={used}
            label="Disk usage"
            showLabel
            tone={info.disk_free_bytes < 5 * 1024 ** 3 ? 'warn' : 'primary'}
            detail="{formatBytes(info.disk_free_bytes)} free of {formatBytes(info.disk_total_bytes)}"
          />
        {/if}
      {:else}
        <ProgressBar label="Loading server info" />
      {/if}
      <div class="row-end signout">
        <Button icon="logout" onclick={signOut}>Sign out</Button>
      </div>
    </Card>
  </div>
</div>

<ConfirmDialog
  open={revoking !== null}
  title="Revoke “{revoking?.name ?? ''}”?"
  message="This device stops syncing immediately and will need to be added again. Files already on it are not deleted."
  confirmLabel="Revoke"
  danger
  onconfirm={revoke}
  onclose={() => (revoking = null)}
/>

<PairDialog
  open={pairing}
  onclose={() => (pairing = false)}
  onpaired={() => void loadDevices()}
  onmanual={() => {
    pairing = false;
    adding = true;
  }}
/>

<Modal
  open={adding}
  title={newToken ? (newScope === 'search' ? 'Null Search token' : 'Device token') : newScope === 'search' ? 'Connect Null Search' : 'Create an API token'}
  size="md"
  onclose={closeAdd}
>
  {#if newToken && newScope === 'search'}
    <p class="muted">
      In Null Search, open <em>Drive</em> settings and paste these two values. The token is shown only once; it can search your
      files and nothing else, and you can revoke it here any time.
    </p>
    <span class="field-label">Server URL</span>
    <div class="token">
      <code data-testid="server-url">{serverURL}</code>
      <Button size="sm" icon={copiedURL ? 'check' : 'copy'} onclick={copyURL}>{copiedURL ? 'Copied' : 'Copy'}</Button>
    </div>
    <span class="field-label">Token</span>
    <div class="token">
      <code data-testid="device-token">{newToken}</code>
      <Button size="sm" icon={copied ? 'check' : 'copy'} onclick={copyToken}>{copied ? 'Copied' : 'Copy'}</Button>
    </div>
    <p class="muted small">Null Search is a website, so this server must be reachable over HTTPS from the browser you search in.</p>
  {:else if newToken}
    <p class="muted">
      Copy this token now: it is shown only once. Use it as <code>Authorization: Bearer …</code> for scripts, or paste it
      into the desktop app.
    </p>
    <div class="token">
      <code data-testid="device-token">{newToken}</code>
      <Button size="sm" icon={copied ? 'check' : 'copy'} onclick={copyToken}>{copied ? 'Copied' : 'Copy'}</Button>
    </div>
  {:else}
    <form id="add-device" onsubmit={addDevice} novalidate>
      <Input label="Name" placeholder="e.g. Work laptop" bind:value={newName} error={addError} data-autofocus />
      <fieldset class="scopes">
        <legend>What can it do?</legend>
        {#each [{ v: 'full', label: 'Everything', hint: 'Sync, scripts, the full API' }, { v: 'search', label: 'Search only', hint: 'For Null Search: finds files, can’t open or change them' }] as const as o (o.v)}
          <label class:checked={newScope === o.v}>
            <input type="radio" name="scope" value={o.v} checked={newScope === o.v} onchange={() => (newScope = o.v)} />
            <span><strong>{o.label}</strong><span class="faint">{o.hint}</span></span>
          </label>
        {/each}
      </fieldset>
    </form>
    <p class="muted small">
      {#if newScope === 'search'}Searches use this token's account only.{:else}For scripts and the API. For the desktop app, use
        <em>Add device</em> and pair with a code instead.{/if}
    </p>
  {/if}
  {#snippet footer()}
    {#if newToken}
      <Button variant="primary" onclick={closeAdd}>Done</Button>
    {:else}
      <Button variant="ghost" onclick={closeAdd}>Cancel</Button>
      <Button type="submit" form="add-device" variant="primary" loading={addBusy}>Create token</Button>
    {/if}
  {/snippet}
</Modal>

<style>
  .settings {
    max-width: 64rem;
    display: flex;
    flex-direction: column;
    gap: var(--space-5);
  }
  h1 {
    font-size: var(--text-xl);
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(min(100%, 26rem), 1fr));
    gap: var(--space-4);
    align-items: start;
  }
  .stack {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }
  .row-end {
    display: flex;
    justify-content: flex-end;
  }
  .signout {
    margin-top: var(--space-5);
    padding-top: var(--space-4);
    border-top: 1px solid var(--border);
  }
  .error {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    color: var(--danger);
    font-size: var(--text-sm);
  }
  .muted {
    color: var(--text-muted);
    font-size: var(--text-sm);
  }
  .small {
    font-size: var(--text-xs);
    margin-top: var(--space-3);
  }
  .devices {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
  }
  .devices li {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    align-items: center;
    gap: var(--space-3);
    padding: var(--space-3) 0;
    border-bottom: 1px solid var(--border);
  }
  .devices li:last-child {
    border-bottom: 0;
  }
  .dev-icon {
    position: relative;
    display: grid;
    place-items: center;
    width: 2.25rem;
    height: 2.25rem;
    border-radius: var(--radius-md);
    background: var(--surface);
    border: 1px solid var(--border);
    color: var(--text-faint);
  }
  .stat-grid {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: var(--space-3);
    margin-bottom: var(--space-4);
  }
  .stat {
    display: flex;
    flex-direction: column;
    gap: var(--space-1);
    padding: var(--space-3);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    background: var(--bg-sunken);
    min-width: 0;
  }
  .stat .num {
    font-family: var(--font-term);
    font-size: 1.6rem;
    line-height: 1;
    white-space: nowrap;
  }
  .stat .lbl {
    color: var(--text-muted);
    font-size: var(--text-xs);
  }
  .stat.accent .num {
    color: var(--ok);
  }
  .ret {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr));
    gap: var(--space-3);
    margin-top: var(--space-4);
    align-items: end;
  }
  .ret-actions {
    grid-column: 1 / -1;
    display: flex;
    gap: var(--space-2);
    justify-content: flex-end;
  }
  @media (max-width: 480px) {
    .stat-grid,
    .ret {
      grid-template-columns: 1fr;
    }
  }
  /* "Lit" only while the device holds a realtime connection. */
  .presence {
    position: absolute;
    right: -3px;
    bottom: -3px;
    width: 0.65rem;
    height: 0.65rem;
    border-radius: 50%;
    background: var(--text-faint);
    border: 2px solid var(--panel);
  }
  .dev-icon.live .presence {
    background: var(--ok);
  }
  .state {
    font-family: var(--font-term);
    font-size: 1rem;
    letter-spacing: 0.04em;
    color: var(--text-faint);
    margin-left: var(--space-2);
  }
  .state.on {
    color: var(--ok);
  }
  .faint {
    color: var(--text-faint);
    font-size: var(--text-xs);
  }
  .scope {
    font-family: var(--font-term);
    font-size: 1rem;
    letter-spacing: 0.04em;
    color: var(--primary);
    margin-left: var(--space-2);
  }
  .field-label {
    display: block;
    margin: var(--space-3) 0 var(--space-1);
    font-size: var(--text-sm);
    color: var(--text-muted);
  }
  .scopes {
    border: 0;
    padding: 0;
    margin: var(--space-4) 0 0;
    display: grid;
    gap: var(--space-2);
  }
  .scopes legend {
    font-size: var(--text-sm);
    color: var(--text-muted);
    margin-bottom: var(--space-2);
  }
  .scopes label {
    display: flex;
    align-items: flex-start;
    gap: var(--space-3);
    padding: var(--space-3);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    cursor: pointer;
  }
  .scopes label.checked {
    border-color: var(--primary);
    box-shadow: var(--glow-primary);
  }
  .scopes label > span {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .scopes input {
    margin-top: 0.2rem;
    accent-color: var(--primary);
  }
  .name-row {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: start;
    gap: var(--space-2);
    margin-top: var(--space-4);
  }
  .name-row :global(.btn) {
    margin-top: 1.6rem;
  }
  .disc-ok {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    color: var(--ok);
    margin-top: var(--space-2);
  }
  .disc-ok .dot {
    width: 0.5rem;
    height: 0.5rem;
    border-radius: 50%;
    background: var(--ok);
    box-shadow: 0 0 6px var(--ok);
  }
  .warnline {
    display: flex;
    gap: var(--space-2);
    align-items: flex-start;
    color: var(--warn);
    margin-top: var(--space-2);
  }
  .dev-icon.live {
    color: var(--ok);
    border-color: var(--ok);
    box-shadow: 0 0 10px var(--ok-soft);
  }
  .dev-info {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .dev-info strong {
    font-weight: 500;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .dev-info span {
    font-size: var(--text-xs);
    color: var(--text-muted);
  }
  .revoked {
    opacity: 0.6;
  }
  .link {
    margin-top: var(--space-3);
    border: 0;
    padding: 0;
    background: none;
    color: var(--primary);
    font-size: var(--text-sm);
    cursor: pointer;
  }
  .themes {
    display: flex;
    gap: var(--space-2);
    border: 0;
    padding: 0;
    margin: 0 0 var(--space-4);
  }
  .themes label {
    flex: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: var(--space-2);
    height: 2.75rem;
    border: 1px solid var(--border-strong);
    border-radius: var(--radius-md);
    background: var(--surface);
    cursor: pointer;
    font-size: var(--text-sm);
  }
  .themes label.checked {
    border-color: var(--primary);
    color: var(--primary);
    box-shadow: var(--glow-primary);
  }
  .themes input,
  .toggle input {
    position: absolute;
    opacity: 0;
    width: 1px;
    height: 1px;
  }
  .themes label:has(input:focus-visible),
  .toggle:has(input:focus-visible) .switch {
    outline: 2px solid var(--primary);
    outline-offset: 2px;
  }
  .toggle {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    cursor: pointer;
    font-size: var(--text-sm);
  }
  .toggle small {
    color: var(--text-faint);
  }
  .switch {
    position: relative;
    flex: none;
    width: 2.5rem;
    height: 1.375rem;
    border-radius: 999px;
    background: var(--bg-sunken);
    border: 1px solid var(--border-strong);
    transition: background var(--dur) var(--ease);
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
    box-shadow: var(--glow-primary);
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
    min-width: 0;
  }
  .path {
    overflow-wrap: anywhere;
  }
  .token {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    margin-top: var(--space-4);
    padding: var(--space-3);
    border: 1px solid var(--primary-line);
    border-radius: var(--radius-md);
    background: var(--bg-sunken);
  }
  .token code {
    flex: 1;
    border: 0;
    background: none;
    padding: 0;
    color: var(--primary);
    overflow-wrap: anywhere;
    user-select: all;
  }
</style>
