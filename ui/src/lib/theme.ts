// Theme and CRT-scanline preferences. Stored per browser (localStorage),
// applied as data-* attributes on <html> so CSS tokens switch instantly.
// public/theme-init.js applies the stored choice before first paint.

export type Theme = 'dark' | 'light';

const THEME_KEY = 'nd.theme';
const SCAN_KEY = 'nd.scanlines';

function read(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function write(key: string, value: string) {
  try {
    localStorage.setItem(key, value);
  } catch {
    /* storage unavailable (private mode): the setting lasts for this page */
  }
}

export function getTheme(): Theme {
  return read(THEME_KEY) === 'light' ? 'light' : 'dark';
}

export function setTheme(theme: Theme) {
  document.documentElement.dataset.theme = theme;
  write(THEME_KEY, theme);
}

export function getScanlines(): boolean {
  return read(SCAN_KEY) === 'on';
}

export function setScanlines(on: boolean) {
  document.documentElement.dataset.scanlines = on ? 'on' : 'off';
  write(SCAN_KEY, on ? 'on' : 'off');
}
