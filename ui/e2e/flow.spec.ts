import { expect, test, type Page } from '@playwright/test';
import { createHmac } from 'node:crypto';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join } from 'node:path';

// The Phase 2 acceptance flow, against the real binary on a fresh data dir:
// setup wizard → sign out → login → upload → rename → delete.

const USER = 'admin';
const PASS = 'correct horse battery';

test.describe.configure({ mode: 'serial' });

async function rowFor(page: Page, name: string) {
  return page.locator('.row').filter({ has: page.getByRole('button', { name, exact: true }) });
}

/**
 * Headers for state-changing requests made with the page's session
 * cookie: the CSRF token from /me (what the app itself does).
 */
async function csrf(page: Page, extra: Record<string, string> = {}): Promise<Record<string, string>> {
  const me = await (await page.request.get('/api/v1/me')).json();
  return { 'X-NullDrive-CSRF': me.csrf_token, ...extra };
}

/** RFC 6238 TOTP, as an authenticator app computes it. */
function totp(secret: string, at = Date.now()): string {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
  let bits = '';
  for (const c of secret.replace(/\s|=/g, '')) bits += alphabet.indexOf(c).toString(2).padStart(5, '0');
  const key = Buffer.from(bits.match(/.{8}/g)!.map((b) => parseInt(b, 2)));
  const msg = Buffer.alloc(8);
  msg.writeBigUInt64BE(BigInt(Math.floor(at / 1000 / 30)));
  const h = createHmac('sha1', key).update(msg).digest();
  const off = h[h.length - 1]! & 0xf;
  return String((h.readUInt32BE(off) & 0x7fffffff) % 1_000_000).padStart(6, '0');
}

async function rowAction(page: Page, name: string, action: string) {
  await page.getByRole('button', { name: `Actions for ${name}` }).click();
  await page.getByRole('menuitem', { name: action }).click();
}

test('first run: setup wizard creates the admin and opens the drive', async ({ page }) => {
  await page.goto('/');
  await expect(page).toHaveURL(/\/setup$/);
  await expect(page.getByRole('heading', { name: /your files/i })).toBeVisible();
  await page.getByRole('button', { name: 'Get started' }).click();

  // Validation: mismatched confirmation is caught client-side.
  await page.getByLabel('Username').fill(USER);
  await page.getByLabel('Password', { exact: true }).fill(PASS);
  await page.getByLabel('Confirm password').fill('something else');
  await page.getByRole('button', { name: 'Create account' }).click();
  await expect(page.getByText("Passwords don't match.")).toBeVisible();

  await page.getByLabel('Confirm password').fill(PASS);
  await page.getByRole('button', { name: 'Create account' }).click();

  // Storage step shows the real data dir from /api/v1/system/info.
  await expect(page.getByRole('heading', { name: /storage location/i })).toBeVisible();
  await expect(page.getByTestId('data-dir')).toContainText('.e2e-data');
  await expect(page.getByRole('progressbar', { name: 'Disk usage' })).toBeVisible();

  // A reload mid-wizard resumes the wizard instead of skipping it.
  await page.reload();
  await expect(page.getByRole('heading', { name: /storage location/i })).toBeVisible();

  await page.getByRole('button', { name: 'Looks good' }).click();
  // Optional encryption step: skipped here (covered by its own test).
  await expect(page.getByRole('heading', { name: 'End-to-end encryption' })).toBeVisible();
  await page.getByRole('button', { name: 'Skip for now' }).click();
  await expect(page.getByRole('heading', { name: /install the desktop app/i })).toBeVisible();
  await page.getByRole('button', { name: 'Open Null Drive' }).click();

  await expect(page).toHaveURL(/\/files$/);
  await expect(page.getByText('Your drive is empty')).toBeVisible();
  // The footer shows the build, injected at compile time via -ldflags.
  await expect(page.getByTestId('build-version')).toContainText(/Null Drive \S+/);

  // The wizard can never run twice.
  const res = await page.request.post('/api/v1/setup', { data: { username: 'evil', password: 'takeover123' } });
  expect(res.status()).toBe(409);
});

test('sign out, then sign in again', async ({ page }) => {
  await page.goto('/files');
  // No session in this fresh browser context: redirected to login.
  await expect(page).toHaveURL(/\/login/);
  await page.getByLabel('Username').fill(USER);
  await page.getByLabel('Password', { exact: true }).fill('wrong password');
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page.getByRole('alert')).toContainText('Wrong username or password');

  await page.getByLabel('Password', { exact: true }).fill(PASS);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page).toHaveURL(/\/files$/);

  await page.getByRole('button', { name: 'Sign out' }).first().click();
  await expect(page).toHaveURL(/\/login/);
  await page.goto('/settings');
  await expect(page).toHaveURL(/\/login\?next=%2Fsettings/);
  await page.getByLabel('Username').fill(USER);
  await page.getByLabel('Password', { exact: true }).fill(PASS);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page).toHaveURL(/\/settings$/); // returned to where we were going
});

test('upload, rename, move and delete files', async ({ page }) => {
  await page.goto('/login');
  await page.getByLabel('Username').fill(USER);
  await page.getByLabel('Password', { exact: true }).fill(PASS);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page).toHaveURL(/\/files$/);

  // Upload through the file picker.
  await page.getByTestId('file-input').setInputFiles([
    { name: 'hello.txt', mimeType: 'text/plain', buffer: Buffer.from('hello from playwright') },
    { name: 'data.csv', mimeType: 'text/csv', buffer: Buffer.from('a,b\n1,2\n') },
  ]);
  await expect(page.getByText('Upload complete.')).toBeVisible();
  await expect(await rowFor(page, 'hello.txt')).toBeVisible();
  await expect(await rowFor(page, 'data.csv')).toBeVisible();

  // Without the CSRF token, a cookie-authenticated write is refused.
  const forged = await page.request.put('/api/v1/files/forged.txt', { data: 'x' });
  expect(forged.status()).toBe(403);

  // Content round-trips (the download link is authenticated by the cookie).
  const dl = await page.request.get('/api/v1/files/hello.txt');
  expect(await dl.text()).toBe('hello from playwright');

  // Uploading the same name again asks; "Keep both" creates a copy.
  await page.getByTestId('file-input').setInputFiles([{ name: 'hello.txt', mimeType: 'text/plain', buffer: Buffer.from('v2') }]);
  await expect(page.getByRole('dialog', { name: 'File already exists' })).toBeVisible();
  await page.getByRole('button', { name: 'Keep both' }).click();
  await expect(await rowFor(page, 'hello (1).txt')).toBeVisible();

  // Rename.
  await rowAction(page, 'hello.txt', 'Rename');
  const nameInput = page.getByRole('dialog', { name: 'Rename' }).getByLabel('New name');
  await expect(nameInput).toHaveValue('hello.txt');
  await nameInput.fill('greeting.txt');
  await page.getByRole('button', { name: 'Rename', exact: true }).click();
  await expect(page.getByText('Renamed to “greeting.txt”.')).toBeVisible();
  await expect(await rowFor(page, 'greeting.txt')).toBeVisible();
  await expect(await rowFor(page, 'hello.txt')).toHaveCount(0);

  // Invalid names are rejected before reaching the server.
  await rowAction(page, 'greeting.txt', 'Rename');
  await page.getByRole('dialog', { name: 'Rename' }).getByLabel('New name').fill('bad:name');
  await page.getByRole('button', { name: 'Rename', exact: true }).click();
  await expect(page.getByText('Name cannot contain ":"')).toBeVisible();
  await page.getByRole('button', { name: 'Cancel' }).click();

  // New folder + move into it + navigate with breadcrumbs.
  await page.getByRole('button', { name: 'New folder' }).first().click();
  await page.getByLabel('Folder name').fill('Archive');
  await page.getByRole('button', { name: 'Create' }).click();
  await expect(await rowFor(page, 'Archive')).toBeVisible();
  await rowAction(page, 'data.csv', 'Move to…');
  await page.getByRole('option', { name: 'Archive' }).click();
  await page.getByRole('button', { name: 'Move here' }).click();
  await expect(await rowFor(page, 'data.csv')).toHaveCount(0);
  await page.getByRole('button', { name: 'Archive', exact: true }).click();
  await expect(page).toHaveURL(/\/files\/Archive$/);
  await expect(await rowFor(page, 'data.csv')).toBeVisible();
  await page.getByRole('navigation', { name: 'Folder path' }).getByRole('button', { name: 'My Drive' }).click();
  await expect(page).toHaveURL(/\/files$/);

  // Filter.
  await page.getByPlaceholder('Filter this folder…').fill('greet');
  await expect(await rowFor(page, 'greeting.txt')).toBeVisible();
  await expect(await rowFor(page, 'Archive')).toHaveCount(0);
  await page.getByPlaceholder('Filter this folder…').fill('');

  // Delete (with confirmation).
  await rowAction(page, 'greeting.txt', 'Delete');
  const confirm = page.getByRole('dialog', { name: 'Delete file?' });
  await expect(confirm).toContainText('greeting.txt');
  await confirm.getByRole('button', { name: 'Delete' }).click();
  await expect(page.getByText('Deleted “greeting.txt”.')).toBeVisible();
  await expect(await rowFor(page, 'greeting.txt')).toHaveCount(0);

  // Deleting a folder deletes its contents.
  await rowAction(page, 'Archive', 'Delete');
  await page.getByRole('dialog', { name: 'Delete folder?' }).getByRole('button', { name: 'Delete' }).click();
  await expect(await rowFor(page, 'Archive')).toHaveCount(0);
  const list = await (await page.request.get('/api/v1/files')).json();
  expect(list.files.map((f: { path: string }) => f.path)).toEqual(['hello (1).txt']);
});

test('pair a device with a code; presence and live file list', async ({ page }) => {
  await page.goto('/login');
  await page.getByLabel('Username').fill(USER);
  await page.getByLabel('Password', { exact: true }).fill(PASS);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page).toHaveURL(/\/files/); // signed in before navigating away
  await page.goto('/settings');

  await page.getByRole('button', { name: 'Add device' }).click();
  const code = (await page.getByTestId('pairing-code').textContent())!.trim();
  expect(code).toMatch(/^NULL-[0-9A-Z]{4}-[0-9A-Z]{2}$/);
  // What the desktop app does: claim the code (typed sloppily).
  const claim = await page.request.post('/api/v1/pairing/claim', {
    data: { code: code.toLowerCase().replace(/-/g, ' '), device_name: 'E2E laptop' },
  });
  expect(claim.status()).toBe(201);
  const { token } = (await claim.json()) as { token: string };
  await expect(page.getByText('Paired with “E2E laptop”')).toBeVisible({ timeout: 5000 });
  await page.getByRole('button', { name: 'Done' }).click();
  const row = page.getByRole('list', { name: 'Active devices' }).getByRole('listitem').filter({ hasText: 'E2E laptop' });
  await expect(row).toContainText('Offline');

  // The device opens its realtime socket: the dot turns on without a reload.
  const ws = new WebSocket(page.url().replace(/^http/, 'ws').replace(/\/settings.*$/, '/api/v1/ws'), {
    headers: { Authorization: `Bearer ${token}`, 'User-Agent': 'NullDrive/0.5.0 (linux/amd64)' },
  } as unknown as string[]);
  await new Promise((r) => (ws.onmessage = r));
  await expect(row).toContainText('Online');

  // A file uploaded by the device appears in an open file list by push.
  // Attach the frame listener synchronously in the websocket event, or
  // the hello can arrive before we start listening.
  const hello = new Promise<void>((resolve) => page.once('websocket', (ws) => ws.once('framereceived', () => resolve())));
  await page.goto('/files');
  await hello; // the page is live
  await expect(page.getByRole('button', { name: 'From the laptop.txt', exact: true })).toHaveCount(0);
  const put = await page.request.put('/api/v1/files/From%20the%20laptop.txt', {
    data: 'pushed', headers: { Authorization: `Bearer ${token}` },
  });
  expect(put.status()).toBe(201);
  await expect(page.getByRole('button', { name: 'From the laptop.txt', exact: true })).toBeVisible({ timeout: 3000 });
  ws.close();
});

test('version history, restore and recently deleted', async ({ page }) => {
  await page.goto('/login');
  await page.getByLabel('Username').fill(USER);
  await page.getByLabel('Password', { exact: true }).fill(PASS);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page).toHaveURL(/\/files/);

  // Three versions of one file through the API (as a device would).
  let version = 0;
  for (const text of ['draft one', 'draft two', 'final']) {
    const res = await page.request.put('/api/v1/files/Essay.txt', {
      data: text,
      headers: await csrf(page, version ? { 'If-Match': `"${version}"` } : {}),
    });
    expect(res.ok()).toBeTruthy();
    version = (await res.json()).version;
  }
  await page.goto('/files');
  await rowAction(page, 'Essay.txt', 'History');
  const dialog = page.getByRole('dialog', { name: /History · Essay.txt/ });
  await expect(dialog.getByRole('listitem')).toHaveCount(3);
  await dialog.getByRole('listitem').nth(2).getByRole('button', { name: /Restore/ }).click();
  await dialog.getByRole('button', { name: 'Restore', exact: true }).click();
  await expect(page.getByText(/Restored the version/)).toBeVisible();
  await expect(dialog.getByRole('listitem')).toHaveCount(4);
  const body = await (await page.request.get('/api/v1/files/Essay.txt')).text();
  expect(body).toBe('draft one');
  await dialog.getByRole('button', { name: 'Close' }).last().click();

  // Delete, then bring it back from Recently deleted.
  await rowAction(page, 'Essay.txt', 'Delete');
  await page.getByRole('dialog').getByRole('button', { name: 'Delete' }).click();
  await expect(page.getByText('Deleted “Essay.txt”')).toBeVisible();
  await page.getByRole('button', { name: 'Deleted' }).first().click();
  await expect(page).toHaveURL(/\/trash$/);
  await page.getByRole('button', { name: 'Restore Essay.txt' }).click();
  await expect(page.getByText('Restored “Essay.txt”.')).toBeVisible();
  await page.goto('/files');
  await expect(page.getByRole('button', { name: 'Essay.txt', exact: true })).toBeVisible();

  // Deep link used by the desktop app's "View history".
  await page.goto('/files?history=Essay.txt');
  await expect(page.getByRole('dialog', { name: /History · Essay.txt/ })).toBeVisible();
});

test('settings: devices and password', async ({ page }) => {
  await page.goto('/login');
  await page.getByLabel('Username').fill(USER);
  await page.getByLabel('Password', { exact: true }).fill(PASS);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.getByRole('button', { name: 'Settings' }).first().click();
  await expect(page).toHaveURL(/\/settings$/);

  // Add a device by API token (scripts): the token is shown once.
  await page.getByRole('button', { name: 'Add device' }).click();
  await page.getByRole('button', { name: 'Create an API token instead' }).click();
  await page.getByRole('dialog').getByLabel('Name', { exact: true }).fill('Test laptop');
  await page.getByRole('button', { name: 'Create token' }).click();
  await expect(page.getByTestId('device-token')).toHaveText(/^ndd_/);
  await page.getByRole('button', { name: 'Done' }).click();
  await expect(page.getByText('Test laptop')).toBeVisible();

  // Revoke it.
  const active = page.getByRole('list', { name: 'Active devices' });
  await active.getByRole('listitem').filter({ hasText: 'Test laptop' }).getByRole('button', { name: 'Revoke' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Revoke' }).click();
  await expect(page.getByText('can no longer sync')).toBeVisible();
  await expect(active.getByText('Test laptop')).toHaveCount(0);

  // Connect Null Search: a search-only token, with the server URL to paste.
  await page.getByRole('button', { name: 'Connect Null Search' }).click();
  await expect(page.getByLabel('Search only')).toBeChecked();
  await page.getByRole('button', { name: 'Create token' }).click();
  const searchToken = (await page.getByTestId('device-token').textContent())!.trim();
  await expect(page.getByTestId('server-url')).toHaveText(new URL(page.url()).origin);
  await page.getByRole('button', { name: 'Done' }).click();
  await expect(active.getByRole('listitem').filter({ hasText: 'Null Search' })).toContainText('Search only');
  const auth = { Authorization: `Bearer ${searchToken}` };
  const found = await page.request.get('/search?q=essay', { headers: auth });
  expect(found.status()).toBe(200);
  expect(Array.isArray(await found.json())).toBe(true);
  expect((await page.request.get('/api/v1/files', { headers: auth })).status()).toBe(403);
  const pre = await page.request.fetch('/search?q=x', {
    method: 'OPTIONS',
    headers: {
      Origin: 'https://search.nullgrid.dev',
      'Access-Control-Request-Method': 'GET',
      'Access-Control-Request-Headers': 'authorization',
      'Access-Control-Request-Private-Network': 'true',
    },
  });
  expect(pre.status()).toBe(204);
  expect(pre.headers()['access-control-allow-origin']).toBe('https://search.nullgrid.dev');
  expect(pre.headers()['access-control-allow-private-network']).toBe('true');

  // A nulldrive:// link opened in the browser: /open?id= finds the file by
  // its stable id and shows it, highlighted, in its folder.
  const listing = await (await page.request.get('/api/v1/files')).json();
  const file = listing.files.find((f: { is_dir: boolean; deleted: boolean }) => !f.is_dir && !f.deleted);
  await page.goto(`/open?id=${file.id}`);
  const name = file.path.split('/').pop();
  await expect(page).toHaveURL(new RegExp(`\\?show=${encodeURIComponent(name).replace(/[.()]/g, '\\$&')}$`));
  await expect(page.locator(`[data-path="${file.path}"]`).first()).toHaveClass(/located/);
  await page.goto(`/open?id=${'0'.repeat(32)}`);
  await expect(page.getByText('Not in this account')).toBeVisible();
  await page.goto('/settings');

  // Change password, then the new one works.
  await page.getByLabel('Current password').fill(PASS);
  await page.getByLabel('New password', { exact: true }).fill('an even better passphrase');
  await page.getByLabel('Confirm new password').fill('an even better passphrase');
  await page.getByRole('button', { name: 'Change password' }).click();
  await expect(page.getByText('Password changed.')).toBeVisible();
  const ok = await page.request.post('/api/v1/auth/login', { data: { username: USER, password: 'an even better passphrase' } });
  expect(ok.status()).toBe(200);
});

test('two-factor authentication: set up, sign in with a code, turn off', async ({ page }) => {
  const pass = 'an even better passphrase';
  await page.goto('/login');
  await page.getByLabel('Username').fill(USER);
  await page.getByLabel('Password', { exact: true }).fill(pass);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page).toHaveURL(/\/files/);
  await page.goto('/settings');
  await page.getByRole('button', { name: 'Set up…' }).click();
  const secret = (await page.getByTestId('totp-secret').textContent())!.replace(/\s/g, '');
  await page.getByLabel('Code from the app').fill(totp(secret));
  await page.getByRole('button', { name: 'Turn on', exact: true }).click();
  await expect(page.getByTestId('totp-recovery-codes').getByRole('listitem')).toHaveCount(10);
  const recovery = (await page.getByTestId('totp-recovery-codes').getByRole('listitem').first().textContent())!.trim();
  await page.getByRole('button', { name: 'Done' }).click();

  // Sign out; signing in now takes a second step.
  await page.getByRole('button', { name: 'Sign out' }).first().click();
  await page.getByLabel('Username').fill(USER);
  await page.getByLabel('Password', { exact: true }).fill(pass);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page.getByRole('heading', { name: 'Two-factor authentication' })).toBeVisible();
  // The code that confirmed setup is spent; a recovery code works once.
  await page.getByLabel('Code').fill(recovery);
  await page.getByRole('button', { name: 'Verify' }).click();
  await expect(page).toHaveURL(/\/files/);

  // The security log recorded it.
  await page.goto('/settings');
  await expect(page.getByText('2FA turned on')).toBeVisible();
  await expect(page.getByText('2FA recovery code used')).toBeVisible();

  // Turn it off again (password + a fresh code) for the tests that follow.
  await page.getByRole('button', { name: 'Turn off' }).click();
  const dialog = page.getByRole('dialog', { name: 'Turn off two-factor authentication' });
  await dialog.getByLabel('Password', { exact: true }).fill(pass);
  // A code from the next time step (allowed skew) can't collide with the
  // one used at setup.
  await dialog.getByLabel('Code (or a recovery code)').fill(totp(secret, Date.now() + 30_000));
  await dialog.getByRole('button', { name: 'Turn off' }).click();
  await expect(page.getByText('Two-factor authentication is off.')).toBeVisible();
});

/** Every file in the server's data dir, read as bytes. */
function dataDirContains(needle: string): string[] {
  const hits: string[] = [];
  const walk = (dir: string) => {
    for (const name of readdirSync(dir)) {
      const p = join(dir, name);
      if (statSync(p).isDirectory()) walk(p);
      else if (readFileSync(p).includes(needle)) hits.push(p);
    }
  };
  walk('.e2e-data');
  return hits;
}

test('end-to-end encryption: turn on, migrate, use, unlock', async ({ page }) => {
  const pass = 'an even better passphrase';
  const encPass = 'twelve monkeys in a row';
  await page.goto('/login');
  await page.getByLabel('Username').fill(USER);
  await page.getByLabel('Password', { exact: true }).fill(pass);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page).toHaveURL(/\/files/);
  await expect(page.getByRole('button', { name: 'Essay.txt', exact: true })).toBeVisible();
  expect(dataDirContains('Essay.txt').length).toBeGreaterThan(0); // control: plaintext names before

  await page.goto('/settings');
  await page.getByRole('button', { name: 'Turn on…' }).click();
  const dialog = page.getByRole('dialog', { name: 'Turn on end-to-end encryption' });
  await dialog.getByLabel('Encryption passphrase', { exact: true }).fill(encPass);
  await dialog.getByLabel('Repeat it', { exact: true }).fill(encPass);
  await dialog.getByRole('checkbox').check();
  await dialog.getByRole('button', { name: 'Create keys' }).click();
  const key = (await dialog.getByTestId('recovery-key').textContent())!.trim();
  expect(key).toMatch(/^([0-9A-Z]{4}-){7}[0-9A-Z]{4}$/);
  const download = page.waitForEvent('download');
  await dialog.getByRole('button', { name: 'Download' }).click();
  expect((await download).suggestedFilename()).toBe('null-drive-recovery-key.txt');
  await dialog.getByLabel(/Type the last group/).fill(key.split('-').pop()!);
  await dialog.getByRole('checkbox', { name: /saved my recovery key/ }).check();
  await dialog.getByRole('button', { name: 'Turn on encryption' }).click();
  await expect(dialog.getByText('End-to-end encryption is on.')).toBeVisible({ timeout: 20_000 });
  await dialog.getByRole('button', { name: 'Done' }).click();
  await expect(page.getByText('since')).toBeVisible();

  // The server now has only encrypted names; the browser shows real ones.
  const list = await (await page.request.get('/api/v1/files', { headers: { 'X-NullDrive-E2EE': '1' } })).json();
  expect(list.files.length).toBeGreaterThan(0);
  for (const f of list.files as { path: string }[]) expect(f.path).toMatch(/^~[A-Za-z0-9_-]+(\/~[A-Za-z0-9_-]+)*$/);
  // An old client (no E2EE header) is refused instead of syncing ciphertext.
  expect((await page.request.get('/api/v1/files')).status()).toBe(409);
  // In-app navigation (a full page load would forget the key; tested below).
  await page.getByRole('button', { name: 'Files', exact: true }).first().click();
  await expect(page.getByRole('button', { name: 'Essay.txt', exact: true })).toBeVisible();

  // Upload and download a new file through the browser's encryption.
  await page.getByTestId('file-input').setInputFiles([{ name: 'secret plans.txt', mimeType: 'text/plain', buffer: Buffer.from('attack at dawn') }]);
  await expect(page.getByText('Upload complete.')).toBeVisible();
  await rowAction(page, 'secret plans.txt', 'Download');
  const dl = await page.waitForEvent('download');
  expect(readFileSync((await dl.path())!, 'utf8')).toBe('attack at dawn');
  await rowAction(page, 'Essay.txt', 'Download');
  const essay = await page.waitForEvent('download');
  expect(readFileSync((await essay.path())!, 'utf8')).toBe('draft one');

  // A reload forgets the key: unlock again (a wrong passphrase first).
  await page.reload();
  await expect(page.getByRole('heading', { name: 'Unlock your files' })).toBeVisible();
  await page.getByLabel('Encryption passphrase', { exact: true }).fill('not it at all');
  await page.getByRole('button', { name: 'Unlock' }).click();
  await expect(page.getByRole('alert')).toContainText("doesn't match");
  await page.getByLabel('Encryption passphrase', { exact: true }).fill(encPass);
  await page.getByRole('button', { name: 'Unlock' }).click();
  await expect(page.getByRole('button', { name: 'secret plans.txt', exact: true })).toBeVisible({ timeout: 10_000 });

  // And with the recovery key instead of the passphrase.
  await page.reload();
  await page.getByRole('button', { name: /Use the recovery key/ }).click();
  await page.getByLabel('Recovery key').fill(key.toLowerCase());
  await page.getByRole('button', { name: 'Unlock' }).click();
  await expect(page.getByRole('heading', { name: 'Set a new passphrase' })).toBeVisible();
  await page.getByRole('button', { name: 'Skip' }).click();
  await expect(page.getByRole('button', { name: 'Essay.txt', exact: true })).toBeVisible();

  // Nothing readable is left in the data directory.
  for (const marker of ['Essay.txt', 'secret plans', 'attack at dawn', 'draft one', 'From the laptop']) {
    expect(dataDirContains(marker), marker).toEqual([]);
  }
});
