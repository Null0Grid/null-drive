# HANDOFF: Null Notes in end-to-end encrypted accounts, Drive side (Phase 8)

**Status:** done. Null Notes can back up and sync into E2EE accounts by following **`docs/NOTES-E2EE.md`**, published to the Project as **`null-grid/notes-e2ee-drive-api.md`**. The server-side protocol already existed (Drive's own E2EE). This phase wrote it down for a third-party client and tested it from the outside. One small additive API change.

| Asked | Answer (doc section) |
|---|---|
| Getting and unlocking the account key | `GET /api/v2/e2ee` → bundle. Argon2id (t/m from the bundle, p=1) → XChaCha20-Poly1305 unwrap (AD `nulldrive/v1/wrap/passphrase`), or recovery key → HKDF → unwrap. Seven HKDF subkeys; verify and pin the fingerprint; keep the master key in the keychain (§1) |
| Per-file format | names: per segment, ISO 7816-4 padding to 16, deterministic SIV-XChaCha20-Poly1305, `~` + base64url. Content: any chunking, each piece sealed `0x01 ‖ SIV(…)` (+41 bytes), ≤ 16 MiB sealed; empty file = no chunks (§2) |
| What to send / what the server sees | `size` = plaintext; `sha256` = HMAC(file-hash key, SHA-256(plaintext)); chunk hash = SHA-256(sealed); `mtime` optional and plaintext (§3) |
| If-Match / If-None-Match / move / `?since=` | all unchanged, with encrypted paths in and out; `moved_to` is encrypted; v1 whole-file calls → use v2 chunks + manifest (§4) |
| Listings | encrypted `path`/`moved_to`, decrypt per segment, skip rows that don't decrypt (plaintext leftovers while `migrating`), sort after decrypting (§4.3) |
| `synced_id`, search | **confirmed:** `synced_id` never set; search returns `[]` + `X-NullDrive-Search: encrypted` (§5) |
| Test vector | `shared/e2ee/testdata/notes-vector.json`, printed in §2.4: master key, subkeys, `Null Notes/Work/Plan.md` per segment, an 81-byte note, sealed chunk, hashes, commit body |

**For Null Notes:**
- Add `X-NullDrive-E2EE: 1` to every request.
- Replace v1 upload/download with chunks + commit / manifest + chunks.
- Match files by Drive `id` + decrypted path.
- Keep all of Phase 7's conflict handling as it is.
- `scripts/e2ee-reference/nulldrive_e2ee.py` is a working example of every step.

**Previous handoff:** Notes sync in `docs/handoffs/phase-7-notes-sync.md`.

## First thing to do (owner)

```bash
make test && make lint && make e2e && make smoke
make e2ee-ref        # needs: pip install pynacl
```
No migration.

## What was built and where

| Path | What |
|---|---|
| `shared/api/types.go`, `ui/src/lib/api/types.ts` | `Manifest` gains optional `id`, `mtime`, `synced_id` (additive) |
| `server/internal/api/handlers_v2.go` | manifests fill them (version manifests: `mtime`, `synced_id`) |
| `shared/e2ee/notes_vector_test.go`, `testdata/notes-vector.json` | **TestNotesVector**: generates (`-update`) and pins the worked example; checks that it decrypts back and shares the master key with `vectors.json` |
| `scripts/e2ee-reference/nulldrive_e2ee.py`, `run.sh`; `make e2ee-ref` | independent Python client: `vectors` checks both vector files (including bundle unlock by passphrase and recovery key); `live` runs the Notes flow against a fresh server |
| `server/internal/api/e2ee_thirdparty_test.go` | **TestNotesE2EEFlow**: the doc as a raw-HTTP test |
| Docs | `docs/NOTES-E2EE.md` (new); API.md (manifest fields, E2EE header / refusal summary); DESIGN.md › Null Notes in encrypted accounts |

## How it was tested

- **TestNotesE2EEFlow** uses raw HTTP and only the e2ee primitives:
  - **Unlock:** a migrating account with a device token; status and bundle; unlock by passphrase and recovery key; a wrong passphrase is refused.
  - **Refusals:**
    - `e2ee_client_required` for a listing and a manifest without the header;
    - `e2ee_required` for a v1 PUT and GET;
    - `e2ee_path_required` for a commit, folder creation, and a move whose source is plaintext.
  - **Create-only:** `201` with a multi-chunk note and mtime (encrypted path, the keyed hash stored, ETag); `409 conflict` with `current`; an identical retry is `200`; no precondition gives `428`; a wrong size gives `400 invalid_manifest`; an empty note works.
  - **Listing:** prefix listing and decryption, with no `synced_id`; the full listing includes the plaintext leftover.
  - **Download:** manifest plus chunks, decrypted and verified; `id`, `mtime` and `version` are present; `/ids/{id}` works.
  - **Replace:** with `If-Match`; a stale version gives `409` with the ETag.
  - **Move:** a stale version gives `409`; success keeps the id; the feed has a tombstone whose `moved_to` is the encrypted path.
  - **Delete:** a stale version gives `409`; the feed tombstone keeps the id; the trash shows the encrypted path.
  - **Search:** `[]` with the `encrypted` header.
  - **Server side:** no plaintext name segment reaches the server.
- **Python reference:**
  - every value in `vectors.json` and `notes-vector.json` matches;
  - the live flow against `bin/nulldrive` passes (setup, turn encryption on with a Python-made bundle, device token, join, refusals, create-only, empty note, list, download, conflict, move, delete, feed, search).
- **Full suites:** all Go under `-race`, including the desktop sync engine's E2EE integration tests (unchanged); golangci-lint; client tests; vitest + svelte-check; Playwright e2e; smoke.

## Known issues / next steps

- `mtime` is plaintext metadata on encrypted accounts. It's optional: Notes can leave it out and detect changes by `sha256`/`version` alone.
- Drive doesn't normalize Unicode in names (no client does), so NFC and NFD spellings are different files. Notes should send names byte-for-byte as it stores them.
- Turning encryption on, changing the passphrase and migrating stay with the Drive apps; Notes only joins.
- No key rotation (as before, see SECURITY.md).

## Draft resume bullet

- Published an end-to-end encryption client protocol for a sibling app (Argon2id/HKDF key hierarchy, deterministic XChaCha20-Poly1305 names and chunks). Validated it with an independent Python implementation written from the spec, cross-language test vectors, and a raw-HTTP conformance test, with no change in behavior for existing clients.
