// The only place that knows about Wails. Wails injects the bound Go
// methods as window.go.main.App.<Method> (returning promises) and the
// runtime as window.runtime. Everything else in the UI calls `api` and
// `on`, so components stay testable and the shared library stays free of
// desktop assumptions.
//
// Outside Wails (`npm run dev` in a browser, Vitest), a small in-memory
// demo backend stands in, so the UI can be developed and tested without
// the Go side.

import type {
  AppState,
  ConflictRecord,
  DiscoveredServer,
  PairRequest,
  Resolution,
  ServerInfo,
  Settings,
  SetupRequest,
  Status,
  SyncEvent,
} from './types';

type Listener = (data: unknown) => void;

interface WailsWindow {
  go?: { main?: { App?: Record<string, (...args: unknown[]) => Promise<unknown>> } };
  runtime?: { EventsOn?: (name: string, cb: Listener) => (() => void) | void; EventsOff?: (name: string) => void };
}

const w = (typeof window === 'undefined' ? {} : window) as unknown as WailsWindow;

export const inWails = (): boolean => !!w.go?.main?.App;

function call<T>(method: string, ...args: unknown[]): Promise<T> {
  const fn = w.go?.main?.App?.[method];
  if (fn) return fn(...args) as Promise<T>;
  const m = (mock as unknown as Record<string, (...a: unknown[]) => unknown>)[method];
  if (!m) return Promise.reject(new Error(`${method} is not available`));
  return Promise.resolve().then(() => m.apply(mock, args) as T);
}

export const api = {
  getState: () => call<AppState>('GetState'),
  activity: () => call<SyncEvent[]>('Activity'),
  conflicts: () => call<ConflictRecord[]>('Conflicts'),
  discover: () => call<DiscoveredServer[]>('Discover'),
  testServer: (url: string, pin = '') => call<ServerInfo>('TestServer', url, pin),
  setup: (req: SetupRequest) => call<void>('Setup', req),
  pairWithCode: (req: PairRequest) => call<void>('PairWithCode', req),
  parsePairLink: (s: string) => call<{ server?: string; code?: string }>('ParsePairLink', s),
  defaultFolder: () => call<string>('DefaultFolder'),
  defaultDeviceName: () => call<string>('DefaultDeviceName'),
  chooseFolder: (current: string) => call<string>('ChooseFolder', current),
  pause: () => call<void>('Pause'),
  resume: () => call<void>('Resume'),
  syncNow: () => call<void>('SyncNow'),
  confirmMassDelete: () => call<void>('ConfirmMassDelete'),
  openFolder: () => call<void>('OpenFolder'),
  revealFile: (path: string) => call<void>('RevealFile', path),
  openHistory: (path: string) => call<void>('OpenHistory', path),
  openWebApp: (page: '' | 'trash' | 'settings') => call<void>('OpenWebApp', page),
  openLogs: () => call<void>('OpenLogs'),
  resolveConflict: (id: number, how: Resolution) => call<void>('ResolveConflict', id, how),
  saveSettings: (s: Settings) => call<void>('SaveSettings', s),
  changeFolder: (folder: string) => call<void>('ChangeFolder', folder),
  unlink: () => call<void>('Unlink'),
  unlockE2EE: (secret: string, recovery: boolean) => call<void>('UnlockE2EE', secret, recovery),
  retrySync: () => call<void>('RetrySync'),
};

/** Subscribe to a backend event ("status" | "activity"). Returns an unsubscribe function. */
export function on<T>(event: 'status' | 'activity', cb: (data: T) => void): () => void {
  if (w.runtime?.EventsOn) {
    const off = w.runtime.EventsOn(event, cb as Listener);
    return typeof off === 'function' ? off : () => w.runtime?.EventsOff?.(event);
  }
  return mock.subscribe(event, cb as Listener);
}

/** Wails returns Go errors as plain strings; normalise to a message. */
export function errText(e: unknown): string {
  if (typeof e === 'string') return e;
  if (e instanceof Error) return e.message;
  return String(e);
}

// ---------------------------------------------------------------------------
// Demo backend (browser dev + tests)

class MockBackend {
  private listeners = new Map<string, Set<Listener>>();
  state: AppState = {
    version: 'dev',
    configured: false,
    config: {
      server_url: '', username: '', device_name: '', device_id: 0, folder: '',
      start_on_login: true, up_kbps: 0, down_kbps: 0, poll_seconds: 30,
    },
    status: { state: 'idle', message: 'Not signed in', last_sync: '', progress: { done: 0, total: 0, path: '' }, conflicts: 0, failed: 0, skipped: [] },
    log_path: '',
  };
  events: SyncEvent[] = [];
  conflictList: ConflictRecord[] = [];

  subscribe(event: string, cb: Listener) {
    if (!this.listeners.has(event)) this.listeners.set(event, new Set());
    this.listeners.get(event)!.add(cb);
    return () => this.listeners.get(event)?.delete(cb);
  }
  emit(event: string, data: unknown) {
    this.listeners.get(event)?.forEach((cb) => cb(data));
  }
  setStatus(patch: Partial<Status>) {
    this.state.status = { ...this.state.status, ...patch };
    this.emit('status', this.state.status);
  }

  GetState() { return structuredClone(this.state); }
  Activity() { return [...this.events]; }
  Conflicts() { return [...this.conflictList]; }
  /** Set by tests to simulate an empty network. */
  discovered: DiscoveredServer[] = [
    { name: 'nas', url: 'http://192.168.1.20:8080', address: '192.168.1.20', version: 'v0.5.0', setup_done: true, addrs: ['192.168.1.20', 'fd00::20'] },
    { name: 'attic-pi', url: 'http://192.168.1.41:8080', address: '192.168.1.41', version: 'v0.5.0', setup_done: false, addrs: ['192.168.1.41'] },
  ];
  Discover() {
    return new Promise<DiscoveredServer[]>((r) => setTimeout(() => r(structuredClone(this.discovered)), 300));
  }
  TestServer(url: string, pin = '') {
    if (!url.trim()) throw 'enter the server address';
    const u = (url.includes('://') ? url : `http://${url}`).replace(/\/$/, '');
    const info: ServerInfo = { url: u, version: 'v0.5.0' };
    if (u.startsWith('https://') && u.includes('.lan')) {
      // Demo: a self-signed homelab certificate.
      info.cert = {
        fingerprint: '3F:A9:0C:5E:12:B7:44:D1:8A:6E:F0:29:C3:57:9B:E4:01:7D:AA:63:58:2F:CE:90:14:B8:6D:E2:47:31:9C:05',
        subject: 'CN=nas.lan', issuer: 'CN=nas.lan', not_after: '2027-10-14T00:00:00Z', self_signed: true,
        trusted: false, problem: 'x509: certificate signed by unknown authority',
      };
    } else if (u.startsWith('http://') && /\.(com|net|org|io|dev)(:|$)/.test(u)) {
      info.warning = 'This address is on the internet but the connection is not encrypted (http://). Your password, device token and files could be read or altered on the way. Use https:// (e.g. behind a reverse proxy) unless this is a VPN address.';
    }
    return info;
  }
  ParsePairLink(s: string) {
    if (!s.trim().toLowerCase().startsWith('nulldrive://')) return {};
    const u = new URL(s.trim().replace(/^nulldrive:/i, 'http:'));
    return { server: u.searchParams.get('server') ?? '', code: u.searchParams.get('code') ?? '' };
  }
  PairWithCode(req: PairRequest) {
    const norm = req.code.toUpperCase().replace(/[^0-9A-Z]/g, '').replace(/^NULL/, '');
    if (norm !== '7K2Q9F' && !req.code.startsWith('nulldrive://')) {
      throw 'that code is not valid or has expired. Create a new one in the web interface (Settings → Devices → Add device)';
    }
    this.state.configured = true;
    this.state.config = { ...this.state.config, server_url: req.server_url, username: 'admin', device_name: req.device_name, folder: req.folder };
    this.runDemoSync();
  }
  DefaultFolder() { return '/home/you/NullDrive'; }
  DefaultDeviceName() { return 'Desktop PC'; }
  ChooseFolder(current: string) { return current || '/home/you/NullDrive'; }
  Setup(req: SetupRequest) {
    if (req.password !== 'demo-password') throw 'wrong username or password';
    this.state.configured = true;
    this.state.config = { ...this.state.config, server_url: req.server_url, username: req.username, device_name: req.device_name, folder: req.folder };
    this.runDemoSync();
  }
  private runDemoSync() {
    const files = ['Documents/Taxes 2025/Return.pdf', 'Photos/Neon city.jpg', 'Projects/nulldrive/README.md', 'Music/Synthwave mix.flac', 'Packing list.md'];
    let i = 0;
    this.setStatus({
      state: 'syncing', message: 'Syncing…', live: true,
      progress: { done: 0, total: files.length, path: files[0]!, bytes: 412 << 20, bytes_total: 980 << 20 },
    });
    const tick = () => {
      if (i < files.length) {
        this.events.unshift({ time: new Date().toISOString(), kind: 'download', path: files[i]! });
        this.emit('activity', this.events[0]);
        i++;
        this.setStatus({ progress: { done: i, total: files.length, path: files[i] ?? '', bytes: (i * 197) << 20, bytes_total: 980 << 20 } });
        setTimeout(tick, 250);
      } else {
        // A sample conflict so the conflicts screen has something to show.
        const at = new Date().toISOString();
        this.conflictList = [{ id: 1, path: 'Packing list.md', conflict_path: 'Packing list (conflict - Desktop PC - 2026-10-14 2315).md', device: 'Desktop PC', at }];
        this.events.unshift({ time: at, kind: 'conflict', path: 'Packing list.md', from: this.conflictList[0]!.conflict_path });
        this.emit('activity', this.events[0]);
        this.setStatus({ state: 'idle', message: 'Up to date', last_sync: at, progress: { done: 0, total: 0, path: '' }, conflicts: 1 });
      }
    };
    setTimeout(tick, 250);
  }
  Pause() { this.setStatus({ state: 'paused', message: 'Paused' }); }
  Resume() { this.setStatus({ state: 'idle', message: 'Up to date' }); }
  SyncNow() { this.setStatus({ state: 'idle', message: 'Up to date', last_sync: new Date().toISOString() }); }
  ConfirmMassDelete() { this.setStatus({ state: 'idle', message: 'Up to date', mass_delete: undefined }); }
  OpenFolder() {}
  RevealFile() {}
  opened: string[] = [];
  OpenHistory(p: string) { this.opened.push(`history:${p}`); }
  OpenWebApp(page: string) { this.opened.push(`web:${page}`); }
  OpenLogs() {}
  ResolveConflict(id: number) {
    this.conflictList = this.conflictList.filter((c) => c.id !== id);
    this.setStatus({ conflicts: this.conflictList.length });
  }
  SaveSettings(s: Settings) { this.state.config = { ...this.state.config, ...s }; }
  ChangeFolder(folder: string) { this.state.config.folder = folder; }
  Unlink() { this.state.configured = false; }
  /** Mock: "correct horse battery" (or any recovery key) unlocks. */
  UnlockE2EE(secret: string, recovery: boolean) {
    if (!recovery && secret !== 'correct horse battery') throw new Error("that passphrase doesn't match");
    this.state.e2ee = 'unlocked';
    this.setStatus({ state: 'idle', message: 'Up to date' });
  }
  RetrySync() {}
}

export const mock = new MockBackend();
