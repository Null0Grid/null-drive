// @vitest-environment node
// The Drive against an in-memory fake of the server API: what reaches the
// "server" for an encrypted account must be ciphertext only, and it must
// come back intact.
import { describe, expect, it } from 'vitest';
import { Drive } from '../drive';
import { Keys, isEncryptedPath, CHUNK_OVERHEAD } from '../crypto/keys';
import type { NullDriveClient } from '../api/client';
import type { ChunkRef, CommitRequest, FileEntry, Manifest } from '../api/types';
import { loadSodium } from '../crypto/sodium';
import { toHex } from '../crypto/bytes';

class FakeServer {
  chunks = new Map<string, Uint8Array>();
  files = new Map<string, { entry: FileEntry; manifest: Manifest }>();
  puts = 0;
  version = 0;
  async checkChunks(hashes: string[]) {
    return hashes.filter((h) => !this.chunks.has(h));
  }
  async putChunk(hash: string, data: Uint8Array): Promise<ChunkRef> {
    const s = await loadSodium();
    if (toHex(s.crypto_hash_sha256(data)) !== hash) throw new Error('hash mismatch');
    this.chunks.set(hash, data.slice());
    this.puts++;
    return { hash, size: data.length };
  }
  async getChunk(hash: string) {
    const c = this.chunks.get(hash);
    if (!c) throw new Error('missing');
    return c.slice();
  }
  async commit(path: string, m: CommitRequest): Promise<FileEntry> {
    const stored = m.chunks.reduce((n, c) => n + c.size, 0);
    if (stored - m.chunks.length * CHUNK_OVERHEAD !== m.size) throw new Error('size mismatch');
    const entry: FileEntry = { path, is_dir: false, size: m.size, sha256: m.sha256, version: ++this.version, modified_at: '', deleted: false };
    this.files.set(path, { entry, manifest: { path, version: entry.version, size: m.size, sha256: m.sha256, chunks: m.chunks } });
    return entry;
  }
  async manifest(path: string) {
    return this.files.get(path)!.manifest;
  }
  async listFiles() {
    return { version: this.version, files: [...this.files.values()].map((f) => f.entry) };
  }
}

function entry(path: string, version: number): { entry: FileEntry; manifest: Manifest } {
  return {
    entry: { path, is_dir: false, size: 1, sha256: '', version, modified_at: '', deleted: false },
    manifest: { path, version, size: 0, sha256: '', chunks: [] },
  };
}

async function blobText(b: Blob) {
  return new TextDecoder().decode(new Uint8Array(await b.arrayBuffer()));
}

describe('Drive (encrypted)', () => {
  it('uploads ciphertext under encrypted names and downloads the plaintext back', async () => {
    const server = new FakeServer();
    const drive = new Drive(server as unknown as NullDriveClient);
    drive.keys = await Keys.fromMaster(new Uint8Array(32).fill(9));
    const secret = 'SECRET-MARKER '.repeat(750_000); // ~10 MB: over the 8 MiB maximum, so 2+ chunks
    const f = await drive.upload('Taxes/2026 return.pdf', new Blob([secret]));
    expect(f.path).toBe('Taxes/2026 return.pdf'); // the caller sees plaintext
    const [stored] = server.files.keys();
    expect(isEncryptedPath(stored!)).toBe(true);
    expect(stored).not.toContain('Taxes');
    for (const c of server.chunks.values()) expect(new TextDecoder().decode(c)).not.toContain('SECRET-MARKER');
    expect(server.chunks.size).toBeGreaterThan(1);

    const list = await drive.listFiles();
    expect(list.files.map((x) => x.path)).toEqual(['Taxes/2026 return.pdf']);
    const back = await drive.fetchDecrypted(drive.keys, 'Taxes/2026 return.pdf');
    expect(await blobText(back)).toBe(secret);

    // Same content again: dedup, nothing re-uploaded.
    const before = server.puts;
    await drive.upload('Copy.pdf', new Blob([secret]));
    expect(server.puts).toBe(before);
  });

  it('detects a chunk modified on the server', async () => {
    const server = new FakeServer();
    const drive = new Drive(server as unknown as NullDriveClient);
    drive.keys = await Keys.fromMaster(new Uint8Array(32).fill(3));
    await drive.upload('a.txt', new Blob(['hello']));
    const [h] = server.chunks.keys();
    const c = server.chunks.get(h!)!;
    c[30] = c[30]! ^ 1;
    await expect(drive.fetchDecrypted(drive.keys, 'a.txt')).rejects.toThrow(/corrupt|modified/);
  });

  it('hides entries it cannot decrypt', async () => {
    const server = new FakeServer();
    const drive = new Drive(server as unknown as NullDriveClient);
    drive.keys = await Keys.fromMaster(new Uint8Array(32).fill(1));
    await drive.upload('mine.txt', new Blob(['x']));
    server.files.set('plaintext-leftover.txt', entry('plaintext-leftover.txt', 99));
    const other = await Keys.fromMaster(new Uint8Array(32).fill(2));
    const foreign = other.encryptPath('someone-else.txt');
    server.files.set(foreign, entry(foreign, 100));
    expect((await drive.listFiles()).files.map((x) => x.path)).toEqual(['mine.txt']);
  });

  it('passes straight through for plain accounts', async () => {
    const server = new FakeServer();
    const drive = new Drive(server as unknown as NullDriveClient);
    server.files.set('plain.txt', entry('plain.txt', 1));
    expect((await drive.listFiles()).files.map((x) => x.path)).toEqual(['plain.txt']);
    expect(drive.encrypted).toBe(false);
  });
});
