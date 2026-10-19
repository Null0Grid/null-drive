import { describe, expect, it, vi } from 'vitest';
import { parse, folderHref } from '../router.svelte';
import { UploadQueue } from '../uploads.svelte';
import { ApiError, encodePath, type NullDriveClient } from '../../lib/api/client';
import type { FileEntry } from '../../lib/api/types';

describe('router', () => {
  it.each([
    ['/', { name: 'files', folder: '' }],
    ['/files', { name: 'files', folder: '' }],
    ['/files/Photos/2026', { name: 'files', folder: 'Photos/2026' }],
    ['/files/My%20Docs/%C5%81%C3%B3d%C5%BA/', { name: 'files', folder: 'My Docs/Łódź' }],
    ['/settings', { name: 'settings' }],
    ['/setup', { name: 'setup' }],
    ['/nope', { name: 'notfound' }],
    ['/files/%E0%A4%A', { name: 'notfound' }],
  ])('%s', (path, want) => expect(parse(path)).toEqual(want));

  it('login only redirects within the app', () => {
    expect(parse('/login', '?next=%2Fsettings')).toEqual({ name: 'login', next: '/settings' });
    expect(parse('/login', '?next=https://evil.example')).toEqual({ name: 'login', next: '/files' });
    expect(parse('/login', '?next=//evil.example')).toEqual({ name: 'login', next: '/files' });
  });

  it('builds folder links that round-trip', () => {
    const href = folderHref('a b/c#d?e');
    expect(href).toBe('/files/a%20b/c%23d%3Fe');
    expect(parse(href)).toEqual({ name: 'files', folder: 'a b/c#d?e' });
    expect(encodePath('x/y z')).toBe('x/y%20z');
  });
});

describe('UploadQueue', () => {
  const entry = (path: string): FileEntry => ({
    path, is_dir: false, size: 1, sha256: 'h', version: 2, modified_at: '', deleted: false,
  });

  it('keeps both copies when the server reports a conflict', async () => {
    const upload = vi
      .fn()
      .mockRejectedValueOnce(new ApiError(409, 'conflict', 'changed'))
      .mockImplementation(async (path: string) => entry(path));
    const uploaded: string[] = [];
    const drained = new Promise<void>((resolve) => {
      const q = new UploadQueue(
        { upload } as unknown as NullDriveClient,
        () => new Set(['notes.txt', 'notes (1).txt']),
        (e) => uploaded.push(e.path),
        resolve,
      );
      q.add([{ file: new File(['hi'], 'notes.txt'), path: 'Docs/notes.txt', ifMatch: 5 }]);
    });
    await drained;
    expect(upload).toHaveBeenCalledTimes(2);
    expect(upload.mock.calls[0]![2]).toMatchObject({ ifMatch: 5 });
    expect(upload.mock.calls[1]![0]).toBe('Docs/notes (2).txt');
    expect(upload.mock.calls[1]![2]).toMatchObject({ ifMatch: 0 });
    expect(uploaded).toEqual(['Docs/notes (2).txt']);
  });

  it('reports other errors and allows retry', async () => {
    const upload = vi.fn().mockRejectedValueOnce(new ApiError(0, 'network', 'connection lost')).mockResolvedValue(entry('a'));
    let drain!: () => void;
    let drained = new Promise<void>((r) => (drain = r));
    const q = new UploadQueue({ upload } as unknown as NullDriveClient, () => new Set(), () => {}, () => drain());
    q.add([{ file: new File(['x'], 'a'), path: 'a' }]);
    await drained;
    expect(q.jobs[0]).toMatchObject({ status: 'error', error: 'connection lost' });
    expect(q.totals.failed).toBe(1);
    drained = new Promise<void>((r) => (drain = r));
    q.retry(q.jobs[0]!.id);
    await drained;
    expect(q.jobs[0]!.status).toBe('done');
    q.clearFinished();
    expect(q.jobs).toHaveLength(0);
  });

  it('runs at most 3 uploads at once', async () => {
    let running = 0;
    let peak = 0;
    const upload = vi.fn(async (path: string) => {
      running++;
      peak = Math.max(peak, running);
      await new Promise((r) => setTimeout(r, 5));
      running--;
      return entry(path);
    });
    await new Promise<void>((resolve) => {
      const q = new UploadQueue({ upload } as unknown as NullDriveClient, () => new Set(), () => {}, resolve);
      q.add(Array.from({ length: 8 }, (_, i) => ({ file: new File(['x'], `f${i}`), path: `f${i}` })));
    });
    expect(upload).toHaveBeenCalledTimes(8);
    expect(peak).toBe(3);
  });
});
