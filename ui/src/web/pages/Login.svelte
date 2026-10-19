<script lang="ts">
  import Logo from '../../lib/components/Logo.svelte';
  import Button from '../../lib/components/Button.svelte';
  import Input from '../../lib/components/Input.svelte';
  import Icon from '../../lib/components/Icon.svelte';
  import { ApiError } from '../../lib/api/client';
  import { client } from '../api';
  import { session } from '../session.svelte';
  import { router } from '../router.svelte';
  import { errorMessage } from '../actions';

  interface Props {
    next: string;
  }
  let { next }: Props = $props();

  let username = $state('');
  let password = $state('');
  let code = $state('');
  /** The account has two-factor authentication: second step. */
  let needCode = $state(false);
  let codeEl = $state<HTMLInputElement>();
  $effect(() => {
    if (needCode) codeEl?.focus();
  });
  let busy = $state(false);
  let error = $state<string | null>(null);

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    error = null;
    if (!username || !password) {
      error = 'Enter your username and password.';
      return;
    }
    if (needCode && !code.trim()) {
      error = 'Enter the code from your authenticator app.';
      return;
    }
    busy = true;
    try {
      const res = await client.login(username, password, needCode ? code.trim() : undefined);
      session.setUser(res.user);
      router.navigate(next, { replace: true });
    } catch (err) {
      if (err instanceof ApiError && err.code === 'totp_required') {
        needCode = true;
      } else if (err instanceof ApiError && err.code === 'invalid_totp') {
        error = "That code isn't valid. Codes change every 30 seconds; check your phone's clock is set automatically.";
        code = '';
      } else if (err instanceof ApiError && (err.code === 'locked_out' || err.code === 'rate_limited')) {
        error = err.message.charAt(0).toUpperCase() + err.message.slice(1) + '.';
      } else {
        error = err instanceof ApiError && err.status === 401 ? 'Wrong username or password.' : errorMessage(err);
        password = '';
        needCode = false;
        code = '';
      }
    } finally {
      busy = false;
    }
  }

  function back() {
    needCode = false;
    code = '';
    password = '';
    error = null;
  }
</script>

<div class="login">
  <form class="card" onsubmit={submit} novalidate aria-labelledby="login-title">
    <Logo size={40} />
    {#if needCode}
      <div class="intro">
        <h1 id="login-title">Two-factor authentication</h1>
        <p>Enter the 6-digit code from your authenticator app, or one of your recovery codes.</p>
      </div>
      <Input
        label="Code"
        bind:value={code}
        autocomplete="one-time-code"
        inputmode="numeric"
        spellcheck="false"
        bind:element={codeEl}
      />
    {:else}
      <div class="intro">
        <h1 id="login-title">Sign in</h1>
        <p>Welcome back. Your files are waiting.</p>
      </div>
      <Input label="Username" bind:value={username} autocomplete="username" autocapitalize="none" spellcheck="false" />
      <Input label="Password" type="password" revealable bind:value={password} autocomplete="current-password" />
    {/if}
    {#if error}<p class="error" role="alert"><Icon name="alert" size={16} /> {error}</p>{/if}
    <Button type="submit" variant="primary" size="lg" full loading={busy}>{needCode ? 'Verify' : 'Sign in'}</Button>
    {#if needCode}<button type="button" class="back" onclick={back}>Back</button>{/if}
  </form>
</div>

<style>
  .login {
    min-height: 100dvh;
    display: grid;
    place-items: center;
    padding: var(--space-4);
  }
  .card {
    width: 100%;
    max-width: 24rem;
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
    padding: var(--space-6);
    background: var(--panel);
    border: 1px solid var(--border);
    border-top: 2px solid var(--primary);
    border-radius: var(--radius-lg);
    box-shadow: var(--shadow-overlay);
  }
  .intro {
    margin: var(--space-2) 0;
  }
  h1 {
    font-size: var(--text-xl);
  }
  .intro p {
    margin-top: var(--space-1);
    color: var(--text-muted);
    font-size: var(--text-sm);
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
  .back {
    background: none;
    border: 0;
    color: var(--text-muted);
    font-size: var(--text-sm);
    cursor: pointer;
  }
  .back:hover {
    color: var(--primary);
  }
  @media (max-width: 480px) {
    .card {
      padding: var(--space-5) var(--space-4);
    }
  }
</style>
