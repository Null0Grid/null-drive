// libsodium (WebAssembly build of the C library) for XChaCha20-Poly1305,
// Argon2id, HMAC-SHA256 and SHA-256. Loaded on demand: the ~400 KB module
// is only fetched by accounts that use end-to-end encryption. HKDF comes
// from WebCrypto (crypto.subtle), which every supported browser has.
//
// The server's CSP allows 'wasm-unsafe-eval' for this (it permits
// compiling WebAssembly, not JavaScript eval).

import type sodiumNS from 'libsodium-wrappers-sumo';

export type Sodium = typeof sodiumNS;

let loading: Promise<Sodium> | null = null;

export function loadSodium(): Promise<Sodium> {
  loading ??= import('libsodium-wrappers-sumo').then(async (m) => {
    const s = (m.default ?? m) as Sodium;
    await s.ready;
    return s;
  });
  return loading;
}
