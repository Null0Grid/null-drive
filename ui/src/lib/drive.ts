// Drive: the file operations the UI performs, independent of whether the
// account is end-to-end encrypted. Plain accounts go straight to the API.
// Encrypted ones (once unlocked) encrypt names and contents here, in the
// browser, before anything is sent, and decrypt what comes back: the
// TypeScript counterpart of shared/e2ee's Remote. Pages call the same
// methods either way.

import { ApiError, type NullDriveClient, type UploadOptions } from './api/client';
import type { ChunkRef, DeleteResponse, E2EEFinishResponse, FileEntry, FileList, MoveResponse, TrashItem, VersionList } from './api/types';
import { toHex } from './crypto/bytes';
import { blobReader, chunks } from './crypto/fastcdc';
import { CorruptError, isEncryptedPath, type Keys } from './crypto/keys';
import { loadSodium } from './crypto/sodium';

/** Chunk requests in flight per transfer (same as the desktop client). */
const PARALLEL = 4;
/** Sealed bytes collected before asking the server which ones it lacks. */
const BATCH_BYTES = 16 << 20;

async function parallel(n: number, tasks: (() => Promise<void>)[]): Promise<void> {
  let next = 0;
  let failed: unknown = null;
  const worker = async () => {
    while (next < tasks.length && failed === null) {
      const t = tasks[next++]!;
      try {
        await t();
      } catch (e) {
        failed ??= e;
      }
    }
  };
  await Promise.all(Array.from({ length: Math.min(n, tasks.length) }, worker));
  if (failed !== null) throw failed;
}

export function saveBlob(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  a.rel = 'noopener';
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 60_000);
}

export interface MigrateProgress {
  done: number;
  total: number;
  path: string;
}

export class Drive {
  /** Set once the account's key is unlocked; null for plain accounts. */
  keys: Keys | null = null;

  constructor(readonly client: NullDriveClient) {}

  get encrypted(): boolean {
    return this.keys !== null;
  }

  private enc(p: string): string {
    return this.keys ? this.keys.encryptPath(p) : p;
  }

  /** Decrypts an entry's path; null if it can't be (plaintext leftover, other key). */
  private dec<T extends { path: string }>(e: T): T | null {
    if (!this.keys) return e;
    if (!isEncryptedPath(e.path)) return null;
    try {
      return { ...e, path: this.keys.decryptPath(e.path) };
    } catch {
      return null;
    }
  }

  private decAll<T extends { path: string }>(xs: T[]): T[] {
    return xs.map((x) => this.dec(x)).filter((x): x is T => x !== null);
  }

  async listFiles(): Promise<FileList> {
    const l = await this.client.listFiles();
    return this.keys ? { ...l, files: this.decAll(l.files) } : l;
  }

  /** The file with this stable id, path decrypted; null if this key can't read its name. */
  async fileById(id: string): Promise<FileEntry | null> {
    return this.dec(await this.client.fileById(id));
  }

  async createFolder(path: string): Promise<FileEntry> {
    const f = await this.client.createFolder(this.enc(path));
    return this.dec(f) ?? f;
  }

  async move(from: string, to: string, ifMatch?: number): Promise<MoveResponse> {
    const r = await this.client.move(this.enc(from), this.enc(to), ifMatch);
    if (!this.keys) return r;
    return {
      ...r,
      moved: r.moved.flatMap((m) => {
        const file = this.dec(m.file);
        const src = this.dec({ path: m.from });
        return file && src ? [{ from: src.path, file }] : [];
      }),
    };
  }

  async remove(path: string, ifMatch?: number): Promise<DeleteResponse> {
    const r = await this.client.remove(this.enc(path), ifMatch);
    return this.keys ? { ...r, deleted: this.decAll(r.deleted) } : r;
  }

  async listVersions(path: string): Promise<VersionList> {
    const l = await this.client.listVersions(this.enc(path));
    return this.keys ? { ...l, path } : l;
  }

  async restoreVersion(path: string, version: number): Promise<FileEntry> {
    const f = await this.client.restoreVersion(this.enc(path), version);
    return this.dec(f) ?? f;
  }

  async trash(): Promise<TrashItem[]> {
    const items = await this.client.trash();
    return this.keys ? this.decAll(items) : items;
  }

  /** Thumbnails load straight from the server, which only works for plain files. */
  thumbnailUrl(path: string): string | undefined {
    return this.keys ? undefined : this.client.downloadUrl(path);
  }

  // ---- content ----

  upload(path: string, file: Blob, opts: UploadOptions = {}): Promise<FileEntry> {
    if (!this.keys) return this.client.upload(path, file, opts);
    return this.uploadEncrypted(this.keys, path, file, opts);
  }

  /**
   * Chunk (FastCDC, same boundaries as the desktop app), seal each chunk,
   * upload the ones the server lacks in batches, then commit the manifest.
   * Plaintext never leaves this function; memory stays bounded by the
   * batch size whatever the file size.
   */
  private async uploadEncrypted(k: Keys, path: string, file: Blob, opts: UploadOptions): Promise<FileEntry> {
    const total = file.size;
    const refs: ChunkRef[] = [];
    const sha = k.sha256();
    let batch: { id: string; sealed: Uint8Array }[] = [];
    let batchBytes = 0;
    let read = 0;
    const flush = async () => {
      if (batch.length === 0) return;
      const missing = new Set(await this.client.checkChunks([...new Set(batch.map((b) => b.id))]));
      const send = batch.filter((b) => missing.delete(b.id));
      await parallel(
        PARALLEL,
        send.map((b) => async () => {
          await this.client.putChunk(b.id, b.sealed, opts.signal);
        }),
      );
      batch = [];
      batchBytes = 0;
    };
    for await (const ch of chunks(blobReader(file), total)) {
      if (opts.signal?.aborted) throw new ApiError(0, 'aborted', 'Upload cancelled.');
      sha.update(ch.data);
      const sealed = k.sealChunk(ch.data);
      const id = k.chunkId(sealed);
      refs.push({ hash: id, size: sealed.length });
      batch.push({ id, sealed });
      batchBytes += sealed.length;
      read += ch.data.length;
      if (batchBytes >= BATCH_BYTES) await flush();
      opts.onProgress?.(read, total);
    }
    await flush();
    const f = await this.client.commit(this.enc(path), { size: total, sha256: k.fileHash(sha.digest()), chunks: refs }, opts.ifMatch);
    opts.onProgress?.(total, total);
    return this.dec(f) ?? f;
  }

  /** Downloads a file (or one version of it) and saves it in the browser. */
  async download(path: string, name: string, version?: number): Promise<void> {
    if (!this.keys) {
      const a = document.createElement('a');
      a.href = version ? this.client.versionUrl(path, version) : this.client.downloadUrl(path);
      a.download = name;
      a.rel = 'noopener';
      document.body.append(a);
      a.click();
      a.remove();
      return;
    }
    saveBlob(await this.fetchDecrypted(this.keys, path, version), name);
  }

  /** Fetch every chunk, check its ID, decrypt, then check the whole file. */
  async fetchDecrypted(k: Keys, path: string, version?: number): Promise<Blob> {
    const m = await this.client.manifest(this.enc(path), version);
    const parts: Uint8Array[] = new Array<Uint8Array>(m.chunks.length);
    await parallel(
      PARALLEL,
      m.chunks.map((c, i) => async () => {
        const blob = await this.client.getChunk(c.hash);
        if (blob.length !== c.size || k.chunkId(blob) !== c.hash) throw new CorruptError('chunk');
        parts[i] = k.openChunk(blob);
      }),
    );
    const sha = k.sha256();
    let size = 0;
    for (const p of parts) {
      sha.update(p);
      size += p.length;
    }
    if (size !== m.size || k.fileHash(sha.digest()) !== m.sha256) throw new CorruptError('file');
    return new Blob(parts as BlobPart[]);
  }

  /** Plaintext download through the chunk API (migration: v1 downloads are refused once encryption is on). */
  private async fetchPlain(path: string): Promise<Blob> {
    const s = await loadSodium();
    const m = await this.client.manifest(path);
    const parts: Uint8Array[] = new Array<Uint8Array>(m.chunks.length);
    await parallel(
      PARALLEL,
      m.chunks.map((c, i) => async () => {
        const b = await this.client.getChunk(c.hash);
        if (toHex(s.crypto_hash_sha256(b)) !== c.hash) throw new CorruptError('chunk');
        parts[i] = b;
      }),
    );
    return new Blob(parts as BlobPart[]);
  }

  /**
   * Encrypt an account's existing files (state "migrating"): copy each
   * plaintext file and folder to its encrypted name, then ask the server to
   * delete all plaintext. Resumable: already-copied files are skipped. Never
   * calls finish if anything couldn't be copied.
   */
  async migrate(onProgress?: (p: MigrateProgress) => void): Promise<E2EEFinishResponse> {
    const k = this.keys;
    if (!k) throw new Error('unlock first');
    const list = await this.client.listFiles();
    const existing = new Map<string, FileEntry>();
    const todo: FileEntry[] = [];
    for (const f of list.files) {
      if (f.deleted) continue;
      if (isEncryptedPath(f.path)) existing.set(f.path, f);
      else todo.push(f);
    }
    todo.sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0));
    const tooLong: string[] = [];
    for (let i = 0; i < todo.length; i++) {
      const f = todo[i]!;
      onProgress?.({ done: i, total: todo.length, path: f.path });
      let ep: string;
      try {
        ep = k.encryptPath(f.path);
      } catch {
        tooLong.push(f.path);
        continue;
      }
      if (f.is_dir) {
        if (!existing.has(ep)) await this.client.createFolder(ep);
        continue;
      }
      const cur = existing.get(ep);
      const plain = await this.fetchPlain(f.path);
      await this.uploadEncrypted(k, f.path, plain, { ifMatch: cur?.version });
    }
    onProgress?.({ done: todo.length, total: todo.length, path: '' });
    if (tooLong.length) throw new Error(`These names are too long to encrypt; rename them and try again: ${tooLong.join(', ')}`);
    return this.client.e2eeFinish(k.authKey);
  }
}
