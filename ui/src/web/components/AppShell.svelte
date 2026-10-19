<script lang="ts">
  import { onMount, type Snippet } from 'svelte';
  import { client } from '../api';
  import Sidebar from '../../lib/components/Sidebar.svelte';
  import Logo from '../../lib/components/Logo.svelte';
  import Button from '../../lib/components/Button.svelte';
  import { getTheme, setTheme, type Theme } from '../../lib/theme';
  import { router } from '../router.svelte';
  import { session } from '../session.svelte';
  import { signOut } from '../actions';

  interface Props {
    active: 'files' | 'trash' | 'settings';
    children: Snippet;
  }
  let { active, children }: Props = $props();

  let theme = $state<Theme>(getTheme());
  function toggleTheme() {
    theme = theme === 'dark' ? 'light' : 'dark';
    setTheme(theme);
  }

  // Shown in the footer so bug reports can say exactly which build is running.
  let build = $state<string | null>(null);
  onMount(() => {
    client.health().then(
      (h) => (build = h.commit && h.commit !== 'none' ? `${h.version} · ${h.commit.slice(0, 7)}` : h.version),
      () => {},
    );
  });

  const nav = [
    { id: 'files', label: 'Files', icon: 'folder' as const },
    { id: 'trash', label: 'Deleted', icon: 'trash' as const },
    { id: 'settings', label: 'Settings', icon: 'settings' as const },
  ];
</script>

<div class="shell">
  <a class="skip" href="#main">Skip to content</a>

  <header class="topbar">
    <Logo size={28} />
    <div class="top-actions">
      <Button
        variant="ghost"
        iconOnly
        icon={theme === 'dark' ? 'sun' : 'moon'}
        label={theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'}
        onclick={toggleTheme}
      />
      <Button variant="ghost" iconOnly icon="logout" label="Sign out" onclick={signOut} />
    </div>
  </header>

  <Sidebar items={nav} {active} onselect={(id) => router.navigate(`/${id}`)}>
    {#snippet header()}
      <Logo size={30} />
    {/snippet}
    {#snippet footer()}
      <div class="user">
        <div class="who">
          <span class="avatar" aria-hidden="true">{session.user?.username.slice(0, 1).toUpperCase()}</span>
          <span class="name">{session.user?.username}</span>
        </div>
        <div class="user-actions">
          <Button
            variant="ghost"
            size="sm"
            iconOnly
            icon={theme === 'dark' ? 'sun' : 'moon'}
            label={theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'}
            onclick={toggleTheme}
          />
          <Button variant="ghost" size="sm" iconOnly icon="logout" label="Sign out" onclick={signOut} />
        </div>
      </div>
      {#if build}<p class="build" data-testid="build-version">Null Drive {build}</p>{/if}
    {/snippet}
  </Sidebar>

  <main id="main" tabindex="-1">
    {@render children()}
  </main>
</div>

<style>
  .shell {
    display: grid;
    grid-template-columns: var(--sidebar-w) minmax(0, 1fr);
    min-height: 100dvh;
  }
  main {
    min-width: 0;
    padding: var(--space-5) var(--space-6) var(--space-7);
  }
  main:focus {
    outline: none;
  }
  .topbar {
    display: none;
  }
  .skip {
    position: absolute;
    left: -999px;
    top: var(--space-2);
    z-index: 100;
    padding: var(--space-2) var(--space-3);
    background: var(--primary);
    color: var(--primary-ink);
    border-radius: var(--radius-sm);
  }
  .skip:focus {
    left: var(--space-2);
  }
  .user {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-2);
    padding: 0 var(--space-1);
  }
  .who {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    min-width: 0;
  }
  .avatar {
    display: grid;
    place-items: center;
    flex: none;
    width: 2rem;
    height: 2rem;
    border-radius: var(--radius-md);
    background: var(--accent-soft);
    color: var(--accent);
    font-family: var(--font-display);
  }
  .name {
    font-size: var(--text-sm);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .user-actions {
    display: flex;
  }
  .build {
    margin-top: var(--space-3);
    padding: 0 var(--space-1);
    font-family: var(--font-term);
    font-size: 1rem;
    color: var(--text-faint);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  @media (max-width: 1024px) {
    main {
      padding: var(--space-5) var(--space-4) var(--space-7);
    }
  }
  @media (max-width: 720px) {
    .shell {
      grid-template-columns: minmax(0, 1fr);
      grid-template-rows: auto 1fr;
    }
    .topbar {
      display: flex;
      align-items: center;
      justify-content: space-between;
      position: sticky;
      top: 0;
      z-index: 20;
      padding: var(--space-2) var(--space-3) var(--space-2) var(--space-4);
      padding-top: calc(var(--space-2) + env(safe-area-inset-top));
      background: color-mix(in srgb, var(--panel) 92%, transparent);
      backdrop-filter: blur(10px);
      border-bottom: 1px solid var(--border);
    }
    .top-actions {
      display: flex;
    }
    main {
      padding: var(--space-4) var(--space-3) calc(5.5rem + env(safe-area-inset-bottom));
    }
  }
</style>
