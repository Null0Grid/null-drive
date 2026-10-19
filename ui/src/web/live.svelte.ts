import { connectRealtime, type RealtimeState } from '../lib/realtime';
import type { RealtimeMessage } from '../lib/api/types';

type Handler = (m: RealtimeMessage) => void;

/** One realtime socket for the whole signed-in web app. */
class Live {
  state = $state<RealtimeState>('offline');
  private handlers = new Set<Handler>();
  private conn: { stop: () => void } | null = null;

  start() {
    if (this.conn || typeof WebSocket === 'undefined') return;
    this.conn = connectRealtime({
      onMessage: (m) => this.handlers.forEach((h) => h(m)),
      onState: (s) => (this.state = s),
    });
  }

  stop() {
    this.conn?.stop();
    this.conn = null;
    this.state = 'offline';
  }

  /** Subscribe; returns an unsubscribe function (use in $effect/onMount). */
  on(h: Handler): () => void {
    this.handlers.add(h);
    return () => this.handlers.delete(h);
  }
}

export const live = new Live();
