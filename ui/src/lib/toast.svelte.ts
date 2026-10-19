// Toast notifications as a tiny reactive store (Svelte 5 runes in a
// .svelte.ts module). Any code can call toast.error("..."); <ToastHost/>
// renders them.

export type ToastKind = 'info' | 'success' | 'error' | 'warning';

export interface Toast {
  id: number;
  kind: ToastKind;
  message: string;
  action?: { label: string; run: () => void };
}

let nextId = 1;

class ToastStore {
  items = $state<Toast[]>([]);

  push(kind: ToastKind, message: string, opts: { timeout?: number; action?: Toast['action'] } = {}): number {
    const id = nextId++;
    this.items.push({ id, kind, message, action: opts.action });
    if (this.items.length > 4) this.items.shift();
    const timeout = opts.timeout ?? (kind === 'error' ? 8000 : 4000);
    if (timeout > 0) setTimeout(() => this.dismiss(id), timeout);
    return id;
  }

  dismiss(id: number) {
    const i = this.items.findIndex((t) => t.id === id);
    if (i >= 0) this.items.splice(i, 1);
  }

  info = (m: string, o?: { timeout?: number; action?: Toast['action'] }) => this.push('info', m, o);
  success = (m: string, o?: { timeout?: number; action?: Toast['action'] }) => this.push('success', m, o);
  warning = (m: string, o?: { timeout?: number; action?: Toast['action'] }) => this.push('warning', m, o);
  error = (m: string, o?: { timeout?: number; action?: Toast['action'] }) => this.push('error', m, o);
}

export const toast = new ToastStore();
