// End-to-end encryption state of the signed-in account in this tab. The
// unlocked keys live in memory only (lib/drive.ts holds them): never in
// localStorage or sessionStorage, which browsers may write to disk for
// session restore. A reload asks for the passphrase again.

import type { E2EEStatus, KeyBundle } from '../lib/api/types';
import { unlockWithPassphrase, unlockWithRecoveryKey, type Keys } from '../lib/crypto/keys';
import { client, drive } from './api';
import { session } from './session.svelte';

class Vault {
  status = $state<E2EEStatus | null>(null);
  unlocked = $state(false);

  /** The account's state as last reported by the server. */
  get state() {
    return session.user?.e2ee ?? 'off';
  }

  async refresh(): Promise<E2EEStatus> {
    const st = await client.e2eeStatus();
    this.status = st;
    if (session.user && session.user.e2ee !== st.state) session.user = { ...session.user, e2ee: st.state };
    return st;
  }

  private async bundle(): Promise<KeyBundle> {
    const st = this.status?.bundle ? this.status : await this.refresh();
    if (!st.bundle) throw new Error('Encryption is not turned on for this account.');
    return st.bundle;
  }

  /** Open the key bundle with the passphrase (or recovery key) without using it yet. */
  async open(secret: string, recovery = false): Promise<Keys> {
    const b = await this.bundle();
    return recovery ? unlockWithRecoveryKey(b, secret) : unlockWithPassphrase(b, secret);
  }

  /** Unlock with the passphrase, or with the recovery key if recovery is set. */
  async unlock(secret: string, recovery = false): Promise<Keys> {
    const keys = await this.open(secret, recovery);
    this.use(keys);
    return keys;
  }

  use(keys: Keys) {
    drive.keys = keys;
    this.unlocked = true;
  }

  /** Forget the keys (sign out, "Lock now"). */
  lock() {
    drive.keys = null;
    this.unlocked = false;
    this.status = null;
  }
}

export const vault = new Vault();
