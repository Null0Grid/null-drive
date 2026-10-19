# Null Drive threat model

This document says what Null Drive protects, against whom, and where the
protection stops. It is meant to be read by people deciding whether to
trust it with their files, and it tries to be specific rather than
reassuring. To report a vulnerability, see [`/SECURITY.md`](../SECURITY.md).

There are two modes per account:

- **Standard**: files are encrypted in transit (if you use HTTPS) but stored
  readable on the server. The server can read, index, preview and dedup
  them across users. This is how most self-hosted storage works.
- **End-to-end encrypted (E2EE)**: opt-in per account. File contents and
  file and folder names are encrypted on your devices before upload. The
  server stores ciphertext and cannot read either.

## Assets

| Asset | Where it lives |
|---|---|
| File contents | Server: `blobs/` (chunks). Clients: the synced folder, decrypted. |
| File and folder names | Server: `nulldrive.db` (`files`, `file_versions`). Clients: the synced folder and the client's index. |
| Metadata: sizes, timestamps, folder structure, which device wrote what | Server database. |
| Account credentials | Password: server, as an Argon2id hash. Sessions and device tokens: server, as SHA-256 hashes; device tokens in the OS keychain on each computer. |
| E2EE master key | Never on the server unwrapped. Wrapped twice (passphrase and recovery key) in `user_keys`. Desktop: OS keychain. Browser: memory of the tab only. |
| TOTP secret | Server database, readable (the server must compute codes to check them). |
| Audit log | Server database (`audit_log`), 180 days. Contains usernames, IP addresses, device names. Never file names. |

## Attackers

### 1. Someone with your stolen or lost laptop

| | Standard | E2EE |
|---|---|---|
| Synced files | **Readable**: the sync folder is plain files on disk. | **Readable**, same reason. |
| Server access | They can keep syncing with the device token from the OS keychain if the OS account is unlocked. | Same, and the master key is in the same keychain. |

**Defence:** full-disk encryption (BitLocker, FileVault, LUKS) and an OS
login password. Then **revoke the device** in Settings → Devices: the token
stops working immediately and any open realtime connection is closed at
once. E2EE does not help here: a device that syncs your files has to be able
to decrypt them. (If you suspect the master key itself leaked, see
"Known limits".)

### 2. A compromised or curious server

This covers an admin who isn't you, a hosting provider, a thief who takes
the NAS or its disks, a backup of the data directory that ends up somewhere
it shouldn't, and an attacker who gains code execution on the server.

| | Standard | E2EE |
|---|---|---|
| File contents | Readable. | **Not readable.** XChaCha20-Poly1305 with keys the server never has. |
| File and folder names | Readable. | **Not readable.** Each path segment is encrypted. |
| Modified or swapped data | Undetected by clients beyond hash checks the server itself controls. | **Detected.** Every chunk and name is authenticated (Poly1305); a modified byte fails decryption, it never yields altered plaintext. |
| Folder structure, file sizes (to 1 chunk overhead), timestamps, number of files, which device uploaded what and when | Visible. | **Still visible.** See "What E2EE does not hide". |
| Offline guessing of the passphrase | n/a | Possible from the stored bundle; each guess costs one Argon2id evaluation (3 passes, 64 MiB). Use a long passphrase; the recovery key (160 random bits) can't be guessed. |
| Serving malicious JavaScript to the web app | Game over anyway. | **Can steal the key from browsers.** The web app is code the server sends; a malicious server can send code that exfiltrates the passphrase. The desktop app and CLI don't have this problem: their code isn't delivered by the server at runtime. |

**The precise E2EE promise:** a server that only *stores and serves* data
(including one that is stolen, imaged, or read by its admin) learns nothing
about contents or names. A server that is *actively malicious* can still
attack users of the **web app**, deny service, delete or roll back files, and
learn metadata. That is the honest limit of any E2EE system with a web
client (Proton, Bitwarden and others make the same caveat).

### 3. A network attacker

| Situation | Effect |
|---|---|
| HTTPS with a trusted certificate (reverse proxy) | Protected. HSTS is sent when the request arrived over TLS. |
| Self-signed HTTPS | Desktop app pins the certificate fingerprint on first use and refuses a changed one. Browsers show their own warning. |
| Plain HTTP on a LAN | Anyone on the network path can read session cookies, device tokens and (standard mode) file contents. The desktop app warns when it sees plain HTTP to a non-local address. In E2EE mode contents and names stay encrypted, but tokens and metadata don't. |
| Plain HTTP over the internet | Don't. Put a reverse proxy with Let's Encrypt in front (INSTALL.md). |

Cross-site attacks from the browser side:

- **CSRF:** the session cookie is `HttpOnly` and `SameSite=Strict`, *and*
  every state-changing cookie-authenticated request needs an
  `X-NullDrive-CSRF` token bound to the session. The token matters on
  homelabs, where `drive.home.example` and another self-hosted app on
  `other.home.example` are the *same site* to the browser, so SameSite
  alone would let a compromised sibling app forge requests.
- **Login CSRF and cross-origin writes:** requests with a foreign `Origin`
  or `Sec-Fetch-Site: cross-site|same-site` are refused for every
  state-changing API call, including login and setup.
- **WebSocket hijacking:** the realtime endpoint checks `Origin`.
- **XSS:** strict CSP (`script-src 'self' 'wasm-unsafe-eval'`, no inline
  script, `object-src 'none'`, `frame-ancestors 'none'`). API responses get
  `default-src 'none'; sandbox`, `nosniff` and `no-store`.
- **Clickjacking:** `frame-ancestors 'none'` and `X-Frame-Options: DENY`.

### 4. A malicious other user on the same server

- Each user only sees and can reference their own files and chunks. Chunk
  downloads, manifest commits and the "which chunks do you have?" check are
  scoped per user, so dedup does not reveal what anyone else stores
  (DESIGN.md › Phase 6).
- In standard mode the server dedups storage across users (the second
  uploader still has to send the bytes). In E2EE mode the same file
  encrypts differently for every account, so there is no cross-user dedup
  at all, and therefore no cross-user side channel.
- More accounts are created with `nulldrive add-user` (no self sign-up).
- **Search:** every account has its own index file (`indexes/<user id>.db`),
  and the file is chosen only from the authenticated user's id, never from
  the request. Tests check that one user's searches never return the
  other's files and that one user's index file contains none of the other's
  text.

### 5. Password guessing

- **Per-address rate limit:** 10 sign-ins per minute.
- **Per-account lockout with backoff:** after 5 consecutive failures, 30 s,
  doubling with each further failure up to 15 min. Unknown usernames are
  tracked the same way, and unknown users and wrong passwords take the same
  time, so neither the lockout nor timing reveals which accounts exist.
  Backoff instead of a hard lock: a hard lock would let anyone lock the
  owner out by knowing the username.
- **Two-factor authentication (TOTP):** optional. Wrong codes count towards
  the lockout; a used code can't be replayed (the last used time step is
  stored); 10 single-use recovery codes, stored hashed. Turning 2FA off
  needs the password *and* a code, so a stolen session can't remove it.
- **Pairing codes:** 30 bits, 5 minutes, single use, 5 claims/min per
  address and 30/min globally: at most ~150 guesses per code lifetime.

### 6. A stolen session or device token

- Sessions expire after 30 days; signing out deletes the session and closes
  that browser's realtime connection. Changing the password ends all other
  sessions and closes their connections.
- Device tokens can't do account-level things (add/revoke devices, turn
  encryption on, change settings, read the audit log).
- Revoking a device invalidates its token and drops its WebSocket at once.
- **Search-only tokens** (for Null Search, which keeps its token in browser
  storage): they can call `GET /search` and `GET /me` and nothing else, so a
  stolen one reveals file names and short text snippets, never whole files,
  and can't change anything. `/search` allows cross-origin reads only from
  the configured Null Search origins and never with cookies.
- Replacing the E2EE key bundle or finishing the migration needs an auth
  key derived from the master key (the server stores only its hash), so a
  stolen session can't swap or destroy the wrapped keys.

### 7. A malicious web page (nulldrive:// links)

The desktop app opens `nulldrive://open?id=<id>` links, and any page can try
to launch one (the browser asks first).

- The link is parsed strictly: one action, a 32-hex-character id, nothing
  else. The server resolves the id for the account this computer syncs; the
  client never takes a path from the link.
- The registered command is `NullDrive.exe --url "%1"`, and when a link is
  present every other argument is ignored, so a crafted link can't add
  `--forget` or `--quit` (tested with one that tries).
- A link never runs code: executables, scripts, installers, shortcuts and
  every type Windows' `AssocIsDangerous` lists (or, on macOS/Linux, files
  with the executable bit, and symlinks) are only shown in their folder.
  Documents open in their default app, as a double-click in the sync folder
  would.
- What a page can learn: nothing. It can't see whether the link worked, and
  ids are random 128-bit values it would need to know already.

### The search index at rest

Standard accounts' file names and up to 512 KiB of text per `.md`/`.txt`/
`.pdf` file sit in plaintext in `indexes/`, like the files themselves sit
in the chunk store; anyone who can read the data directory could already
read both. For E2EE accounts no index is kept: turning encryption on
deletes it, and it is never rebuilt (the server couldn't read the files
anyway). Backups of the data directory include `indexes/`; it can be left
out, because it is rebuilt automatically.

## What E2EE does not hide

- **Structure:** how many files and folders, how they nest, and which
  entries share a name (names are encrypted deterministically per account,
  so two items called `README.md` anywhere in the tree have the same
  ciphertext; see DESIGN.md for why).
- **Name length** to within 16 bytes (names are padded to 16-byte buckets).
- **Exact file sizes** (stored for quotas and the UI) and chunk sizes, which
  reveal content-defined boundaries. Someone holding a candidate file can't
  confirm you have it (that would need your key), but sizes are a weak
  fingerprint.
- **Within-account equality:** identical chunks in your own files are
  stored once, so the server sees that two of your files share data.
- **Timing and devices:** when files change, from which device, from which
  IP.
- **Your username, device names, the audit log.**

## What happens to old plaintext when you turn E2EE on

Turning encryption on with existing files re-uploads each one encrypted,
then deletes everything unencrypted: current files, their whole history and
Recently deleted. Specifically the server:

1. deletes those rows and the chunks only they used **immediately** (not
   after the usual 24 h grace period);
2. runs SQLite with `secure_delete`, so deleted rows are overwritten, then
   rebuilds the database file (`VACUUM`) and truncates the WAL.

This is verified by tests that scan every byte of the data directory for
known names and contents after a migration (Go integration test, the
browser end-to-end test, and the smoke test).

It can **not** undo copies that already exist elsewhere: earlier backups of
the data directory, filesystem snapshots (ZFS/Btrfs), or blocks the
filesystem or SSD hasn't overwritten yet. If that matters, enable E2EE
before uploading anything sensitive, and rotate out old backups.

Plaintext chunks also used by *another* account's files are kept (they are
that user's data).

## Known limits and non-goals

- **Web app trust** (above). Use the desktop app for the strongest guarantee.
- **Lost passphrase and recovery key = lost files.** By design there is no
  backdoor: the admin can reset your *login* password, not your encryption.
- **No key rotation.** Changing the passphrase or the recovery key re-wraps
  the master key; it does not re-encrypt data. If the master key itself is
  exposed (e.g. read from a device's keychain), the remedy today is to
  download everything, create a new account with new keys, and upload again.
- **No forward secrecy** for stored data (normal for file storage).
- **TOTP secrets are stored readable** on the server. Encrypting them with
  a key on the same disk would only look safer.
- **No turning E2EE off** in place yet (download, then re-upload to a
  standard account).
- **Plain HTTP is allowed** on LANs because many homelabs have no
  certificate; the app warns instead of refusing.
- **Denial of service** by the server or anyone controlling it is out of
  scope.
- **Side channels on the client machine** (malware, keyloggers) are out of
  scope.

## Cryptography summary

All primitives come from vetted libraries: `golang.org/x/crypto`
(Argon2id, XChaCha20-Poly1305), the Go standard library (HKDF, HMAC,
SHA-256) and libsodium in the browser (the same algorithms), with WebCrypto
for HKDF. The two implementations are pinned to the same test vectors.

| Purpose | Construction |
|---|---|
| Passphrase → key-encryption key | Argon2id, 16-byte random salt, t=3, m=64 MiB, p=1 (parameters stored with the bundle) |
| Recovery key → key-encryption key | HKDF-SHA256 (the recovery key is 160 random bits; no stretching needed) |
| Wrapping the master key | XChaCha20-Poly1305, random 24-byte nonce, purpose-bound associated data |
| Subkeys | HKDF-SHA256(master, info="nulldrive/v1/<purpose>") |
| Chunks and names | XChaCha20-Poly1305 with a synthetic nonce = HMAC-SHA256(siv-key, ad‖0‖plaintext)[:24] (SIV construction): deterministic per account |
| File hash stored by the server | HMAC-SHA256(file-hash key, SHA-256(plaintext)) |
| Proof of key possession | auth key = HKDF(master, "auth"); server stores SHA-256(auth key) |
| TOTP | RFC 6238, HMAC-SHA1, 6 digits, 30 s, ±1 step, RFC test vectors in the test suite |

## Automated checks

- Go tests: crypto round trips, every single-bit tamper of a chunk, wrong
  passphrase and recovery key, purpose binding of wraps, cross-account
  isolation, server refusal of plaintext writes on encrypted accounts,
  migration, plaintext scan of the data dir; lockout and backoff, per-IP
  limits, TOTP (vectors, replay, recovery codes), CSRF, Origin guard,
  headers including HSTS only over TLS, audit log, revocation dropping
  sockets.
- Browser tests: byte-identical results with the Go vectors, FastCDC
  boundaries identical to Go, and an end-to-end Playwright run that turns
  encryption on, migrates, uploads, downloads, unlocks and scans the data
  directory.
- CI: `govulncheck` (server, shared packages, desktop client), `npm audit`
  for both frontends, CodeQL (Go and TypeScript, security-extended
  queries), Dependabot for every ecosystem, weekly scheduled scans.
