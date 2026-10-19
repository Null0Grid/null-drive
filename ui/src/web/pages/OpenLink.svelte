<script lang="ts">
  import EmptyState from '../../lib/components/EmptyState.svelte';
  import Button from '../../lib/components/Button.svelte';
  import { drive } from '../api';
  import { router, folderHref } from '../router.svelte';
  import { session } from '../session.svelte';
  import { ApiError } from '../../lib/api/client';
  import { errorMessage } from '../actions';

  // /open?id=<file id>: a nulldrive:// link opened in the browser (the
  // desktop app offers this when the file isn't on that computer). Finds
  // the file by its stable id and shows it in its folder; folders open.

  interface Props {
    id: string;
  }
  let { id }: Props = $props();

  type State =
    | { kind: 'loading' }
    | { kind: 'missing' }
    | { kind: 'deleted'; name: string }
    | { kind: 'unreadable' }
    | { kind: 'error'; message: string };
  let state = $state<State>({ kind: 'loading' });

  function parentAndName(p: string): [string, string] {
    const i = p.lastIndexOf('/');
    return i < 0 ? ['', p] : [p.slice(0, i), p.slice(i + 1)];
  }

  $effect(() => {
    const want = id;
    state = { kind: 'loading' };
    drive
      .fileById(want)
      .then((f) => {
        if (want !== id) return;
        if (!f) {
          state = { kind: 'unreadable' };
          return;
        }
        const [parent, name] = parentAndName(f.path);
        if (f.deleted) {
          state = { kind: 'deleted', name };
          return;
        }
        if (f.is_dir) {
          router.navigate(folderHref(f.path), { replace: true });
          return;
        }
        router.navigate(`${folderHref(parent)}?show=${encodeURIComponent(name)}`, { replace: true });
      })
      .catch((e) => {
        if (want !== id) return;
        state = e instanceof ApiError && e.status === 404 ? { kind: 'missing' } : { kind: 'error', message: errorMessage(e) };
      });
  });
</script>

{#if state.kind === 'loading'}
  <p class="finding" aria-busy="true">Finding the file…</p>
{:else if state.kind === 'missing'}
  <EmptyState
    icon="search"
    title="Not in this account"
    message={`No file with this link exists for ${session.user?.username ?? 'this account'}. It may belong to another account, or it was deleted for good.`}
  >
    <Button variant="primary" icon="home" onclick={() => router.navigate('/files')}>Go to My Drive</Button>
  </EmptyState>
{:else if state.kind === 'deleted'}
  <EmptyState icon="trash" title="This file was deleted" message={`“${state.name}” is in Recently deleted. You can restore it from there.`}>
    <Button variant="primary" icon="trash" onclick={() => router.navigate('/trash')}>Open Recently deleted</Button>
  </EmptyState>
{:else if state.kind === 'unreadable'}
  <EmptyState icon="alert" title="Can't read this file's name" message="It's stored with an encryption key this browser doesn't have.">
    <Button variant="primary" icon="home" onclick={() => router.navigate('/files')}>Go to My Drive</Button>
  </EmptyState>
{:else}
  <EmptyState icon="alert" tone="danger" title="Couldn't open the link" message={state.message}>
    <Button variant="primary" icon="home" onclick={() => router.navigate('/files')}>Go to My Drive</Button>
  </EmptyState>
{/if}

<style>
  .finding {
    padding: var(--space-6);
    color: var(--text-muted);
  }
</style>
