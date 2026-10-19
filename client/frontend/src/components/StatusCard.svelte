<script lang="ts">
  import Button from '$lib/components/Button.svelte';
  import Icon, { type IconName } from '$lib/components/Icon.svelte';
  import ProgressBar from '$lib/components/ProgressBar.svelte';
  import { formatBytes, formatDate } from '$lib/util/format';
  import type { Status } from '../types';

  interface Props {
    status: Status;
    onpause: () => void;
    onresume: () => void;
    onsync: () => void;
    onopen: () => void;
    onconfirm: () => void;
  }
  let { status, onpause, onresume, onsync, onopen, onconfirm }: Props = $props();

  const look: Record<Status['state'], { icon: IconName; title: string; tone: string }> = {
    starting: { icon: 'refresh', title: 'Starting', tone: 'primary' },
    syncing: { icon: 'refresh', title: 'Syncing', tone: 'accent' },
    idle: { icon: 'check', title: 'Up to date', tone: 'ok' },
    paused: { icon: 'info', title: 'Paused', tone: 'muted' },
    offline: { icon: 'alert', title: 'Offline', tone: 'warn' },
    error: { icon: 'alert', title: 'Needs attention', tone: 'danger' },
    attention: { icon: 'alert', title: 'Waiting for you', tone: 'danger' },
  };
  const l = $derived(look[status.state] ?? look.idle);
  const p = $derived(status.progress);
  // Only show the message line when it says more than the title.
  const detail = $derived(
    status.message && !status.message.toLowerCase().startsWith(l.title.toLowerCase().slice(0, 6)) ? status.message : '',
  );
</script>

<section class="status {l.tone}" aria-live="polite" data-state={status.state}>
  <div class="badge" class:spin={status.state === 'syncing'}><Icon name={l.icon} size={28} /></div>
  <div class="text">
    <h2>{l.title}</h2>
    {#if detail}<p class="msg">{detail}</p>{/if}
    {#if status.last_sync && status.state !== 'syncing'}
      <p class="meta">Last sync {formatDate(status.last_sync)}</p>
    {/if}
    {#if status.state !== 'paused' && status.state !== 'offline' && status.state !== 'starting'}
      <p class="link" class:live={status.live} title={status.live ? 'Changes from other devices arrive within seconds (WebSocket push).' : 'Push channel unavailable; checking the server periodically.'}>
        <span class="dot" aria-hidden="true"></span>{status.live ? 'Live' : 'Polling'}
      </p>
    {/if}
  </div>
  <div class="buttons">
    {#if status.state === 'paused'}
      <Button variant="primary" icon="refresh" onclick={onresume}>Resume</Button>
    {:else}
      <Button icon="refresh" onclick={onsync} disabled={status.state === 'syncing'}>Sync now</Button>
      <Button variant="ghost" onclick={onpause}>Pause</Button>
    {/if}
    <Button variant="ghost" icon="folder" onclick={onopen}>Open folder</Button>
  </div>
  {#if status.state === 'syncing' && p.total > 0}
    <div class="progress">
      <ProgressBar value={p.done / p.total} label="Sync progress" showLabel detail="{p.done} / {p.total}" />
      {#if p.path}
        <p class="path">
          {p.path}{#if p.bytes_total}<span class="bytes">&nbsp;· {formatBytes(p.bytes ?? 0)} of {formatBytes(p.bytes_total)}</span>{/if}
        </p>
      {/if}
    </div>
  {/if}
  {#if status.mass_delete}
    <div class="guard" role="alert">
      <p>
        <strong>Large deletion paused.</strong> {status.mass_delete.Count} of {status.mass_delete.Total} files are missing from this
        folder. If you deleted them on purpose, confirm to delete them from the server too. Otherwise, check the folder (or drive)
        first.
      </p>
      <div class="guard-actions">
        <Button variant="danger" onclick={onconfirm}>Delete them on the server</Button>
        <Button variant="ghost" icon="folder" onclick={onopen}>Check the folder</Button>
      </div>
    </div>
  {/if}
</section>

<style>
  .bytes {
    color: var(--text-faint);
    font-variant-numeric: tabular-nums;
  }
  .link {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    margin-top: var(--space-1);
    font-family: var(--font-term);
    font-size: 1.05rem;
    letter-spacing: 0.04em;
    color: var(--text-muted);
  }
  .link .dot {
    width: 0.5rem;
    height: 0.5rem;
    border-radius: 50%;
    background: var(--text-faint);
  }
  .link.live {
    color: var(--ok);
  }
  .link.live .dot {
    background: var(--ok);
    box-shadow: 0 0 6px var(--ok);
  }
  .status {
    --tone: var(--primary);
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    align-items: center;
    gap: var(--space-4);
    padding: var(--space-5);
    background: var(--panel);
    border: 1px solid var(--border);
    border-left: 3px solid var(--tone);
    border-radius: var(--radius-lg);
  }
  .accent {
    --tone: var(--accent);
  }
  .ok {
    --tone: var(--ok);
  }
  .muted {
    --tone: var(--text-faint);
  }
  .warn {
    --tone: var(--warn);
  }
  .danger {
    --tone: var(--danger);
  }
  .badge {
    display: grid;
    place-items: center;
    width: 3.25rem;
    height: 3.25rem;
    border-radius: 50%;
    border: 1px solid var(--tone);
    color: var(--tone);
    box-shadow: 0 0 14px color-mix(in srgb, var(--tone) 35%, transparent);
  }
  .muted .badge {
    box-shadow: none;
  }
  .spin :global(.icon) {
    animation: spin 1.4s linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
  h2 {
    font-size: var(--text-lg);
    color: var(--tone);
  }
  .msg {
    color: var(--text);
    font-size: var(--text-sm);
    margin-top: var(--space-1);
  }
  .meta {
    color: var(--text-faint);
    font-size: var(--text-xs);
    margin-top: var(--space-1);
  }
  .buttons {
    display: flex;
    flex-wrap: wrap;
    justify-content: flex-end;
    gap: var(--space-2);
  }
  .progress,
  .guard {
    grid-column: 1 / -1;
  }
  .path {
    margin-top: var(--space-2);
    font-size: var(--text-xs);
    color: var(--text-faint);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .guard {
    padding: var(--space-4);
    border: 1px solid var(--danger);
    border-radius: var(--radius-md);
    background: var(--danger-soft);
    font-size: var(--text-sm);
  }
  .guard-actions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
    margin-top: var(--space-3);
  }
  @media (max-width: 640px) {
    .status {
      grid-template-columns: auto minmax(0, 1fr);
    }
    .buttons {
      grid-column: 1 / -1;
      justify-content: flex-start;
    }
  }
</style>
