import { describe, expect, it } from 'vitest';
import { fileKind, formatBytes, formatDate, isPreviewableImage } from '../format';
import { splitExt, uniqueName, validateName } from '../names';
import { allFolders, childrenOf, compareNames, exists, sortItems } from '../paths';
import { passwordStrength } from '../strength';
import type { FileEntry } from '../../api/types';

const f = (path: string, size = 1, extra: Partial<FileEntry> = {}): FileEntry => ({
  path, size, is_dir: false, sha256: 'x', version: 1, modified_at: '2026-10-01T00:00:00.000Z', deleted: false, ...extra,
});
const d = (path: string) => f(path, 0, { is_dir: true, sha256: '' });

describe('formatBytes', () => {
  it.each([
    [0, '0 B'], [1023, '1023 B'], [1024, '1.0 KB'], [1536, '1.5 KB'], [10 * 1024, '10 KB'],
    [5 * 1024 ** 2, '5.0 MB'], [1.07 * 1024 ** 3, '1.1 GB'], [-1, '—'], [NaN, '—'],
  ])('%s → %s', (n, want) => expect(formatBytes(n)).toBe(want));
});

describe('formatDate', () => {
  const now = new Date('2026-10-14T12:00:00Z');
  it('is relative for recent times', () => {
    expect(formatDate('2026-10-14T11:59:50Z', now)).toBe('just now');
    expect(formatDate('2026-10-14T11:30:00Z', now)).toBe('30 min ago');
  });
  it('handles garbage', () => expect(formatDate('nope', now)).toBe('—'));
});

describe('fileKind', () => {
  it.each([
    ['photo.JPG', 'image'], ['a.tar.gz', 'archive'], ['main.go', 'code'], ['Report.pdf', 'pdf'],
    ['noext', 'file'], ['.bashrc', 'file'], ['song.flac', 'audio'],
  ])('%s → %s', (name, kind) => expect(fileKind(name)).toBe(kind));
  it('folders win', () => expect(fileKind('x.jpg', true)).toBe('folder'));
  it('previewable images', () => {
    expect(isPreviewableImage('a.png')).toBe(true);
    expect(isPreviewableImage('a.heic')).toBe(false);
  });
});

describe('validateName (mirrors shared/pathutil)', () => {
  it.each(['report.pdf', 'Zdjęcia', '.env', 'a b c', 'console.log'])('accepts %s', (n) => expect(validateName(n)).toBeNull());
  it.each([
    ['', 'empty'], ['   ', 'empty'], ['..', 'reserved'], ['a/b', 'slashes'], ['a\\b', 'slashes'],
    ['what?', '"?"'], ['a:b', '":"'], ['trail.', 'dot or space'], ['trail ', 'dot or space'],
    ['CON', 'reserved on Windows'], ['nul.txt', 'reserved on Windows'], ['tab\there', 'control'],
    ['x'.repeat(256), 'too long'],
  ])('rejects %j (%s)', (n, msg) => expect(validateName(n)).toContain(msg));
});

describe('uniqueName / splitExt', () => {
  it('splits extensions', () => {
    expect(splitExt('a.tar.gz')).toEqual(['a.tar', '.gz']);
    expect(splitExt('.env')).toEqual(['.env', '']);
    expect(splitExt('README')).toEqual(['README', '']);
  });
  it('finds the next free name', () => {
    expect(uniqueName('a.txt', new Set())).toBe('a.txt');
    expect(uniqueName('a.txt', new Set(['a.txt']))).toBe('a (1).txt');
    expect(uniqueName('a.txt', new Set(['a.txt', 'a (1).txt']))).toBe('a (2).txt');
    expect(uniqueName('a (1).txt', new Set(['a (1).txt']))).toBe('a (2).txt');
    expect(uniqueName('Photos', new Set(['Photos']))).toBe('Photos (1)');
  });
});

describe('childrenOf', () => {
  const entries = [
    d('Docs'), f('Docs/a.pdf', 100), d('Docs/Sub'), f('Docs/Sub/b.pdf', 50),
    f('Implicit/deep/x.txt', 7), // folder rows missing (pre-migration data)
    f('top.txt', 3), f('gone.txt', 9, { deleted: true }), d('Empty'),
  ];
  it('lists direct children with folder totals', () => {
    const root = childrenOf(entries, '');
    const byName = Object.fromEntries(root.map((i) => [i.name, i]));
    expect(Object.keys(byName).sort()).toEqual(['Docs', 'Empty', 'Implicit', 'top.txt']);
    expect(byName.Docs).toMatchObject({ isDir: true, size: 150, count: 2 });
    expect(byName.Implicit).toMatchObject({ isDir: true, size: 7, count: 1 });
    expect(byName.Empty).toMatchObject({ isDir: true, size: 0, count: 0 });
    expect(byName['top.txt']).toMatchObject({ isDir: false, size: 3 });
  });
  it('lists a subfolder', () => {
    expect(childrenOf(entries, 'Docs').map((i) => i.name).sort()).toEqual(['Sub', 'a.pdf']);
  });
  it('does not confuse prefixes', () => {
    expect(childrenOf([f('Docs2/x')], 'Docs')).toEqual([]);
  });
  it('exists() sees explicit, implicit and missing paths', () => {
    expect(exists(entries, 'Docs/a.pdf')).toMatchObject({ path: 'Docs/a.pdf' });
    expect(exists(entries, 'Implicit/deep')).toBe('implicit');
    expect(exists(entries, 'gone.txt')).toBeUndefined();
  });
  it('allFolders includes implicit ones', () => {
    expect(allFolders(entries)).toEqual(['Docs', 'Docs/Sub', 'Empty', 'Implicit', 'Implicit/deep']);
  });
});

describe('sortItems', () => {
  const items = childrenOf([f('img10.png', 5), f('img2.png', 50), d('zeta'), f('Alpha.txt', 1)], '');
  it('folders first, natural name order', () => {
    expect(sortItems(items, 'name').map((i) => i.name)).toEqual(['zeta', 'Alpha.txt', 'img2.png', 'img10.png']);
  });
  it('by size descending', () => {
    expect(sortItems(items, 'size', true).map((i) => i.name)).toEqual(['zeta', 'img2.png', 'img10.png', 'Alpha.txt']);
  });
  it('compareNames is case-insensitive', () => expect(compareNames('a', 'B')).toBeLessThan(0));
});

describe('passwordStrength', () => {
  it.each([
    ['', 0], ['short', 0], ['password123', 1], ['admin-admin-admin', 1], ['aaaaaaaaaa', 1],
    ['purple-tiger', 2], ['purple tiger lamp', 3], ['correct horse battery', 4], ['Correct horse 9!', 4],
  ])('%j → %i', (pw, score) => expect(passwordStrength(pw as string, 'admin').score).toBe(score));
});
