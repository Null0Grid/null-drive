<script lang="ts">
  import { onMount } from 'svelte';
  import Card from '../../lib/components/Card.svelte';
  import Button from '../../lib/components/Button.svelte';
  import Icon from '../../lib/components/Icon.svelte';
  import ProgressBar from '../../lib/components/ProgressBar.svelte';
  import type { AuditEvent } from '../../lib/api/types';
  import { formatDateTime } from '../../lib/util/format';
  import { client } from '../api';
  import { errorMessage } from '../actions';

  // The security log (admins): sign-ins, devices, settings, encryption.
  // Never file names: the server doesn't log them, and on encrypted
  // accounts it doesn't know them.

  const labels: Record<string, string> = {
    setup_completed: 'Server set up',
    login: 'Signed in',
    login_failed: 'Sign-in failed',
    login_locked: 'Account locked (too many failures)',
    logout: 'Signed out',
    password_changed: 'Password changed',
    totp_enabled: '2FA turned on',
    totp_disabled: '2FA turned off',
    totp_recovery_code_used: '2FA recovery code used',
    device_added: 'Device added',
    device_revoked: 'Device revoked',
    pairing_created: 'Pairing code created',
    pairing_claim_failed: 'Wrong pairing code',
    settings_changed: 'Settings changed',
    e2ee_enabled: 'Encryption turned on',
    e2ee_finished: 'Encryption finished (plaintext deleted)',
    e2ee_keys_changed: 'Encryption keys re-wrapped',
    gc_run: 'Storage cleanup run',
  };
  const warn = new Set(['login_failed', 'login_locked', 'pairing_claim_failed', 'totp_disabled', 'totp_recovery_code_used']);

  let events = $state<AuditEvent[] | null>(null);
  let next = $state<number | undefined>(undefined);
  let error = $state<string | null>(null);
  let busy = $state(false);

  async function load(more = false) {
    busy = true;
    try {
      const r = await client.audit(more ? next : undefined, 25);
      events = more ? [...(events ?? []), ...r.events] : r.events;
      next = r.next;
      error = null;
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }
  onMount(() => void load());

  function detail(e: AuditEvent): string {
    const d = e.detail ?? {};
    const parts: string[] = [];
    if (d.device) parts.push(d.device);
    if (d.via) parts.push(`via ${d.via}`);
    if (d.reason) parts.push(d.reason === 'totp' ? 'wrong code' : 'wrong password');
    if (d.username && !e.username) parts.push(`as “${d.username}”`);
    if (d.changed) parts.push(d.changed.replace(/,/g, ', '));
    if (d.for) parts.push(`for ${d.for}`);
    if (d.purged_paths) parts.push(`${d.purged_paths} unencrypted items deleted`);
    return parts.join(' · ');
  }
</script>

<Card title="Security log" subtitle="Sign-ins, devices, settings and encryption changes. Kept for 180 days.">
  {#snippet actions()}
    <Button size="sm" variant="ghost" iconOnly icon="refresh" label="Refresh" onclick={() => load()} />
  {/snippet}
  {#if error}
    <p class="error" role="alert"><Icon name="alert" size={16} /> {error}</p>
  {:else if events === null}
    <ProgressBar label="Loading security log" />
  {:else if events.length === 0}
    <p class="muted">Nothing yet.</p>
  {:else}
    <ul class="log" aria-label="Security events">
      {#each events as e (e.id)}
        <li class:warn={warn.has(e.event)}>
          <span class="when" title={formatDateTime(e.at)}>{formatDateTime(e.at)}</span>
          <span class="what">{labels[e.event] ?? e.event}{#if e.username}<span class="who">&nbsp;· {e.username}</span>{/if}</span>
          <span class="meta">{[detail(e), e.ip].filter(Boolean).join(' · ')}</span>
        </li>
      {/each}
    </ul>
    {#if next}<div class="more"><Button size="sm" loading={busy} onclick={() => load(true)}>Load more</Button></div>{/if}
  {/if}
</Card>

<style>
  .log {
    list-style: none;
    padding: 0;
    margin: 0;
    display: flex;
    flex-direction: column;
    max-height: 22rem;
    overflow-y: auto;
  }
  li {
    display: grid;
    grid-template-columns: 12.5rem 1fr;
    gap: 0 var(--space-3);
    padding: var(--space-2) 0;
    border-bottom: 1px solid var(--border);
    font-size: var(--text-sm);
  }
  li.warn .what {
    color: var(--warn);
  }
  .when {
    color: var(--text-faint);
    font-family: var(--font-mono);
    font-size: var(--text-xs);
    grid-row: span 2;
    padding-top: 0.1rem;
  }
  .who {
    color: var(--text-muted);
  }
  .meta {
    color: var(--text-faint);
    font-size: var(--text-xs);
  }
  .more {
    display: flex;
    justify-content: center;
    margin-top: var(--space-3);
  }
  .muted {
    color: var(--text-muted);
    font-size: var(--text-sm);
  }
  .error {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    color: var(--danger);
    font-size: var(--text-sm);
  }
  @media (max-width: 480px) {
    li {
      grid-template-columns: 1fr;
    }
    .when {
      grid-row: auto;
    }
  }
</style>
