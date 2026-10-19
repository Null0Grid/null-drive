<script lang="ts">
  import { onMount } from 'svelte';
  import ToastHost from '../lib/components/ToastHost.svelte';
  import Logo from '../lib/components/Logo.svelte';
  import Button from '../lib/components/Button.svelte';
  import EmptyState from '../lib/components/EmptyState.svelte';
  import { ApiError } from '../lib/api/client';
  import { client } from './api';
  import { router } from './router.svelte';
  import { session } from './session.svelte';
  import { live } from './live.svelte';
  import AppShell from './components/AppShell.svelte';
  import Setup from './pages/Setup.svelte';
  import Login from './pages/Login.svelte';
  import Files from './pages/Files.svelte';
  import Settings from './pages/Settings.svelte';
  import Trash from './pages/Trash.svelte';
  import OpenLink from './pages/OpenLink.svelte';
  import Unlock from './pages/Unlock.svelte';
  import { vault } from './vault.svelte';

  async function init() {
    session.status = 'loading';
    try {
      const { setup_complete } = await client.setupStatus();
      if (!setup_complete) {
        session.status = 'setup';
        return;
      }
      try {
        const me = await client.me();
        session.setUser(me.user);
      } catch (e) {
        if (e instanceof ApiError && e.status === 401) session.setUser(null);
        else throw e;
      }
    } catch {
      session.status = 'offline';
    }
  }

  onMount(init);

  // One realtime socket while signed in: live file list, device presence.
  $effect(() => {
    if (session.status === 'authed') live.start();
    else live.stop();
  });

  // Keys never outlive the session.
  $effect(() => {
    if (session.status !== 'authed') vault.lock();
  });

  // An end-to-end encrypted account shows nothing until it's unlocked in
  // this tab (and an interrupted migration is finished).
  const needsUnlock = $derived(
    session.status === 'authed' &&
      !!session.user &&
      session.user.e2ee !== undefined &&
      session.user.e2ee !== 'off' &&
      (!vault.unlocked || session.user.e2ee === 'migrating'),
  );

  const route = $derived(router.route);
  const inWizard = $derived(session.status === 'setup' || (session.status === 'authed' && session.wizardStep !== null));

  // Keep the URL in line with the screen being shown.
  $effect(() => {
    const r = route;
    if (inWizard) {
      if (r.name !== 'setup') router.navigate('/setup', { replace: true });
    } else if (session.status === 'anon') {
      if (r.name !== 'login') {
        const here = location.pathname + location.search;
        const next = r.name === 'setup' || here === '/' ? '' : `?next=${encodeURIComponent(here)}`;
        router.navigate(`/login${next}`, { replace: true });
      }
    } else if (session.status === 'authed') {
      if (r.name === 'login') router.navigate(r.next, { replace: true });
      else if (r.name === 'setup') router.navigate('/files', { replace: true });
    }
  });
</script>

<ToastHost />

{#if session.status === 'loading'}
  <div class="splash" aria-busy="true">
    <Logo size={44} />
    <span class="blink" aria-label="Loading">_</span>
  </div>
{:else if session.status === 'offline'}
  <div class="splash">
    <EmptyState icon="alert" tone="danger" title="Can't reach the server" message="Null Drive isn't responding. Check that the server is running, then try again.">
      <Button variant="primary" icon="refresh" onclick={init}>Retry</Button>
    </EmptyState>
  </div>
{:else if inWizard}
  <Setup />
{:else if session.status === 'anon'}
  <Login next={route.name === 'login' ? route.next : '/files'} />
{:else if needsUnlock}
  <Unlock />
{:else if session.status === 'authed'}
  <AppShell active={route.name === 'settings' ? 'settings' : route.name === 'trash' ? 'trash' : 'files'}>
    {#if route.name === 'files'}
      <Files folder={route.folder} history={route.history} show={route.show} />
    {:else if route.name === 'open'}
      <OpenLink id={route.id} />
    {:else if route.name === 'trash'}
      <Trash />
    {:else if route.name === 'settings'}
      <Settings />
    {:else if route.name === 'notfound'}
      <EmptyState icon="alert" title="Page not found" message="That address doesn't match anything in Null Drive.">
        <Button variant="primary" icon="home" onclick={() => router.navigate('/files')}>Go to My Drive</Button>
      </EmptyState>
    {/if}
  </AppShell>
{/if}

<style>
  .splash {
    min-height: 100dvh;
    display: grid;
    place-items: center;
    align-content: center;
    gap: var(--space-4);
    padding: var(--space-4);
  }
  .blink {
    font-family: var(--font-term);
    font-size: 2rem;
    color: var(--primary);
    text-shadow: var(--glow-text);
    animation: blink 1s steps(1) infinite;
  }
  @keyframes blink {
    50% {
      opacity: 0;
    }
  }
</style>
