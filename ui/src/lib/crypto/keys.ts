// End-to-end encryption in the browser: the TypeScript twin of shared/e2ee
// (Go). Both produce byte-identical output, pinned by the shared fixture
// shared/e2ee/testdata/vectors.json (see __tests__/crypto.test.ts).
//
// Key hierarchy (docs/DESIGN.md › Phase 7):
//   passphrase --Argon2id--> KEK --unwraps--> master key --HKDF--> subkeys
//   recovery key --HKDF----> KEK --unwraps--> (same master key)
//
// Nothing here is new cryptography: XChaCha20-Poly1305, Argon2id,
// HMAC-SHA256 and SHA-256 come from libsodium, HKDF from WebCrypto.

import type { KeyBundle } from '../api/types';
import { concat, equalBytes, fromBase64, fromBase64Url, fromUtf8, toBase64, toBase64Url, toHex, utf8 } from './bytes';
import { formatRecoveryKey, parseRecoveryKey, RECOVERY_KEY_BYTES } from './recovery';
import { loadSodium, type Sodium } from './sodium';

export const FORMAT_VERSION = 1;
export const KEY_SIZE = 32;
const NONCE = 24;
const TAG = 16;
/** Bytes an encrypted chunk adds: version byte + nonce + tag. */
export const CHUNK_OVERHEAD = 1 + NONCE + TAG;
export const CHUNK_VERSION = 1;
export const NAME_PREFIX = '~';
export const MAX_NAME_BYTES = 255;
export const MIN_PASSPHRASE = 10;
const NAME_PAD = 16;

const AD_WRAP_PASSPHRASE = 'nulldrive/v1/wrap/passphrase';
const AD_WRAP_RECOVERY = 'nulldrive/v1/wrap/recovery';
const AD_CHUNK = 'nulldrive/v1/chunk';
const AD_NAME = 'nulldrive/v1/name';

export class WrongKeyError extends Error {
  constructor() {
    super('That passphrase or recovery key is not correct.');
    this.name = 'WrongKeyError';
  }
}
export class CorruptError extends Error {
  constructor(what = 'data') {
    super(`Encrypted ${what} is corrupt or was modified.`);
    this.name = 'CorruptError';
  }
}

export interface KDFParams {
  alg: 'argon2id';
  time: number;
  memory_kib: number;
  threads: 1;
}

/** Same as the Go default: 3 passes over 64 MiB, one lane. */
export const DEFAULT_KDF: KDFParams = { alg: 'argon2id', time: 3, memory_kib: 64 * 1024, threads: 1 };

/**
 * Browsers only provide WebCrypto (crypto.subtle) to secure pages: HTTPS,
 * or localhost. Over plain http:// to a name or LAN address it's missing,
 * so encryption can't work there at all.
 */
export const INSECURE_CONTEXT_MESSAGE =
  'End-to-end encryption needs a secure connection: open Null Drive over HTTPS (or on localhost). ' +
  'Over plain http:// the browser doesn’t provide the encryption functions it needs. The desktop app works either way.';

/** True where the browser gives this page WebCrypto. */
export const cryptoAvailable = () => !!globalThis.crypto?.subtle;

async function hkdf(ikm: Uint8Array, salt: Uint8Array, info: string): Promise<Uint8Array> {
  if (!cryptoAvailable()) throw new Error(INSECURE_CONTEXT_MESSAGE);
  const key = await crypto.subtle.importKey('raw', ikm as BufferSource, 'HKDF', false, ['deriveBits']);
  const bits = await crypto.subtle.deriveBits(
    { name: 'HKDF', hash: 'SHA-256', salt: salt as BufferSource, info: utf8(info) as BufferSource },
    key,
    KEY_SIZE * 8,
  );
  return new Uint8Array(bits);
}

const subkey = (master: Uint8Array, purpose: string) => hkdf(master, new Uint8Array(0), 'nulldrive/v1/' + purpose);

/** The unwrapped keys of one account. Lives only in memory. */
export class Keys {
  private constructor(
    private readonly s: Sodium,
    private readonly master: Uint8Array,
    private readonly nameEnc: Uint8Array,
    private readonly nameSIV: Uint8Array,
    private readonly chunkEnc: Uint8Array,
    private readonly chunkSIV: Uint8Array,
    private readonly fileHashKey: Uint8Array,
    /** Hex of the auth subkey: proves possession of the master key to the server. */
    readonly authKey: string,
    /** 16 hex characters identifying the master key (not secret). */
    readonly fingerprint: string,
  ) {}

  static async fromMaster(master: Uint8Array): Promise<Keys> {
    if (master.length !== KEY_SIZE) throw new Error('master key must be 32 bytes');
    const s = await loadSodium();
    const [ne, ns, ce, cs, fh, au, fp] = (await Promise.all(
      ['name-enc', 'name-siv', 'chunk-enc', 'chunk-siv', 'file-hash', 'auth', 'fingerprint'].map((p) => subkey(master, p)),
    )) as [Uint8Array, Uint8Array, Uint8Array, Uint8Array, Uint8Array, Uint8Array, Uint8Array];
    return new Keys(s, master.slice(), ne, ns, ce, cs, fh, toHex(au), toHex(fp.subarray(0, 8)));
  }

  /** Copy of the master key (only used to re-wrap it). */
  masterKey(): Uint8Array {
    return this.master.slice();
  }

  // ---- deterministic AEAD (SIV construction, as in Go's siv/openSIV) ----

  private sivNonce(sivKey: Uint8Array, ad: string, pt: Uint8Array): Uint8Array {
    const st = this.s.crypto_auth_hmacsha256_init(sivKey);
    this.s.crypto_auth_hmacsha256_update(st, utf8(ad));
    this.s.crypto_auth_hmacsha256_update(st, new Uint8Array([0]));
    this.s.crypto_auth_hmacsha256_update(st, pt);
    return this.s.crypto_auth_hmacsha256_final(st).subarray(0, NONCE);
  }

  private siv(encKey: Uint8Array, sivKey: Uint8Array, ad: string, pt: Uint8Array): Uint8Array {
    const nonce = this.sivNonce(sivKey, ad, pt);
    const ct = this.s.crypto_aead_xchacha20poly1305_ietf_encrypt(pt, utf8(ad), null, nonce, encKey);
    return concat(nonce, ct);
  }

  private openSIV(encKey: Uint8Array, sivKey: Uint8Array, ad: string, blob: Uint8Array, what: string): Uint8Array {
    if (blob.length < NONCE + TAG) throw new CorruptError(what);
    const nonce = blob.subarray(0, NONCE);
    let pt: Uint8Array;
    try {
      pt = this.s.crypto_aead_xchacha20poly1305_ietf_decrypt(null, blob.subarray(NONCE), utf8(ad), nonce, encKey);
    } catch {
      throw new CorruptError(what);
    }
    if (!equalBytes(this.sivNonce(sivKey, ad, pt), nonce)) throw new CorruptError(what);
    return pt;
  }

  // ---- chunks ----

  /** Encrypt a chunk (deterministic for this account: same data, same bytes, same ID). */
  sealChunk(plain: Uint8Array): Uint8Array {
    return concat(new Uint8Array([CHUNK_VERSION]), this.siv(this.chunkEnc, this.chunkSIV, AD_CHUNK, plain));
  }

  openChunk(blob: Uint8Array): Uint8Array {
    if (blob.length < CHUNK_OVERHEAD || blob[0] !== CHUNK_VERSION) throw new CorruptError('chunk');
    return this.openSIV(this.chunkEnc, this.chunkSIV, AD_CHUNK, blob.subarray(1), 'chunk');
  }

  /** Server ID of a stored chunk: SHA-256 (hex) of its sealed bytes. */
  chunkId(sealed: Uint8Array): string {
    return toHex(this.s.crypto_hash_sha256(sealed));
  }

  /** The value stored as a file's sha256: HMAC-SHA256 of the plaintext's SHA-256. */
  fileHash(sha256: Uint8Array): string {
    return toHex(this.s.crypto_auth_hmacsha256(sha256, this.fileHashKey));
  }

  /** Streaming SHA-256 of a file's plaintext. */
  sha256(): { update(b: Uint8Array): void; digest(): Uint8Array } {
    const st = this.s.crypto_hash_sha256_init();
    return {
      update: (b) => this.s.crypto_hash_sha256_update(st, b),
      digest: () => this.s.crypto_hash_sha256_final(st),
    };
  }

  // ---- names ----

  encryptName(name: string): string {
    const b = utf8(name);
    if (b.length === 0 || b.length > MAX_NAME_BYTES || name.includes('/')) throw new Error(`Can't encrypt the name “${name}”.`);
    const padded = new Uint8Array((Math.floor(b.length / NAME_PAD) + 1) * NAME_PAD);
    padded.set(b);
    padded[b.length] = 0x80;
    return NAME_PREFIX + toBase64Url(this.siv(this.nameEnc, this.nameSIV, AD_NAME, padded));
  }

  decryptName(seg: string): string {
    if (!isEncryptedName(seg)) throw new CorruptError('name');
    const padded = this.openSIV(this.nameEnc, this.nameSIV, AD_NAME, fromBase64Url(seg.slice(1)), 'name');
    let i = padded.length - 1;
    while (i >= 0 && padded[i] === 0) i--;
    if (i < 1 || padded[i] !== 0x80) throw new CorruptError('name');
    const name = fromUtf8(padded.subarray(0, i));
    if (name.includes('/')) throw new CorruptError('name');
    return name;
  }

  encryptPath(p: string): string {
    return p.split('/').map((s) => this.encryptName(s)).join('/');
  }

  decryptPath(p: string): string {
    return p.split('/').map((s) => this.decryptName(s)).join('/');
  }
}

const MIN_ENC = NONCE + TAG + NAME_PAD;
const MAX_ENC = NONCE + TAG + (Math.floor(MAX_NAME_BYTES / NAME_PAD) + 1) * NAME_PAD;

/** Shape check, same rules as e2ee.IsEncryptedName. */
export function isEncryptedName(seg: string): boolean {
  if (!seg.startsWith(NAME_PREFIX)) return false;
  const body = seg.slice(1);
  if (/[^A-Za-z0-9_-]/.test(body) || body.length % 4 === 1) return false;
  const n = Math.floor((body.length * 6) / 8);
  return n >= MIN_ENC && n <= MAX_ENC && (n - MIN_ENC) % NAME_PAD === 0;
}

export const isEncryptedPath = (p: string) => p !== '' && p.split('/').every(isEncryptedName);

// ---- key bundle ----

function kdfOK(k: KeyBundle['kdf']): boolean {
  return k.alg === 'argon2id' && k.threads === 1 && k.time >= 1 && k.time <= 20 && k.memory_kib >= 8 * 1024 && k.memory_kib <= 1024 * 1024;
}

async function passphraseKEK(pass: string, kdf: KeyBundle['kdf'], salt: Uint8Array): Promise<Uint8Array> {
  const s = await loadSodium();
  return s.crypto_pwhash(KEY_SIZE, utf8(pass), salt, kdf.time, kdf.memory_kib * 1024, s.crypto_pwhash_ALG_ARGON2ID13);
}

const recoveryKEK = (rk: Uint8Array, salt: Uint8Array) => hkdf(rk, salt, 'nulldrive/v1/recovery-kek');

async function wrap(kek: Uint8Array, master: Uint8Array, ad: string): Promise<string> {
  const s = await loadSodium();
  const nonce = s.randombytes_buf(NONCE);
  return toBase64(concat(nonce, s.crypto_aead_xchacha20poly1305_ietf_encrypt(master, utf8(ad), null, nonce, kek)));
}

async function unwrap(kek: Uint8Array, wrapped: string, ad: string): Promise<Uint8Array> {
  const s = await loadSodium();
  const b = fromBase64(wrapped);
  if (b.length !== NONCE + KEY_SIZE + TAG) throw new Error('invalid key bundle');
  try {
    return s.crypto_aead_xchacha20poly1305_ietf_decrypt(null, b.subarray(NONCE), utf8(ad), b.subarray(0, NONCE), kek);
  } catch {
    throw new WrongKeyError();
  }
}

async function checked(b: KeyBundle, master: Uint8Array): Promise<Keys> {
  const k = await Keys.fromMaster(master);
  if (k.fingerprint !== b.fingerprint) throw new Error('The key bundle does not match its fingerprint.');
  return k;
}

function checkBundle(b: KeyBundle) {
  if (b.version !== FORMAT_VERSION || !kdfOK(b.kdf) || fromBase64(b.kdf.salt).length !== 16 || fromBase64(b.recovery_salt).length !== 16)
    throw new Error('Unsupported or invalid key bundle.');
}

export async function unlockWithPassphrase(b: KeyBundle, pass: string): Promise<Keys> {
  checkBundle(b);
  const kek = await passphraseKEK(pass, b.kdf, fromBase64(b.kdf.salt));
  return checked(b, await unwrap(kek, b.passphrase_wrapped, AD_WRAP_PASSPHRASE));
}

export async function unlockWithRecoveryKey(b: KeyBundle, recoveryKey: string): Promise<Keys> {
  checkBundle(b);
  const rk = parseRecoveryKey(recoveryKey);
  if (!rk) throw new WrongKeyError();
  const kek = await recoveryKEK(rk, fromBase64(b.recovery_salt));
  return checked(b, await unwrap(kek, b.recovery_wrapped, AD_WRAP_RECOVERY));
}

export function checkPassphrase(p: string): string | null {
  if ([...p].length < MIN_PASSPHRASE) return `Use at least ${MIN_PASSPHRASE} characters.`;
  if (p.length > 1024) return 'That passphrase is too long.';
  return null;
}

/** Create a new master key and its bundle. Returns the recovery key to show once. */
export async function createKeys(pass: string, kdf: KDFParams = DEFAULT_KDF): Promise<{ bundle: KeyBundle; keys: Keys; recoveryKey: string }> {
  const err = checkPassphrase(pass);
  if (err) throw new Error(err);
  const s = await loadSodium();
  const master = s.randombytes_buf(KEY_SIZE);
  const rk = s.randombytes_buf(RECOVERY_KEY_BYTES);
  const keys = await Keys.fromMaster(master);
  const salt = s.randombytes_buf(16);
  const recoverySalt = s.randombytes_buf(16);
  const bkdf = { ...kdf, salt: toBase64(salt) };
  const bundle: KeyBundle = {
    version: FORMAT_VERSION,
    kdf: bkdf,
    passphrase_wrapped: await wrap(await passphraseKEK(pass, bkdf, salt), master, AD_WRAP_PASSPHRASE),
    recovery_wrapped: await wrap(await recoveryKEK(rk, recoverySalt), master, AD_WRAP_RECOVERY),
    recovery_salt: toBase64(recoverySalt),
    fingerprint: keys.fingerprint,
  };
  return { bundle, keys, recoveryKey: formatRecoveryKey(rk) };
}

/** Same bundle, master key re-wrapped under a new passphrase (recovery key unchanged). */
export async function withPassphrase(b: KeyBundle, keys: Keys, pass: string, kdf: KDFParams = DEFAULT_KDF): Promise<KeyBundle> {
  const err = checkPassphrase(pass);
  if (err) throw new Error(err);
  const s = await loadSodium();
  const salt = s.randombytes_buf(16);
  const bkdf = { ...kdf, salt: toBase64(salt) };
  return { ...b, kdf: bkdf, passphrase_wrapped: await wrap(await passphraseKEK(pass, bkdf, salt), keys.masterKey(), AD_WRAP_PASSPHRASE) };
}

/** Same bundle with a new recovery key (the old one stops working). */
export async function withNewRecoveryKey(b: KeyBundle, keys: Keys): Promise<{ bundle: KeyBundle; recoveryKey: string }> {
  const s = await loadSodium();
  const rk = s.randombytes_buf(RECOVERY_KEY_BYTES);
  const salt = s.randombytes_buf(16);
  return {
    bundle: { ...b, recovery_salt: toBase64(salt), recovery_wrapped: await wrap(await recoveryKEK(rk, salt), keys.masterKey(), AD_WRAP_RECOVERY) },
    recoveryKey: formatRecoveryKey(rk),
  };
}
