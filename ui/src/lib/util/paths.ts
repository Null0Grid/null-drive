import type { FileEntry } from '../api/types';

// Paths are server paths: relative, '/'-separated, no leading slash. The
// root folder is the empty string.

export function basename(path: string): string {
  const i = path.lastIndexOf('/');
  return i < 0 ? path : path.slice(i + 1);
}

export function dirname(path: string): string {
  const i = path.lastIndexOf('/');
  return i < 0 ? '' : path.slice(0, i);
}

export function join(folder: string, name: string): string {
  return folder ? `${folder}/${name}` : name;
}

/** "a/b/c" → ["a", "a/b", "a/b/c"] */
export function prefixes(path: string): string[] {
  if (!path) return [];
  const parts = path.split('/');
  return parts.map((_, i) => parts.slice(0, i + 1).join('/'));
}

export function isWithin(child: string, parent: string): boolean {
  return parent === '' ? child !== '' : child.startsWith(parent + '/');
}

/** An entry in a folder view (a FileEntry plus derived display data). */
export interface Item {
  path: string;
  name: string;
  isDir: boolean;
  size: number; // files: own size; folders: total of everything inside
  count: number; // folders: number of files inside (recursive)
  modifiedAt: string;
  version: number;
  sha256: string;
}

/**
 * Direct children of `folder` from the flat server listing. Folders are
 * listed even if the server only knows them implicitly (a file deeper
 * down), so the view never hides data. Folder size and modified time are
 * aggregated from their contents.
 */
export function childrenOf(entries: FileEntry[], folder: string): Item[] {
  const prefix = folder ? folder + '/' : '';
  const out = new Map<string, Item>();
  for (const e of entries) {
    if (e.deleted || !e.path.startsWith(prefix) || e.path === folder) continue;
    const rest = e.path.slice(prefix.length);
    const slash = rest.indexOf('/');
    const name = slash < 0 ? rest : rest.slice(0, slash);
    const path = prefix + name;
    const direct = slash < 0;
    let item = out.get(name);
    if (!item) {
      item = {
        path,
        name,
        isDir: !direct || e.is_dir,
        size: 0,
        count: 0,
        modifiedAt: e.modified_at,
        version: direct ? e.version : 0,
        sha256: direct ? e.sha256 : '',
      };
      out.set(name, item);
    }
    if (direct) {
      item.version = e.version;
      item.sha256 = e.sha256;
      item.isDir = e.is_dir;
      if (!e.is_dir) item.size = e.size;
    } else {
      item.isDir = true;
    }
    if (!direct && !e.is_dir) {
      item.size += e.size;
      item.count += 1;
    }
    if (e.modified_at > item.modifiedAt) item.modifiedAt = e.modified_at;
  }
  return [...out.values()];
}

/** True if a live entry exists at path (file or folder, explicit or implicit). */
export function exists(entries: FileEntry[], path: string): FileEntry | 'implicit' | undefined {
  let implicit = false;
  for (const e of entries) {
    if (e.deleted) continue;
    if (e.path === path) return e;
    if (!implicit && e.path.startsWith(path + '/')) implicit = true;
  }
  return implicit ? 'implicit' : undefined;
}

/** Every folder path (explicit or implicit), sorted, for the move dialog. */
export function allFolders(entries: FileEntry[]): string[] {
  const set = new Set<string>();
  for (const e of entries) {
    if (e.deleted) continue;
    const parts = e.path.split('/');
    const upto = e.is_dir ? parts.length : parts.length - 1;
    for (let i = 1; i <= upto; i++) set.add(parts.slice(0, i).join('/'));
  }
  return [...set].sort(compareNames);
}

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });

/** Natural, case-insensitive order: "img2" before "img10". */
export function compareNames(a: string, b: string): number {
  return collator.compare(a, b);
}

export type SortKey = 'name' | 'size' | 'modified';

/** Folders first, then by key; ties broken by name. */
export function sortItems(items: Item[], key: SortKey, desc = false): Item[] {
  const dir = desc ? -1 : 1;
  return [...items].sort((a, b) => {
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1;
    let c = 0;
    if (key === 'size') c = a.size - b.size;
    else if (key === 'modified') c = a.modifiedAt < b.modifiedAt ? -1 : a.modifiedAt > b.modifiedAt ? 1 : 0;
    if (c === 0) c = compareNames(a.name, b.name);
    return c * dir;
  });
}
