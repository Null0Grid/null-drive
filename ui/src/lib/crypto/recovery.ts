// Recovery key text form, identical to shared/e2ee (Go): 20 random bytes
// as 32 Crockford base32 characters in groups of four.

const CROCKFORD = '0123456789ABCDEFGHJKMNPQRSTVWXYZ';
export const RECOVERY_KEY_BYTES = 20;

export function formatRecoveryKey(b: Uint8Array): string {
  let out = '';
  let acc = 0;
  let bits = 0;
  let n = 0;
  for (const x of b) {
    acc = ((acc << 8) | x) & 0xffff;
    bits += 8;
    while (bits >= 5) {
      bits -= 5;
      if (n > 0 && n % 4 === 0) out += '-';
      out += CROCKFORD[(acc >> bits) & 31];
      n++;
    }
  }
  return out;
}

/** Lenient: any case, dashes/spaces ignored, O→0 and I/L→1. Null if malformed. */
export function parseRecoveryKey(s: string): Uint8Array | null {
  const out: number[] = [];
  let acc = 0;
  let bits = 0;
  let n = 0;
  for (let ch of s.toUpperCase()) {
    if ('- \t\r\n'.includes(ch)) continue;
    if (ch === 'O') ch = '0';
    if (ch === 'I' || ch === 'L') ch = '1';
    const i = CROCKFORD.indexOf(ch);
    if (i < 0) return null;
    acc = ((acc << 5) | i) & 0xffff;
    bits += 5;
    n++;
    if (bits >= 8) {
      bits -= 8;
      out.push((acc >> bits) & 0xff);
    }
  }
  if (n !== 32 || out.length !== RECOVERY_KEY_BYTES) return null;
  return new Uint8Array(out);
}

/** True if the input has the shape of a recovery key (vs. a passphrase). */
export const looksLikeRecoveryKey = (s: string) => parseRecoveryKey(s) !== null;
