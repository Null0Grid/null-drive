import { toast } from '../lib/toast.svelte';
import { client } from './api';
import { router } from './router.svelte';
import { session } from './session.svelte';
import { vault } from './vault.svelte';

export async function signOut() {
  try {
    await client.logout();
  } catch {
    // Already signed out server-side: still clear the local state.
  }
  vault.lock();
  session.setUser(null);
  router.navigate('/login', { replace: true });
  toast.info('Signed out.');
}

/** Turn any thrown value into a message fit for a toast. */
export function errorMessage(e: unknown): string {
  if (e instanceof Error) return e.message;
  return String(e);
}
