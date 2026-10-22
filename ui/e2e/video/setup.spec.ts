import { expect, test } from '@playwright/test';
import { caption, chapter, click, finish, hold, PASS, shot, showCard, stage, terminal, type } from './kit';

// "Set up the server" (about a minute): start it, the setup wizard, connect
// a computer. Recorded by scripts/record-video.sh setup. The terminal is a
// card with the real command and the server's real first log lines; the
// desktop app's setup screens are its real screenshots.

test('server setup video', async ({ baseURL }) => {
  const { ctx, page } = await stage(baseURL!);

  // ---- title ----
  await showCard(page, `<div class="wrap"><div class="logo"><span>NULL</span> DRIVE</div>
    <h1>Set up your server in a minute</h1><p>On a NAS, a mini PC, a Raspberry Pi or a VPS.</p>
    <div class="chips"><div><b>1</b>Start it</div><div><b>2</b>Create the admin account</div><div><b>3</b>Connect a computer</div></div></div>`);
  await chapter(page, 'Intro');
  await hold(page, 4500);

  // ---- start it ----
  await chapter(page, 'Start the server');
  const cmd = 'docker run -d --name nulldrive -p 8080:8080 -v nulldrive-data:/data --restart unless-stopped ghcr.io/null0grid/null-drive:latest';
  await terminal(page, 'you@homelab: ~', [
    { cmd, pause: 600 },
    { out: '<span class="d">latest: Pulling from null0grid/null-drive … Status: Downloaded newer image</span>', pause: 500 },
    { out: '7c41e2a9f0d3b8c5e6a1…', pause: 900 },
    { cmd: 'docker logs nulldrive', pause: 400 },
    { out: '{"level":"INFO","msg":"lan discovery active","instance":"Null Drive on homelab","port":8080,"addrs":["192.168.1.20"]}', pause: 200 },
    { out: '{"level":"INFO","msg":"listening","addr":":8080","data_dir":"/data","schema_version":8,"version":"v5.1.1"}', pause: 400 },
  ], ['Step 1', 'Start Null Drive with one Docker command. Your files live in the nulldrive-data volume.']);
  await caption(page, 'Step 1', 'It’s running on port 8080, and it announces itself on your network.');
  await hold(page, 3500);

  await showCard(page, `<div class="wrap"><h2>No Docker? Pick yours.</h2><p>Every option runs the same server.</p><div class="grid">
    <section><h3>Ubuntu / Debian</h3><div>sudo apt install ./nulldrive_5.1.1_amd64.deb</div></section>
    <section><h3>Snap</h3><div>sudo snap install --dangerous ./nulldrive_5.1.1_amd64.snap</div></section>
    <section><h3>Arch Linux</h3><div>nulldrive-bin (AUR)<br>makepkg -si</div></section>
    <section><h3>NAS</h3><div>CasaOS · ZimaOS · Unraid · TrueNAS SCALE</div></section>
    <section><h3>Docker Compose</h3><div>deploy/docker-compose.yml</div></section>
    <section><h3>Plain binary</h3><div>Linux · macOS · Windows<br>amd64 and arm64</div></section></div></div>`);
  await caption(page, 'Step 1', 'Or install it as a package, a snap, or an app on your NAS. All on the releases page.');
  await hold(page, 5500);

  // ---- wizard ----
  await page.goto('/');
  await chapter(page, 'Create the admin account');
  await caption(page, 'Step 2', 'Open http://your-server:8080 in a browser. A new server starts the setup wizard.');
  await expect(page.getByRole('heading', { name: /your files/i })).toBeVisible();
  await hold(page, 3000);
  await click(page, page.getByRole('button', { name: 'Get started' }));
  await caption(page, 'Step 2', 'Create the admin account. Do it right away: the first visitor gets it.');
  await hold(page, 700);
  await type(page, page.getByLabel('Password', { exact: true }), PASS, 40);
  await type(page, page.getByLabel('Confirm password'), PASS, 40);
  await hold(page, 700);
  await click(page, page.getByRole('button', { name: 'Create account' }));
  await expect(page.getByTestId('data-dir')).toBeVisible();
  await page.evaluate(() => { for (const el of document.querySelectorAll('[data-testid="data-dir"]')) el.textContent = '/data'; });
  await caption(page, 'Step 2', 'Check where your files will be stored and how much space is free.');
  await hold(page, 3500);
  await click(page, page.getByRole('button', { name: 'Looks good' }));
  await caption(page, 'Step 2', 'End-to-end encryption is optional; you can turn it on later in Settings.');
  await hold(page, 3500);
  await click(page, page.getByRole('button', { name: 'Skip for now' }));
  await hold(page, 2000);
  await click(page, page.getByRole('button', { name: 'Open Null Drive' }));
  await expect(page.getByText('Your drive is empty')).toBeVisible();
  await caption(page, 'Step 2', 'Done: the web app works right away, from any browser.');
  await hold(page, 3000);

  // ---- connect a computer ----
  await chapter(page, 'Connect a computer');
  await caption(page, 'Step 3', 'To sync a folder on a computer: Settings → Devices → Add device.');
  await click(page, page.getByRole('button', { name: 'Settings' }).first());
  await hold(page, 1200);
  await click(page, page.getByRole('button', { name: 'Add device' }));
  await expect(page.getByTestId('pairing-code')).toBeVisible();
  await caption(page, 'Step 3', 'You get a one-time code, good for 5 minutes. No password typed on the new computer.');
  await hold(page, 5000);
  await page.keyboard.press('Escape');
  await showCard(page, `<div class="slide"><img src="${shot('desktop-setup-server')}"></div>`);
  await caption(page, 'Step 3', 'Install the desktop app (Windows or Linux). It finds your server on the network by itself.');
  await hold(page, 5000);
  await showCard(page, `<div class="slide"><img src="${shot('desktop-setup-code')}"></div>`);
  await caption(page, 'Step 3', 'Choose “Pair with a code” and enter it.');
  await hold(page, 4000);
  await showCard(page, `<div class="slide"><img src="${shot('desktop-syncing')}"></div>`);
  await caption(page, 'Step 3', 'Your NullDrive folder now syncs both ways, live.');
  await hold(page, 4500);

  // ---- outro ----
  await showCard(page, `<div class="wrap"><div class="logo"><span>NULL</span> DRIVE</div>
    <h1>That’s it.</h1><p>Using it from outside your home? Put HTTPS in front (Caddy, Traefik or nginx).</p>
    <p>And back up the data folder. Both are in docs/INSTALL.md.</p>
    <p style="margin-top:24px;color:#d7deeb">github.com/Null0Grid/null-drive &nbsp;·&nbsp; nullgrid.dev</p></div>`);
  await caption(page, '', '');
  await chapter(page, 'Next steps');
  await hold(page, 5000);

  await finish(page, 'test-results/video-setup', 'Null Drive: set up the server');
  await ctx.close();
});
