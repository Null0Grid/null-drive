// Mirrors of the Go structs returned by the bound App methods
// (client/app.go and shared/sync). JSON field names are the Go json tags.

export type SyncState = 'starting' | 'syncing' | 'idle' | 'paused' | 'offline' | 'error' | 'attention';

export interface Skipped {
  path: string;
  reason: string;
}

export interface Status {
  state: SyncState;
  message: string;
  last_sync: string;
  progress: { done: number; total: number; path: string; bytes?: number; bytes_total?: number };
  conflicts: number;
  failed: number;
  skipped: Skipped[] | null;
  mass_delete?: { Side: string; Count: number; Total: number };
  /** True while the realtime push channel (WebSocket) is connected. */
  live?: boolean;
}

export interface SyncEvent {
  time: string;
  kind: string;
  path: string;
  from?: string;
  error?: string;
}

export interface ConflictRecord {
  id: number;
  path: string;
  conflict_path: string;
  device: string;
  at: string;
}

export interface Config {
  server_url: string;
  username: string;
  device_name: string;
  device_id: number;
  folder: string;
  start_on_login: boolean;
  up_kbps: number;
  down_kbps: number;
  poll_seconds: number;
  cert_sha256?: string;
  e2ee_fingerprint?: string;
}

export interface AppState {
  version: string;
  configured: boolean;
  config: Config;
  status: Status;
  log_path: string;
  /** End-to-end encryption: off, unlocked, locked (needs the passphrase) or migrating. */
  e2ee?: 'off' | 'unlocked' | 'locked' | 'migrating';
  /** The Microsoft Store (MSIX) build: Windows manages starting at sign-in. */
  packaged?: boolean;
}

export interface CertInfo {
  fingerprint: string;
  subject: string;
  issuer: string;
  not_after: string;
  self_signed: boolean;
  trusted: boolean;
  problem?: string;
}

export interface ServerInfo {
  url: string;
  version: string;
  /** Plain HTTP to a non-LAN address. */
  warning?: string;
  /** HTTPS certificate; when !trusted the user must confirm the fingerprint. */
  cert?: CertInfo;
}

export interface DiscoveredServer {
  name: string;
  url: string;
  address: string;
  version: string;
  setup_done: boolean;
  addrs: string[];
}

export interface SetupRequest {
  server_url: string;
  username: string;
  password: string;
  device_name: string;
  folder: string;
  cert_sha256: string;
}

export interface PairRequest {
  server_url: string;
  /** A code like NULL-7K2Q-9F, or a full nulldrive://pair link. */
  code: string;
  device_name: string;
  folder: string;
  cert_sha256: string;
}

export interface Settings {
  start_on_login: boolean;
  up_kbps: number;
  down_kbps: number;
  poll_seconds: number;
}

export type Resolution = 'keep-both' | 'keep-server' | 'keep-mine';
