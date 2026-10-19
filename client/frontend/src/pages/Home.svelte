<script lang="ts">
  import { onMount } from 'svelte';
  import Logo from '$lib/components/Logo.svelte';
  import { toast } from '$lib/toast.svelte';
  import { api, errText, on } from '../bridge';
  import type { AppState, ConflictRecord, Resolution, SyncEvent } from '../types';
  import StatusCard from '../components/StatusCard.svelte';
  import ActivityList from '../components/ActivityList.svelte';
  import ConflictList from '../components/ConflictList.svelte';
  import SettingsPanel from '../components/SettingsPanel.svelte';
  import UnlockCard from '../components/UnlockCard.svelte';

  interface Props {
    app: AppState;
    onchange: () => void;
  }
  let { app, onchange }: Props = $props();

  type Tab = 'activity' | 'conflicts' | 'settings';
  let tab = $state<Tab>('activity');
  let events = $state<SyncEvent[]>([]);
  let conflicts = $state<ConflictRecord[]>([]);

  async function loadConflicts() {
    conflicts = await api.conflicts();
  }

  onMount(() => {
    void api.activity().then((e) => (events = e));
    void loadConflicts();
    return on<SyncEvent>('activity', (ev) => {
      events = [ev, ...events].slice(0, 200);
      if (ev.kind === 'conflict') void loadConflicts();
    });
  });

  // Keep the conflict list fresh when the count changes.
  $effect(() => {
    if (app.status.conflicts !== conflicts.length) void loadConflicts();
  });

  async function act(fn: () => Promise<void>) {
    try {
      await fn();
    } catch (err) {
      toast.error(errText(err));
    }
  }

  async function resolve(id: number, how: Resolution) {
    await act(() => api.resolveConflict(id, how));
    await loadConflicts();
  }

  const tabs: { id: Tab; label: string }[] = [
    { id: 'activity', label: 'Activity' },
    { id: 'conflicts', label: 'Conflicts' },
    { id: 'settings', label: 'Settings' },
  ];
</script>

<div class="home">
  <header>
    <Logo size={28} />
    <span class="server" title={app.config.server_url}>{app.config.username} @ {app.config.server_url.replace(/^https?:\/\//, '')}</span>
  </header>

  {#if app.e2ee === 'locked' || app.e2ee === 'migrating'}
    <UnlockCard mode={app.e2ee} onunlocked={onchange} />
  {:else}
  <StatusCard
    status={app.status}
    onpause={() => act(api.pause)}
    onresume={() => act(api.resume)}
    onsync={() => act(api.syncNow)}
    onopen={() => act(api.openFolder)}
    onconfirm={() => act(api.confirmMassDelete)}
  />
  {/if}

  <div class="tabs" role="tablist" aria-label="Sections">
    {#each tabs as t (t.id)}
      <button
        type="button"
        role="tab"
        id="tab-{t.id}"
        aria-selected={tab === t.id}
        aria-controls="panel-{t.id}"
        class:active={tab === t.id}
        onclick={() => (tab = t.id)}
      >
        {t.label}
        {#if t.id === 'conflicts' && conflicts.length > 0}<span class="count">{conflicts.length}</span>{/if}
      </button>
    {/each}
  </div>

  <div class="panel" role="tabpanel" id="panel-{tab}" aria-labelledby="tab-{tab}">
    {#if tab === 'activity'}
      <ActivityList
        {events}
        skipped={app.status.skipped ?? []}
        onreveal={(p) => act(() => api.revealFile(p))}
        onhistory={(p) => act(() => api.openHistory(p))}
      />
    {:else if tab === 'conflicts'}
      <ConflictList {conflicts} onresolve={resolve} onreveal={(p) => act(() => api.revealFile(p))} />
    {:else}
      <SettingsPanel {app} {onchange} />
    {/if}
  </div>
</div>

<style>
  .home {
    max-width: 60rem;
    margin: 0 auto;
    padding: var(--space-5) var(--space-5) var(--space-6);
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-3);
  }
  .server {
    font-size: var(--text-xs);
    color: var(--text-faint);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .tabs {
    display: flex;
    gap: var(--space-1);
    border-bottom: 1px solid var(--border);
  }
  .tabs button {
    position: relative;
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    padding: var(--space-2) var(--space-4);
    border: 0;
    background: none;
    color: var(--text-muted);
    font-size: var(--text-sm);
    cursor: pointer;
  }
  .tabs button:hover {
    color: var(--text);
  }
  .tabs button.active {
    color: var(--primary);
  }
  .tabs button.active::after {
    content: '';
    position: absolute;
    left: var(--space-3);
    right: var(--space-3);
    bottom: -1px;
    height: 2px;
    background: var(--primary);
    box-shadow: var(--glow-primary);
  }
  .count {
    min-width: 1.25rem;
    padding: 0 0.35rem;
    border-radius: 999px;
    background: var(--warn);
    color: var(--bg);
    font-size: var(--text-xs);
    font-weight: 700;
    text-align: center;
  }
  @media (max-width: 520px) {
    .home {
      padding: var(--space-4) var(--space-3);
    }
    .server {
      display: none;
    }
  }
</style>
