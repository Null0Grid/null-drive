import { expect, test, type Page } from '@playwright/test';
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

// Records the getting-started tutorial video against a fresh server:
//   npx playwright test --project=tutorial      (then scripts/tutorial-video.sh)
// Writes ui/test-results/tutorial/*.webm and chapters.json. Captions and a
// visible cursor are drawn into the page; the terminal and desktop-app scenes
// are recreated cards (real commands, real log lines, real app screenshots),
// because a browser can't film a terminal or a native window.

const W = 1280;
const H = 720;
const USER = 'admin';
const PASS = 'correct horse battery';
const ENC = 'twelve monkeys in a row';
const root = (p: string) => fileURLToPath(new URL(`../../${p}`, import.meta.url));
const shot = (name: string) => `data:image/png;base64,${readFileSync(root(`docs/screenshots/${name}.png`)).toString('base64')}`;

test.use({ viewport: { width: W, height: H }, actionTimeout: 15_000 });
test.setTimeout(10 * 60_000);

// Cursor + caption overlay, injected into every document.
const overlay = () => {
  const install = () => {
    if (document.getElementById('__tut_cursor')) return;
    const st = document.createElement('style');
    st.textContent = `
      #__tut_cursor{position:fixed;z-index:2147483647;left:0;top:0;width:22px;height:22px;pointer-events:none;
        transform:translate(-3px,-2px);transition:left .18s ease-out,top .18s ease-out}
      #__tut_cursor svg{filter:drop-shadow(0 1px 2px rgba(0,0,0,.6))}
      .__tut_ripple{position:fixed;z-index:2147483646;width:34px;height:34px;margin:-17px 0 0 -17px;border-radius:50%;
        border:2px solid #00e5ff;pointer-events:none;animation:__tutr .5s ease-out forwards}
      @keyframes __tutr{from{transform:scale(.3);opacity:1}to{transform:scale(1.4);opacity:0}}
      #__tut_cap{position:fixed;z-index:2147483645;left:50%;bottom:14px;transform:translateX(-50%);max-width:86%;
        display:flex;align-items:center;gap:14px;padding:12px 20px;border-radius:12px;pointer-events:none;
        background:rgba(6,8,14,.88);border:1px solid rgba(0,229,255,.45);box-shadow:0 10px 30px rgba(0,0,0,.5);
        color:#e6edf7;font:500 17px/1.35 ui-sans-serif,system-ui,'Segoe UI',sans-serif;transition:opacity .25s}
      #__tut_cap b{color:#00e5ff;font:700 13px/1 ui-monospace,monospace;letter-spacing:.08em;white-space:nowrap;
        padding:5px 8px;border:1px solid rgba(0,229,255,.5);border-radius:6px}
      #__tut_cap:empty{opacity:0}`;
    document.documentElement.append(st);
    const c = document.createElement('div');
    c.id = '__tut_cursor';
    c.innerHTML = '<svg width="22" height="22" viewBox="0 0 24 24"><path d="M3 2l7.5 19 2.4-7.6L20.5 11z" fill="#fff" stroke="#000" stroke-width="1.4" stroke-linejoin="round"/></svg>';
    const saved = (window as unknown as { __tutPos?: [number, number] }).__tutPos ?? [640, 360];
    c.style.left = saved[0] + 'px';
    c.style.top = saved[1] + 'px';
    document.documentElement.append(c);
    addEventListener('mousemove', (e) => {
      c.style.left = e.clientX + 'px';
      c.style.top = e.clientY + 'px';
      try { sessionStorage.setItem('__tutPos', JSON.stringify([e.clientX, e.clientY])); } catch { /* */ }
    }, true);
    addEventListener('mousedown', (e) => {
      const r = document.createElement('div');
      r.className = '__tut_ripple';
      r.style.left = e.clientX + 'px';
      r.style.top = e.clientY + 'px';
      document.documentElement.append(r);
      setTimeout(() => r.remove(), 600);
    }, true);
    const cap = document.createElement('div');
    cap.id = '__tut_cap';
    document.documentElement.append(cap);
    try {
      const p = JSON.parse(sessionStorage.getItem('__tutPos') || 'null');
      if (p) { c.style.left = p[0] + 'px'; c.style.top = p[1] + 'px'; }
      const k = JSON.parse(sessionStorage.getItem('__tutCap') || 'null');
      if (k) cap.innerHTML = k;
    } catch { /* */ }
  };
  if (document.readyState === 'loading') addEventListener('DOMContentLoaded', install);
  else install();
};

const chapters: { title: string; start: number }[] = [];
let t0 = 0;

async function caption(page: Page, step: string, text: string) {
  const html = text ? (step ? `<b>${step}</b><span>${text}</span>` : `<span>${text}</span>`) : '';
  await page.evaluate((h) => {
    const c = document.getElementById('__tut_cap');
    if (c) c.innerHTML = h;
    try { sessionStorage.setItem('__tutCap', JSON.stringify(h)); } catch { /* */ }
  }, html);
}

// Chapter boundaries are found in the recorded video itself
// (scripts/tutorial-video.sh): wall-clock times drift from video time, so
// each chapter starts with an 8×8 px marker in the top-left corner, in a
// colour unique to it, for 1.5 s. Invisible in practice; exact to the frame.
const MARKERS = ['#ff00ff', '#00ff00', '#0000ff', '#ffff00', '#ff0000', '#00ffff', '#ffffff', '#ff8000'];

async function chapter(page: Page, title: string) {
  const color = MARKERS[chapters.length % MARKERS.length]!;
  chapters.push({ title, start: (Date.now() - t0) / 1000 });
  await page.evaluate((c) => {
    const m = document.createElement('div');
    m.style.cssText = `position:fixed;left:0;top:0;width:8px;height:8px;background:${c};z-index:2147483647;pointer-events:none`;
    document.documentElement.append(m);
    setTimeout(() => m.remove(), 1500);
  }, color);
}

const hold = (page: Page, ms: number) => page.waitForTimeout(ms);

/** Move the visible cursor to an element in a smooth glide, then click. */
async function click(page: Page, loc: ReturnType<Page['locator']>, pause = 350) {
  await loc.scrollIntoViewIfNeeded();
  const b = await loc.boundingBox();
  if (b) await page.mouse.move(b.x + b.width / 2, b.y + b.height / 2, { steps: 18 });
  await hold(page, pause);
  await loc.click();
}

async function type(page: Page, loc: ReturnType<Page['locator']>, text: string, delay = 55) {
  await click(page, loc, 200);
  await loc.fill('');
  await loc.pressSequentially(text, { delay });
}

const card = (body: string) => `<!doctype html><html><head><meta charset="utf-8"><style>
  html,body{margin:0;height:100%;background:#0a0a0f;color:#d7deeb;font-family:ui-sans-serif,system-ui,'Segoe UI',sans-serif}
  body{display:grid;place-items:center;background:radial-gradient(900px 500px at 80% -10%,rgba(0,229,255,.10),transparent 60%),
    radial-gradient(800px 500px at -10% 110%,rgba(255,46,136,.08),transparent 60%),#0a0a0f}
  .logo{font:700 64px/1 ui-monospace,monospace;letter-spacing:.12em}.logo span{color:#00e5ff;text-shadow:0 0 18px rgba(0,229,255,.55)}
  h1{font-size:30px;font-weight:600;margin:22px 0 10px}p{color:#8d9ab0;font-size:20px;margin:6px 0}
  .wrap{text-align:center;max-width:980px;padding:0 40px}
  .steps{display:flex;gap:14px;justify-content:center;margin-top:34px;flex-wrap:wrap}
  .steps div{border:1px solid #313b54;border-radius:10px;padding:10px 16px;font-size:17px;color:#d7deeb}
  .steps b{color:#00e5ff;margin-right:8px}
  .term{width:1040px;border-radius:12px;overflow:hidden;border:1px solid #313b54;box-shadow:0 24px 60px rgba(0,0,0,.6);text-align:left}
  .bar{background:#151a26;padding:10px 14px;display:flex;gap:8px;align-items:center;color:#8d9ab0;font-size:14px}
  .bar i{width:12px;height:12px;border-radius:50%;display:inline-block}
  pre{margin:0;background:#06060a;padding:22px 24px;min-height:330px;font:16px/1.6 ui-monospace,'Cascadia Mono',Menlo,monospace;
    white-space:pre-wrap;overflow-wrap:anywhere;color:#c8d3e6}
  .p{color:#3dff8f}.c{color:#00e5ff}.d{color:#5d6a82}
  .slide{display:flex;flex-direction:column;align-items:center;gap:16px}
  .slide img{max-width:1100px;max-height:560px;border-radius:12px;border:1px solid #313b54;box-shadow:0 24px 60px rgba(0,0,0,.6)}
  .pair{display:flex;gap:22px;align-items:center}.pair img{max-width:560px;max-height:520px}
</style></head><body>${body}</body></html>`;

test('getting started tutorial', async ({ browser, baseURL }) => {
  const ctx = await browser.newContext({
    viewport: { width: W, height: H },
    recordVideo: { dir: 'test-results/tutorial', size: { width: W, height: H } },
    acceptDownloads: true,
  });
  await ctx.addInitScript(overlay);
  const page = await ctx.newPage();
  t0 = Date.now();

  // ---- title ----
  await page.setContent(card(`<div class="wrap"><div class="logo"><span>NULL</span> DRIVE</div>
    <h1>Getting started in five minutes</h1><p>Self-hosted file sync: like Dropbox, but you own the server.</p>
    <div class="steps"><div><b>1</b>Start the server</div><div><b>2</b>Setup wizard</div><div><b>3</b>Use the web app</div>
    <div><b>4</b>Connect a computer</div><div><b>5</b>2FA &amp; encryption</div></div></div>`));
  await page.evaluate(overlay);
  await chapter(page, 'Intro');
  await hold(page, 6000);

  // ---- terminal ----
  const cmd = 'docker run -d --name nulldrive -p 8080:8080 -v nulldrive-data:/data --restart unless-stopped ghcr.io/null0grid/null-drive:latest';
  await page.setContent(card(`<div class="term"><div class="bar"><i style="background:#ff5f57"></i><i style="background:#febc2e"></i>
    <i style="background:#28c840"></i><span style="margin-left:10px">luc@homelab: ~</span></div><pre id="t"></pre></div>`));
  await page.evaluate(overlay);
  await chapter(page, 'Step 1: Start the server');
  await caption(page, 'STEP 1', 'On your server (NAS, mini PC, Pi), start Null Drive with one Docker command.');
  await page.evaluate(async (c) => {
    const t = document.getElementById('t')!;
    const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
    const line = (html: string) => { t.innerHTML += html + '\n'; };
    t.innerHTML = '<span class="p">luc@homelab</span>:<span class="c">~</span>$ ';
    for (const ch of c) { t.innerHTML += ch; await sleep(28); }
    t.innerHTML += '\n';
    await sleep(700);
    line('<span class="d">Unable to find image \'ghcr.io/null0grid/null-drive:latest\' locally</span>');
    await sleep(500);
    line('<span class="d">latest: Pulling from Null0Grid/null-drive … Download complete</span>');
    await sleep(700);
    line('3f9c1e7a2b54d0c86e1f…');
    await sleep(900);
    t.innerHTML += '<span class="p">luc@homelab</span>:<span class="c">~</span>$ ';
    for (const ch of 'docker logs nulldrive') { t.innerHTML += ch; await sleep(40); }
    t.innerHTML += '\n';
    await sleep(500);
    line('{"level":"INFO","msg":"lan discovery active","instance":"Null Drive on homelab","port":8080}');
    line('{"level":"INFO","msg":"listening","addr":":8080","data_dir":"/data","schema_version":5}');
  }, cmd);
  await hold(page, 2500);
  await caption(page, 'STEP 1', 'That’s it. Now open http://&lt;server-ip&gt;:8080 in a browser.');
  await hold(page, 4000);

  // ---- wizard ----
  await page.goto('/');
  await chapter(page, 'Step 2: Setup wizard');
  await caption(page, 'STEP 2', 'A fresh server opens the setup wizard.');
  await expect(page.getByRole('heading', { name: /your files/i })).toBeVisible();
  await hold(page, 2500);
  await click(page, page.getByRole('button', { name: 'Get started' }));
  await caption(page, 'STEP 2', 'Create the admin account. Your password stays hashed on the server.');
  await hold(page, 800);
  await type(page, page.getByLabel('Password', { exact: true }), PASS, 45);
  await type(page, page.getByLabel('Confirm password'), PASS, 45);
  await hold(page, 900);
  await click(page, page.getByRole('button', { name: 'Create account' }));
  await caption(page, 'STEP 2', 'Confirm where files are stored and how much space is free.');
  await expect(page.getByTestId('data-dir')).toBeVisible();
  await page.evaluate(() => { for (const el of document.querySelectorAll('[data-testid="data-dir"]')) el.textContent = '/data'; });
  await hold(page, 3500);
  await click(page, page.getByRole('button', { name: 'Looks good' }));
  await caption(page, 'STEP 2', 'End-to-end encryption is optional. Skip it for now; it’s in Settings later.');
  await hold(page, 4000);
  await click(page, page.getByRole('button', { name: 'Skip for now' }));
  await caption(page, 'STEP 2', 'Done. The desktop app comes later; the web app already does everything.');
  await hold(page, 3500);
  await click(page, page.getByRole('button', { name: 'Open Null Drive' }));

  // ---- web app ----
  await expect(page.getByText('Your drive is empty')).toBeVisible();
  await chapter(page, 'Step 3: Use the web app');
  await caption(page, 'STEP 3', 'Upload with the button, or drag files and whole folders onto the page.');
  await expect(page.getByText('Your drive is empty')).toBeVisible();
  await page.mouse.move(W - 140, 100, { steps: 20 });
  await hold(page, 1500);
  const upload = page.getByRole('button', { name: 'Upload', exact: true }).first();
  const ub = await upload.boundingBox();
  if (ub) await page.mouse.move(ub.x + ub.width / 2, ub.y + ub.height / 2, { steps: 15 });
  await hold(page, 600);
  await page.getByTestId('file-input').setInputFiles([
    { name: 'Lease agreement.pdf', mimeType: 'application/pdf', buffer: Buffer.alloc(820_000, 3) },
    { name: 'Budget 2026.xlsx', mimeType: 'application/vnd.ms-excel', buffer: Buffer.alloc(58_000, 5) },
    { name: 'Trip photos.zip', mimeType: 'application/zip', buffer: Buffer.alloc(3_400_000, 9) },
    { name: 'Server notes.txt', mimeType: 'text/plain', buffer: Buffer.from('ZimaBoard: 192.168.1.20\n') },
  ]);
  await expect(page.getByText('Upload complete.')).toBeVisible();
  await hold(page, 2500);
  await click(page, page.getByRole('button', { name: 'Clear finished uploads' }));

  await caption(page, 'STEP 3', 'Make folders, and move or rename things from the ⋯ menu.');
  await click(page, page.getByRole('button', { name: 'New folder' }).first());
  await type(page, page.getByLabel('Folder name'), 'Documents', 70);
  await click(page, page.getByRole('button', { name: 'Create' }));
  await hold(page, 1500);
  await click(page, page.getByRole('button', { name: 'Actions for Lease agreement.pdf' }));
  await hold(page, 500);
  await click(page, page.getByRole('menuitem', { name: 'Move to…' }));
  await hold(page, 700);
  await click(page, page.getByRole('option', { name: 'Documents' }));
  await click(page, page.getByRole('button', { name: 'Move here' }));
  await hold(page, 1800);

  await caption(page, 'STEP 3', 'Every change is kept as a version. Upload a newer budget…');
  await page.getByTestId('file-input').setInputFiles([{ name: 'Budget 2026.xlsx', mimeType: 'application/vnd.ms-excel', buffer: Buffer.alloc(61_000, 6) }]);
  await expect(page.getByRole('dialog', { name: 'File already exists' })).toBeVisible();
  await hold(page, 1500);
  await click(page, page.getByRole('button', { name: 'Replace' }));
  await expect(page.getByText('Upload complete.')).toBeVisible();
  await hold(page, 1000);
  await click(page, page.getByRole('button', { name: 'Clear finished uploads' }));
  await caption(page, 'STEP 3', '…then open its History: see when and where it changed, and restore any version.');
  await click(page, page.getByRole('button', { name: 'Actions for Budget 2026.xlsx' }));
  await click(page, page.getByRole('menuitem', { name: 'History' }));
  await hold(page, 4500);
  await page.keyboard.press('Escape');
  await hold(page, 600);

  await caption(page, 'STEP 3', 'Deleted something by mistake? It waits in Deleted for 120 days.');
  await click(page, page.getByRole('button', { name: 'Actions for Server notes.txt' }));
  await click(page, page.getByRole('menuitem', { name: 'Delete' }));
  await hold(page, 1200);
  await click(page, page.getByRole('dialog').getByRole('button', { name: 'Delete' }));
  await hold(page, 1200);
  await click(page, page.getByRole('button', { name: 'Deleted' }).first());
  await hold(page, 2000);
  await click(page, page.getByRole('button', { name: 'Restore Server notes.txt' }));
  await hold(page, 2500);

  // ---- devices ----
  await chapter(page, 'Step 4: Connect a computer');
  await caption(page, 'STEP 4', 'To sync a folder on a computer, add it from Settings → Devices.');
  await click(page, page.getByRole('button', { name: 'Settings' }).first());
  await hold(page, 1500);
  await click(page, page.getByRole('button', { name: 'Add device' }));
  await expect(page.getByTestId('pairing-code')).toBeVisible();
  await caption(page, 'STEP 4', 'You get a one-time code (5 minutes, single use). No password typed on the new computer.');
  await hold(page, 5000);
  await page.keyboard.press('Escape');
  await page.setContent(card(`<div class="slide"><div class="pair"><img src="${shot('desktop-setup-server')}"></div></div>`));
  await page.evaluate(overlay);
  await caption(page, 'STEP 4', 'In the desktop app, the server shows up by itself on your network. Choose it, then Pair with a code.');
  await hold(page, 5500);
  await page.setContent(card(`<div class="slide"><img src="${shot('desktop-syncing')}"></div>`));
  await page.evaluate(overlay);
  await caption(page, 'STEP 4', 'Pick a folder and it syncs both ways, live. Edits on two computers? Both versions are kept.');
  await hold(page, 5500);

  // ---- security ----
  await page.goto('/settings');
  await chapter(page, 'Step 5: Two-factor sign-in and encryption');
  await caption(page, 'STEP 5', 'Optional: turn on two-factor sign-in with any authenticator app.');
  const twofa = page.getByRole('button', { name: 'Set up…' });
  await twofa.scrollIntoViewIfNeeded();
  await click(page, twofa);
  await expect(page.getByTestId('totp-secret')).toBeVisible();
  await page.mouse.move(W / 2, H / 2 + 40, { steps: 10 });
  await hold(page, 4500);
  await click(page, page.getByRole('button', { name: 'Cancel' }));
  await hold(page, 600);

  await caption(page, 'STEP 5', 'Optional: end-to-end encryption. Files and names are encrypted on your device.');
  await click(page, page.getByRole('button', { name: 'Turn on…' }));
  const dialog = page.getByRole('dialog', { name: 'Turn on end-to-end encryption' });
  await hold(page, 2500);
  await type(page, dialog.getByLabel('Encryption passphrase', { exact: true }), ENC, 40);
  await type(page, dialog.getByLabel('Repeat it', { exact: true }), ENC, 40);
  await click(page, dialog.getByRole('checkbox'));
  await click(page, dialog.getByRole('button', { name: 'Create keys' }));
  const key = (await dialog.getByTestId('recovery-key').textContent())!.trim();
  await caption(page, 'STEP 5', 'Save the recovery key (download or print). Lose it and the passphrase, and the files are gone.');
  await hold(page, 5000);
  await type(page, dialog.getByLabel(/Type the last group/), key.split('-').pop()!, 120);
  await click(page, dialog.getByRole('checkbox', { name: /saved my recovery key/ }));
  await click(page, dialog.getByRole('button', { name: 'Turn on encryption' }));
  await caption(page, 'STEP 5', 'Existing files are encrypted in the browser; the unencrypted copies are deleted from the server.');
  await expect(dialog.getByText('End-to-end encryption is on.')).toBeVisible({ timeout: 60_000 });
  await hold(page, 3000);
  await click(page, dialog.getByRole('button', { name: 'Done' }));
  await hold(page, 1500);
  await page.goto('/files');
  await caption(page, 'STEP 5', 'From now on each browser asks for the passphrase to decrypt your files.');
  await expect(page.getByRole('heading', { name: 'Unlock your files' })).toBeVisible();
  await hold(page, 2000);
  await type(page, page.getByLabel('Encryption passphrase', { exact: true }), ENC, 45);
  await click(page, page.getByRole('button', { name: 'Unlock' }));
  await expect(page.getByRole('button', { name: 'Documents', exact: true })).toBeVisible({ timeout: 15_000 });
  await caption(page, 'STEP 5', 'Same drive, same names, but the server only ever sees ciphertext.');
  await hold(page, 4000);

  // ---- outro ----
  await page.setContent(card(`<div class="wrap"><div class="logo"><span>NULL</span> DRIVE</div>
    <h1>You're set up.</h1><p>Next: back up the data folder, and put HTTPS in front if you'll use it from outside.</p>
    <p style="margin-top:22px;color:#d7deeb">docs/INSTALL.md &nbsp;·&nbsp; docs/SECURITY.md &nbsp;·&nbsp; the Getting Started tutorial doc</p></div>`));
  await page.evaluate(overlay);
  await caption(page, '', '');
  await chapter(page, 'Next steps');
  await hold(page, 6000);

  const total = (Date.now() - t0) / 1000;
  writeFileSync('test-results/tutorial/chapters.json', JSON.stringify({ total, chapters, markers: MARKERS.slice(0, chapters.length), baseURL }, null, 2));
  const video = page.video()!;
  await ctx.close();
  await video.saveAs('test-results/tutorial/tutorial.webm');
});
