// Shared by the product videos (demo.spec.ts, setup.spec.ts). They're
// recorded by scripts/record-video.sh: a virtual screen (Xvfb, 1600×900)
// filmed with ffmpeg, a full-screen Chromium on it, and for the live-sync
// scenes the real Linux desktop app beside it. Captions, a visible cursor
// and chapter markers are drawn into the page.
import { chromium, expect, type APIRequestContext, type BrowserContext, type Frame, type Locator, type Page } from '@playwright/test';
import { execFileSync, spawn, type ChildProcess } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

export const USER = 'admin';
export const PASS = 'correct horse battery';
/** CSS pixels of the full-screen page (1600×900 screen at 1.25×). */
export const W = 1280;
export const H = 720;
export const root = (p: string) => fileURLToPath(new URL(`../../../${p}`, import.meta.url));
export const shot = (name: string) => `data:image/png;base64,${readFileSync(root(`docs/screenshots/${name}.png`)).toString('base64')}`;

// ---------- the browser ----------

/** Full-screen Chromium on the virtual screen (its first window is the kiosk one). */
export async function stage(baseURL: string): Promise<{ ctx: BrowserContext; page: Page }> {
  const profile = mkdtempSync(join(tmpdir(), 'ndv-profile-'));
  // No "Save password?" bubble over the video.
  mkdirSync(join(profile, 'Default'), { recursive: true });
  writeFileSync(join(profile, 'Default', 'Preferences'), JSON.stringify({ credentials_enable_service: false, profile: { password_manager_enabled: false } }));
  const ctx = await chromium.launchPersistentContext(profile, {
    executablePath: process.env.PW_CHROMIUM || undefined,
    headless: false,
    viewport: null,
    baseURL,
    acceptDownloads: true,
    args: ['--kiosk', '--window-position=0,0', '--window-size=1280,720', '--force-device-scale-factor=1.25',
      '--disable-infobars', '--hide-scrollbars', '--no-first-run', '--disable-features=Translate', '--password-store=basic',
      '--no-proxy-server'],
  });
  await ctx.addInitScript(overlay);
  const page = ctx.pages()[0] ?? (await ctx.newPage());
  return { ctx, page };
}

// Cursor + caption overlay, injected into every document (frames included).
export const overlay = () => {
  const install = () => {
    if (document.getElementById('__v_cursor')) return;
    const top = window === window.top;
    const st = document.createElement('style');
    st.textContent = `
      #__v_cursor{position:fixed;z-index:2147483647;left:0;top:0;width:22px;height:22px;pointer-events:none;
        transform:translate(-3px,-2px);transition:left .18s ease-out,top .18s ease-out}
      #__v_cursor svg{filter:drop-shadow(0 1px 2px rgba(0,0,0,.6))}
      .__v_ripple{position:fixed;z-index:2147483646;width:34px;height:34px;margin:-17px 0 0 -17px;border-radius:50%;
        border:2px solid #00e5ff;pointer-events:none;animation:__vr .5s ease-out forwards}
      @keyframes __vr{from{transform:scale(.3);opacity:1}to{transform:scale(1.4);opacity:0}}
      #__v_cap{position:fixed;z-index:2147483645;left:50%;bottom:16px;transform:translateX(-50%);max-width:88%;
        display:flex;align-items:center;gap:14px;padding:12px 20px;border-radius:12px;pointer-events:none;
        background:rgba(6,8,14,.9);border:1px solid rgba(0,229,255,.45);box-shadow:0 10px 30px rgba(0,0,0,.5);
        color:#e6edf7;font:500 17px/1.35 Inter,ui-sans-serif,system-ui,sans-serif;transition:opacity .25s}
      #__v_cap b{color:#00e5ff;font:700 12px/1 'JetBrains Mono',ui-monospace,monospace;letter-spacing:.1em;white-space:nowrap;
        padding:5px 8px;border:1px solid rgba(0,229,255,.5);border-radius:6px;text-transform:uppercase}
      #__v_cap:empty{opacity:0}
      #__v_cap.left{left:24px;transform:none;max-width:560px}`;
    document.documentElement.append(st);
    const c = document.createElement('div');
    c.id = '__v_cursor';
    c.innerHTML = '<svg width="22" height="22" viewBox="0 0 24 24"><path d="M3 2l7.5 19 2.4-7.6L20.5 11z" fill="#fff" stroke="#000" stroke-width="1.4" stroke-linejoin="round"/></svg>';
    c.style.left = '640px';
    c.style.top = '360px';
    document.documentElement.append(c);
    addEventListener('mousemove', (e) => {
      c.style.display = '';
      c.style.left = e.clientX + 'px';
      c.style.top = e.clientY + 'px';
      try { sessionStorage.setItem('__vPos', JSON.stringify([e.clientX, e.clientY])); } catch { /* */ }
    }, true);
    addEventListener('mousedown', (e) => {
      const r = document.createElement('div');
      r.className = '__v_ripple';
      r.style.left = e.clientX + 'px';
      r.style.top = e.clientY + 'px';
      document.documentElement.append(r);
      setTimeout(() => r.remove(), 600);
    }, true);
    // In a frame the cursor shows only while the mouse is over it.
    if (!top) {
      c.style.display = 'none';
      document.addEventListener('mouseleave', () => (c.style.display = 'none'));
      return;
    }
    const cap = document.createElement('div');
    cap.id = '__v_cap';
    document.documentElement.append(cap);
    try {
      const p = JSON.parse(sessionStorage.getItem('__vPos') || 'null');
      if (p) { c.style.left = p[0] + 'px'; c.style.top = p[1] + 'px'; }
      const k = JSON.parse(sessionStorage.getItem('__vCap') || 'null');
      if (k) { cap.innerHTML = k[0]; cap.className = k[1]; }
    } catch { /* */ }
  };
  if (document.readyState === 'loading') addEventListener('DOMContentLoaded', install);
  else install();
};

/** Caption at the bottom; `left` keeps it on the left half (desktop app on the right). */
export async function caption(page: Page, step: string, text: string, left = false) {
  const html = text ? (step ? `<b>${step}</b><span>${text}</span>` : `<span>${text}</span>`) : '';
  await page.evaluate(([h, cls]) => {
    const c = document.getElementById('__v_cap');
    if (c) { c.innerHTML = h; c.className = cls; }
    try { sessionStorage.setItem('__vCap', JSON.stringify([h, cls])); } catch { /* */ }
  }, [html, left ? 'left' : ''] as const);
}

// ---------- chapters ----------

// scripts/record-video.sh finds each chapter in the recording itself: it
// starts with an 8×8 px marker in the screen's top-left corner, in a colour
// unique to it, for 1.5 s. The last colour marks the end.
export const MARKERS = ['#ff00ff', '#00ff00', '#0000ff', '#ffff00', '#ff0000', '#00ffff', '#ffffff',
  '#ff8000', '#8000ff', '#00ff80', '#ff0080', '#80ff00', '#0080ff', '#ff8080', '#80ff80', '#8080ff'];
const chapters: string[] = [];

export async function chapter(page: Page, title: string) {
  if (chapters.length >= MARKERS.length - 1) throw new Error('too many chapters');
  const color = MARKERS[chapters.length]!;
  chapters.push(title);
  await mark(page, color);
}

async function mark(page: Page, color: string) {
  await page.evaluate((c) => {
    const m = document.createElement('div');
    m.style.cssText = `position:fixed;left:0;top:0;width:8px;height:8px;background:${c};z-index:2147483647;pointer-events:none`;
    document.documentElement.append(m);
    setTimeout(() => m.remove(), 1500);
  }, color);
}

/** Marks the end and writes the chapter list for record-video.sh. */
export async function finish(page: Page, dir: string, title: string) {
  await mark(page, MARKERS[chapters.length]!);
  await hold(page, 1700);
  mkdirSync(dir, { recursive: true });
  writeFileSync(join(dir, 'chapters.json'), JSON.stringify({ title, chapters, markers: MARKERS.slice(0, chapters.length + 1) }, null, 2));
}

// ---------- pointer and keyboard ----------

export const hold = (page: Page, ms: number) => page.waitForTimeout(ms);

/** Glide the visible cursor to an element, then click. Works inside frames. */
export async function click(page: Page, loc: Locator, pause = 350) {
  await loc.scrollIntoViewIfNeeded();
  const b = await loc.boundingBox();
  if (b) await page.mouse.move(b.x + b.width / 2, b.y + b.height / 2, { steps: 18 });
  await hold(page, pause);
  await loc.click();
}

export async function type(page: Page, loc: Locator, text: string, delay = 50) {
  await click(page, loc, 200);
  await loc.fill('');
  await loc.pressSequentially(text, { delay });
}

// ---------- cards ----------

export const card = (body: string) => `<!doctype html><html><head><meta charset="utf-8"><style>
  html,body{margin:0;height:100%;background:#0a0a0f;color:#d7deeb;font-family:Inter,ui-sans-serif,system-ui,sans-serif;overflow:hidden}
  body{display:grid;place-items:center;background:radial-gradient(900px 500px at 80% -10%,rgba(0,229,255,.10),transparent 60%),
    radial-gradient(800px 500px at -10% 110%,rgba(255,46,136,.08),transparent 60%),#0a0a0f}
  .logo{font:700 64px/1 'JetBrains Mono',ui-monospace,monospace;letter-spacing:.12em}.logo span{color:#00e5ff;text-shadow:0 0 18px rgba(0,229,255,.55)}
  h1{font-size:32px;font-weight:600;margin:22px 0 10px}h2{font-size:26px;font-weight:600;margin:0 0 6px}
  p{color:#8d9ab0;font-size:20px;margin:6px 0}
  .wrap{text-align:center;max-width:1080px;padding:0 40px}
  .chips{display:flex;gap:12px;justify-content:center;margin-top:30px;flex-wrap:wrap}
  .chips div{border:1px solid #313b54;border-radius:999px;padding:8px 16px;font-size:16px;color:#d7deeb;background:rgba(255,255,255,.02)}
  .chips b{color:#00e5ff;margin-right:6px}
  .term{width:1080px;border-radius:12px;overflow:hidden;border:1px solid #313b54;box-shadow:0 24px 60px rgba(0,0,0,.6);text-align:left}
  .bar{background:#151a26;padding:10px 14px;display:flex;gap:8px;align-items:center;color:#8d9ab0;font-size:14px}
  .bar i{width:12px;height:12px;border-radius:50%;display:inline-block}
  pre{margin:0;background:#06060a;padding:22px 24px;min-height:300px;font:15px/1.6 'JetBrains Mono',ui-monospace,monospace;
    white-space:pre-wrap;overflow-wrap:anywhere;color:#c8d3e6}
  .p{color:#3dff8f}.c{color:#00e5ff}.d{color:#5d6a82}.k{color:#ff2e88}
  .slide{display:flex;flex-direction:column;align-items:center;gap:16px}
  .slide img{max-width:1120px;max-height:560px;border-radius:12px;border:1px solid #313b54;box-shadow:0 24px 60px rgba(0,0,0,.6)}
  .grid{display:grid;grid-template-columns:repeat(3,1fr);gap:16px;margin-top:26px;text-align:left}
  .grid section{border:1px solid #313b54;border-radius:14px;padding:16px 18px;background:rgba(255,255,255,.02)}
  .grid h3{margin:0 0 8px;font:700 13px/1 'JetBrains Mono',monospace;letter-spacing:.1em;color:#00e5ff;text-transform:uppercase}
  .grid div{font-size:16px;line-height:1.7;color:#d7deeb}
  .split{position:fixed;inset:0 0 auto 0;height:656px;display:flex}
  .split iframe{border:0;width:640px;height:656px;background:#0a0a0f}
  .phone{width:300px;height:620px;border-radius:38px;border:10px solid #1b2130;overflow:hidden;box-shadow:0 30px 70px rgba(0,0,0,.65)}
  .phone iframe{border:0;width:390px;height:806px;transform:scale(.769);transform-origin:0 0}
</style></head><body>${body}</body></html>`;

export async function showCard(page: Page, body: string) {
  await page.setContent(card(body));
  await page.evaluate(overlay);
}

/**
 * A card on the server's own origin, so web-app frames in it stay signed in
 * (a frame in an about:blank page would be third-party and get no cookie).
 * `hideCursor`: the frames draw their own cursor.
 */
export async function sameOriginCard(page: Page, body: string, hideCursor = false) {
  await page.goto('/', { waitUntil: 'commit' });
  await page.evaluate((html) => { document.open(); document.write(html); document.close(); }, card(body));
  await page.evaluate(overlay);
  if (hideCursor) await page.addStyleTag({ content: '#__v_cursor{display:none!important}' });
}

/** Typed terminal lines in a card: [prompt?, text, pause ms]. */
export async function terminal(page: Page, title: string, lines: { cmd?: string; out?: string; pause?: number }[], cap?: [string, string]) {
  await showCard(page, `<div class="term"><div class="bar"><i style="background:#ff5f57"></i><i style="background:#febc2e"></i>
    <i style="background:#28c840"></i><span style="margin-left:10px">${title}</span></div><pre id="t"></pre></div>`);
  if (cap) await caption(page, cap[0], cap[1]);
  await page.evaluate(async (ls) => {
    const t = document.getElementById('t')!;
    const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
    for (const l of ls) {
      if (l.cmd !== undefined) {
        t.insertAdjacentHTML('beforeend', '<span class="p">you@homelab</span>:<span class="c">~</span>$ ');
        for (const ch of l.cmd) { t.insertAdjacentText('beforeend', ch); await sleep(22); }
        t.insertAdjacentText('beforeend', '\n');
      }
      if (l.out !== undefined) t.insertAdjacentHTML('beforeend', l.out + '\n');
      await sleep(l.pause ?? 500);
    }
  }, lines);
}

// ---------- server data ----------

export function art(hueA: number, hueB: number, seed: number): Buffer {
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

/** The session's API, with its CSRF header. */
export class Api {
  csrf = '';
  constructor(readonly req: APIRequestContext) {}
  static async login(req: APIRequestContext) {
    const a = new Api(req);
    const res = await req.post('/api/v1/auth/login', { data: { username: USER, password: PASS } });
    expect(res.ok(), 'login').toBeTruthy();
    a.csrf = (await res.json()).csrf_token;
    return a;
  }
  h(extra: Record<string, string> = {}) { return { 'X-NullDrive-CSRF': this.csrf, ...extra }; }
  url(path: string) { return `/api/v1/files/${path.split('/').map(encodeURIComponent).join('/')}`; }
  async put(path: string, body: Buffer | string, headers: Record<string, string> = {}) {
    const res = await this.req.put(this.url(path), { data: body, headers: this.h(headers) });
    expect(res.ok(), `upload ${path}: ${res.status()}`).toBeTruthy();
    return (await res.json()).version as number;
  }
  async folder(path: string) { await this.req.post('/api/v1/folders', { data: { path }, headers: this.h() }); }
  async del(path: string) { expect((await this.req.delete(this.url(path), { headers: this.h() })).ok()).toBeTruthy(); }
  /** A device token, as the desktop app's sign-in creates. */
  async device(name: string) {
    const res = await this.req.post('/api/v1/devices', { data: { name }, headers: this.h() });
    expect(res.ok(), 'create device').toBeTruthy();
    const d = await res.json();
    return { token: d.token as string, id: d.device.id as number };
  }
}

/** Finishes the setup wizard through the API (for videos that don't show it). */
export async function setupServer(req: APIRequestContext) {
  const res = await req.post('/api/v1/setup', { data: { username: USER, password: PASS } });
  expect(res.ok(), 'setup').toBeTruthy();
}

// ---------- the desktop app ----------

/** The real Linux desktop app, signed in as `device`, on the right half. */
export class DesktopApp {
  proc?: ChildProcess;
  wid = '';
  readonly home: string;
  readonly folder: string;
  constructor(readonly baseURL: string) {
    this.home = process.env.NDV_HOME || mkdtempSync(join(tmpdir(), 'ndv-home-'));
    this.folder = join(this.home, 'NullDrive');
  }
  get env() {
    return { ...process.env, HOME: this.home, XDG_CONFIG_HOME: join(this.home, '.config'), XDG_DATA_HOME: join(this.home, '.local/share'),
      APPIMAGE_EXTRACT_AND_RUN: '1', GDK_DPI_SCALE: '1.25', NO_PROXY: '*', no_proxy: '*' };
  }
  /** What the setup screens save: settings, and the token in the keyring. */
  signIn(device: { token: string; id: number }, deviceName: string) {
    const conf = join(this.home, '.config', 'Null Drive');
    mkdirSync(conf, { recursive: true });
    mkdirSync(this.folder, { recursive: true });
    writeFileSync(join(conf, 'config.json'), JSON.stringify({ server_url: this.baseURL, username: USER, device_name: deviceName,
      device_id: device.id, folder: this.folder, poll_seconds: 5 }));
    execFileSync('secret-tool', ['store', '--label=Null Drive', 'service', 'Null Drive', 'username', `${this.baseURL}|${deviceName}`],
      { input: device.token, env: this.env, timeout: 10_000 });
  }
  async start() {
    const bin = process.env.NULLDRIVE_APPIMAGE;
    if (!bin) throw new Error('NULLDRIVE_APPIMAGE: the Linux desktop app to film');
    this.proc = spawn(bin, [], { env: this.env, stdio: 'ignore', detached: true });
    for (let i = 0; i < 60 && !this.wid; i++) {
      try { this.wid = execFileSync('xdotool', ['search', '--onlyvisible', '--class', 'nulldrive-desktop'], { encoding: 'utf8' }).trim().split('\n').pop()!; }
      catch { await new Promise((r) => setTimeout(r, 300)); }
    }
    if (!this.wid) throw new Error('the desktop app window never appeared');
    this.place();
  }
  /** Right half, above the caption band. */
  place() { execFileSync('xdotool', ['windowmove', this.wid, '800', '0', 'windowsize', this.wid, '800', '820', 'windowraise', this.wid]); }
  /** Click inside the app window at (x, y) in its own pixels. */
  click(x: number, y: number) {
    execFileSync('xdotool', ['mousemove', '--window', this.wid, String(x), String(y), 'click', '1']);
    execFileSync('xdotool', ['mousemove', '1599', '899']); // park the real pointer out of view
  }
  hide() { if (this.wid) execFileSync('xdotool', ['windowunmap', this.wid]); }
  show() { if (this.wid) { execFileSync('xdotool', ['windowmap', this.wid]); this.place(); } }
  stop() { if (this.proc?.pid) { try { process.kill(-this.proc.pid, 'SIGTERM'); } catch { /* gone */ } } }
}

/** Waits until `fn` is true (for files arriving through sync). */
export async function until(fn: () => boolean | Promise<boolean>, ms = 20_000) {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    if (await fn()) return;
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error('timed out waiting');
}

export type { Frame };
