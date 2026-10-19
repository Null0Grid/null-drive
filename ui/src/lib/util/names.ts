// Client-side mirror of shared/pathutil's per-segment rules, so the UI can
// reject a bad name while the user types instead of after a round trip.
// The server remains the authority.

const forbidden = /[<>:"|?*\\/\x00-\x1f\x7f]/;
const reserved = new Set([
  'CON', 'PRN', 'AUX', 'NUL',
  'COM1', 'COM2', 'COM3', 'COM4', 'COM5', 'COM6', 'COM7', 'COM8', 'COM9',
  'LPT1', 'LPT2', 'LPT3', 'LPT4', 'LPT5', 'LPT6', 'LPT7', 'LPT8', 'LPT9',
]);

/** Returns an error message, or null if the name is valid. */
export function validateName(name: string): string | null {
  if (name.trim() === '') return 'Name cannot be empty';
  if (name === '.' || name === '..') return 'That name is reserved';
  if (new TextEncoder().encode(name).length > 255) return 'Name is too long';
  const bad = name.match(forbidden);
  if (bad) {
    const ch = bad[0];
    return ch === '/' || ch === '\\'
      ? 'Name cannot contain slashes'
      : `Name cannot contain ${ch.charCodeAt(0) < 32 ? 'control characters' : `"${ch}"`}`;
  }
  if (/[. ]$/.test(name)) return 'Name cannot end with a dot or space';
  const base = (name.split('.')[0] ?? '').trimEnd().toUpperCase();
  if (reserved.has(base)) return `"${name}" is reserved on Windows`;
  return null;
}

/** Split "photo.final.jpg" → ["photo.final", ".jpg"]; dotfiles keep their name. */
export function splitExt(name: string): [string, string] {
  const dot = name.lastIndexOf('.');
  if (dot <= 0) return [name, ''];
  return [name.slice(0, dot), name.slice(dot)];
}

/**
 * A name not in `taken`: "report.pdf" → "report (1).pdf" → "report (2).pdf".
 * Used for "keep both" when an upload or conflict would overwrite a file.
 */
export function uniqueName(name: string, taken: Set<string>): string {
  if (!taken.has(name)) return name;
  const [stem, ext] = splitExt(name);
  const m = stem.match(/^(.*) \((\d+)\)$/);
  const root = m ? m[1]! : stem;
  let n = m ? Number(m[2]) + 1 : 1;
  for (;;) {
    const candidate = `${root} (${n})${ext}`;
    if (!taken.has(candidate)) return candidate;
    n++;
  }
}
