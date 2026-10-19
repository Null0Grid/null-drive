# Null Notes in an end-to-end encrypted Null Drive: the client protocol

How a third-party client (here: Null Notes backup and two-way sync) reads and writes an account that has end-to-end encryption (E2EE) turned on. On such accounts, plain uploads get `409 e2ee_required`. Applies to Null Drive server 0.8.3+. Everything that isn't about encryption is unchanged from [NOTES-SYNC.md](NOTES-SYNC.md) and [NOTES-BACKUP.md](NOTES-BACKUP.md): the same versions, preconditions, `409 conflict` with `current`, tombstones, `moved_to` and trash. Full reference: [API.md](API.md). Threat model: [SECURITY.md](SECURITY.md).

**Short version:**
- Fetch the key bundle from `GET /api/v2/e2ee`. Unlock it locally with the user's E2EE passphrase (Argon2id, then XChaCha20-Poly1305) or their recovery key. Derive seven subkeys from the 32-byte master key with HKDF-SHA256.
- Encrypt every path segment deterministically to `~<base64url>`. Seal content in chunks of any size.
- Talk to Drive only through the chunked API (`/api/v2/chunks`, `PUT /api/v2/files/{path}`, `…/manifest`), with `X-NullDrive-E2EE: 1` on every request.
- `If-Match`, `If-None-Match: *`, moves, deletes, `?prefix=` and `?since=` work exactly as on plain accounts, with encrypted paths in and out.
- There is no `synced_id`, and search is off.

**Proof that this is enough:**
- Null Drive does nothing new for third-party clients; the desktop app and web app have used this protocol since E2EE shipped.
- `scripts/e2ee-reference/nulldrive_e2ee.py` is an independent Python client (about 500 lines: PyNaCl plus the standard library). It follows this document step by step and shares no code with the Go and TypeScript implementations.
  - It reproduces every test vector.
  - It runs the whole Notes flow against a live server: `make e2ee-ref`.
- `TestNotesE2EEFlow` (`server/internal/api/e2ee_thirdparty_test.go`) does the same flow over raw HTTP.

## 0. Conventions

- **Binary values in JSON:** bundle fields use standard base64 **with** padding (`+/=`). Encrypted names use base64url **without** padding (`-_`). Hashes and keys are lowercase hex.
- **Strings:** every string that is hashed or encrypted is UTF-8. Associated data (AD) and HKDF `info` are the ASCII labels shown, with no terminator.
- **AEAD:** XChaCha20-Poly1305 (IETF, 24-byte nonce, 16-byte tag), as in libsodium `crypto_aead_xchacha20poly1305_ietf_*` and Go `chacha20poly1305.NewX`. Output = ciphertext ‖ tag.
- **HKDF:** HKDF-SHA256 (RFC 5869), output 32 bytes. "No salt" means the RFC default of 32 zero bytes.
- **Header:** `X-NullDrive-E2EE: 1` on **every** request to an encrypted account. It says "I can decrypt what you send me". Without it, listings and v2 file operations answer `409 e2ee_client_required`, so an old client fails safely instead of syncing files named `~kD3x…`.

## 1. The account key

### 1.1 Is the account encrypted?

```
GET /api/v1/me            → { "user": { "e2ee": "off" | "migrating" | "on", … } }
GET /api/v2/e2ee          (any token)
→ 200 {
  "state": "on",                      "off" | "migrating" | "on"
  "fingerprint": "5280d8c8b52fe7f7",
  "enabled_at": "2026-10-18T07:12:00Z",
  "plaintext_files": 0,               files not yet encrypted (only while migrating)
  "bundle": {
    "version": 1,
    "kdf": { "alg": "argon2id", "time": 3, "memory_kib": 65536, "threads": 1, "salt": "<16 bytes, base64>" },
    "passphrase_wrapped": "<72 bytes, base64>",
    "recovery_wrapped":   "<72 bytes, base64>",
    "recovery_salt":      "<16 bytes, base64>",
    "fingerprint": "5280d8c8b52fe7f7"
  }
}
```

- `off` has no bundle. Use the plain API, as in NOTES-SYNC.
- `migrating`: encryption is on, but the desktop or web app is still re-uploading the account's older plaintext files. **Treat it exactly like `on`.** New writes must be encrypted; the plaintext leftovers are covered in §4.3.
- Turning encryption on, changing the passphrase and replacing the recovery key are jobs for the Drive apps (desktop, web, `nulldrive-e2ee` CLI). Null Notes only joins an account that already has encryption and never writes the bundle.
- The bundle lives on the server, in the `users` table, stored verbatim. The server can't open it: it has neither the passphrase nor the recovery key.

### 1.2 Unlock with the passphrase

The user types their **E2EE passphrase**, which is not their Drive login password.

```
kek    = Argon2id(password = passphrase (UTF-8), salt = kdf.salt,
                  t = kdf.time, m = kdf.memory_kib KiB, p = 1, output 32 bytes)   (Argon2 v1.3)
blob   = base64decode(passphrase_wrapped)            72 bytes = nonce(24) ‖ ciphertext(32) ‖ tag(16)
master = XChaCha20-Poly1305-Open(key = kek, nonce = blob[0:24], ct = blob[24:],
                                 ad = "nulldrive/v1/wrap/passphrase")
```
In libsodium: `crypto_pwhash(32, pass, salt, opslimit = time, memlimit = memory_kib × 1024, crypto_pwhash_ALG_ARGON2ID13)`. libsodium always uses p = 1, which is why Drive fixes `threads` at 1.

**Refuse a bundle you don't understand** before deriving anything:
- `version` must be 1 and `alg` must be `argon2id`.
- `time` must be 1–20, `memory_kib` 8192–1048576, `threads` 1.
- The salt must be 16 bytes and each wrap 72 bytes.

These are also the limits Drive's own clients enforce.

A wrong passphrase makes the AEAD open fail. That is the only "wrong passphrase" signal.

### 1.3 Unlock with the recovery key (alternative)

The recovery key is 20 random bytes shown to the user as 32 Crockford base32 characters in groups of 4, like `B9D5-MPJT-B9D5-MPJT-B9D5-MPJT-B9D5-MPJT`.
- Alphabet: `0123456789ABCDEFGHJKMNPQRSTVWXYZ`.
- Decode most-significant bit first, 5 bits per character, 160 bits in all.
- Be lenient when parsing: ignore case, `-` and spaces, and read `O` as `0` and `I`/`L` as `1`.

```
kek    = HKDF(ikm = recovery key bytes (20), salt = base64decode(recovery_salt), info = "nulldrive/v1/recovery-kek")
master = XChaCha20-Poly1305-Open(kek, nonce = rw[0:24], ct = rw[24:], ad = "nulldrive/v1/wrap/recovery")   rw = base64decode(recovery_wrapped)
```

### 1.4 Subkeys and the checks

```
subkey(purpose) = HKDF(ikm = master, salt = none, info = "nulldrive/v1/" + purpose)
purposes: name-enc  name-siv  chunk-enc  chunk-siv  file-hash  auth  fingerprint

fingerprint = hex(subkey("fingerprint")[0:8])        must equal bundle.fingerprint, else stop
auth_key    = hex(subkey("auth"))                    only for bundle changes; Notes doesn't need it
```

- **What to store on the device:** the 32-byte master key, in the OS keychain the way Notes keeps its other secrets. Never store the passphrase.
- **Pin the fingerprint.** If a later `GET /api/v2/e2ee` shows a different fingerprint, stop syncing and ask the user to unlock again.
- A changed passphrase or recovery key re-wraps the **same** master key, so a stored master key keeps working. Only a new fingerprint means new keys.

## 2. Per-file encryption

### 2.1 The SIV construction (names and chunks)

Both names and chunks are sealed **deterministically**, so that the same input under the same account gives the same output:

```
siv_seal(enc_key, siv_key, ad, pt):
    nonce = HMAC-SHA256(siv_key, ad ‖ 0x00 ‖ pt)[0:24]
    return nonce ‖ XChaCha20-Poly1305-Seal(enc_key, nonce, pt, ad)

siv_open(enc_key, siv_key, ad, blob):
    pt = XChaCha20-Poly1305-Open(enc_key, blob[0:24], blob[24:], ad)
    require blob[0:24] == HMAC-SHA256(siv_key, ad ‖ 0x00 ‖ pt)[0:24]     (reject otherwise)
```

**Why it's deterministic** (DESIGN.md › Phase 7):
- A client finds `Null Notes/Work/Plan.md` by encrypting the path, instead of listing and decrypting everything.
- Identical chunks still deduplicate.
- A retried upload is recognised as identical, so it gets `200` and not `409`.

### 2.2 Names and paths

Each `/`-separated segment is encrypted on its own. The plaintext path rules are the same as for plain accounts: relative, `/` separators, no `.`/`..` and no empty segments. A segment is 1–255 bytes of UTF-8.

```
padded = utf8(name) ‖ 0x80 ‖ 0x00 …    ISO/IEC 7816-4 padding up to the next multiple of 16 (always ≥ 1 byte)
segment = "~" + base64url_nopad( siv_seal(name-enc, name-siv, "nulldrive/v1/name", padded) )
path    = join("/", segment for each plaintext segment)
```
- **Deterministic and independent of the folder:**
  - "Work" encrypts to the same segment anywhere in the account.
  - An encrypted prefix is the per-segment encryption of the plaintext prefix.
  - A rename changes only the last segment.
- **Lengths:** encrypted segments are 76 to 396 characters (`"~"` + base64url of 24 + 16 + 16·n bytes, n = 1…16). A whole path has no length limit on Drive.
- **Decrypting:**
  1. Strip the `~` and decode base64url.
  2. `siv_open`.
  3. Strip trailing `0x00` bytes, then require and strip one `0x80`.
  4. Require valid UTF-8 with no `/`.
- **Unicode normalization:** Drive doesn't normalize names, and neither do its clients. `é` as NFC and as NFD are different names with different ciphertexts. Use names byte-for-byte as Notes stores them. When matching to an existing Drive file, compare decrypted paths, not encrypted ones, if normalization can differ.
- **Windows-safe names:** the server can't check the rules from NOTES-BACKUP (`<>:"|?*`, `CON`, trailing dot, …) on ciphertext. Desktop clients that pull the file would trip over a bad name, so Notes should apply those rules itself before uploading.

### 2.3 Content and chunking

```
sealed_chunk = 0x01 ‖ siv_seal(chunk-enc, chunk-siv, "nulldrive/v1/chunk", plaintext piece)     (41 bytes of overhead)
chunk hash   = hex(SHA-256(sealed_chunk))          the chunk's name on the server
```
- **Chunking is the client's choice.** Split the plaintext anywhere. Each piece must be 1 byte or more, and each sealed chunk at most 16 MiB (16 777 216 bytes), so a piece holds at most 16 777 175 bytes.
  - Notes: one chunk per note is fine. Fixed 4 MiB pieces for anything bigger are fine too.
  - The desktop app uses FastCDC (512 KiB min, 2 MiB average, 8 MiB max). Matching that only buys deduplication with files the desktop app uploaded.
- **An empty file has no chunks** (`"chunks": []`).
- `0x01` is the chunk format version. Refuse anything else.

### 2.4 Worked example (test vector)

`shared/e2ee/testdata/notes-vector.json` holds every intermediate value. Both the Go tests and the Python reference check it. The master key is the same as in `vectors.json`, whose bundle (passphrase `vector passphrase 123`, recovery key `B9D5-MPJT-B9D5-MPJT-B9D5-MPJT-B9D5-MPJT`, test KDF parameters t=1, m=8192) unlocks to it.

| | |
|---|---|
| master key | `000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f` |
| subkey `name-enc` | `28527987d99d11dee336d411fd43708b3314fbe3551522f8c3f6b07abeaa34d9` |
| subkey `name-siv` | `689503732a768fa127c6c63a6a244c70c25513747d2c2022ff4f9d65acc19746` |
| subkey `chunk-enc` | `06494a6e9c24b2e9117c88cf4f4cc633fdaf7df0e56ae46d94a6b43bbf0786f5` |
| subkey `chunk-siv` | `ed77c6c8feedc89850694f379b29bbc42ddbd5457e8753befa40e2c13fa47ca0` |
| subkey `file-hash` | `57f51c8409414e1ab4d1c94d1e2e2935fb1b5bc7edf650ddfe830b9f5df2a3cd` |
| fingerprint | `5280d8c8b52fe7f7` |

**Path** `Null Notes/Work/Plan.md`:

| segment | padded (hex) | synthetic nonce | encrypted |
|---|---|---|---|
| `Null Notes` | `4e756c6c204e6f746573800000000000` | `8901995e06d00283ea1138d3c3886558aadb7ba2446a49ed` | `~iQGZXgbQAoPqETjTw4hlWKrbe6JEaknt97UL9qRVdIYWjb8PRmsqKoaQ1bAvQdhvFkppTnaYmNM` |
| `Work` | `576f726b800000000000000000000000` | `2518ec7d6a18e1f877916fa27232d129a9ef14da10c87fa2` | `~JRjsfWoY4fh3kW-icjLRKanvFNoQyH-iOTML5IdPYTnYOt-Itf6WEi88CHV1G0qH_Qfumwg6UO4` |
| `Plan.md` | `506c616e2e6d64800000000000000000` | `bf62d9e94f9710e16a278e59e01ea3e59c8378d09c597de9` | `~v2LZ6U-XEOFqJ45Z4B6j5ZyDeNCcWX3pDSqzsJsPLn3DVtA64I9BENCuWsun3_6B34uHI0x-Syg` |

**Note** (81 bytes):
```
---
id: 3f2504e0-4f89-11d3-9a0c-0305e82c3301
---
# Plan
Ship the encrypted sync.
```

| | |
|---|---|
| SHA-256(plaintext) | `a692142e8f2cdd35e8823662bde3ad120229fe065a5e565ce6a67d21971b3811` |
| `sha256` sent to Drive (file hash, §3) | `5655b16c8fcf689c68ff1ef393aac39ae7cedbcf766628770d8eac81a09e5abe` |
| chunk nonce | `bfc7ef4c895f9a4d4ac0e0ec3f123498c4cacff12be19c8c` |
| sealed chunk (122 bytes) | `01bfc7ef4c895f9a4d4ac0e0ec3f123498c4cacff12be19c8cbd34e5ca3cc16a4598a14b80211894bff9a9f349a7e6ed21bbdb1cf7df58458540a0d7a3d03a3ec395f29a4e949f92904fbbf8e187a7bba109e452da16f1591213c167d75082c31e1aa03f4bef1843766e95b68639389112547bc69107695e1980` |
| chunk hash | `96bd09b77dbb03c062d57bf0ee102f555b57abbd97d31214a20c8b75992fe81e` |

Commit body:
```json
{ "size": 81, "sha256": "5655b16c8fcf689c68ff1ef393aac39ae7cedbcf766628770d8eac81a09e5abe", "mtime": "2026-10-18T08:00:00Z",
  "chunks": [ { "hash": "96bd09b77dbb03c062d57bf0ee102f555b57abbd97d31214a20c8b75992fe81e", "size": 122 } ] }
```
`vectors.json` adds edge cases: a 1-byte name, a 16-byte name (two padding blocks), non-ASCII, a 255-byte name, empty and multi-KiB chunks, and file hashes.

## 3. What the server sees, and what to send

| Field | Plain account | Encrypted account |
|---|---|---|
| path segments | plaintext | `~…` per §2.2 (anything else → `409 e2ee_path_required`) |
| `size` (commit, listings) | bytes | **plaintext** size. The server checks it against the chunks: `Σ chunk sizes − 41 × number of chunks` must equal `size`, else `400 invalid_manifest` |
| `sha256` (commit, listings, manifest) | SHA-256 of content | **`hex(HMAC-SHA256(file-hash, SHA-256(plaintext)))`**. Stored as sent and never checked: the server can't. Compare it the same way as on a plain account (equal means same content) |
| chunk `hash` | SHA-256 of the chunk | SHA-256 of the **sealed** chunk (verified on upload) |
| `mtime` | optional | optional, and stored **in plaintext**. Send it if Notes needs it for change detection, or leave it out to keep edit times private (the server still knows when uploads happen) |
| `synced_id` | from frontmatter | **never set**: the server can't read frontmatter |

The server can't read content or names, but it still sees:
- the tree's shape;
- which items share a name;
- name lengths, to within 16 bytes;
- exact sizes;
- times and devices.

SECURITY.md › What E2EE does not hide has the full list.

## 4. The API on an encrypted account

The calls are the same as in NOTES-SYNC, plus the E2EE header, with encrypted paths in and out. Each path segment is URL-encoded as always. `~`, `-` and `_` are safe, so encrypted segments pass through unchanged.

### 4.1 What works unchanged

| | Encrypted account |
|---|---|
| `If-Match: <version>` on commit, `DELETE`, move | ✅ same `409 conflict` + `current` (+ `ETag`), `428`, `404` |
| `If-None-Match: *` / `If-Match: 0` (create-only), `If-Match: *` | ✅ |
| identical content is a `200` no-op | ✅ for free: sealing is deterministic, so a retry sends the same `sha256` and chunks. `X-Content-SHA256` doesn't apply (v2) |
| `POST /api/v1/files/move` | ✅ `from`/`to` encrypted. Keeps `id`, `mtime`, history; new `version`. Tombstone `moved_to` is the encrypted new path |
| `DELETE /api/v1/files/{path}` | ✅ goes to the trash (`trash_days`, default 30) |
| `?prefix=` | ✅ pass the **encrypted** prefix |
| `?since=` | ✅ tombstones, `deleted`, `moved_to` (encrypted), same `id` rules |
| `POST /api/v1/folders` | ✅ encrypted path. Commits create missing folders anyway |
| `GET /api/v2/ids/{id}` | ✅ returns the row (encrypted path) |
| version history, restore, `GET /api/v2/trash` | ✅ encrypted paths |
| WebSocket change push (`/api/v1/ws`) | ✅ unchanged (versions only, no names) |

### 4.2 What changes

| Plain-account call | Encrypted account | Use instead |
|---|---|---|
| `PUT /api/v1/files/{path}` (whole-file upload) | `409 e2ee_required` | chunk upload + commit, below |
| `GET`/`HEAD /api/v1/files/{path}` (download) | `409 e2ee_required` | manifest + chunks, below |
| `GET /api/v2/files/{path}/versions/{v}/content` | `409 e2ee_required` | `…/versions/{v}/manifest` + chunks |
| any listing / v2 op without `X-NullDrive-E2EE: 1` | `409 e2ee_client_required` | send the header |
| a plaintext name in a commit, folder, restore or move (`from` **or** `to`) | `409 e2ee_path_required` | encrypt it |
| `GET /search` | `200 []` with `X-NullDrive-Search: encrypted` | nothing: see §5 |

**Upload** (= v1 `PUT`):
```
POST /api/v2/chunks/check        { "hashes": ["96bd09b7…"] }          → 200 { "missing": ["96bd09b7…"] }
PUT  /api/v2/chunks/96bd09b7…    <sealed bytes>                       → 201 { "hash": "96bd09b7…", "size": 122 }
PUT  /api/v2/files/~iQGZ…/~JRjs…/~v2LZ…
     If-None-Match: *            (or If-Match: <version>, or If-Match: *)
     Content-Type: application/json
     X-NullDrive-E2EE: 1
     { "size": 81, "sha256": "5655b16c…", "mtime": "2026-10-18T08:00:00Z", "chunks": [ { "hash": "96bd09b7…", "size": 122 } ] }
→ 201 created / 200 replaced: the file (encrypted "path", "id", "version", "size", "sha256", "mtime"), ETag: "<version>"
```
- A chunk is verified against its hash on upload (`400 hash_mismatch` otherwise). Already-stored chunks don't need uploading again.
- `422 missing_chunks` with `"missing": […]`: upload those and retry the commit.
- Chunks are per user. Only chunks this account uploaded can be referenced.

**Download** (= v1 `GET`):
```
GET /api/v2/files/~iQGZ…/~JRjs…/~v2LZ…/manifest
→ 200 { "path": "~iQGZ…/~JRjs…/~v2LZ…", "id": "8d4e8fe1…", "version": 813, "size": 81, "sha256": "5655b16c…",
        "mtime": "2026-10-18T08:00:00Z", "chunks": [ { "hash": "96bd09b7…", "size": 122 } ] }
GET /api/v2/chunks/96bd09b7…     → 200 <sealed bytes>   (per chunk, in order; open each, concatenate)
```
- Then check `len(plaintext) == size` and `file_hash(plaintext) == sha256`.
- The manifest's `version` is the `If-Match` value for the next write. The fields `id` and `mtime` are new in 0.8.3; the manifest is the encrypted counterpart of the v1 download headers.
- If the file changes between the manifest and the chunk reads, the old chunks stay readable for at least the GC grace period (24 h) and the version is still consistent. Compare `version` against your next listing as usual.

### 4.3 Listings and decryption

```
GET /api/v1/files?prefix=~iQGZ…%2F~JRjs…&since=812
X-NullDrive-E2EE: 1
→ 200 { "version": 840, "files": [ { "id": "8d4e8fe1…", "path": "~iQGZ…/~JRjs…/~v2LZ…", "is_dir": false,
                                     "size": 81, "sha256": "5655b16c…", "version": 813, "mtime": "…", … } ] }
```
- Decrypt `path`, and `moved_to` if present, segment by segment (§2.2).
- **Rows that don't decrypt:** while `state` is `migrating`, the account's older plaintext files are still listed under their plaintext names (no `~`). Skip them. They are being re-uploaded encrypted, then deleted by the app doing the migration.
  - They can't appear under an encrypted `?prefix=`, because a plaintext segment never equals an encrypted one.
  - A row whose `~` name doesn't open with this key is corrupt or belongs to other keys. Skip it and report it.
- Sort order is by encrypted path, which is meaningless. Sort after decrypting.
- `synced_id` is never present (see below). `id`, `version`, `deleted`, `deleted_at`, `moved_to` mean what they mean in NOTES-SYNC.

## 5. `synced_id` and search: confirmed off

- **`synced_id` is absent** on encrypted accounts: in listings, manifests, `ids`, everywhere. On plain accounts Drive reads it from the frontmatter, which it can't do here. That won't change: making it settable by the client would put note ids (stable identifiers that link content across apps) back on the server in plaintext.
  - **Matching** works by Drive `id` and decrypted path. Notes knows every note id it uploads; keep a local map from file `id` to note id. For a file Notes hasn't seen (uploaded by another device), the note id is in its frontmatter after download, and the download is needed anyway.
  - `.nullnotes/ids.json` is uploaded like any file (encrypted), so it still carries the ids of `.txt`/`.html`/`.canvas` notes.
- **Search is off.** Drive deletes the account's search index when encryption is turned on and never builds one again. `GET /search` returns `200 []` with `X-NullDrive-Search: encrypted`, so Null Search can tell "Drive is end-to-end encrypted" apart from "no results". Notes stays searchable in Null Search through Notes' own index, which is the copy the `synced_id` deduplication used to prefer anyway.

## 6. Errors worth handling (in addition to NOTES-SYNC's)

| Response | Meaning | Do |
|---|---|---|
| `409 e2ee_client_required` | header missing | send `X-NullDrive-E2EE: 1` (a client bug) |
| `409 e2ee_required` | v1 whole-file call on an encrypted account | use the chunked API. If the account was plain at setup, it was just switched: re-check `GET /api/v2/e2ee` and ask the user to unlock |
| `409 e2ee_path_required` | a plaintext name | encrypt it (a client bug) |
| `400 invalid_manifest` | sizes don't add up (forgot the 41 bytes per chunk, or sent the sealed total), or a malformed hash | |
| `422 missing_chunks` | not uploaded yet, or uploaded by another account | upload the listed ones, retry |
| AEAD open fails on the bundle | wrong passphrase / recovery key | ask again |
| fingerprint ≠ pinned | the account's keys changed (or a server tampering attempt) | stop syncing, ask the user |
| AEAD open fails on a chunk or name | corrupt data, or different keys | don't overwrite anything; report |

## 7. Checklist for Null Notes

1. On setup: `GET /api/v2/e2ee`. If `state` isn't `off`, ask for the E2EE passphrase (or recovery key). Unlock, verify the fingerprint, store the master key in the keychain and pin the fingerprint.
2. Add `X-NullDrive-E2EE: 1` to every request.
3. Encrypt every path (and the `?prefix=`) per segment. Decrypt every listed `path` and `moved_to`, and skip rows that don't decrypt.
4. Replace v1 upload and download with chunk check/upload + commit and manifest + chunks. Send the plaintext size and the keyed file hash.
5. Keep all of NOTES-SYNC's preconditions and conflict handling as they are.
6. Match files by `id` + decrypted path, with note ids from a local map or the downloaded frontmatter.
7. Check your implementation against `notes-vector.json` and `vectors.json`. `scripts/e2ee-reference/nulldrive_e2ee.py` is a working example of every step.
