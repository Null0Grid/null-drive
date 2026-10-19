<script lang="ts">
  import { onMount } from 'svelte';
  import ToastHost from '$lib/components/ToastHost.svelte';
  import Logo from '$lib/components/Logo.svelte';
  import { getTheme, setTheme } from '$lib/theme';
  import { api, on } from './bridge';
  import type { AppState, Status } from './types';
  import Setup from './pages/Setup.svelte';
  import Home from './pages/Home.svelte';

  let appState = $state<AppState | null>(null);

  async function refresh() {
    appState = await api.getState();
  }

  onMount(() => {
    setTheme(getTheme());
    void refresh();
    return on<Status>('status', (s) => {
      if (appState) appState.status = s;
    });
  });
</script>

<ToastHost />

{#if !appState}
  <div class="splash"><Logo size={44} /></div>
{:else if !appState.configured}
  <Setup ondone={refresh} />
{:else}
  <Home app={appState} onchange={refresh} />
{/if}

<style>
  .splash {
    min-height: 100dvh;
    display: grid;
    place-items: center;
  }
</style>
