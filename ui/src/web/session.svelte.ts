import type { User } from '../lib/api/types';
import { toast } from '../lib/toast.svelte';

// Which top-level screen the app is in:
//   loading → asking the server
//   setup   → first run: no admin yet, show the wizard
//   anon    → set up, not logged in
//   authed  → logged in
//   offline → the server could not be reached
export type Status = 'loading' | 'setup' | 'anon' | 'authed' | 'offline';

const WIZARD_KEY = 'nd.wizardStep';

class Session {
  status = $state<Status>('loading');
  user = $state<User | null>(null);
  /**
   * Step of the setup wizard still in progress after the account was
   * created (storage, done). Kept in sessionStorage so a reload mid-wizard
   * returns to it instead of jumping straight into the app.
   */
  wizardStep = $state<string | null>(readWizard());

  setUser(user: User | null) {
    this.user = user;
    this.status = user ? 'authed' : 'anon';
  }

  setWizardStep(step: string | null) {
    this.wizardStep = step;
    try {
      if (step) sessionStorage.setItem(WIZARD_KEY, step);
      else sessionStorage.removeItem(WIZARD_KEY);
    } catch {
      /* ignore */
    }
  }

  /** Called by the API client on any 401. */
  expired() {
    if (this.status === 'authed') {
      this.user = null;
      this.status = 'anon';
      toast.warning('Your session has ended. Please sign in again.');
    }
  }
}

function readWizard(): string | null {
  try {
    return sessionStorage.getItem(WIZARD_KEY);
  } catch {
    return null;
  }
}

export const session = new Session();
