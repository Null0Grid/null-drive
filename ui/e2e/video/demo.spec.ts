import { expect, test, type Page } from '@playwright/test';
import { existsSync, readdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { Api, art, caption, chapter, click, DesktopApp, finish, hold, sameOriginCard, setupServer, showCard, stage, type, until, USER } from './kit';

// The feature tour (about four minutes). Recorded by scripts/record-video.sh
// demo, which also needs NULLDRIVE_APPIMAGE: the live-sync scenes film the
// real Linux desktop app, signed in to this server, beside the web app.

const ENC = 'twelve monkeys in a row';
const SMALL = (n: number, fill: number) => Buffer.alloc(n, fill);

async function seed(api: Api) {
  for (const f of ['Documents/Taxes 2025', 'Music', 'Projects/nulldrive', 'Backups', 'Photos']) await api.folder(f);
  const photos: [string, number, number][] = [['Neon city', 185, 330], ['Rain on glass', 200, 280], ['Arcade', 320, 190],
    ['Rooftop at 3am', 160, 300], ['Signal', 140, 200], ['Night market', 300, 40], ['Harbour lights', 210, 20], ['Server room', 175, 130]];
  for (const [i, [name, a, b]] of photos.entries()) await api.put(`Photos/${name}.svg`, art(a, b, i + 1));
  await api.put('Documents/Lease agreement.pdf', SMALL(240_000, 3));
  await api.put('Documents/Taxes 2025/Return.pdf', SMALL(81_000, 4));
  await api.put('Documents/Budget 2026.xlsx', SMALL(58_000, 5));
  await api.put('Music/Synthwave mix.flac', SMALL(900_000, 6));
  await api.put('Projects/nulldrive/main.go', 'package main\n\nfunc main() {}\n');
  await api.put('Projects/nulldrive/README.md', '# Null Drive\n');
  await api.put('Backups/router-config-2026-10.tar.gz', SMALL(120_000, 8));
  await api.put('Packing list.md', '- passport\n- charger\n');
  await api.put('Homelab rack.svg', art(185, 120, 9));
  await api.put('Server notes.txt', 'ZimaBoard: 192.168.1.20\n');
}

/** A device paired with a code, as the desktop app does; its requests carry its name. */
async function pairedDevice(page: Page, api: Api, name: string, ua: string) {
  const code = (await (await page.request.post('/api/v1/pairing', { data: {}, headers: api.h() })).json()).code as string;
  const token = (await (await page.request.post('/api/v1/pairing/claim', { data: { code, device_name: name } })).json()).token as string;
  const headers = { Authorization: `Bearer ${token}`, 'User-Agent': ua };
  await page.request.get('/api/v1/files', { headers });
  return headers;
}

/** The budget edited from three places, so its history has a story. */
async function budgetHistory(page: Page, api: Api, desk: Record<string, string>, laptop: Record<string, string>) {
  const url = api.url('Documents/Budget 2026.xlsx');
  let v = (await (await page.request.get('/api/v1/files')).json()).files.find((f: { path: string }) => f.path === 'Documents/Budget 2026.xlsx').version;
  for (const [i, headers] of [desk, laptop, {}, desk].entries()) {
    const res = await page.request.put(url, { data: SMALL(58_000 + i * 1500, 11 + i), headers: api.h({ ...headers, 'If-Match': `"${v}"` }) });
    expect(res.ok()).toBeTruthy();
    v = (await res.json()).version;
  }
}

test('feature demo video', async ({ baseURL }) => {
  const { ctx, page } = await stage(baseURL!);
  await setupServer(page.request);
  const api = await Api.login(page.request);
  await seed(api);
  const desk = await pairedDevice(page, api, 'Desktop PC', 'NullDrive/5.1.1 (windows/amd64)');
  const work = await pairedDevice(page, api, 'Work laptop', 'NullDrive/5.1.1 (windows/amd64)');
  await budgetHistory(page, api, desk, work);
  for (const p of ['Backups/router-config-2026-10.tar.gz', 'Projects/nulldrive/main.go']) await api.del(p);
  // One failed sign-in for the security log.
  await page.request.post('/api/v1/auth/login', { data: { username: USER, password: 'hunter2' } });
  await page.addInitScript(() => localStorage.setItem('nd.view', 'list'));

  // ---- title ----
  await showCard(page, `<div class="wrap"><div class="logo"><span>NULL</span> DRIVE</div>
    <h1>Your own file sync server</h1><p>Like Dropbox, but you own the server. A tour of everything it does.</p>
    <div class="chips"><div><b>▸</b>Web app</div><div><b>▸</b>Desktop sync</div><div><b>▸</b>Version history</div>
    <div><b>▸</b>Conflicts kept</div><div><b>▸</b>End-to-end encryption</div><div><b>▸</b>Two-factor</div><div><b>▸</b>Null Grid</div></div></div>`);
  await chapter(page, 'Intro');
  await hold(page, 5500);

  // ---- web app ----
  await page.goto('/files');
  await expect(page.getByRole('button', { name: 'Photos', exact: true })).toBeVisible();
  await chapter(page, 'Your drive in the browser');
  await caption(page, 'Web app', 'Your files, on your server, in any browser. No install needed.');
  await page.mouse.move(700, 300, { steps: 25 });
  await hold(page, 2500);
  await click(page, page.getByRole('button', { name: 'Photos', exact: true }));
  await click(page, page.getByRole('button', { name: 'Grid view' }));
  await expect(page.locator('.tile img').first()).toBeVisible();
  await caption(page, 'Web app', 'Grid view with thumbnails, or a compact list.');
  await hold(page, 3500);
  await type(page, page.getByLabel('Filter by name'), 'night', 90);
  await caption(page, 'Web app', 'Filter any folder as you type.');
  await hold(page, 2500);
  await page.getByLabel('Filter by name').fill('');
  await click(page, page.getByRole('button', { name: 'List view' }));
  await click(page, page.getByRole('button', { name: 'Switch to light mode' }));
  await caption(page, 'Web app', 'Light or dark, your choice.');
  await hold(page, 2500);
  await click(page, page.getByRole('button', { name: 'Switch to dark mode' }));
  await hold(page, 800);

  // ---- upload and organise ----
  await page.goto('/files');
  await chapter(page, 'Upload and organise');
  await caption(page, 'Upload', 'Upload files or whole folders, or drag them onto the page. Big files resume if the connection drops.');
  const cdp = await ctx.newCDPSession(page);
  await cdp.send('Network.enable');
  await cdp.send('Network.emulateNetworkConditions', { offline: false, latency: 20, downloadThroughput: -1, uploadThroughput: 900_000 });
  const up = page.getByRole('button', { name: 'Upload', exact: true }).first();
  const ub = await up.boundingBox();
  if (ub) await page.mouse.move(ub.x + ub.width / 2, ub.y + ub.height / 2, { steps: 18 });
  await hold(page, 500);
  await page.getByTestId('file-input').setInputFiles([
    { name: 'Field recording.wav', mimeType: 'audio/wav', buffer: SMALL(3_000_000, 1) },
    { name: 'Scan 001.pdf', mimeType: 'application/pdf', buffer: SMALL(600_000, 2) },
    { name: 'Trip ideas.md', mimeType: 'text/markdown', buffer: Buffer.from('# Lisbon\n- tram 28\n') },
  ]);
  await hold(page, 3000);
  await cdp.send('Network.emulateNetworkConditions', { offline: false, latency: 0, downloadThroughput: -1, uploadThroughput: -1 });
  await expect(page.getByText('Upload complete.')).toBeVisible({ timeout: 30_000 });
  await hold(page, 1200);
  await click(page, page.getByRole('button', { name: 'Clear finished uploads' }));
  await caption(page, 'Organise', 'Make folders, then move, rename or download anything from its ⋯ menu.');
  await click(page, page.getByRole('button', { name: 'New folder' }).first());
  await type(page, page.getByLabel('Folder name'), 'Trips', 80);
  await click(page, page.getByRole('button', { name: 'Create' }));
  await hold(page, 1200);
  await click(page, page.getByRole('button', { name: 'Actions for Trip ideas.md' }));
  await hold(page, 600);
  await click(page, page.getByRole('menuitem', { name: 'Move to…' }));
  await click(page, page.getByRole('option', { name: 'Trips' }));
  await click(page, page.getByRole('button', { name: 'Move here' }));
  await hold(page, 2000);

  // ---- versions ----
  await click(page, page.getByRole('button', { name: 'Documents', exact: true }));
  await chapter(page, 'Every version kept');
  await caption(page, 'History', 'Every change is a new version. Open a file’s History…');
  await hold(page, 1200);
  await click(page, page.getByRole('button', { name: 'Actions for Budget 2026.xlsx' }));
  await click(page, page.getByRole('menuitem', { name: 'History' }));
  const hist = page.getByRole('dialog', { name: /History/ });
  await expect(hist.getByRole('listitem').first()).toBeVisible();
  await caption(page, 'History', '…to see when it changed and from which device, and restore any version.');
  await hold(page, 3500);
  await click(page, hist.getByRole('button', { name: /^Restore version from/ }).nth(2));
  await hold(page, 1200);
  await click(page, hist.getByRole('alertdialog', { name: 'Confirm restore' }).getByRole('button', { name: 'Restore' }));
  await caption(page, 'History', 'Restoring adds a new version, so nothing is lost either way.');
  await hold(page, 3000);
  await click(page, hist.getByRole('button', { name: 'Close', exact: true }).last());

  // ---- deleted ----
  await click(page, page.getByRole('button', { name: 'Deleted' }).first());
  await chapter(page, 'Recently deleted');
  await caption(page, 'Deleted', 'Deleted files wait here before they’re gone for good. One click brings them back.');
  await expect(page.getByText('main.go')).toBeVisible();
  await hold(page, 3000);
  await click(page, page.getByRole('button', { name: 'Restore main.go' }));
  await hold(page, 2500);

  // ---- live sync with the real desktop app ----
  const app = new DesktopApp(baseURL!);
  app.signIn(await api.device('Laptop'), 'Laptop');
  await sameOriginCard(page, `<div class="split"><iframe id="web" src="/files"></iframe></div>`, true);
  const web = page.frameLocator('#web');
  await expect(web.getByRole('button', { name: 'Photos', exact: true })).toBeVisible();
  await chapter(page, 'Live sync with the desktop app');
  await caption(page, 'Desktop app', 'The desktop app keeps a folder in sync. Here it’s on Linux, starting for the first time…', true);
  await app.start();
  await until(() => existsSync(join(app.folder, 'Server notes.txt')), 30_000);
  await caption(page, 'Desktop app', '…and the whole drive is on the computer in seconds.', true);
  await hold(page, 3500);

  await caption(page, 'Live', 'Add a file in the browser…', true);
  await web.getByTestId('file-input').setInputFiles([{ name: 'Boarding pass.pdf', mimeType: 'application/pdf', buffer: SMALL(140_000, 9) }]);
  await until(() => existsSync(join(app.folder, 'Boarding pass.pdf')));
  await caption(page, 'Live', '…and it’s on the computer a moment later.', true);
  await hold(page, 2500);
  await click(page, web.getByRole('button', { name: 'Clear finished uploads' }));
  await hold(page, 800);
  await caption(page, 'Live', 'Save a file on the computer…', true);
  writeFileSync(join(app.folder, 'Notes from the laptop.md'), '# Ideas\n- rack cable labels\n');
  const notes = web.getByRole('button', { name: 'Notes from the laptop.md', exact: true });
  await expect(notes).toBeVisible({ timeout: 20_000 });
  await notes.scrollIntoViewIfNeeded();
  const nb = await notes.boundingBox();
  if (nb) await page.mouse.move(nb.x + 120, nb.y + nb.height / 2, { steps: 15 });
  await caption(page, 'Live', '…and the web app shows it right away. Only changed parts of big files travel.', true);
  await hold(page, 4000);

  // ---- conflicts ----
  await chapter(page, 'Conflicts keep both');
  await caption(page, 'Conflicts', 'Now the same file is edited in two places while the laptop is paused (offline)…', true);
  app.click(275, 250); // Pause
  await hold(page, 1500);
  writeFileSync(join(app.folder, 'Server notes.txt'), 'ZimaBoard: 192.168.1.20\nPi 5: 192.168.1.41 (laptop edit)\n');
  const v = (await (await page.request.get('/api/v1/files')).json()).files.find((f: { path: string }) => f.path === 'Server notes.txt').version;
  await api.put('Server notes.txt', 'ZimaBoard: 192.168.1.20\nNAS: 192.168.1.10 (browser edit)\n', { 'If-Match': `"${v}"` });
  await hold(page, 2500);
  await caption(page, 'Conflicts', '…then it reconnects. Null Drive never silently overwrites: both versions are kept.', true);
  app.click(130, 225); // Resume (the paused card puts it first)
  await until(() => readdirSync(app.folder).some((n) => n.startsWith('Server notes (conflict')), 30_000);
  await hold(page, 1500);
  app.click(220, 347); // Conflicts tab
  await hold(page, 4500);
  await caption(page, 'Conflicts', 'The other version sits next to it as a “conflict” copy, on every device.', true);
  await hold(page, 3500);
  app.click(90, 347); // back to Activity

  // ---- devices ----
  app.hide();
  await page.goto('/settings');
  await chapter(page, 'Add devices');
  await caption(page, 'Devices', 'Every computer and app has its own token: see when each was last online, and revoke any.');
  // The device's name and its Online badge are one heading: "Laptop Online".
  const laptop = page.locator('.dev-info strong', { hasText: /^Laptop\s*Online/ });
  await expect(laptop).toBeVisible();
  await laptop.evaluate((el) => el.scrollIntoView({ block: 'center' }));
  const lb = await laptop.boundingBox();
  if (lb) await page.mouse.move(lb.x + 40, lb.y + lb.height / 2, { steps: 20 });
  await hold(page, 3500);
  await click(page, page.getByRole('button', { name: 'Add device' }));
  await expect(page.getByTestId('pairing-code')).toBeVisible();
  await caption(page, 'Devices', 'Add a computer with a one-time code or the QR code. No password typed on it.');
  await hold(page, 4500);
  await page.keyboard.press('Escape');
  await hold(page, 600);

  // ---- Null Grid ----
  await chapter(page, 'Part of Null Grid');
  await caption(page, 'Null Grid', 'Null Search can search your Drive: connect it with a token that can search and nothing else.');
  await click(page, page.getByRole('button', { name: 'Connect Null Search' }));
  await click(page, page.getByRole('button', { name: 'Create token' }));
  await expect(page.getByTestId('device-token')).toBeVisible();
  await hold(page, 3500);
  await click(page, page.getByRole('button', { name: 'Done' }));
  await showCard(page, `<div class="wrap"><h2>Null Grid</h2><p>Three apps, one self-hosted setup.</p><div class="grid">
    <section><h3>Null Drive</h3><div>Files in sync on every computer, with every version kept.</div></section>
    <section><h3>Null Search</h3><div>Searches the web and your Drive. Results open straight from your synced folder.</div></section>
    <section><h3>Null Notes</h3><div>Backs up your vault to Drive, or syncs it both ways, encrypted accounts included.</div></section></div></div>`);
  await caption(page, 'Null Grid', 'Null Notes backs up or syncs its vault to Drive, even on encrypted accounts.');
  await hold(page, 5000);

  // ---- security ----
  app.stop();
  await page.goto('/settings');
  await chapter(page, 'Two-factor and the security log');
  await caption(page, 'Security', 'Turn on two-factor sign-in with any authenticator app.');
  const twofa = page.getByRole('button', { name: 'Set up…' });
  await twofa.scrollIntoViewIfNeeded();
  await click(page, twofa);
  await expect(page.getByTestId('totp-secret')).toBeVisible();
  await hold(page, 4000);
  await click(page, page.getByRole('button', { name: 'Cancel' }));
  const log = page.getByRole('heading', { name: 'Security log' });
  await log.scrollIntoViewIfNeeded();
  await page.mouse.move(640, 420, { steps: 15 });
  await caption(page, 'Security', 'The security log shows sign-ins, failed attempts and device changes. Failed attempts are rate-limited.');
  await hold(page, 4500);

  // ---- phone ----
  await sameOriginCard(page, `<div style="display:flex;gap:56px;align-items:center">
    <div class="phone"><iframe src="/files/Photos"></iframe></div><div class="phone"><iframe src="/files/Documents"></iframe></div></div>`, true);
  await chapter(page, 'On your phone');
  await caption(page, 'Phone', 'The web app works on a phone too: upload, browse, move and restore from anywhere.');
  await hold(page, 5500);

  // ---- encryption ----
  await page.goto('/settings');
  await chapter(page, 'End-to-end encryption');
  await caption(page, 'Encryption', 'Optional end-to-end encryption: contents and names are encrypted on your devices.');
  await click(page, page.getByRole('button', { name: 'Turn on…' }));
  const dialog = page.getByRole('dialog', { name: 'Turn on end-to-end encryption' });
  await hold(page, 2000);
  await type(page, dialog.getByLabel('Encryption passphrase', { exact: true }), ENC, 35);
  await type(page, dialog.getByLabel('Repeat it', { exact: true }), ENC, 35);
  await click(page, dialog.getByRole('checkbox'));
  await click(page, dialog.getByRole('button', { name: 'Create keys' }));
  const key = (await dialog.getByTestId('recovery-key').textContent())!.trim();
  await caption(page, 'Encryption', 'You get a recovery key to print or save. The server can’t help you: that’s the point.');
  await hold(page, 4000);
  await type(page, dialog.getByLabel(/Type the last group/), key.split('-').pop()!, 110);
  await click(page, dialog.getByRole('checkbox', { name: /saved my recovery key/ }));
  await click(page, dialog.getByRole('button', { name: 'Turn on encryption' }));
  await caption(page, 'Encryption', 'Your existing files are encrypted right here in the browser.');
  await expect(dialog.getByText('End-to-end encryption is on.')).toBeVisible({ timeout: 120_000 });
  await hold(page, 2500);
  await click(page, dialog.getByRole('button', { name: 'Done' }));
  await page.goto('/files');
  await expect(page.getByRole('heading', { name: 'Unlock your files' })).toBeVisible();
  await caption(page, 'Encryption', 'Each browser and desktop app asks for the passphrase. The server only ever sees ciphertext.');
  await hold(page, 1500);
  await type(page, page.getByLabel('Encryption passphrase', { exact: true }), ENC, 40);
  await click(page, page.getByRole('button', { name: 'Unlock' }));
  await expect(page.getByRole('button', { name: 'Photos', exact: true })).toBeVisible({ timeout: 20_000 });
  await caption(page, 'Encryption', 'Same drive, same names, and delta sync still works.');
  await hold(page, 3500);

  // ---- platforms ----
  await showCard(page, `<div class="wrap"><h2>Runs where you do</h2><div class="grid">
    <section><h3>Server</h3><div>Docker (amd64 + arm64)<br>Ubuntu / Debian .deb · snap<br>Arch (AUR) · plain binary</div></section>
    <section><h3>NAS</h3><div>CasaOS · ZimaOS<br>Unraid · TrueNAS SCALE<br>Raspberry Pi 4 / 5</div></section>
    <section><h3>Desktop app</h3><div>Windows: Microsoft Store, installer<br>Linux: AppImage, Flatpak,<br>.deb, .rpm, AUR</div></section></div></div>`);
  await caption(page, '', '');
  await chapter(page, 'Runs where you do');
  await hold(page, 5500);

  // ---- outro ----
  await showCard(page, `<div class="wrap"><div class="logo"><span>NULL</span> DRIVE</div>
    <h1>Your files. Your server.</h1><p>Set it up in a minute: see “Set up the server”.</p>
    <p style="margin-top:24px;color:#d7deeb">github.com/Null0Grid/null-drive &nbsp;·&nbsp; nullgrid.dev</p></div>`);
  await chapter(page, 'Get it');
  await hold(page, 5000);

  await finish(page, 'test-results/video-demo', 'Null Drive: feature tour');
  await ctx.close();
});
