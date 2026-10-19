import { NullDriveClient } from '../lib/api/client';
import { Drive } from '../lib/drive';
import { session } from './session.svelte';

// The web UI talks to the server it was served from, authenticated by the
// HttpOnly session cookie, so no token is ever handled in JavaScript.
export const client = new NullDriveClient({
  onUnauthorized: () => session.expired(),
});

/**
 * File operations for the pages. For an end-to-end encrypted account,
 * drive.keys is set after the unlock screen and everything is encrypted
 * and decrypted here in the browser (see lib/drive.ts).
 */
export const drive = new Drive(client);
