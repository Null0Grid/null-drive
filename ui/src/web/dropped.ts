// Collect files from a drag-and-drop, including whole folders.
//
// DataTransfer.files only lists top-level files. To accept dropped folders
// we walk the (non-standard but universally supported) FileSystemEntry API
// and keep each file's path relative to the drop.

export interface DroppedFile {
  file: File;
  /** "photo.jpg" or "Trip/Day 1/photo.jpg" for files inside a dropped folder. */
  relativePath: string;
}

export async function collectDropped(dt: DataTransfer): Promise<DroppedFile[]> {
  const entries: FileSystemEntry[] = [];
  for (const item of Array.from(dt.items ?? [])) {
    if (item.kind !== 'file') continue;
    const entry = item.webkitGetAsEntry?.();
    if (entry) entries.push(entry);
  }
  // Fallback (old browsers, tests): plain files only.
  if (entries.length === 0) {
    return Array.from(dt.files).map((file) => ({ file, relativePath: file.name }));
  }
  const out: DroppedFile[] = [];
  for (const entry of entries) await walk(entry, '', out);
  return out;
}

async function walk(entry: FileSystemEntry, prefix: string, out: DroppedFile[]) {
  const path = prefix ? `${prefix}/${entry.name}` : entry.name;
  if (entry.isFile) {
    const file = await new Promise<File>((resolve, reject) => (entry as FileSystemFileEntry).file(resolve, reject));
    out.push({ file, relativePath: path });
  } else if (entry.isDirectory) {
    const reader = (entry as FileSystemDirectoryEntry).createReader();
    // readEntries returns results in batches (100 in Chrome) until empty.
    for (;;) {
      const batch = await new Promise<FileSystemEntry[]>((resolve, reject) => reader.readEntries(resolve, reject));
      if (batch.length === 0) break;
      for (const child of batch) await walk(child, path, out);
    }
  }
}

/** Files from an <input type="file"> (webkitdirectory inputs carry paths). */
export function fromInput(files: FileList): DroppedFile[] {
  return Array.from(files).map((file) => ({ file, relativePath: file.webkitRelativePath || file.name }));
}
