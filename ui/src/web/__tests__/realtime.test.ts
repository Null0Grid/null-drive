import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { connectRealtime, defaultRealtimeUrl } from '../../lib/realtime';
import type { RealtimeMessage } from '../../lib/api/types';
import QrCode from '../../lib/components/QrCode.svelte';
import PairDialog from '../components/PairDialog.svelte';
import { client } from '../api';

class FakeSocket {
  static all: FakeSocket[] = [];
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onclose: (() => void) | null = null;
  closed = false;
  constructor(readonly url: string) {
    FakeSocket.all.push(this);
  }
  send(msg: RealtimeMessage) {
    this.onmessage?.({ data: JSON.stringify(msg) } as MessageEvent);
  }
  drop() {
    this.onclose?.();
  }
  close() {
    this.closed = true;
  }
}

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  FakeSocket.all = [];
});

describe('connectRealtime', () => {
  it('builds ws/wss URLs from the page location', () => {
    expect(defaultRealtimeUrl({ protocol: 'http:', host: 'nas:8080' } as Location)).toBe('ws://nas:8080/api/v1/ws');
    expect(defaultRealtimeUrl({ protocol: 'https:', host: 'drive.example.com' } as Location)).toBe('wss://drive.example.com/api/v1/ws');
  });

  it('delivers messages and reconnects with growing, jittered delays', () => {
    vi.useFakeTimers();
    const got: RealtimeMessage[] = [];
    const states: string[] = [];
    const conn = connectRealtime({
      url: 'ws://x/api/v1/ws',
      onMessage: (m) => got.push(m),
      onState: (s) => states.push(s),
      WebSocketImpl: FakeSocket as unknown as typeof WebSocket,
      random: () => 1,
    });
    FakeSocket.all[0]!.send({ type: 'hello', version: 3 });
    FakeSocket.all[0]!.send({ type: 'changed', version: 4 });
    expect(got.map((m) => m.version)).toEqual([3, 4]);
    expect(states).toEqual(['connecting', 'live']);

    // Drop twice: 1s, then 2s before reconnect attempts.
    FakeSocket.all[0]!.drop();
    vi.advanceTimersByTime(999);
    expect(FakeSocket.all).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(FakeSocket.all).toHaveLength(2);
    FakeSocket.all[1]!.drop();
    vi.advanceTimersByTime(1999);
    expect(FakeSocket.all).toHaveLength(2);
    vi.advanceTimersByTime(1);
    expect(FakeSocket.all).toHaveLength(3);
    // A successful hello resets the backoff.
    FakeSocket.all[2]!.send({ type: 'hello', version: 4 });
    FakeSocket.all[2]!.drop();
    vi.advanceTimersByTime(1000);
    expect(FakeSocket.all).toHaveLength(4);

    conn.stop();
    expect(FakeSocket.all[3]!.closed).toBe(true);
    vi.advanceTimersByTime(60_000);
    expect(FakeSocket.all).toHaveLength(4);
  });

  it('ignores garbage frames', () => {
    const got: RealtimeMessage[] = [];
    connectRealtime({ url: 'ws://x', onMessage: (m) => got.push(m), WebSocketImpl: FakeSocket as unknown as typeof WebSocket }).stop();
    FakeSocket.all[0]!.onmessage?.({ data: 'not json' } as MessageEvent);
    expect(got).toEqual([]);
  });
});

describe('QrCode', () => {
  it('renders an accessible SVG with modules', () => {
    render(QrCode, { text: 'nulldrive://pair?code=NULL-7K2Q-9F&server=http%3A%2F%2Fnas%3A8080', label: 'Pairing QR' });
    const svg = screen.getByRole('img', { name: 'Pairing QR' });
    const d = svg.querySelector('path')!.getAttribute('d')!;
    expect(d.length).toBeGreaterThan(500);
    expect(svg.getAttribute('viewBox')).toMatch(/^-4 -4 \d+ \d+$/);
  });
});

describe('PairDialog', () => {
  it('shows a code, waits, then reports the paired device', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const expires = new Date(Date.now() + 5 * 60_000).toISOString();
    vi.spyOn(client, 'createPairing').mockResolvedValue({ id: 7, code: 'NULL-7K2Q-9F', status: 'pending', expires_at: expires });
    const getPairing = vi
      .spyOn(client, 'getPairing')
      .mockResolvedValueOnce({ id: 7, status: 'pending', expires_at: expires })
      .mockResolvedValue({
        id: 7, status: 'claimed', expires_at: expires,
        device: { id: 3, name: 'Work laptop', created_at: new Date().toISOString(), added_via: 'pairing' },
      });
    const cancel = vi.spyOn(client, 'cancelPairing').mockResolvedValue({ id: 7, status: 'cancelled', expires_at: expires });
    const onpaired = vi.fn();
    const onclose = vi.fn();
    render(PairDialog, { open: true, onclose, onpaired });

    expect(await screen.findByTestId('pairing-code')).toHaveTextContent('NULL-7K2Q-9F');
    expect(screen.getByText(/expires in/)).toHaveTextContent(/4:5\d|5:00/);
    expect(screen.getByRole('img', { name: /QR code/ })).toBeInTheDocument();

    await vi.advanceTimersByTimeAsync(4100);
    expect(getPairing).toHaveBeenCalledTimes(2);
    expect(await screen.findByText(/Paired with “Work laptop”/)).toBeInTheDocument();
    expect(onpaired).toHaveBeenCalledWith(expect.objectContaining({ name: 'Work laptop' }));
    expect(cancel).not.toHaveBeenCalled();
    await userEvent.setup({ advanceTimers: vi.advanceTimersByTime }).click(screen.getByRole('button', { name: 'Done' }));
    expect(onclose).toHaveBeenCalled();
  });

  it('offers a new code when the old one expires', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const exp: Record<number, string> = {
      1: new Date(Date.now() + 2000).toISOString(),
      2: new Date(Date.now() + 300_000).toISOString(),
    };
    const create = vi
      .spyOn(client, 'createPairing')
      .mockResolvedValueOnce({ id: 1, code: 'NULL-AAAA-AA', status: 'pending', expires_at: exp[1]! })
      .mockResolvedValue({ id: 2, code: 'NULL-BBBB-BB', status: 'pending', expires_at: exp[2]! });
    vi.spyOn(client, 'getPairing').mockImplementation(async (id) => ({ id, status: 'pending', expires_at: exp[id]! }));
    render(PairDialog, { open: true, onclose: () => {} });
    expect(await screen.findByTestId('pairing-code')).toHaveTextContent('NULL-AAAA-AA');
    await vi.advanceTimersByTimeAsync(3000);
    await userEvent.setup({ advanceTimers: vi.advanceTimersByTime }).click(await screen.findByRole('button', { name: 'New code' }));
    expect(await screen.findByTestId('pairing-code')).toHaveTextContent('NULL-BBBB-BB');
    expect(create).toHaveBeenCalledTimes(2);
  });
});
