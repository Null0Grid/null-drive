import { expect, test, type APIRequestContext, type Page } from '@playwright/test';
import { fileURLToPath } from 'node:url';

// Generates docs/screenshots/*.png for the README: `make screenshots`.
// Runs against a fresh server, seeds realistic demo content through the
// API, then captures each screen in dark, light and phone layouts.

const out = (name: string) => fileURLToPath(new URL(`../../docs/screenshots/${name}.png`, import.meta.url));
const USER = 'admin';
const PASS = 'correct horse battery';

test.describe.configure({ mode: 'serial' });
test.use({ viewport: { width: 1360, height: 820 }, deviceScaleFactor: 2 });

// Abstract neon artwork (generated here, no third-party images) so the grid
// view has real thumbnails.
function art(hueA: number, hueB: number, seed: number): Buffer {
  const shapes = Array.from({ length: 6 }, (_, i) => {
    const x = (seed * 37 + i * 53) % 320;
    const y = (seed * 17 + i * 41) % 240;
    const r = 20 + ((seed + i * 13) % 60);
    return `<circle cx="${x}" cy="${y}" r="${r}" fill="none" stroke="hsl(${i % 2 ? hueA : hueB} 100% 60%)" stroke-width="3" opacity="0.85"/>`;
  }).join('');
  return Buffer.from(
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 320 240"><defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1">` +
      `<stop offset="0" stop-color="hsl(${hueA} 70% 12%)"/><stop offset="1" stop-color="hsl(${hueB} 70% 6%)"/></linearGradient></defs>` +
      `<rect width="320" height="240" fill="url(#g)"/>${shapes}` +
      `<path d="M0 ${180 - seed * 3} L320 ${120 + seed * 4}" stroke="hsl(${hueB} 100% 65%)" stroke-width="2"/></svg>`,
  );
}

// Cookie-authenticated writes need the session's CSRF token (as the app sends).
let csrfToken = '';
async function refreshCsrf(req: APIRequestContext) {
  csrfToken = (await (await req.get('/api/v1/me')).json()).csrf_token;
}
const h = (extra: Record<string, string> = {}) => ({ 'X-NullDrive-CSRF': csrfToken, ...extra });

async function put(req: APIRequestContext, path: string, body: Buffer | string) {
  const res = await req.put(`/api/v1/files/${path.split('/').map(encodeURIComponent).join('/')}`, { data: body, headers: h() });
  expect(res.ok(), `upload ${path}`).toBeTruthy();
}

async function seed(req: APIRequestContext) {
  await refreshCsrf(req);
  const bytes = (n: number) => Buffer.alloc(n, 7);
  for (const f of ['Documents/Taxes 2025', 'Music', 'Projects/nulldrive', 'Backups']) {
    await req.post('/api/v1/folders', { data: { path: f }, headers: h() });
  }
  await put(req, 'Photos/Neon city.svg', art(185, 330, 1));
  await put(req, 'Photos/Rain on glass.svg', art(200, 280, 2));
  await put(req, 'Photos/Arcade.svg', art(320, 190, 3));
  await put(req, 'Photos/Rooftop at 3am.svg', art(160, 300, 4));
  await put(req, 'Photos/Signal.svg', art(140, 200, 5));
  await put(req, 'Photos/Night market.svg', art(300, 40, 6));
  await put(req, 'Photos/Holiday clip.mp4', bytes(48_000_000));
  await put(req, 'Documents/Lease agreement.pdf', bytes(2_340_000));
  await put(req, 'Documents/Taxes 2025/Return.pdf', bytes(812_000));
  await put(req, 'Documents/Budget 2026.xlsx', bytes(58_000));
  await put(req, 'Music/Synthwave mix.flac', bytes(31_500_000));
  await put(req, 'Projects/nulldrive/main.go', 'package main\n');
  await put(req, 'Projects/nulldrive/README.md', '# Null Drive\n');
  await put(req, 'Backups/router-config-2026-10.tar.gz', bytes(4_200_000));
  await put(req, 'Packing list.md', '- passport\n- charger\n');
  await put(req, 'Homelab rack.svg', art(185, 120, 7));
  await put(req, 'Server notes.txt', 'ZimaBoard: 192.168.1.20\n');
}

async function shot(page: Page, name: string) {
  await page.waitForTimeout(250); // let transitions settle
  // Show a typical install path rather than the test's temp dir.
  await page.evaluate(() => {
    for (const el of document.querySelectorAll('[data-testid="data-dir"]')) el.textContent = '/srv/nulldrive/data';
    for (const el of document.querySelectorAll('[data-testid="mdns-addrs"]')) el.textContent = '192.168.1.20, fd00::20';
  });
  await page.screenshot({ path: out(name) });
}

async function login(page: Page) {
  const res = await page.request.post('/api/v1/auth/login', { data: { username: USER, password: PASS } });
  expect(res.ok()).toBeTruthy();
  csrfToken = (await res.json()).csrf_token;
}

test('wizard', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByRole('heading', { name: /your files/i })).toBeVisible();
  await shot(page, 'setup-1-welcome');
  await page.getByRole('button', { name: 'Get started' }).click();
  await page.getByLabel('Password', { exact: true }).fill(PASS);
  await page.getByLabel('Confirm password').fill(PASS);
  await shot(page, 'setup-2-account');
  await page.getByRole('button', { name: 'Create account' }).click();
  await expect(page.getByTestId('data-dir')).toBeVisible();
  await shot(page, 'setup-3-storage');
  await page.getByRole('button', { name: 'Looks good' }).click();
  await expect(page.getByRole('heading', { name: 'End-to-end encryption' })).toBeVisible();
  await shot(page, 'setup-4-privacy');
  await page.getByRole('button', { name: 'Skip for now' }).click();
  await shot(page, 'setup-5-done');
  await seed(page.request);
});

test('login', async ({ page }) => {
  await page.goto('/login');
  await page.getByLabel('Username').fill(USER);
  await shot(page, 'login');
});

test('file browser, dark', async ({ page }) => {
  await login(page);
  await page.goto('/files');
  await expect(page.getByRole('button', { name: 'Photos', exact: true })).toBeVisible();
  await shot(page, 'files-list-dark');

  await page.getByRole('button', { name: 'Photos', exact: true }).click();
  await page.getByRole('button', { name: 'Grid view' }).click();
  await expect(page.locator('.tile img').first()).toBeVisible();
  await shot(page, 'files-grid-dark');
  await page.getByRole('button', { name: 'List view' }).click();

  await page.goto('/files/Documents');
  await page.getByRole('button', { name: 'Actions for Lease agreement.pdf' }).click();
  await shot(page, 'files-menu');
  await page.getByRole('menuitem', { name: 'Move to…' }).click();
  await page.getByRole('option', { name: 'Backups' }).click();
  await shot(page, 'files-move-dialog');
  await page.getByRole('button', { name: 'Cancel' }).click();

  await page.goto('/files');
  await page.getByRole('button', { name: 'Actions for Backups' }).click();
  await page.getByRole('menuitem', { name: 'Delete' }).click();
  await shot(page, 'files-delete-confirm');
  await page.getByRole('button', { name: 'Cancel' }).click();

  // Upload in progress: throttle the network so the panel shows progress.
  const cdp = await page.context().newCDPSession(page);
  await cdp.send('Network.enable');
  await cdp.send('Network.emulateNetworkConditions', { offline: false, latency: 20, downloadThroughput: -1, uploadThroughput: 1_500_000 });
  await page.getByTestId('file-input').setInputFiles([
    { name: 'Field recording.wav', mimeType: 'audio/wav', buffer: Buffer.alloc(6_000_000, 1) },
    { name: 'Scan 001.pdf', mimeType: 'application/pdf', buffer: Buffer.alloc(900_000, 2) },
    { name: 'Notes.txt', mimeType: 'text/plain', buffer: Buffer.from('upload demo') },
  ]);
  await page.waitForTimeout(1500);
  await shot(page, 'files-uploading');
  await cdp.send('Network.emulateNetworkConditions', { offline: false, latency: 0, downloadThroughput: -1, uploadThroughput: -1 });
  await expect(page.getByText('Upload complete.')).toBeVisible({ timeout: 30_000 });
});

// A device as the desktop app would be: paired with a code, reporting its
// version, holding a realtime socket so it shows as online.
async function pairedDevice(page: Page, name: string, ua: string, online: boolean) {
  const code = (await (await page.request.post('/api/v1/pairing', { data: {}, headers: h() })).json()).code as string;
  const res = await page.request.post('/api/v1/pairing/claim', { data: { code, device_name: name } });
  const token = (await res.json()).token as string;
  const headers = { Authorization: `Bearer ${token}`, 'User-Agent': ua };
  await page.request.get('/api/v1/files', { headers });
  if (online) {
    const ws = new WebSocket(new URL('/api/v1/ws', page.url()).href.replace(/^http/, 'ws'), { headers } as unknown as string[]);
    await new Promise((r) => (ws.onmessage = r));
  }
  return headers;
}

// History for the README: a spreadsheet edited from three places, a
// duplicated video (dedup savings) and a few deleted files.
async function historyAndTrash(page: Page, desk: Record<string, string>, laptop: Record<string, string>) {
  const path = '/api/v1/files/Documents/Budget%202026.xlsx';
  let v = (await (await page.request.get('/api/v1/files')).json()).files.find((f: { path: string }) => f.path === 'Documents/Budget 2026.xlsx').version;
  for (const [i, headers] of [desk, laptop, {}, desk].entries()) {
    const res = await page.request.put(path, { data: Buffer.alloc(58_000 + i * 1500, 11 + i), headers: h({ ...headers, 'If-Match': `"${v}"` }) });
    expect(res.ok()).toBeTruthy();
    v = (await res.json()).version;
  }
  await put(page.request, 'Backups/Holiday clip (copy).mp4', Buffer.alloc(48_000_000, 7));
  for (const p of ['Packing list.md', 'Backups/router-config-2026-10.tar.gz', 'Projects/nulldrive/main.go']) {
    expect((await page.request.delete(`/api/v1/files/${p.split('/').map(encodeURIComponent).join('/')}`, { headers: h() })).ok()).toBeTruthy();
  }
}

test('settings, dark', async ({ page }) => {
  await login(page);
  await page.goto('/files');
  const desk = await pairedDevice(page, 'Desktop PC', 'NullDrive/0.6.0 (windows/amd64)', true);
  const laptop = await pairedDevice(page, 'Work laptop', 'NullDrive/0.6.0 (darwin/arm64)', false);
  await page.request.post('/api/v1/devices', { data: { name: 'Backup script' }, headers: h() });
  await historyAndTrash(page, desk, laptop);
  await page.goto('/settings');
  await expect(page.getByText('Desktop PC').first()).toBeVisible();
  await expect(page.getByText('Online', { exact: true })).toBeVisible();
  await expect(page.getByTestId('stored-bytes')).toBeVisible();
  await shot(page, 'settings-dark');
  await page.waitForTimeout(250);
  await page.locator('section', { has: page.getByRole('heading', { name: 'Storage' }) }).first().screenshot({ path: out('settings-storage') });

  await page.getByRole('button', { name: 'Add device' }).click();
  await expect(page.getByTestId('pairing-code')).toBeVisible();
  await page.evaluate(() => {
    // A stable code for the README (the QR still encodes the real one).
    document.querySelector('[data-testid="pairing-code"]')!.textContent = 'NULL-7K2Q-9F';
  });
  await shot(page, 'pair-dialog');
  await page.keyboard.press('Escape');

  // Connecting Null Search: a search-only token.
  await page.getByRole('button', { name: 'Connect Null Search' }).click();
  await shot(page, 'search-token-choice');
  await page.getByRole('button', { name: 'Create token' }).click();
  await expect(page.getByTestId('device-token')).toBeVisible();
  await page.evaluate(() => {
    document.querySelector('[data-testid="device-token"]')!.textContent = 'ndd_9vQ2…search-only…Lx7c';
    document.querySelector('[data-testid="server-url"]')!.textContent = 'https://drive.example.com';
  });
  await shot(page, 'search-token');
  await page.getByRole('button', { name: 'Done' }).click();

  await page.goto('/files/Documents?history=Budget%202026.xlsx');
  await expect(page.getByRole('dialog', { name: /History/ }).getByRole('listitem')).toHaveCount(5);
  await shot(page, 'history-dialog');
  await page.keyboard.press('Escape');
  await page.goto('/trash');
  await expect(page.getByText('main.go')).toBeVisible();
  await shot(page, 'trash');
});

test('light mode', async ({ page }) => {
  await login(page);
  await page.addInitScript(() => localStorage.setItem('nd.theme', 'light'));
  await page.goto('/files');
  await expect(page.getByRole('button', { name: 'Photos', exact: true })).toBeVisible();
  await shot(page, 'files-list-light');
  await page.goto('/files/Photos');
  await page.getByRole('button', { name: 'Grid view' }).click();
  await expect(page.locator('.tile img').first()).toBeVisible();
  await shot(page, 'files-grid-light');
  await page.goto('/settings');
  await shot(page, 'settings-light');
});

test.describe('phone', () => {
  test.use({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true });

  test('phone layouts', async ({ page }) => {
    await login(page);
    await page.addInitScript(() => localStorage.setItem('nd.view', 'list'));
    await page.goto('/files/Documents');
    await expect(page.getByRole('button', { name: 'Taxes 2025', exact: true })).toBeVisible();
    await shot(page, 'phone-files');
    await page.getByRole('button', { name: 'Actions for Budget 2026.xlsx' }).click();
    await shot(page, 'phone-menu');
    await page.keyboard.press('Escape');
    await page.goto('/files/Photos');
    await page.getByRole('button', { name: 'Grid view' }).click();
    await expect(page.locator('.tile img').first()).toBeVisible();
    await shot(page, 'phone-grid');
    await page.goto('/settings');
    await shot(page, 'phone-settings');
  });
});

// Phase 7: two-factor setup, the security log, turning on encryption
// (which migrates the demo files in the browser) and the unlock screen.
// Last, because it encrypts the account.
test('security screens', async ({ page }) => {
  test.setTimeout(180_000);
  await login(page);
  // A few log entries worth showing.
  await page.request.post('/api/v1/auth/login', { data: { username: USER, password: 'wrong guess 1' } });
  await login(page);
  await page.goto('/settings');
  await page.getByRole('button', { name: 'Set up…' }).click();
  await expect(page.getByTestId('totp-secret')).toBeVisible();
  await shot(page, 'totp-setup');
  await page.getByRole('button', { name: 'Cancel' }).click();
  await page.reload();
  await expect(page.getByText('Sign-in failed').first()).toBeVisible();
  await page.locator('section', { has: page.getByRole('heading', { name: 'Security log' }) }).first().screenshot({ path: out('audit-log') });

  await page.getByRole('button', { name: 'Turn on…' }).click();
  const dialog = page.getByRole('dialog', { name: 'Turn on end-to-end encryption' });
  await dialog.getByLabel('Encryption passphrase', { exact: true }).fill('twelve monkeys in a row');
  await dialog.getByLabel('Repeat it', { exact: true }).fill('twelve monkeys in a row');
  await dialog.getByRole('checkbox').check();
  await shot(page, 'encryption-passphrase');
  await dialog.getByRole('button', { name: 'Create keys' }).click();
  const key = (await dialog.getByTestId('recovery-key').textContent())!.trim();
  await shot(page, 'encryption-recovery-key');
  await dialog.getByLabel(/Type the last group/).fill(key.split('-').pop()!);
  await dialog.getByRole('checkbox', { name: /saved my recovery key/ }).check();
  await dialog.getByRole('button', { name: 'Turn on encryption' }).click();
  await expect(dialog.getByText('End-to-end encryption is on.')).toBeVisible({ timeout: 150_000 });
  await dialog.getByRole('button', { name: 'Done' }).click();
  await page.waitForTimeout(300);
  await page.locator('section', { has: page.getByRole('heading', { name: 'End-to-end encryption' }) }).first().screenshot({ path: out('settings-encryption') });

  await page.goto('/files');
  await page.getByRole('heading', { name: 'Unlock your files' }).waitFor();
  await page.getByLabel('Encryption passphrase', { exact: true }).fill('twelve monkeys in a row');
  await shot(page, 'unlock');
  await page.getByRole('button', { name: 'Unlock' }).click();
  await expect(page.getByRole('button', { name: 'Photos', exact: true })).toBeVisible({ timeout: 15_000 });
  await shot(page, 'files-encrypted');
});
