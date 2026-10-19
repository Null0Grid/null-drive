# HANDOFF — Phase 7: End-to-end encryption + security hardening

**Status:** complete and tested. Accounts can turn on **end-to-end encryption**: file contents *and* names are encrypted on the device (desktop app, CLI or browser) before upload, and the server stores only ciphertext. Delta sync, dedup within the account, history, trash and conflicts keep working. Turning it on migrates existing files and then deletes every plaintext copy; tests scan every byte of the data directory afterwards and find no names or contents. The security pass added 2FA, login lockout, CSRF tokens, stricter headers, an audit log, and the CI security scans. The threat model is in `docs/SECURITY.md`.
**Next phase:** Phase 8, packaging, release, docs and resume polish.

## First thing to do (owner)

Go modules still can't be downloaded here, so I used two stand-ins: **the real `golang.org/x/crypto`** (from its GitHub mirror) and the pure-Go SQLite driver `github.com/ncruces/go-sqlite3` in place of `modernc.org/sqlite` (same DSN parameters). The desktop app compiled against stubs for Wails, systray and keyring, plus the real fsnotify. One new Go dependency: `golang.org/x/term` (the CLI's hidden password prompt). One new npm dependency: `libsodium-wrappers-sumo` 0.8.4 (web UI only, lazy-loaded).

```bash
go mod tidy && (cd client && go mod tidy)
cd ui && npm ci && cd ..
make test && make lint && make e2e && make smoke     # make screenshots to refresh images
```

**Upgrading a server:** migration `0005` adds tables and columns (no data changes). Back up first as usual. Phase 4–6 desktop clients keep working on standard accounts. On an encrypted account they stop with "update Null Drive" instead of syncing ciphertext.

## What was built and where

| Path | What |
|---|---|
| `shared/e2ee/keys.go` | Key hierarchy: Argon2id passphrase KEK, HKDF recovery KEK, purpose-bound XChaCha20-Poly1305 wraps of a random master key, HKDF subkeys, auth key, fingerprint, Crockford recovery-key codec, passphrase change, recovery-key rotation, the **SIV construction** (HMAC-derived nonce + XChaCha20-Poly1305) |
| `shared/e2ee/content.go` | Deterministic per-account chunk sealing (ID = SHA-256 of sealed bytes), name encryption (16-byte padding, `~`+base64url), path helpers, `IsEncryptedName` shape check used by the server, file-hash HMAC |
| `shared/e2ee/remote.go` | `e2ee.Remote`: wraps the API client so the sync engine sees plaintext paths; never falls back to whole-file transfers |
| `shared/e2ee/account.go` | `TurnOn`, `Unlock`, `ChangePassphrase`, resumable `Migrate` (refuses to finish if a name can't be encrypted) |
| `shared/e2ee/testdata/vectors.json` | Cross-implementation fixture (Go ⇄ TypeScript): names, paths, chunks, file hashes, a bundle + passphrase + recovery key |
| `shared/client/chunked.go`, `e2ee.go` | `TransferOptions.Codec` (seal/open every chunk, plaintext offsets, verified end to end), `X-NullDrive-E2EE` header, E2EE endpoints, `LoginTOTP` |
| `shared/sync/` | `Engine.ContentHash` / `ScanWith` (merge in the keyed hash space), `Index.UseKeyFingerprint` (reset when the key changes, so files are re-adopted without conflicts) |
| `shared/chunker/cross_test.go` + `testdata/cdc-vectors.json` | Chunk boundaries pinned for the browser port |
| `shared/pathutil` | Longer limits for encrypted segments only (396 bytes per segment, 16 KiB per path) |
| `server/internal/db/migrations/0005_security_e2ee.sql` | `user_keys`, TOTP columns, `totp_recovery_codes`, `audit_log`; DSN adds `secure_delete` |
| `server/internal/meta/e2ee.go` | Enable (on / migrating), bundle update with auth-key check, `FinishE2EE` purge, `DeleteChunkIfOrphaned`, `Compact` (VACUUM + WAL truncate) |
| `server/internal/meta/{totp,audit}.go`, `auth/totp.go` | TOTP storage + RFC 6238 (RFC vectors), recovery codes, audit log store with pruning |
| `server/internal/api/handlers_e2ee.go` | `/api/v2/e2ee` endpoints; enforcement: E2EE-aware clients only, no whole-file transfers, encrypted names only (commit, folder, move from/to, restore), plaintext-size check with chunk overhead |
| `server/internal/api/security.go` | Lockout with backoff, CSRF tokens (HMAC of the session), Origin/`Sec-Fetch-Site` guard, security headers + HSTS only over TLS, audit helper, `GET /admin/audit` |
| `server/internal/api/handlers_{auth,totp,…}.go` | Login: per-IP limit → lockout → password → 2FA; TOTP setup/enable/disable; audit events everywhere relevant; pairing response carries the wrapped bundle |
| `server/internal/realtime/hub.go` | Browser sessions tracked on sockets: sign-out and password change close them (device revoke already did) |
| `server/cmd/nulldrive` | `nulldrive disable-2fa <user>` (lost phone *and* recovery codes) |
| `cmd/nulldrive-e2ee/` | **CLI:** `status`, `enable` (+ migrate), `migrate`, `verify`, `passphrase`, `recovery-key`; prompts without echo; env vars for scripts |
| `client/e2ee.go`, `app.go`, `config.go` | Desktop: resolves account state, keeps the master key in the OS keychain, `UnlockE2EE`, `RetrySync`, pauses while another device migrates, notices when the account becomes encrypted mid-sync, removes the key on sign-out |
| `client/frontend/src/components/UnlockCard.svelte` | Desktop unlock / "encryption being turned on" card |
| `ui/src/lib/crypto/` | Browser crypto twin: libsodium (lazy WebAssembly) + WebCrypto HKDF, FastCDC port with 64-bit Gear in two 32-bit halves, recovery-key codec |
| `ui/src/lib/drive.ts` | `Drive`: the pages' file API; encrypts and decrypts in the browser when unlocked (batched chunk upload with bounded memory, verified download, migration) |
| `ui/src/web/vault.svelte.ts`, `pages/Unlock.svelte` | Key in memory only; unlock with passphrase or recovery key (then optionally set a new passphrase); resumes an interrupted migration |
| `ui/src/web/components/EncryptionSetup.svelte`, `RecoveryKeyBox.svelte`, `EncryptionCard.svelte` | Turn-on wizard (recovery key confirmed **before** anything is sent), download/print/copy, change passphrase, new recovery key, lock |
| `ui/src/web/components/TwoFactorCard.svelte`, `AuditCard.svelte`, `pages/Login.svelte`, `pages/Setup.svelte` | 2FA setup with QR + recovery codes, login code step, security log, optional encryption step in the setup wizard |
| `ui/src/lib/api/client.ts` | CSRF header from login/`/me`, E2EE header, v2 chunk/commit/manifest calls, 2FA, audit |
| `.github/workflows/security.yml`, `codeql.yml`, `release.yml` | govulncheck (server + desktop), npm audit (both frontends), CodeQL Go + TS (security-extended), weekly; releases also wait for security |
| `docs/SECURITY.md`, `SECURITY.md` | Threat model (assets, four attackers, per-mode table, what E2EE doesn't hide, known limits) and the vulnerability-reporting policy |
| `docs/DESIGN.md` › Phase 7 | Diagram, SIV construction, why not convergent encryption, names per segment, per-content instead of per-file keys, migration order, pairing and why the code can't be a key, web-app trust limits, the security pass |
| `docs/API.md`, `INSTALL.md`, `README.md`, `scripts/smoke.sh` | New endpoints, error codes, CSRF/E2EE conventions; E2EE + 2FA how-to; README section with screenshots; smoke covers headers, CSRF, Origin, audit, and E2EE through the CLI |

## How to run and test

```bash
make run                                   # Settings → End-to-end encryption / Two-factor authentication / Security log
make test && make lint && make e2e && make smoke
go test ./shared/e2ee -run TestVectors -update && go test ./shared/chunker -run TestCrossVectors -update   # only if the formats change on purpose
bin/nulldrive-e2ee -server http://localhost:8080 -user admin status
```

What I verified here:
- **Go:** `go test -race` on every package, `-count=3` on api, synctest and e2ee; golangci-lint **0 issues**; gofmt clean; `go vet` incl. windows/darwin cross-vet of `shared/` and `cmd/`; the desktop module builds, vets and tests against the stubs.
- **UI:** Vitest **128/128** (web) and **18/18** (desktop); svelte-check 0/0 in both; `npm audit` **0 vulnerabilities** in both.
- **Playwright:** **8/8** e2e. New: 2FA set up → sign in with a recovery code → turn off; E2EE turned on with existing files → in-browser migration → server lists only `~…` names, old clients get 409 → upload + download decrypted → reload → wrong passphrase → unlock → recovery key → **data dir scanned for names/contents: none**.
- **Smoke test** passed, including E2EE via the CLI and the plaintext scan. Screenshots regenerated and reviewed.

Key tests, by area:
- **Crypto:** round trips, determinism, **every single-bit flip of a sealed chunk rejected**, truncation/extension, wrong passphrase and recovery key, swapped wraps, fingerprint mismatch, weak KDF rejected, passphrase change keeps the recovery key, rotation kills the old one, cross-account isolation, Go ⇄ TS vectors (incl. Argon2id unlock of a Go bundle in the browser code), FastCDC boundaries identical in TS.
- **Server enforcement:** old client 409, v1 upload 409, plaintext folder/commit/move 409, ciphertext size passed off as plaintext size rejected, bundle change without auth key 403, double enable 409, device token can't enable, pairing returns the wrapped bundle and it unlocks.
- **End to end (Go):** two encrypted devices sync, edit, rename, delete and conflict correctly; migration of a plaintext account incl. history and trash; the old device re-adopts **with zero transfers and zero conflicts**; data-dir scan with a control check that it does find plaintext before.
- **Security:** lockout 30 s → 60 s backoff, case-insensitive, unknown users too; per-IP limit; TOTP RFC vectors, replay, skew, recovery codes single-use, 2FA failures lock; CSRF (missing/wrong/cross-session token, bearer exempt, `/me` returns it); Origin guard table; headers incl. HSTS only over TLS and not from a spoofed `X-Forwarded-Proto`; audit sequence and paging; sign-out closes the browser socket.

## Known issues / shortcuts

- **CI security scans weren't run here.** The sandbox can't reach the Go vulnerability database or GitHub Actions. `npm audit` was run (clean). The workflows are YAML-validated; check the first run of `security` and `codeql` after pushing (CodeQL needs code scanning enabled, which is free on public repos).
- **The web app's E2EE trusts the JavaScript the server sends.** That's stated in SECURITY.md and DESIGN.md. The desktop app and CLI don't have this limit.
- **Sizes, folder structure, equal names (within the account) and timing stay visible** to the server. By design and documented.
- **No master-key rotation, no turning E2EE off in place, no passphrase-free device-to-device key transfer** (a design for a SAS-verified X25519 exchange is in DESIGN.md).
- The browser migration holds one file in memory at a time (a 10 GB file would be a problem; use the CLI for big accounts, as INSTALL.md says).
- `ChunkOverhead` (41 bytes) is a protocol constant the server knows; changing the chunk format means a new `FormatVersion`.
- The lockout state is in memory (cleared by a restart, which an attacker can't trigger). Fine for one server; a cluster would need it in the DB.
- TOTP secrets are stored readable on the server (documented: there's no key to protect them with that isn't on the same disk).
- Pre-existing gosec warnings in `client/platform.go`/`app.go` (folder permissions, `exec` of the OS opener); CI lints only the root module. Phase 8 can tidy them.

## What Phase 8 needs to know

- New binary: **`nulldrive-e2ee`** (built by `make build` into `bin/`). Release packaging should ship it next to `nulldrive` (`scripts/build-release.sh` doesn't yet).
- New admin subcommand: `nulldrive disable-2fa <user>`.
- Migrations are at **0005**. New settings key: `csrf_key` (created on first start).
- The release workflow now depends on `security.yml`, and Dependabot already covers every ecosystem.
- The desktop app's state has `e2ee` (`off`/`unlocked`/`locked`/`migrating`); store listings/screenshots could show the unlock card.
- Version strings: the desktop client still sends its own version via User-Agent; consider bumping to 0.7.0 when tagging.
- Resume material: SECURITY.md and DESIGN.md › Phase 7 are written to be talked through in an interview (convergent vs. per-account SIV, why the pairing code can't carry a key, web-crypto trust limits).

## Draft resume bullets

- Designed and implemented optional end-to-end encryption for a self-hosted file-sync system in Go and TypeScript (Argon2id → wrapped master key → HKDF subkeys, XChaCha20-Poly1305, encrypted file names, a recovery key confirmed before anything is sent). Kept delta sync and deduplication working by sealing chunks deterministically per account (SIV construction) instead of convergent encryption, so the server can't confirm what files a user holds. Byte-identical Go and browser implementations are pinned by shared test vectors.
- Built a zero-plaintext migration and a security hardening pass: existing files are re-encrypted client-side, and the server then purges every plaintext copy, its history and orphaned chunks (secure_delete + VACUUM), verified by tests that scan the whole data directory. Added TOTP 2FA, per-account lockout with backoff, CSRF tokens, CSP/HSTS, an audit log, govulncheck/CodeQL/npm-audit CI gates and a written threat model.
