// TypeScript mirror of shared/api/types.go (the v1 wire format).
// Keep the two in sync; field names are the JSON names.

export type E2EEState = 'off' | 'migrating' | 'on';

export interface User {
  id: number;
  username: string;
  is_admin: boolean;
  created_at: string;
  /** Two-factor authentication is on (Phase 7). */
  totp_enabled?: boolean;
  /** End-to-end encryption state (Phase 7). */
  e2ee?: E2EEState;
}

export interface SessionResponse {
  token: string;
  expires_at: string;
  user: User;
  /** Sent back as X-NullDrive-CSRF on state-changing requests. */
  csrf_token?: string;
}

export type TokenScope = 'full' | 'search';

export interface Device {
  id: number;
  name: string;
  created_at: string;
  last_seen_at?: string;
  revoked_at?: string;
  last_ip?: string;
  /** e.g. "NullDrive/0.5.0 (windows/amd64)" */
  client_version?: string;
  added_via?: 'password' | 'pairing';
  /** "search": a token that can only call GET /search (Null Search). */
  scope?: TokenScope;
  /** Holds a realtime connection right now. */
  online?: boolean;
}

export type PairingStatus = 'pending' | 'claimed' | 'expired' | 'cancelled';

export interface PairingResponse {
  id: number;
  /** Only present in the response that created the code. */
  code?: string;
  status: PairingStatus;
  expires_at: string;
  device?: Device;
}

export interface ServerSettings {
  retention_full_days: number;
  retention_daily_days: number;
  /** Deleted files stay in Recently deleted this many days (default 30). */
  trash_days: number;
  server_name: string;
  mdns_enabled: boolean;
  mdns_forced_off: boolean;
  mdns_active: boolean;
  mdns_addrs?: string[];
  mdns_error?: string;
}

export interface RealtimeMessage {
  type: 'hello' | 'changed' | 'presence';
  version?: number;
  device_id?: number;
  online?: boolean;
}

export interface CreateDeviceResponse {
  device: Device;
  token: string;
}

export interface MeResponse {
  user: User;
  device?: Device;
  csrf_token?: string;
}

export interface FileEntry {
  /** Stable id (survives renames and moves): search results, nulldrive:// links. */
  id?: string;
  path: string;
  is_dir: boolean;
  size: number;
  sha256: string;
  version: number;
  modified_at: string;
  deleted: boolean;
  deleted_at?: string;
  /** The uploading device's modification time, if it sent one. */
  mtime?: string;
  /** Null Notes note id from a Markdown file's frontmatter. */
  synced_id?: string;
  /** On a move's tombstone (change feed): where the file went. */
  moved_to?: string;
}

export interface FileList {
  version: number;
  files: FileEntry[];
}

export interface MoveResponse {
  version: number;
  moved: { from: string; file: FileEntry }[];
}

export interface DeleteResponse {
  version: number;
  deleted: FileEntry[];
}

export interface SystemInfo {
  version: string;
  data_dir: string;
  disk_total_bytes: number;
  disk_free_bytes: number;
  disk_error?: string;
}

export interface HealthResponse {
  status: string;
  version: string;
  commit: string;
}

export interface ErrorBody {
  error: { code: string; message: string; current?: FileEntry };
}

// ---- v2: versions, trash, stats (Phase 6) ----

export interface FileVersion {
  version: number;
  size: number;
  sha256: string;
  created_at: string;
  /** Device name; absent for the web app / API sessions. */
  device?: string;
  device_id?: number;
  origin: 'upload' | 'restore' | 'migrated';
  chunks: number;
  current: boolean;
}

export interface VersionList {
  path: string;
  deleted: boolean;
  versions: FileVersion[];
}

export interface TrashItem {
  path: string;
  size: number;
  deleted_at: string;
  version: number;
  versions: number;
  purge_at: string;
}

export interface Stats {
  files: number;
  logical_bytes: number;
  history_bytes: number;
  versions: number;
  stored_bytes: number;
  trash_files: number;
  server_stored_bytes?: number;
  server_logical_bytes?: number;
  server_chunks?: number;
  server_reclaimable_bytes?: number;
  retention_full_days: number;
  retention_daily_days: number;
  trash_days: number;
}

export interface GCReport {
  versions_pruned: number;
  uploads_expired: number;
  chunks_deleted: number;
  orphans_deleted: number;
  bytes_freed: number;
  duration_ms: number;
}

// ---- v2 chunks + manifests (Phase 6), used by in-browser encryption ----

export interface ChunkRef {
  hash: string;
  size: number;
}

export interface Manifest {
  path: string;
  version: number;
  size: number;
  sha256: string;
  chunks: ChunkRef[];
  /** Stable file id (current version only). */
  id?: string;
  mtime?: string;
  /** Never set on encrypted accounts. */
  synced_id?: string;
}

export interface CommitRequest {
  size: number;
  sha256: string;
  chunks: ChunkRef[];
}

// ---- end-to-end encryption (Phase 7) ----

/** Client-produced key bundle (shared/e2ee.Bundle). Byte fields are base64. */
export interface KeyBundle {
  version: number;
  kdf: {
    alg: string;
    time: number;
    memory_kib: number;
    threads: number;
    salt: string;
  };
  passphrase_wrapped: string;
  recovery_wrapped: string;
  recovery_salt: string;
  fingerprint: string;
}

export interface E2EEStatus {
  state: E2EEState;
  bundle?: KeyBundle;
  fingerprint?: string;
  enabled_at?: string;
  plaintext_files: number;
}

export interface E2EEFinishResponse {
  state: E2EEState;
  purged_paths: number;
  chunks_deleted: number;
  bytes_freed: number;
}

// ---- two-factor authentication + audit log (Phase 7) ----

export interface TOTPSetupResponse {
  secret: string;
  uri: string;
}

export interface AuditEvent {
  id: number;
  at: string;
  event: string;
  username?: string;
  device_id?: number;
  ip?: string;
  detail?: Record<string, string>;
}

export interface AuditList {
  events: AuditEvent[];
  next?: number;
}
