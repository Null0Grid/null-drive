import type { RealtimeMessage } from './api/types';

/**
 * Browser side of GET /api/v1/ws. The socket authenticates with the
 * session cookie (browsers cannot set headers on a WebSocket handshake);
 * the server checks the Origin to stop other sites from riding that
 * cookie. Messages only say *that* something changed, never what: the
 * page re-fetches through the normal API, so a dropped message costs at
 * most a stale view until the next change or reconnect.
 */

export type RealtimeState = 'connecting' | 'live' | 'offline';

export interface RealtimeOptions {
  url?: string;
  onMessage: (m: RealtimeMessage) => void;
  onState?: (s: RealtimeState) => void;
  /** Injected in tests. */
  WebSocketImpl?: typeof WebSocket;
  random?: () => number;
  minDelay?: number;
  maxDelay?: number;
}

export function defaultRealtimeUrl(loc: Location = window.location): string {
  return `${loc.protocol === 'https:' ? 'wss:' : 'ws:'}//${loc.host}/api/v1/ws`;
}

/**
 * Connects and keeps reconnecting with jittered exponential backoff
 * (1s → 30s) until stop() is called. A tab in the background keeps its
 * socket; browsers throttle timers there but sockets stay open.
 */
export function connectRealtime(o: RealtimeOptions): { stop: () => void } {
  const WS = o.WebSocketImpl ?? WebSocket;
  const rnd = o.random ?? Math.random;
  const min = o.minDelay ?? 1000;
  const max = o.maxDelay ?? 30_000;
  let delay = 0;
  let stopped = false;
  let sock: WebSocket | null = null;
  let timer: ReturnType<typeof setTimeout> | null = null;

  const open = () => {
    if (stopped) return;
    o.onState?.('connecting');
    try {
      sock = new WS(o.url ?? defaultRealtimeUrl());
    } catch {
      schedule();
      return;
    }
    sock.onmessage = (ev: MessageEvent) => {
      let m: RealtimeMessage;
      try {
        m = JSON.parse(String(ev.data)) as RealtimeMessage;
      } catch {
        return;
      }
      if (m.type === 'hello') {
        delay = 0;
        o.onState?.('live');
      }
      o.onMessage(m);
    };
    sock.onclose = () => {
      sock = null;
      if (!stopped) {
        o.onState?.('offline');
        schedule();
      }
    };
  };

  const schedule = () => {
    delay = delay === 0 ? min : Math.min(delay * 2, max);
    // Full jitter, with a floor so a broken server is never hammered.
    timer = setTimeout(open, Math.max(min / 2, rnd() * delay));
  };

  open();
  return {
    stop() {
      stopped = true;
      if (timer) clearTimeout(timer);
      sock?.close();
    },
  };
}
