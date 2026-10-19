/** Human-readable byte size: 0 B, 1.4 KB, 23 MB, 1.07 GB (1024-based). */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return '—';
  if (bytes < 1024) return `${bytes} B`;
  const units = ['KB', 'MB', 'GB', 'TB', 'PB'];
  let v = bytes / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  const digits = v < 10 ? 1 : 0;
  return `${v.toFixed(digits)} ${units[i]}`;
}

/** Relative time for recent dates, a short absolute date otherwise. */
export function formatDate(iso: string | Date, now: Date = new Date()): string {
  const d = typeof iso === 'string' ? new Date(iso) : iso;
  if (Number.isNaN(d.getTime())) return '—';
  const s = Math.round((now.getTime() - d.getTime()) / 1000);
  if (s < 45) return 'just now';
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400 && d.getDate() === now.getDate()) return `${Math.round(s / 3600)} h ago`;
  const sameYear = d.getFullYear() === now.getFullYear();
  return d.toLocaleDateString(undefined, {
    day: 'numeric',
    month: 'short',
    ...(sameYear ? {} : { year: 'numeric' }),
  });
}

/** Full timestamp for tooltips. */
export function formatDateTime(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleString();
}

export type FileKind =
  | 'folder'
  | 'image'
  | 'video'
  | 'audio'
  | 'archive'
  | 'code'
  | 'text'
  | 'pdf'
  | 'doc'
  | 'sheet'
  | 'file';

const kinds: Record<string, FileKind> = {};
const add = (kind: FileKind, exts: string) => exts.split(' ').forEach((e) => (kinds[e] = kind));
add('image', 'jpg jpeg png gif webp avif bmp svg heic heif tif tiff ico raw cr2 nef arw dng');
add('video', 'mp4 mkv mov avi webm m4v wmv flv mpg mpeg');
add('audio', 'mp3 flac wav ogg opus m4a aac wma aiff');
add('archive', 'zip tar gz tgz bz2 xz 7z rar zst iso dmg');
add('code', 'js ts jsx tsx svelte go py rs c h cpp hpp java kt rb php sh ps1 bat sql html css scss json yaml yml toml xml lua');
add('text', 'txt md markdown log ini cfg conf env csv tsv rtf');
add('pdf', 'pdf');
add('doc', 'doc docx odt pages epub');
add('sheet', 'xls xlsx ods numbers');

export function extension(name: string): string {
  const dot = name.lastIndexOf('.');
  return dot > 0 ? name.slice(dot + 1).toLowerCase() : '';
}

export function fileKind(name: string, isDir = false): FileKind {
  if (isDir) return 'folder';
  return kinds[extension(name)] ?? 'file';
}

/** Images a browser can render directly in an <img> (for grid thumbnails). */
export function isPreviewableImage(name: string): boolean {
  return ['jpg', 'jpeg', 'png', 'gif', 'webp', 'avif', 'svg', 'bmp', 'ico'].includes(extension(name));
}
