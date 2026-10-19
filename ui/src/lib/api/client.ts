import type {
  AuditList,
  ChunkRef,
  CommitRequest,
  CreateDeviceResponse,
  DeleteResponse,
  Device,
  E2EEFinishResponse,
  E2EEStatus,
  ErrorBody,
  FileEntry,
  FileList,
  GCReport,
  HealthResponse,
  KeyBundle,
  Manifest,
  MeResponse,
  MoveResponse,
  PairingResponse,
  ServerSettings,
  SessionResponse,
  Stats,
  SystemInfo,
  TOTPSetupResponse,
  TokenScope,
  TrashItem,
  VersionList,
} from './types';

/** A non-2xx API response. `code` is the stable machine-readable error code. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly current?: FileEntry,
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

export interface ClientOptions {
  /** Server origin, e.g. "http://nas:8080". Empty = same origin (web UI). */
  baseUrl?: string;
  /**
   * Bearer token. The web UI leaves this unset and relies on the HttpOnly
   * session cookie; the desktop client (Phase 4) passes its device token.
   */
  token?: string;
  /** Called on any 401 so the app can send the user to the login screen. */
  onUnauthorized?: () => void;
}

export interface UploadOptions {
  /** Expected current version; 0 = create only. Omit for new files. */
  ifMatch?: number;
  onProgress?: (loaded: number, total: number) => void;
  signal?: AbortSignal;
}

/** Percent-encode each path segment (keeps the '/' separators). */
export function encodePath(path: string): string {
  return path.split('/').map(encodeURIComponent).join('/');
}

/**
 * Typed client for the Null Drive v1 API. It has no dependency on the
 * page, the router or Svelte, so the desktop client can reuse it as is.
 */
export class NullDriveClient {
  private baseUrl: string;
  private token?: string;
  private onUnauthorized?: () => void;
  private csrf?: string;

  constructor(opts: ClientOptions = {}) {
    this.baseUrl = (opts.baseUrl ?? '').replace(/\/$/, '');
    this.token = opts.token;
    this.onUnauthorized = opts.onUnauthorized;
  }

  /**
   * CSRF token from login/setup/me. The session cookie is HttpOnly, so the
   * page can't read it; the server hands this token over instead, and
   * state-changing requests must echo it in X-NullDrive-CSRF.
   */
  setCsrf(token: string | undefined) {
    this.csrf = token;
  }

  url(path: string, api: 'v1' | 'v2' = 'v1'): string {
    return `${this.baseUrl}/api/${api}${path}`;
  }

  private headers(extra: Record<string, string> = {}, method = 'GET'): Record<string, string> {
    // This UI understands end-to-end encrypted accounts (it decrypts in
    // the browser after the unlock screen), so it always says so.
    const h: Record<string, string> = { 'X-NullDrive-E2EE': '1', ...extra };
    if (this.token) h.Authorization = `Bearer ${this.token}`;
    if (this.csrf && method !== 'GET' && method !== 'HEAD') h['X-NullDrive-CSRF'] = this.csrf;
    return h;
  }

  private async request<T>(
    method: string,
    path: string,
    body?: unknown,
    headers: Record<string, string> = {},
    api: 'v1' | 'v2' = 'v1',
  ): Promise<T> {
    let res: Response;
    try {
      res = await fetch(this.url(path, api), {
        method,
        headers: this.headers(
          body === undefined ? headers : { 'Content-Type': 'application/json', ...headers },
          method,
        ),
        body: body === undefined ? undefined : JSON.stringify(body),
        credentials: 'same-origin',
      });
    } catch {
      throw new ApiError(0, 'network', 'Cannot reach the server. Check your connection.');
    }
    if (res.status === 204) return undefined as T;
    const text = await res.text();
    let data: unknown = undefined;
    try {
      data = text ? JSON.parse(text) : undefined;
    } catch {
      /* non-JSON body: handled below */
    }
    if (!res.ok) throw this.toError(res.status, data, text);
    return data as T;
  }

  private toError(status: number, data: unknown, text: string): ApiError {
    if (status === 401) this.onUnauthorized?.();
    const e = (data as ErrorBody | undefined)?.error;
    if (e) return new ApiError(status, e.code, e.message, e.current);
    return new ApiError(status, 'http_' + status, text.slice(0, 200) || `Request failed (${status})`);
  }

  // ---- system & setup ----
  health = () => this.request<HealthResponse>('GET', '/health');
  setupStatus = () => this.request<{ setup_complete: boolean }>('GET', '/setup/status');
  setup = (username: string, password: string) =>
    this.request<SessionResponse>('POST', '/setup', {
      username,
      password,
    }).then((r) => this.withCsrf(r));
  systemInfo = () => this.request<SystemInfo>('GET', '/system/info');

  // ---- auth ----
  /** totp: authenticator or recovery code, when the server asked for one (code "totp_required"). */
  login = (username: string, password: string, totp?: string) =>
    this.request<SessionResponse>(
      'POST',
      '/auth/login',
      totp ? { username, password, totp } : { username, password },
    ).then((r) => this.withCsrf(r));
  logout = () => this.request<void>('POST', '/auth/logout').finally(() => this.setCsrf(undefined));
  me = () => this.request<MeResponse>('GET', '/me').then((r) => this.withCsrf(r));
  private withCsrf<T extends { csrf_token?: string }>(r: T): T {
    if (r.csrf_token) this.setCsrf(r.csrf_token);
    return r;
  }

  // ---- two-factor authentication ----
  totpSetup = () => this.request<TOTPSetupResponse>('POST', '/auth/totp/setup', {});
  totpEnable = (code: string) =>
    this.request<{ recovery_codes: string[] }>('POST', '/auth/totp/enable', {
      code,
    });
  totpDisable = (password: string, code: string) =>
    this.request<void>('POST', '/auth/totp/disable', { password, code });

  // ---- audit log (admin) ----
  audit = (before?: number, limit = 50) =>
    this.request<AuditList>('GET', `/admin/audit?limit=${limit}${before ? `&before=${before}` : ''}`);

  // ---- end-to-end encryption (the bundle is opaque here; lib/crypto opens it) ----
  e2eeStatus = () => this.request<E2EEStatus>('GET', '/e2ee', undefined, {}, 'v2');
  e2eeEnable = (bundle: KeyBundle, auth_key: string) =>
    this.request<E2EEStatus>('POST', '/e2ee', { bundle, fingerprint: bundle.fingerprint, auth_key }, {}, 'v2');
  e2eeUpdateBundle = (bundle: KeyBundle, auth_key: string) =>
    this.request<E2EEStatus>('PUT', '/e2ee/bundle', { bundle, fingerprint: bundle.fingerprint, auth_key }, {}, 'v2');
  e2eeFinish = (auth_key: string) => this.request<E2EEFinishResponse>('POST', '/e2ee/finish', { auth_key }, {}, 'v2');

  // ---- v2 chunks + manifests ----
  checkChunks = (hashes: string[]) =>
    this.request<{ missing: string[] }>('POST', '/chunks/check', { hashes }, {}, 'v2').then((r) => r.missing);
  manifest = (path: string, version?: number) =>
    this.request<Manifest>(
      'GET',
      version ? `/files/${encodePath(path)}/versions/${version}/manifest` : `/files/${encodePath(path)}/manifest`,
      undefined,
      {},
      'v2',
    );
  commit = (path: string, m: CommitRequest, ifMatch?: number) =>
    this.request<FileEntry>(
      'PUT',
      `/files/${encodePath(path)}`,
      m,
      ifMatch === undefined ? {} : { 'If-Match': `"${ifMatch}"` },
      'v2',
    );

  async putChunk(hash: string, data: Uint8Array, signal?: AbortSignal): Promise<ChunkRef> {
    const res = await this.raw('PUT', this.url(`/chunks/${hash}`, 'v2'), data, signal, {
      'Content-Type': 'application/octet-stream',
    });
    return (await res.json()) as ChunkRef;
  }

  async getChunk(hash: string, signal?: AbortSignal): Promise<Uint8Array> {
    const res = await this.raw('GET', this.url(`/chunks/${hash}`, 'v2'), undefined, signal);
    return new Uint8Array(await res.arrayBuffer());
  }

  private async raw(
    method: string,
    url: string,
    body: Uint8Array | undefined,
    signal?: AbortSignal,
    extra: Record<string, string> = {},
  ) {
    let res: Response;
    try {
      res = await fetch(url, {
        method,
        headers: this.headers(extra, method),
        body: body as BodyInit | undefined,
        credentials: 'same-origin',
        signal,
      });
    } catch {
      if (signal?.aborted) throw new ApiError(0, 'aborted', 'Cancelled.');
      throw new ApiError(0, 'network', 'Cannot reach the server. Check your connection.');
    }
    if (!res.ok) {
      const text = await res.text();
      let data: unknown;
      try {
        data = JSON.parse(text);
      } catch {
        data = undefined;
      }
      throw this.toError(res.status, data, text);
    }
    return res;
  }
  changePassword = (current_password: string, new_password: string) =>
    this.request<void>('POST', '/auth/password', {
      current_password,
      new_password,
    });

  // ---- devices ----
  listDevices = () => this.request<{ devices: Device[] }>('GET', '/devices').then((r) => r.devices);
  createDevice = (name: string, scope: TokenScope = 'full') =>
    this.request<CreateDeviceResponse>('POST', '/devices', scope === 'full' ? { name } : { name, scope });
  revokeDevice = (id: number) => this.request<Device>('DELETE', `/devices/${id}`);

  // ---- pairing ----
  createPairing = () => this.request<PairingResponse>('POST', '/pairing', {});
  getPairing = (id: number) => this.request<PairingResponse>('GET', `/pairing/${id}`);
  cancelPairing = (id: number) => this.request<PairingResponse>('DELETE', `/pairing/${id}`);

  // ---- server settings ----
  getSettings = () => this.request<ServerSettings>('GET', '/system/settings');
  updateSettings = (
    patch: Partial<
      Pick<
        ServerSettings,
        'server_name' | 'mdns_enabled' | 'retention_full_days' | 'retention_daily_days' | 'trash_days'
      >
    >,
  ) => this.request<ServerSettings>('PATCH', '/system/settings', patch);

  // ---- v2: history, trash, stats ----
  listVersions = (path: string) =>
    this.request<VersionList>('GET', `/files/${encodePath(path)}/versions`, undefined, {}, 'v2');
  /** URL of one version's content (a plain link downloads it with the session cookie). */
  versionUrl = (path: string, version: number) =>
    this.url(`/files/${encodePath(path)}/versions/${version}/content`, 'v2');
  restoreVersion = (path: string, version: number) =>
    this.request<FileEntry>('POST', `/files/${encodePath(path)}/versions/${version}/restore`, undefined, {}, 'v2');
  /** Resolves a stable file id (nulldrive://open?id=) to the file, deleted ones included. */
  fileById = (id: string) => this.request<FileEntry>('GET', `/ids/${encodeURIComponent(id)}`, undefined, {}, 'v2');
  trash = () => this.request<{ items: TrashItem[] }>('GET', '/trash', undefined, {}, 'v2').then((r) => r.items);
  stats = () => this.request<Stats>('GET', '/stats', undefined, {}, 'v2');
  runGC = () => this.request<GCReport>('POST', '/admin/gc', {}, {}, 'v2');

  // ---- files ----
  listFiles = (since?: number) =>
    this.request<FileList>('GET', since === undefined ? '/files' : `/files?since=${since}`);
  createFolder = (path: string) => this.request<FileEntry>('POST', '/folders', { path });
  move = (from: string, to: string, ifMatch?: number) =>
    this.request<MoveResponse>(
      'POST',
      '/files/move',
      ifMatch === undefined ? { from, to } : { from, to, if_match: ifMatch },
    );
  remove = (path: string, ifMatch?: number) =>
    this.request<DeleteResponse>(
      'DELETE',
      `/files/${encodePath(path)}`,
      undefined,
      ifMatch === undefined ? {} : { 'If-Match': `"${ifMatch}"` },
    );

  /** URL that downloads a file (the session cookie authenticates it). */
  downloadUrl(path: string): string {
    return this.url(`/files/${encodePath(path)}`);
  }

  /**
   * Upload with progress. Uses XMLHttpRequest because fetch() still has no
   * upload progress events in browsers.
   */
  upload(path: string, file: Blob, opts: UploadOptions = {}): Promise<FileEntry> {
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      xhr.open('PUT', this.url(`/files/${encodePath(path)}`));
      for (const [k, v] of Object.entries(this.headers({}, 'PUT'))) xhr.setRequestHeader(k, v);
      if (opts.ifMatch !== undefined) xhr.setRequestHeader('If-Match', `"${opts.ifMatch}"`);
      xhr.setRequestHeader('Content-Type', 'application/octet-stream');
      xhr.upload.onprogress = (e) => opts.onProgress?.(e.loaded, e.lengthComputable ? e.total : file.size);
      xhr.onload = () => {
        let data: unknown;
        try {
          data = JSON.parse(xhr.responseText);
        } catch {
          data = undefined;
        }
        if (xhr.status >= 200 && xhr.status < 300) resolve(data as FileEntry);
        else reject(this.toError(xhr.status, data, xhr.responseText));
      };
      xhr.onerror = () => reject(new ApiError(0, 'network', 'Upload failed: connection lost.'));
      xhr.onabort = () => reject(new ApiError(0, 'aborted', 'Upload cancelled.'));
      if (opts.signal) {
        if (opts.signal.aborted) return xhr.abort();
        opts.signal.addEventListener('abort', () => xhr.abort(), {
          once: true,
        });
      }
      xhr.send(file);
    });
  }
}
