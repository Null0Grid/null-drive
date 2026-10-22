#!/usr/bin/env node
// Regenerates docs/screenshots/desktop-*.png: the desktop app's UI in its
// browser preview (client/frontend with the demo backend from bridge.ts),
// at the app's default window size, 2x.
//
//   node scripts/desktop-screenshots.mjs
//
// Uses Playwright from ui/node_modules (run `npm ci` in ui/ and
// client/frontend/ first).
import { spawn } from 'node:child_process';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const root = fileURLToPath(new URL('..', import.meta.url));
const require = createRequire(path.join(root, 'ui', 'package.json'));
const { chromium } = require('@playwright/test');
const out = (n) => path.join(root, 'docs', 'screenshots', `desktop-${n}.png`);
const PORT = 5181;

const vite = spawn(path.join(root, 'client', 'frontend', 'node_modules', '.bin', 'vite'), ['--port', String(PORT), '--strictPort'], {
  cwd: path.join(root, 'client', 'frontend'),
  stdio: ['ignore', 'pipe', 'inherit'],
  detached: true, // own process group, so it can be stopped with its children
});
vite.stdout.resume();
for (let i = 0; ; i++) {
  try {
    if ((await fetch(`http://localhost:${PORT}/`)).ok) break;
  } catch {
    /* not up yet */
  }
  if (i > 120) throw new Error('vite did not start');
  await new Promise((r) => setTimeout(r, 250));
}

// PW_CHROMIUM=/path/to/chrome uses that Chromium instead of Playwright's own.
const browser = await chromium.launch({ executablePath: process.env.PW_CHROMIUM || undefined });
try {
  const page = await browser.newPage({ viewport: { width: 980, height: 700 }, deviceScaleFactor: 2, colorScheme: 'dark' });
  const shot = async (name, full = false) => {
    await page.waitForTimeout(350); // let transitions settle
    await page.screenshot({ path: out(name), fullPage: full });
    console.log('wrote', path.relative(root, out(name)));
  };
  await page.goto(`http://localhost:${PORT}/`);
  await page.getByText('Not set up').first().waitFor().catch(() => {});
  await page.waitForTimeout(600);
  await shot('setup-server');

  await page.getByLabel('Or enter an address').fill('https://nas.lan');
  await page.getByRole('button', { name: 'Continue' }).click();
  await page.getByRole('button', { name: 'Trust this certificate' }).waitFor();
  await shot('setup-trust');

  await page.getByRole('button', { name: 'Trust this certificate' }).click();
  await page.getByText('Pair with a code').waitFor();
  await shot('setup-method');

  await page.getByText('Pair with a code').click();
  await page.getByLabel('Pairing code').fill('NULL-7K2Q-9F');
  await shot('setup-code');

  await page.getByRole('button', { name: 'Continue' }).click();
  await page.getByLabel('Computer name').waitFor();
  await page.getByLabel('Computer name').fill('Desktop PC');
  await shot('setup-device');

  await page.getByRole('button', { name: /Pair and sync/ }).click();
  await page.getByRole('tab', { name: /Activity/ }).waitFor();
  await page.waitForTimeout(700);
  await shot('syncing');

  await page.getByText('Up to date').first().waitFor({ timeout: 20_000 });
  await shot('activity');
  await page.getByRole('tab', { name: /Conflicts/ }).click();
  await shot('conflicts');
  await page.getByRole('tab', { name: /Settings/ }).click();
  await shot('settings', true);
} finally {
  await browser.close();
  process.kill(-vite.pid);
}
process.exit(0);
