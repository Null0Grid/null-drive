// A ~40-line history router. The app has five screens, so a routing
// library would be more code to learn than to write. The server's SPA
// fallback serves index.html for every non-API path, so deep links and
// reloads work.

import { encodePath } from '../lib/api/client';

export type Route =
  | { name: 'files'; folder: string; history?: string; show?: string }
  | { name: 'open'; id: string }
  | { name: 'trash' }
  | { name: 'settings' }
  | { name: 'login'; next: string }
  | { name: 'setup' }
  | { name: 'notfound' };

export function parse(pathname: string, search = ''): Route {
  // ?history=<name> opens that file's version history (the desktop app
  // links here: "View history").
  // ?show=<name> scrolls to that entry and highlights it (/open links).
  const params = new URLSearchParams(search);
  const history = params.get('history') || undefined;
  const show = params.get('show') || undefined;
  const files = (folder: string): Route => ({
    name: 'files',
    folder,
    ...(history ? { history } : {}),
    ...(show ? { show } : {}),
  });
  if (pathname === '/' || pathname === '/files' || pathname === '/files/') return files('');
  if (pathname.startsWith('/files/')) {
    try {
      return files(pathname.slice('/files/'.length).replace(/\/+$/, '').split('/').map(decodeURIComponent).join('/'));
    } catch {
      return { name: 'notfound' };
    }
  }
  // /open?id=<file id>: where nulldrive:// links land when the desktop app
  // can't open the file (and what Null Search could link to).
  if (pathname === '/open') {
    const id = (params.get('id') ?? '').toLowerCase();
    return /^[0-9a-f]{32}$/.test(id) ? { name: 'open', id } : { name: 'notfound' };
  }
  if (pathname === '/trash') return { name: 'trash' };
  if (pathname === '/settings') return { name: 'settings' };
  if (pathname === '/setup') return { name: 'setup' };
  if (pathname === '/login') {
    const next = new URLSearchParams(search).get('next') ?? '/files';
    // Only same-app paths: never redirect to another origin after login.
    return { name: 'login', next: next.startsWith('/') && !next.startsWith('//') ? next : '/files' };
  }
  return { name: 'notfound' };
}

export function folderHref(folder: string): string {
  return folder ? `/files/${encodePath(folder)}` : '/files';
}

class Router {
  url = $state({ pathname: location.pathname, search: location.search });
  route = $derived(parse(this.url.pathname, this.url.search));

  constructor() {
    addEventListener('popstate', () => this.sync());
  }

  private sync() {
    this.url = { pathname: location.pathname, search: location.search };
  }

  navigate(to: string, { replace = false } = {}) {
    if (to === location.pathname + location.search) return;
    if (replace) history.replaceState(null, '', to);
    else history.pushState(null, '', to);
    this.sync();
    window.scrollTo(0, 0);
  }
}

export const router = new Router();
