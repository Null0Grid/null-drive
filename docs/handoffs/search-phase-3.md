# HANDOFF — Cross-product search, Phase 3 (Null Drive server)

**Status:** done. Null Drive implements the Null franchise search contract v1: `GET /search?q=&limit=` with a **separate SQLite FTS5 index file per user** (`<data>/indexes/<user id>.db`), filenames and the text of `.md`/`.txt`/`.pdf` indexed on upload, the user's auth token required (only their index is ever opened), and CORS for the Null Search origin (with the Private Network Access preflight).
**Done-when, checked:** user A can't find user B's files and vice versa (by name or content), and filename + content search works for each. Checked by Go tests, by the e2e suite, and by a script against the real binary in Chromium with two accounts (the second made with `nulldrive add-user`) and a cross-origin `fetch` from another origin.
**Previous handoff** (Phase 8 part 1, Windows installer + logos): `docs/handoffs/phase-8-part1.md`. The rest of Phase 8 (Store, winget submission, signing, Linux/macOS) is unchanged and still to do.

## First thing to do (owner)

```bash
go mod tidy                      # adds the go.sum lines for github.com/ledongthuc/pdf (new dependency)
make test && make lint && make e2e && make smoke
```

Then connect Null Search: web app → **Settings → Devices → Connect Null Search → Create token**, and paste the server URL + token into Null Search's *Drive* settings. Null Search is HTTPS, so Drive must be reachable over **HTTPS** from the browser (reverse proxy, see INSTALL.md); a plain `http://192.168…` address is blocked as mixed content. Running Null Search from a dev server? `NULLDRIVE_SEARCH_ORIGINS=https://search.nullgrid.dev,http://localhost:3000`.

## What was built and where

| Path | What |
|---|---|
| `server/internal/search/index.go` | `Manager`: one SQLite DB per user, built from the server's **change feed** (remembers the last version applied, catches up after restarts, rebuilds if deleted, on a schema change or if the database was restored from an older backup). Kicked on every change; each search waits ≤700 ms for it to catch up (Null Search's timeout is 1.2 s). Ranking, snippets, `synced_id` |
| `server/internal/search/query.go` | Query parsing: FTS5 trigram (case-insensitive substring) + separator-tolerant words (`libre ai` = `libre-ai` = `LibreAI`) + squashed-name match (`libreai` → `Libre AI.md`) + `LIKE` for words under 3 characters. User input is always quoted |
| `server/internal/search/extract.go` | Text from Markdown (frontmatter stripped, `id:` kept), text and PDF (`github.com/ledongthuc/pdf`, pure Go, with recover + 20 s limit). Skips Null Notes locked notes (`NNLOCK1\n`), `*.nntmp`, `.nullnotes/` (but reads `ids.json`) |
| `server/internal/api/handlers_search.go` | `GET /search` and `/api/v1/search`; CORS (allowlist, GET + Authorization only, no credentials, PNA); search-only token scope |
| `server/internal/db/migrations/0006_search.sql`, `meta/files.go` | **Stable file ids** (`files.uid`): survive rename/move/overwrite/restore; now also `id` in file listings |
| `meta/meta.go`, `api/handlers_auth.go`, `api/middleware.go` | Device token **scope** `full` / `search`; search tokens get `403 insufficient_scope` outside `/search` and `/me` |
| `server/cmd/nulldrive` | `nulldrive add-user <name>` (prints a random password); `NULLDRIVE_SEARCH_ORIGINS` (default `https://search.nullgrid.dev`, or `off`); indexes closed on shutdown |
| `api/handlers_e2ee.go` | Turning on end-to-end encryption deletes the account's index; encrypted accounts are never indexed (`[]` + `X-NullDrive-Search: encrypted`) |
| `ui/src/web/pages/Settings.svelte` | **Connect Null Search** button; token dialog with *Everything / Search only*; shows server URL + token to paste; "Search only" badge in the device list |
| Docs | `docs/API.md` › Search (+ scope, error codes); `docs/DESIGN.md` › Cross-product search; `docs/INSTALL.md` › More accounts, Search from Null Search, env var, backups; `docs/SECURITY.md` (other users, search tokens, the index at rest); README section + config row; screenshots `search-token-choice.png`, `search-token.png` |

## How it was tested

- **Go** (all packages, `-race`): `api/search_test.go`: per-user isolation by name and content for every file type, the index files themselves contain no other user's text; result shape; limits; rename/move/overwrite/delete follow; Null Notes vault `synced_id` (frontmatter and `ids.json`), locked notes and temp files skipped; scope enforcement on 8 endpoints; CORS preflight/PNA/unlisted origins/no CORS on other APIs; E2EE drops the index and never recreates it; restart catch-up, deleted index, schema change, restored-older-database rebuild (verified to fail without the fix). `search/extract_test.go`: frontmatter, PDF (generated fixture, real parser), broken PDF, lock files, clipping. `meta/uid_test.go`: stable ids. Config: origin parsing.
- **E2EE plaintext scan:** the sync suite now runs with search on; the control step confirms the scan *finds* plaintext in `indexes/` before encryption, and the full data-directory scan finds nothing after.
- **E2E (Playwright, real binary):** 8/8, incl. connecting Null Search in the UI, searching with the shown token, the token refused on `/api/v1/files`, and the preflight. Screenshot suite 7/7.
- **Real binary + Chromium script:** two accounts, uploads over HTTP, cross-origin fetch from another origin returns results; an unlisted origin and the files API are blocked by the browser.
- golangci-lint 0 issues, svelte-check 0/0, Vitest 128/128, smoke test, client tests.

## Known issues / next steps

- **`nulldrive://open?id=` does nothing yet.** The desktop app doesn't register the `nulldrive://` scheme (pairing links are pasted). Next: a protocol handler in the installer + single-instance forwarding (like `--quit`), resolving the id to a local path (ids are already in file listings).
- Not indexed: Office documents, scanned PDFs (no OCR), PDFs over 64 MB (name only); no typo tolerance (same as the FTS5 trigram reference).
- `go.sum` needs `go mod tidy` for the new PDF module (the sandbox used `GOFLAGS`/`GONOSUMDB`).
- Null Search side: its Drive integration (Phase 4) already sends `Authorization: Bearer` to `<server>/search`, which matches. Pair it with a search-only token.

## Draft resume bullets

- Built per-user full-text search for a self-hosted file sync server (Go, SQLite FTS5 trigram): one index database per account derived only from the authenticated identity, kept current from the server's change feed (self-healing after restarts, deleted indexes or restored backups), with PDF/Markdown text extraction hardened against hostile files.
- Exposed it through a cross-product search contract consumed directly from the browser by a separate web app: least-privilege search-only tokens, an origin-allowlisted CORS/Private Network Access policy, and automatic index destruction when an account switches to end-to-end encryption, verified by byte-level scans of the data directory.
