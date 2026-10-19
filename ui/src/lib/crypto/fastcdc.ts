// FastCDC content-defined chunking: a port of shared/chunker (Go) that
// cuts at exactly the same byte offsets (pinned by
// shared/chunker/testdata/cdc-vectors.json). Identical boundaries mean a
// file uploaded from the browser produces the same chunks as from the
// desktop app, so dedup and delta sync work across both.
//
// The Gear hash is a 64-bit value; JavaScript numbers can't do 64-bit
// integer arithmetic, so it is kept as two 32-bit halves. The masks only
// test high bits, so only the high half is compared.

export interface Params {
  min: number;
  avg: number;
  max: number;
}

export const DEFAULT_PARAMS: Params = { min: 512 << 10, avg: 2 << 20, max: 8 << 20 };

const GEAR_HI = new Uint32Array(256);
const GEAR_LO = new Uint32Array(256);
(() => {
  // SplitMix64 from the seed "NULLDRIV", exactly as chunker.gear in Go.
  const M = (1n << 64n) - 1n;
  let x = 0x4e554c4c44524956n;
  for (let i = 0; i < 256; i++) {
    x = (x + 0x9e3779b97f4a7c15n) & M;
    let z = x;
    z = ((z ^ (z >> 30n)) * 0xbf58476d1ce4e5b9n) & M;
    z = ((z ^ (z >> 27n)) * 0x94d049bb133111ebn) & M;
    z ^= z >> 31n;
    GEAR_HI[i] = Number(z >> 32n);
    GEAR_LO[i] = Number(z & 0xffffffffn);
  }
})();

function masks(p: Params): [number, number] {
  const n = Math.log2(p.avg);
  if (!Number.isInteger(n) || n + 2 > 32 || n < 3) throw new Error('avg must be a power of two between 8 and 2^30');
  const top = (bits: number) => (bits >= 32 ? 0xffffffff : (0xffffffff << (32 - bits)) >>> 0);
  return [top(n + 2), top(n - 2)];
}

/** Length of the first chunk of data (len(data) if no cut point is found below max). */
export function cut(data: Uint8Array, p: Params = DEFAULT_PARAMS): number {
  let n = data.length;
  if (n <= p.min) return n;
  if (n > p.max) n = p.max;
  const normal = Math.min(p.avg, n);
  const [strict, loose] = masks(p);
  let hi = 0;
  let lo = 0;
  let i = p.min;
  for (; i < n; i++) {
    const b = data[i]!;
    // fp = (fp << 1) + gear[b]  (mod 2^64)
    hi = ((hi << 1) | (lo >>> 31)) >>> 0;
    const sum = ((lo << 1) >>> 0) + GEAR_LO[b]!;
    lo = sum >>> 0;
    hi = (hi + GEAR_HI[b]! + (sum > 0xffffffff ? 1 : 0)) >>> 0;
    if ((hi & (i < normal ? strict : loose)) === 0) return i + 1;
  }
  return n;
}

export interface Chunk {
  offset: number;
  data: Uint8Array;
}

/** Reads [start, end) of the input. */
export type ReadRange = (start: number, end: number) => Promise<Uint8Array>;

export const blobReader =
  (b: Blob): ReadRange =>
  async (start, end) =>
    new Uint8Array(await b.slice(start, end).arrayBuffer());

/**
 * Yields the chunks of an input of `size` bytes. Like the Go chunker it
 * keeps up to 2×max bytes buffered and refills when less than max remain,
 * so cut() always sees the same window the Go code does.
 */
export async function* chunks(read: ReadRange, size: number, p: Params = DEFAULT_PARAMS): AsyncGenerator<Chunk> {
  let buf = new Uint8Array(0);
  let start = 0;
  let pos = 0;
  let offset = 0;
  for (;;) {
    if (buf.length - start < p.max && pos < size) {
      const want = Math.min(2 * p.max - (buf.length - start), size - pos);
      const more = await read(pos, pos + want);
      if (more.length !== want) throw new Error('file changed while reading');
      pos += want;
      const next = new Uint8Array(buf.length - start + more.length);
      next.set(buf.subarray(start));
      next.set(more, buf.length - start);
      buf = next;
      start = 0;
    }
    if (start === buf.length) return;
    const n = cut(buf.subarray(start), p);
    yield { offset, data: buf.subarray(start, start + n) };
    start += n;
    offset += n;
  }
}
