# Null Drive — Design Notes

Short notes on *why* things are the way they are. Each section: the decision, the reasoning, the trade-off. New phases append sections.

---

## Phase 1 — Server core

### Architecture at a glance

```
            HTTP (REST, JSON + raw bytes)
                      │
   ┌──────────────────▼───────────────────┐
   │ api: routing, middleware, handlers   │  auth, request IDs, logging, errors
   └───────┬──────────────────────┬───────┘
           │                      │
   ┌───────▼────────┐     ┌───────▼────────┐
   │ meta (SQLite)  │     │ storage (disk) │
   │ users, devices │     │ blobs/ab/cd/   │
   │ files → sha256 │────▶│   <sha256>     │
   └────────────────┘     └────────────────┘
```

Metadata and content are separate stores joined by a hash. Code under `shared/` (path rules, wire types) is used by the server now and by the desktop client in Phase 4.

### Content-addressed storage

**Decision.** File bytes are stored as blobs named by their SHA-256 (`data/blobs/ab/cd/<sha256>`), never under the user's path. The `files` table maps `(user, path) → sha256`.

**Why.**
- **Free deduplication.** Identical content is stored once, whoever uploads it. This is the foundation for Phase 6 chunk-level dedup.
- **Renames and moves are metadata-only.** Moving a 20 GB folder updates rows; no bytes move.
- **Integrity for free.** The name *is* the checksum; a blob can be re-verified at any time. Clients can send `X-Content-SHA256` and the server refuses mismatched uploads.
- **No path-injection surface on disk.** User paths never touch the file system, so a traversal bug in path handling cannot write outside the data dir. Blob paths are built only from validated 64-hex strings.
- **Atomic writes.** Uploads stream to `data/tmp/`, are fsynced and hashed, then `rename(2)`d into place. A crash leaves at worst a temp file (cleaned at startup), never a half-written blob under a valid name.

**Fan-out.** Two levels of two hex characters gives 65,536 leaf directories, so even millions of blobs keep directories small for ext4/NTFS lookups and tools like `ls` or `rsync`.

**Trade-offs.**
- The data dir is not browsable as a normal folder tree. Users who want that should use the client. Backups are still trivial: copy the whole data dir (DB + blobs) together.
- Overwritten content leaves an unreferenced blob behind. Phase 1 does not delete blobs; Phase 6 adds reference counting and a GC with a grace period. Keeping old blobs is also what makes version history cheap later.
- Cross-user dedup is a (small) side channel: a user could, in theory, time an upload to learn whether some content exists. Acceptable for a self-hosted, few-user server; revisited for E2EE in Phase 7.

### Why SQLite

**Decision.** One SQLite file (`data/nulldrive.db`) via `modernc.org/sqlite`, a pure-Go port.

**Why.**
- **Zero-ops.** No database server to install, configure or upgrade: matches the "one Docker command" goal. Backup = copy the file (or `sqlite3 .backup`).
- **Pure Go, no CGO.** `CGO_ENABLED=0` gives a static binary that cross-compiles to linux/arm64 for a Raspberry Pi and runs on a distroless image.
- **Right-sized.** A personal or family server has a handful of writers. SQLite in WAL mode handles thousands of small transactions per second, far more than sync metadata needs.
- **Real transactions.** Everything correctness-critical (version checks, the change counter, setup) relies on ACID transactions.

**Connection settings** (`server/internal/db/db.go`): WAL (readers don't block the writer), `busy_timeout=5000` (wait rather than fail on a lock), `foreign_keys=1` (off by default in SQLite), `synchronous=NORMAL` (safe under WAL; at most the last transaction is lost on power failure, never corruption), and `_txlock=immediate`.

**`BEGIN IMMEDIATE` is the key one.** A default (deferred) transaction starts as a reader and upgrades to a writer on its first write. Two such transactions that both read then try to write can deadlock, and one fails with `SQLITE_BUSY`. Taking the write lock at `BEGIN` makes writers queue on `busy_timeout` instead. It also makes every *read-check-write* sequence (compare `If-Match`, then update) race-free with no application-level locks. `TestConcurrentUpdatesOneWinner` fires 8 concurrent writers at the same version: exactly 1 wins, 7 get 409.

**Trade-off.** Single writer. If Null Drive ever needed many concurrent heavy writers (multi-tenant hosting), the `meta` package is the only place that would change (e.g. to Postgres).

**Migrations** are numbered SQL files embedded in the binary (`go:embed`) and applied on startup, each in its own transaction, tracked in `schema_migrations`. No external migration tool, no files to ship next to the binary.

### Global change counter (sync cursor)

**Decision.** One monotonic counter (`change_counter`) for the whole server. Each create/update/delete/move takes the next value inside the same transaction, and stamps it on every row it changes. `GET /files?since=N` returns rows with `version > N`.

**Why a single counter** rather than timestamps or per-file versions?
- **Clocks lie.** Timestamps from different machines (or an NTP jump on the server) can go backwards; a counter cannot.
- **One cursor per client.** A client remembers one number and asks "what changed since N?". This is the same idea as a database replication log position or Dropbox's cursor.
- **It also serves as the per-file version** for optimistic concurrency. Because the counter is global, a version number is never reused, even after delete and re-create, so an old `If-Match` can never accidentally match a newer file.

**Subtle ordering detail.** `ListFiles` reads the counter *before* querying rows. Counter bumps commit atomically with the rows they stamp, so every change ≤ the returned value is already visible. Reading in the opposite order could hand the client a version newer than the rows it got, and the client would skip that change forever. The safe order means a change can at worst be sent twice (harmless: clients apply idempotently).

**Gaps.** The counter is global, so one user's feed has gaps where other users changed things. Clients treat versions as opaque, ordered cursors, so gaps don't matter.

### Tombstones (soft delete)

**Decision.** `DELETE` sets `deleted_at` and bumps `version`; the row stays.

**Why.** Sync needs to *propagate* deletes. If the row vanished, a client that was offline during the delete would see the file missing from the server, still have it locally, and could not tell "deleted on server" from "new local file not uploaded yet". It would re-upload, resurrecting the deleted file. With a tombstone in the change feed, the client knows to delete its copy.

Moves are modelled as tombstone(old path) + upsert(new path) in one transaction with one version, so the same machinery tells other clients the old path is gone. Folder deletes and moves tombstone every file inside, still with one version.

Recreating a deleted path reuses the row (clears `deleted_at`, new version). Tombstones are kept forever in Phase 1; Phase 6's retention policy will prune them after a window (a client offline longer than that does a full rescan).

### Optimistic concurrency: `If-Match`, 409 and 428

**Decision.** Uploads, deletes and moves can carry the version the client last saw (`If-Match: "7"`). If the file has changed since, the server answers `409 Conflict` and includes the current metadata. Overwriting an existing file with no `If-Match` at all is refused with `428 Precondition Required`.

**Why optimistic, not locks.** Clients are often offline or on flaky links; a held lock from a laptop that went to sleep would block everyone. Optimistic concurrency never blocks: it detects the conflict at write time and lets the *client* resolve it. That fits goal 3, "never silently lose data": in Phase 4 the client turns a 409 into a "keep both" conflict copy instead of overwriting.

**Why 428 for blind overwrites.** Without it, a buggy or naive client that forgets `If-Match` silently becomes last-writer-wins and can destroy someone else's edit. Making the precondition mandatory for overwrites (but not for creating new files) turns that bug into a loud error. HTTP has a status for exactly this (RFC 6585).

**Details worth knowing:**
- `If-Match: 0` means "create only, fail if it exists".
- Editing a file that was deleted on the server (client sends its old version; the server has a tombstone with a newer one) is a 409: the edit/delete conflict surfaces instead of the delete silently winning.
- **Idempotent retries.** If the content is identical to what's stored, the PUT succeeds with no new version, whatever `If-Match` says. That covers "my upload succeeded but the response was lost; I retried with the old version". With `X-Content-SHA256`, the server recognises this before reading the body.
- **Fail before upload.** Preconditions are checked against current metadata *before* reading the request body, so a 2 GB upload that would 409 is rejected immediately (`Expect: 100-continue` clients don't even send it). The authoritative check is repeated inside the write transaction. If a race slips between the two, the uploaded blob is simply unreferenced until GC.
- File/folder collisions (`a` is a file and someone uploads `a/b`) are rejected with `409 path_collision`, because no file system can represent both.

### Paths: validated, never trusted

All user paths go through `shared/pathutil.Clean`: reject `..`, absolute paths, drive letters, null bytes and control characters; normalize `\` to `/`; drop empty and `.` segments.

It also enforces **Windows' naming rules for everyone** (`<>:"|?*`, trailing dot/space, `CON`/`NUL`/`COM1`…). The desktop client is Windows-first; a file the server accepts but Windows cannot create would be an unsyncable file on every Windows device. Rejecting it at upload is kinder than failing forever in a sync loop.

**Routing detail.** Go's `ServeMux` cleans URL paths and *redirects* `/files/a/../b` to `/files/b`. For a file API that silently turns a bad path into a different valid one. File routes are therefore dispatched by hand from the raw escaped path, so `..` and `%2e%2e` reach `Clean` and get a `400`. Tests cover encoded, double-encoded-looking and backslash variants.

The rules live in `shared/` so the Phase 4 client validates identically before uploading.

### Authentication

**Passwords: argon2id**, RFC 9106's memory-constrained profile (t=3, 64 MiB, 4 lanes). Argon2id is memory-hard: every guess costs 64 MiB of RAM, which makes GPU/ASIC cracking expensive. Bcrypt would also have been acceptable but isn't memory-hard and truncates at 72 bytes. Hashes are stored as PHC strings (`$argon2id$v=19$m=65536,t=3,p=4$salt$hash`), so parameters can be raised later without breaking existing passwords.

**Login timing.** For unknown usernames the server still verifies against a dummy hash, so "no such user" and "wrong password" take the same time and return the same message: no username enumeration.

**Two token types.**
- **Session tokens** (`nds_…`, 30 days) for the web UI and scripts. Login also sets them as an `HttpOnly`, `SameSite=Strict` cookie so browser JavaScript never holds the token (XSS can't exfiltrate it). `SameSite=Strict` blocks classic CSRF until Phase 7 adds explicit CSRF tokens.
- **Device tokens** (`ndd_…`, until revoked) for sync clients. Each device has its own, so one lost laptop can be revoked without logging anything else out. Registering or revoking devices requires a *session*: a stolen device token can sync files but cannot mint more devices or revoke others.

**Token storage.** Tokens are 256 random bits; the DB stores only their SHA-256. A leaked database therefore leaks no usable credentials. A fast hash is correct here (unlike passwords): 256-bit random tokens cannot be brute-forced, so a slow KDF would only add latency to every request.

**Prefixes** make tokens self-describing (the server knows which table to check) and easy for secret scanners like GitHub's to spot if one is ever committed.

### HTTP layer

- **Downloads are hardened against stored XSS.** User files are served from the same origin as the future web UI, so an uploaded `.html` or `.svg` opened in the browser could run script with the user's session. Every download is sent with `Content-Disposition: attachment`, `Content-Security-Policy: sandbox` and `nosniff`. The web UI therefore shows image thumbnails only through `<img>` (which never executes script) and offers no inline HTML preview.
- **`http.ServeContent`** gives Range requests (resumable downloads), `If-None-Match`/304 and `Last-Modified` for free.
- **Timeouts.** `ReadHeaderTimeout` bounds slowloris attacks. There is deliberately no `ReadTimeout`/`WriteTimeout`: they cap the *whole* request, which would kill large uploads and downloads on slow links.
- **Strict JSON.** Unknown fields are rejected, so client typos surface as 400 instead of being silently ignored.
- **Structured logs** (`log/slog`, JSON to stdout) with a request ID per request, echoed in the `X-Request-ID` header and in 500 error messages, so a user's bug report can be matched to a log line. Tokens are never logged.
- **Graceful shutdown.** On SIGTERM (what `docker stop` sends) the server stops accepting connections, gives in-flight requests up to 30 s, then closes the DB cleanly.

### Configuration

Only bootstrap settings come from the environment (`NULLDRIVE_DATA_DIR`, `NULLDRIVE_PORT`, `NULLDRIVE_LOG_LEVEL`) with defaults that just work. Everything a user configures after first run lives in the DB (`settings` table), so Docker users never edit a file.

### Deliberately out of scope for Phase 1

- Blob garbage collection and version history (Phase 6).
- Rate limiting, lockout, 2FA, CSRF tokens, CSP for the UI (Phase 7).
- Case-insensitive collisions (`Readme.md` vs `README.md` are different files on the server but collide on Windows/macOS). The client will need a policy in Phase 4.
- Unicode normalization (macOS can produce NFD names; Windows/Linux NFC). Planned alongside the client.
- Upload size limits and quotas.
- Pagination of `GET /files` (fine into the tens of thousands of files).

---

## Phase 2 — Web UI and setup wizard

### The UI is embedded in the server binary

**Decision.** `make build` compiles the Svelte app with Vite straight into `server/internal/webui/dist`, and `go:embed` bakes it into the Go binary. The server serves the app for every path outside `/api/`, with a fallback to `index.html` so client-side routes (`/files/Photos`, `/settings`) work on reload and as bookmarks.

**Why.**
- **One thing to run.** Goal 1 is "one Docker command". With the UI embedded there is no nginx, no second container, no `static/` folder to mount, no CORS.
- **Can't drift.** The UI in a binary is always the UI built for that API version. A separately deployed frontend can be newer or older than the server it talks to.
- **Same origin = simple, safe auth.** The browser uses the `HttpOnly`, `SameSite=Strict` session cookie set by login. JavaScript never sees a token, so an XSS bug can't steal one, and there's no CORS or token storage to get wrong.

**Trade-offs.** The binary grows by ~0.5 MB (mostly fonts), and a UI change needs a server rebuild. For a self-hosted single-binary product both are fine. During development `make dev` runs Vite with hot reload and proxies `/api` to the Go server, so the rebuild cost only applies to releases.

**Details.**
- Plain `go build` / `go test` work without Node: `dist/` holds a committed `.gitkeep`, and an unbuilt binary serves a short "UI not built" page while the API works normally.
- Caching: Vite content-hashes everything under `assets/`, so those are `immutable` for a year; `index.html` is `no-cache`, so a new release is picked up on the next load.
- A missing *file* (`/assets/x.js`) is a real 404, not `index.html`, so a broken asset fails loudly.
- `ui/` contains a stub `go.mod` so `go ./...` doesn't descend into `ui/node_modules` (some npm packages ship `.go` files).

### Content Security Policy for the app

The app shell is served with `script-src 'self'` (no inline scripts, no third-party scripts), `connect-src 'self'`, `font-src 'self'`, `frame-ancestors 'none'`. Consequences that shaped the code:
- The "apply saved theme before first paint" snippet is an external file (`/theme-init.js`) instead of the usual inline script.
- Vite's asset inlining is off (`assetsInlineLimit: 0`): it turns small fonts into `data:` URIs, which the CSP (correctly) blocks.
- Fonts are bundled from npm (`@fontsource`), not Google Fonts: a self-hosted app should work on an offline LAN and shouldn't tell a third party whenever it's opened. Only the latin + latin-ext subsets ship, and `unicode-range` means browsers download only what a page uses.
- `style-src` allows `'unsafe-inline'` because Svelte sets some `style` attributes. Script injection is what CSP actually needs to stop, and that stays locked down.

### Shared component library (`ui/src/lib`)

**Decision.** One Vite project with two halves:
- `ui/src/lib`: the component library (Button, Input, Card, Modal, Toast, FileRow, FileTile, Breadcrumbs, ProgressBar, EmptyState, Sidebar, Menu, PasswordStrength, Icon, Logo), design tokens, the typed API client, and pure helpers (paths, names, formatting, password strength). Exported from `ui/src/lib/index.ts` as `@nulldrive/ui`.
- `ui/src/web`: the web app, i.e. routing, session, pages and page-specific dialogs.

**Rules for `src/lib`**, so the Phase 4 Wails desktop app can import it unchanged:
1. **No routing, no `window.location`, no global `fetch`.** Components get data through props and report intent through callback props (`onopen`, `onaction`, `onnavigate`). Breadcrumbs, for example, calls `onnavigate("Photos/2026")`; the web app turns that into a URL, and the desktop app can turn it into whatever it uses.
2. **No server assumptions.** `NullDriveClient` takes a `baseUrl` and an optional bearer `token`: the web UI passes neither (same origin + cookie), the desktop client will pass its server URL and device token.
3. **Styling only through CSS variables** (`styles/tokens.css`). Import the tokens and you get the Neon Lab look in either app.
4. **No component-library framework** (no Skeleton, Flowbite…). Fifteen small components are cheap to own, keep the bundle at ~46 KB of gzipped JS, and are exactly what the design needs.

**Why one project instead of a published npm package?** Until a second consumer exists, a separate package adds versioning and publishing for nothing. In Phase 4 the Wails frontend can import `ui/src/lib` by path (or as an npm workspace) and stays in lockstep in the same repo.

### Explicit folders (revises a Phase 1 decision)

Phase 1 had **implicit** folders: `docs` existed while some file path started with `docs/`. The web UI's "New folder" exposed the flaw: an empty folder can't be represented, so a new folder would vanish on refresh, and later a synced empty folder on the desktop could never reach the server.

**Decision.** Migration `0002_folders.sql` adds `files.is_dir`; folders are rows (size 0, empty hash) in the same table, versioned, tombstoned and moved by the same code paths as files. Invariant: **every live entry's parent folders exist as live folder rows**. Upload, `POST /folders` and move create missing parents in the same transaction and version. The migration back-fills parents for existing data with a recursive CTE (tested in `TestFolderBackfill`).

**Why rows in `files` rather than a separate `folders` table?** Delete, move and the change feed already operate on path ranges in `files`. A second table would double every one of those queries and their tests. Sync clients see one ordered feed of path changes, folders included.

**Behaviour worth knowing:** deleting the last file leaves an empty folder (like every desktop file manager); `POST /folders` is idempotent (`mkdir -p`); file-vs-folder name clashes are `409 path_collision`.

### Uploads in the browser

- **XHR, not `fetch`**, because browsers still don't expose upload progress for `fetch`.
- **Queue with 3 parallel uploads**: enough to fill a LAN link with small files without starving one big file. Each job can be cancelled or retried.
- **Name clashes are asked about once per batch** (Replace / Keep both / Skip), with "Keep both" as the default button, in line with goal 3.
- **Race safety:** "Replace" sends `If-Match` with the version the user saw. If someone changed the file meanwhile (`409`), or a file or folder appeared at that name (`428` / `path_collision`), the queue **automatically keeps both** by uploading as `name (1).ext` and says so in the upload panel. It never overwrites a version the user didn't see.
- **Drag and drop accepts folders** (walks `webkitGetAsEntry`), preserving the tree; there's also an "Upload a folder" picker.

### Visual design: Neon Lab

Dark-first (`#0a0a0f` base, blue-grey panels), Silkscreen for display type, JetBrains Mono for UI text, VT323 for the "terminal" status line. The rule that keeps it usable rather than noisy: **neon only where it means something**. Cyan marks the primary action, focus, the current page and live progress. Pink is the brand and selection accent. Green means OK (a finished upload, a recently active device). Amber and red are reserved for warnings and destructive actions. Everything idle is grey.

Light mode isn't an inverted dark mode: neon cyan on white fails contrast, so light mode uses deeper hues (`#007a8a`) and drops the glow. CRT scanlines are an opt-in cosmetic toggle. Silkscreen only has capitals, so anything that shows a user's file name (dialog titles, rows) uses the body font, so names render exactly as stored.

### Routing and state without libraries

Five screens don't need a router package: `router.svelte.ts` (~40 lines) maps `location.pathname` to a route and wraps `history.pushState`; the Go SPA fallback makes deep links work. Global state is two tiny Svelte 5 rune classes (`session`, `toast`). The login `?next=` redirect only accepts same-app paths (`/…`, not `//…` or URLs), so it can't be used as an open redirect.

The setup wizard records its step in `sessionStorage` after the account is created, so a reload during "Storage" or "Done" resumes the wizard instead of skipping it.

### Responsive and accessible by default

- One layout, three breakpoints of CSS. On phones the sidebar becomes a bottom tab bar (thumb reach), dialogs become bottom sheets, list rows fold size and date into a second line, and touch targets are at least 40 px.
- Keyboard and screen reader basics are built into the components: real `<button>`s everywhere, labelled icon-only buttons, a modal with focus trap, `Esc` and focus restore, menus with arrow keys, `aria-current` in breadcrumbs and nav, live regions for toasts and progress, `prefers-reduced-motion` respected, and a skip link.

### Testing strategy

| Layer | Tool | What it proves |
|---|---|---|
| Go packages | `go test` (table-driven) | Folder invariants, migrations, SPA handler caching/fallback, new endpoints |
| UI logic + components | Vitest + Testing Library (jsdom) | Path/name rules mirror the server, upload queue concurrency and keep-both fallback, router parsing, component a11y contracts (roles, labels, focus, keyboard) |
| Whole product | Playwright against the **real binary** on a fresh data dir | Wizard → login → upload → keep-both → rename → move → filter → delete, plus settings (devices, password) |

Playwright starts `bin/nulldrive` itself (`webServer` in `playwright.config.ts`), so the e2e suite tests exactly what ships: embedded UI, real SQLite, real disk. The same harness regenerates `docs/screenshots/` (`make screenshots`) from seeded demo content.

---

## Phase 3 — Docker, CI/CD, one-click install

### The image

```
 node:22-alpine  (build platform)      golang:1.24-alpine  (build platform)       distroless/static:nonroot  (target)
┌──────────────────────────┐   dist/  ┌──────────────────────────────────┐  bin  ┌───────────────────────────────┐
│ npm ci && vite build     │ ───────▶ │ CGO_ENABLED=0 GOOS/GOARCH=target │ ────▶ │ /nulldrive   /data (65532)     │
│ (UI is arch-independent) │          │ go build -ldflags "-X version…"  │       │ USER 65532 · HEALTHCHECK · VOL │
└──────────────────────────┘          └──────────────────────────────────┘       └───────────────────────────────┘
```

**distroless/static.** The final image contains the static binary, CA certificates, `/etc/passwd` and tzdata, and nothing else: no shell, no package manager, no libc. That's possible because the server is pure Go (`modernc.org/sqlite` instead of the C SQLite library), which was a Phase 1 decision paying off here.
- *Smaller attack surface:* an attacker who gets code execution finds no shell or tools to pivot with.
- *Fewer CVEs to triage:* image scanners have almost nothing to flag, because there are no OS packages.
- *Size:* about 2 MB on top of the binary itself.
- *Trade-off:* you can't `docker exec -it … sh` to debug. The binary covers what operators need instead: `nulldrive healthcheck` (the image has no curl for `HEALTHCHECK`), `nulldrive version`, `nulldrive reset-password <user>`. For real debugging, run the plain binary or attach a debug container (`docker debug` / `kubectl debug`).

Why not Alpine? It would add a shell and `apk` (useful for poking around, also for an attacker) for no runtime benefit to a static binary. `scratch` would also work but lacks CA certs, tzdata and a `nonroot` user entry, which distroless provides.

**Non-root by default.** The image runs as uid 65532 (`nonroot`). `/data` is pre-created with that owner, and Docker copies a directory's ownership into a *new named volume*, so the documented `-v nulldrive-data:/data` works with no `chown`. Bind mounts keep host ownership, so the server probes the data dir at startup and fails with the exact `chown` command to run, rather than failing later on the first upload. The NAS templates pick each platform's convention instead: Unraid `99:100` (nobody:users), TrueNAS `568` (apps), CasaOS root **with all capabilities dropped** (its AppData folders are root-owned; without `CAP_DAC_OVERRIDE` root can only write files it owns).

**Hardening in compose and templates:** `read_only: true` (the binary writes only to `/data`), `cap_drop: [ALL]` (it binds 8080, an unprivileged port, and needs no capabilities), `no-new-privileges`, and `stop_grace_period: 30s` to match the server's 30-second graceful shutdown (Docker's default is 10 s, which would cut off in-flight uploads).

**Multi-arch without emulation.** The Node and Go stages run on the build machine's native architecture (`--platform=$BUILDPLATFORM`) and Go cross-compiles for the target via `GOOS/GOARCH`. The final stage only `COPY`s files, so no instruction ever runs under QEMU. An amd64 + arm64 build costs about the same as a single-arch build, and there's no QEMU setup in CI at all.

**Layer caching.** `package-lock.json` and `go.mod/go.sum` are copied before the sources, so dependency downloads are cached until dependencies change; BuildKit cache mounts keep the npm, Go module and Go build caches between builds, and CI persists them in the GitHub Actions cache (`type=gha`).

### Versioning

The version comes from the Git tag and is stamped into the binary with `-ldflags -X …/buildinfo.Version=… -X …Commit=…` (Makefile: `git describe`; Docker/CI: `--build-arg VERSION/COMMIT`). It's visible in `/api/v1/health`, the web UI sidebar footer, Settings → Server, `nulldrive version`, the OCI image labels, and server logs at startup. There's no version file to forget to bump: **the tag is the single source of truth**.

### Release pipeline

```
git push origin v0.3.0
        │
        ▼
 release.yml ──▶ ci.yml (reused via workflow_call)
                  ├─ go:     go test -race, golangci-lint
                  ├─ ui:     svelte-check, vitest
                  ├─ e2e:    make build → smoke.sh → Playwright vs. real binary
                  ├─ docker: hadolint → buildx amd64+arm64 (no push) → template validation
                  └─ actionlint
        │  all green
        ├──▶ image:    buildx amd64+arm64 → ghcr.io/<owner>/nulldrive
        │               tags 0.3.0 · 0.3 · v0.3.0 · latest   (+ SBOM, SLSA provenance)
        └──▶ binaries: vite build once → scripts/build-release.sh
                        linux/darwin/windows × amd64/arm64 (.tar.gz/.zip) + SHA256SUMS.txt
        │
        ▼
 release: gh release create v0.3.0 --generate-notes (+ install snippet); "-rc" tags → pre-release, :latest untouched
```

Design choices:
- **Release is gated on the full CI suite** by calling `ci.yml` as a reusable workflow, so a broken commit can't be released by tagging it.
- **Least-privilege tokens.** Workflows default to `contents: read`; only the image job gets `packages: write` and only the release job gets `contents: write`. Everything uses the built-in `GITHUB_TOKEN`, so there are **no long-lived secrets** to leak or rotate.
- **Plain scripts over a release framework.** GoReleaser would also work, but `scripts/build-release.sh` (≈50 lines) runs identically on a laptop and in CI and is easy to read in a code review. The UI is built once and embedded into all six binaries.
- **Pre-releases are safe to cut**: `docker/metadata-action` only moves `:latest` (and `:X.Y`) for non-prerelease semver tags.
- **Auto-generated release notes** grouped by PR label (`.github/release.yml`); Dependabot PRs land under "Dependencies".
- **Dependabot** updates Go modules, npm (dev and prod grouped), GitHub Actions and the Dockerfile base images weekly, one grouped PR per ecosystem to avoid PR spam. Playwright major bumps are held back because the browser build must match.

### App-store templates

One image serves every platform. The templates (`deploy/`) differ only in each NAS's conventions (user ids, data paths, metadata format), and CI validates all of them (`docker compose config`, `xmllint`) on every push, so they can't silently rot. Store-specific notes, including why each one runs as the user it does, are in `deploy/SUBMITTING.md`.

### Account recovery

`nulldrive reset-password <user>` generates a random password (about 100 bits, no look-alike characters) instead of reading one from the terminal: nothing is echoed or stored in shell history, and it works in a TTY-less `docker exec`. The trust boundary is deliberate: anyone who can run commands in the container can read the data anyway. All web sessions of that user are ended; device tokens keep syncing.

---

## Phase 4 — Desktop client and the sync algorithm

### Architecture

```
 file watcher (fsnotify, debounced 1.5 s) ─┐
 poll timer (30 s, server changes) ────────┼──▶ Agent ──▶ Engine.Sync()  (one cycle)
 "Sync now" / tray / resume ───────────────┘     │        1. refresh remote mirror   GET /files?since=<cursor>
   backoff 2 s → 5 min while offline             │        2. scan local folder       size+mtime, hash only if changed
   pause / resume, status, activity ring         │        3. plan                    Decide() per path + rename detection
                                                 │        4. execute                 ordered ops, each re-checks its preconditions
                                                 ▼
                                      Index (SQLite, outside the sync folder)
                                      base   = last-synced state per path   ← the "B" in the 3-way merge
                                      remote = mirror of the server         ← kept fresh from the change feed
                                      cursor, conflicts
```

The engine (`shared/sync`) is pure Go with no UI, watcher or keychain code, so it is unit-tested and integration-tested against a real server. The Wails app (`client/`) is a thin shell around it: bindings for the UI, fsnotify, OS keychain, tray, autostart and log files.

### Why a three-way merge

Comparing only "local vs server" can't tell *who* changed a file. If `report.pdf` exists on the server but not locally, that could be a delete on this computer (→ delete it on the server) or a file someone else just created (→ download it). The right actions are opposite. Adding **Base**, the state both sides agreed on at the last successful sync, answers the question: whichever side differs from Base changed.

That single idea is what makes the hard cases safe:
- A laptop offline for a week comes back. Files deleted elsewhere are deleted locally (local == Base, server gone), not re-uploaded.
- Files created elsewhere are downloaded (no Base), not deleted.

### Decision table

`L` = local, `B` = base, `R` = remote. "=B" means unchanged since the last sync (same type and content hash). Every row is a test case in `shared/sync/decide_test.go`, plus a property test that loops over all combinations and asserts no decision discards a side that changed.

| Local | Remote | Decision | Notes |
|---|---|---|---|
| =B | =B | nothing (or record the newer server version) | |
| new / edited | =B | **upload** (`If-Match: B.version`; new files `If-Match: 0`) | |
| deleted | =B | **delete on server** (`If-Match: B.version`) | guarded against mass deletion |
| =B | new / edited | **download** (atomic: temp file → verify hash → rename) | |
| =B | deleted | **delete locally** (only if still unchanged at that moment) | |
| deleted | deleted | forget | |
| edited | edited, same content | converge (record as synced) | also covers crash-after-upload |
| **edited** | **edited, different** | **conflict: keep both** | |
| new | new, different | conflict: keep both | |
| edited | deleted | **re-upload: the edit wins** | |
| deleted | edited | **download: the edit wins** | |
| file ↔ folder | | replace type / conflict | rare; never destroys content |

### Conflict policy: never overwrite

When both sides changed a file differently, the server's version keeps the original name and this computer's version is renamed and uploaded:

```
plan.txt                                          ← the version that reached the server first
plan (conflict - Laptop - 2026-10-14 2315).txt    ← this computer's version
```

- **Keep both, always.** Picking a winner automatically (last-writer-wins, newest mtime) silently destroys someone's work, and clocks across machines aren't trustworthy. The user decides; the client lists conflicts with *Keep both / Use my copy / Discard my copy*, and those resolutions are ordinary file operations that sync normally.
- **Edits beat deletes.** Deleting someone's fresh edit is the one unrecoverable outcome, while an unwanted resurrected file costs one more delete.
- **The device name is in the copy's name**, so on other machines it's clear where the version came from.

### Safety mechanisms

| Risk | Protection |
|---|---|
| Two writers race on the server | Every write carries `If-Match` (Phase 1's optimistic concurrency). A `409` means "changed since our snapshot": skip, re-evaluate next cycle with fresh state. |
| File changes during a transfer | Uploads send `X-Content-SHA256`, so the server rejects content that doesn't match the scan's hash. Downloads go to `name.nulldrive-part`, are hash-verified, and the target is re-checked just before the rename; if it was edited meanwhile, the local edit is kept as a conflict copy. |
| Crash at any point | The index is updated only *after* an operation succeeds, and every operation is idempotent. Crash after upload, before recording it: next cycle sees identical content on both sides and converges. Crash mid-download: the `.nulldrive-part` file is removed by the next scan. Crash after renaming a conflict copy: the copy is just a new local file. (`TestCrashAfterUploadRecovers` simulates this.) |
| Locked or unreadable file (Office on Windows) | Reported as "can't sync" and **excluded from the plan**, so it is never mistaken for a delete. The same applies to paths that become ignored. |
| Sync folder missing (unplugged drive) | The cycle stops with an error instead of concluding "everything was deleted". |
| Wiped folder / wrong folder selected | **Mass-delete guard:** a cycle that would delete ≥25 files *and* >50% of synced files *on the server* stops and asks. It guards local→server only: local deletes are *inferred* from absence, while server deletes arrive as explicit tombstones from a device where the guard already ran. |
| Names other OSes can't store | Local names failing `shared/pathutil` (the same rules as the server) are skipped and listed in the UI. |

### Renames

A rename looks like "delete A + create B". The planner pairs them by content hash, on both sides:
- **Local rename → server move** (`POST /files/move`): no re-upload, and other devices see a rename.
- **Server rename → local rename**: no re-download.

If several deleted files share the hash, the one with the same file name wins (a move to another folder), otherwise the first by path, so results are deterministic. If the move fails (e.g. the target appeared meanwhile), it falls back to upload + delete, each with its own safety checks. Folder renames currently become per-file moves plus folder create/delete. That's correct, but one server call per file.

### Change detection

The scan trusts the recorded hash when size and mtime match the base entry, so a steady-state scan reads only metadata; changed files are re-hashed. Hashes, not mtimes, decide what changed. The mtime is only a cache key, so clock skew between machines can't cause a wrong decision.

### Ignore rules

Built-in: Office lock files `~$*`, `*.tmp`, `.DS_Store`, `Thumbs.db`, `desktop.ini`, editor swap files, in-progress downloads. Users add gitignore-style lines in `.nullignore` (which itself syncs, so every device applies the same rules): `*.log`, `build/`, `docs/drafts/*.md`, `!keep.log`. Matching is case-insensitive so rules behave the same on Windows and Linux. Ignored paths are never uploaded, downloaded or deleted.

### The desktop app

- **Separate Go module (`client/`).** Wails needs native toolchains (WebView2 on Windows, WebKitGTK on Linux); keeping it out of the root module means the server's `go test ./...` and Linux CI never need them. It imports `shared/*` via a `replace` directive.
- **UI reuse.** `client/frontend` imports `ui/src/lib` (same components and Neon Lab tokens as the web app) through a Vite alias, with `resolve.dedupe: ['svelte']` so there's only one Svelte runtime. All Wails specifics live in `bridge.ts`. Outside Wails it switches to an in-memory demo backend, which is how the UI is developed in a browser, unit-tested, and screenshotted.
- **Credentials.** The password is used once: log in → register the device → log out the session. Only the device token is kept, in the OS keychain (Windows Credential Manager via `go-keyring`), never in a file. "Sign out of this computer" deletes it.
- **Index and config** live in the OS config dir (`%AppData%\Null Drive`), never inside the synced folder. The index file is per (server, folder), so pointing the app at another folder or server starts clean instead of misapplying old state.
- **Tray.** Status icon colour-coded like the UI (cyan synced, pink syncing, grey paused, amber offline, red error); closing the window keeps syncing in the tray. Runs on its own locked OS thread next to Wails' UI thread (Windows/Linux).
- **Logs.** JSON lines to a size-rotated file (5 MB × 4), with an "Open logs" button for bug reports.

### Deliberately not handled yet

- **Large files are whole-file transfers.** A 4 GB video re-uploads entirely after a 1-byte change, and an interrupted transfer restarts. *(Solved in Phase 6: chunking, resume, dedup.)*
- **Realtime.** Server changes are picked up by polling (30 s); Phase 5 adds push (see below).
- **Case-only collisions** (`a.txt` vs `A.txt`, which can coexist on Linux but not Windows/macOS) and **Unicode normalization** (NFC vs NFD names on macOS) are not reconciled yet.
- **Transfers are sequential** within a cycle; parallel transfers would speed up many small files.
- **Selective sync**, **symlinks** (skipped and reported), extended attributes and permissions are not synced.
- **Coarse mtime filesystems** (FAT32: 2 s): a same-size edit within the same timestamp tick could be missed until the next change. The periodic rescan can't catch it either, because it trusts size+mtime.
- **macOS tray**: Wails v2 and the tray library both need the main thread there; the window works, the menu-bar icon is future work.

---

## Phase 5 — Realtime push, pairing, LAN discovery

Goal: an edit on one device appears on the others within a couple of seconds; a new computer on the same LAN finds the server with zero typing, and one off the LAN joins with a short code. Everything in this phase is networking, so this section is longer than the others.

### Push vs poll

| | Polling (`GET /files?since=` every N s) | Push (WebSocket notification, then `?since=`) |
|---|---|---|
| Latency | N/2 on average (15 s at N=30) | One round trip (~10 ms on a LAN) |
| Idle cost | One request per device every N s, forever | One idle TCP connection + a 2-byte ping every 25 s |
| Works through | Anything that passes HTTP | Proxies must pass `Upgrade`; some corporate proxies and captive portals don't |
| Failure mode | Always works, just slowly | Silent half-open connections unless you add keepalives |

Neither is enough on its own, so the client does both. **Push is the fast path; polling is the safety net.** While the socket is up the poll interval stretches from 30 s to 5 minutes; when it drops, polling returns to 30 s.

The key decision is that **notifications carry no data**: only "the feed is now at version N". The client reacts by running the same sync it runs for a poll or a local change, which reads `GET /files?since=<cursor>`. Consequences:

- One code path fetches changes, the one already tested in Phase 4.
- Lost, duplicated or reordered notifications are harmless: at worst one extra or one delayed sync. No sequence numbers, acks or replay buffers are needed.
- The socket never carries file metadata, so it needs no authorization logic beyond "which user is this".
- The server **coalesces**: each connection holds only the newest version number, not a queue. A burst of 1,000 uploads becomes a handful of notifications, and a stalled client costs 8 bytes, never an unbounded buffer.

`hello` on connect includes the current version. A client that was disconnected compares it with its cursor and syncs only if something changed in the meantime, which closes the gap between losing the socket and getting it back.

**Origin suppression.** The device that made a change isn't notified about it (it already knows). Browser tabs always are, and refresh their listing.

### Why WebSockets and not SSE or long polling

The channel is one-way (server to client), so Server-Sent Events would do. WebSockets won on three points: ping/pong control frames give keepalive without inventing an application-level heartbeat; the desktop client needs a Go client anyway and RFC 6455 is simple to implement and test; and the same socket can carry client-to-server messages later (e.g. "I'm uploading X" for presence) without a second connection. Long polling was rejected: it's a poll with extra state.

### A stdlib WebSocket (`shared/ws`)

About 400 lines: server handshake (`Accept`, using `http.ResponseController.Hijack`), client handshake (`Dial`, including `wss://` with a caller-supplied `tls.Config` for certificate pinning), framing with masking, fragmentation, interleaved control frames, close handshake, and size limits. No compression or subprotocols: the payloads are 30-byte JSON. Tests cover the RFC worked example, fragmented messages with a ping in the middle, and each protocol violation (unmasked client frame, orphan continuation, unknown opcode, oversized message, fragmented control frame) mapping to the right close code.

Two details that matter:
- `http.Server.Shutdown` does not track hijacked connections, so without help a restart would wait on every socket. `RegisterOnShutdown(hub.Shutdown)` sends close code **1001 (going away)**. Clients treat that as "reconnect after a random 1–6 s", so a restart doesn't bring every client back in the same millisecond (thundering herd), and it doesn't count as a failure.
- Writes have a 10 s deadline. A peer that went to sleep mid-connection can't block a broadcast.

### Keepalive numbers

- **Ping every 25 s, from both sides.** Home routers' NAT tables and proxies drop idle TCP mappings. nginx's default `proxy_read_timeout` is 60 s, many NATs time out idle flows in 60–300 s, and AWS ALB's default idle timeout is 60 s. 25 s stays under all of them with margin.
- **Dead after 2.5 × 25 s = 62.5 s without any frame.** TCP alone can take 15+ minutes to notice a vanished peer (laptop lid closed, Wi-Fi changed). Any frame (data, ping or pong) resets the read deadline, so one lost ping doesn't kill a healthy connection.

### Reconnect and fallback

```
connect ──ok──▶ live ──drop──▶ backoff (full jitter, 1 s → 60 s) ──▶ connect
   │                              ▲
   └─ fails / dies within 30 s ───┘   ×4  ──▶  "polling" (retry every 2.5–5 min)
```

- **Full jitter** (`sleep = random(0, backoff)`, floored at half the minimum) spreads clients out after an outage instead of having them retry in lockstep.
- A connection that survives **30 s** counts as healthy and resets the failure count. One that dies sooner counts as a failure. This catches the typical broken proxy: it accepts the upgrade, then kills the stream after a few seconds.
- After **4** consecutive failures the client reports **Polling** (shown in the app) and only retries every few minutes. **404, 400, 405 or 426 on the handshake** goes straight to polling: the endpoint is missing (older server) or something stripped `Upgrade`. **401** stops the notifier: the device was revoked, and the sync engine's own requests will show that error.

Measured in `TestRealtimePushPropagatesWithinSeconds` (real server, two engines, polling set to 1 h): an edit reaches the other device's disk in **10–30 ms** after the sync is triggered. On a real machine add the file watcher's 1.5 s debounce, so expect ~1.5–2 s end to end.

### Presence

The hub knows which devices hold a socket, so "online" is exact rather than guessed from `last_seen_at`. Browser sessions get `presence` events, so the devices page updates its dot live. Revoking a device closes its socket immediately (`1008`).

### LAN discovery: how mDNS/DNS-SD works

**mDNS** (RFC 6762) is ordinary DNS messages sent over UDP multicast to `224.0.0.251:5353` / `[ff02::fb]:5353`, link-local scope, for names under `.local`. Every host listens; whoever owns the name answers. **DNS-SD** (RFC 6763) is a convention for finding *services* with plain DNS records:

```
_nulldrive._tcp.local.                    PTR  Null Drive on nas._nulldrive._tcp.local.   "who offers this service?"
Null Drive on nas._nulldrive._tcp.local.  SRV  0 0 8080 nulldrive-nas.local.              "on which host and port?"
Null Drive on nas._nulldrive._tcp.local.  TXT  txtvers=1 version=0.5.0 name=nas setup=done api=/api/v1
nulldrive-nas.local.                      A    192.168.1.20                                 "at which address?"
nulldrive-nas.local.                      AAAA fd00::20
```

A browser asks for the PTR. The responder answers with the PTR and puts SRV, TXT and A/AAAA in the additional section (§12), so one round trip is enough. TXT keys are public to the whole LAN, so they hold nothing secret: version, display name, and whether setup is finished (`setup=pending` lets the client say "open it in a browser first").

The implementation (`shared/mdns`, ~700 lines, stdlib only):
- DNS wire format for the five record types, with **name compression on read**. Real responders compress, and compression pointers are checked to point backwards with a jump limit, so a hostile packet can't loop the parser. It is fuzz-tested (`FuzzUnpack`).
- A responder that answers PTR/SRV/TXT/A/AAAA/ANY queries, the `_services._dns-sd._udp` enumeration, **known-answer suppression** (§7.1), the **QU bit** (§5.4) and **legacy unicast** queries (§6.7: reply to the sender's port, echo the ID and question, TTL ≤ 10 s, no cache-flush bit). It sends announcements at start (0, 1 and 4 s) and **goodbye packets** (TTL 0) on shutdown, so clients drop the server immediately rather than after the record TTL.
- **Cache-flush bit** on SRV/TXT/A/AAAA (records this host owns) but not on the shared PTR, as the spec requires.
- TTLs: 120 s for host-bound records, 75 min for the others (§10).

**The client never binds port 5353.** It queries from an ephemeral port, which makes it a legacy-unicast query, and responders answer straight back to that port. So it coexists with Bonjour, Avahi and Windows' own mDNS service (which hold 5353), needs no inbound firewall rule, and needs no admin rights. Each query is sent 3 times, 400 ms apart, because multicast over Wi-Fi is lossy (no link-layer retransmission for multicast frames).

### Multiple NICs, IPv4/IPv6

A NAS often has a LAN port, a storage/VLAN port and a VPN interface. Advertising every address to everyone means clients try unreachable ones first. So:

- **The responder joins the group on every interface** (`net.ListenMulticastUDP` only joins one, so the rest are joined with `IP_ADD_MEMBERSHIP` / `IPV6_JOIN_GROUP`; see `sockopt_unix.go` and `sockopt_windows.go`).
- **Answers are per subnet.** The responder looks at the query's source address and answers with the addresses of the interface whose subnet contains it. Asked from `192.168.1.0/24`, it advertises `192.168.1.20`, not the storage NIC's `10.10.0.5`. Multicast replies go out of that same interface (`IP_MULTICAST_IF`), not just the default route's.
- **Announcements are sent per interface**, each carrying that interface's addresses.
- **IPv6 link-local** (`fe80::`) addresses are only advertised to link-local askers, since they're useless without a zone ID. IPv4 link-local (`169.254/16`) is never advertised.
- **Virtual bridges are skipped** (`docker0`, `br-*`, `veth*`, `virbr*`, CNI/flannel/calico).
- **The client picks** an address on a subnet it is directly attached to, then IPv4 over IPv6, then global over link-local (`toDiscovered`). All addresses are kept for display.

### Limits of mDNS (and what to do about them)

- **It does not cross routers.** `224.0.0.251` is link-local, TTL 255 but never forwarded, so a server on VLAN 20 is invisible from VLAN 10. Fix it on the network: an **mDNS reflector/repeater** (Avahi `enable-reflector=yes`, UniFi "mDNS" / "Multicast DNS", pfSense/OPNsense "mdns-repeater" or Avahi package, MikroTik RouterOS 7.16+ mDNS repeater). Otherwise use the manual address or a pairing link.
- **Docker bridge networks** don't pass multicast. The container also only knows its `172.17.x` address, so even a forwarded announcement would point at nothing. Use `network_mode: host` (Linux) for discovery. The server detects the bridge case (`/.dockerenv` and only `172.16/12` addresses) and says so in the log and in Settings. `NULLDRIVE_ADVERTISE_ADDR` / `NULLDRIVE_ADVERTISE_PORT` override what's announced (useful with macvlan/ipvlan or a host-side reflector). Docker Desktop on macOS/Windows has no host networking for this, so discovery won't work there.
- **Wi-Fi** "client isolation" / guest networks and some mesh systems filter multicast.
- **No probing/conflict resolution** (§8.1): two servers with the same name would collide. Host labels are prefixed (`nulldrive-<name>`) to avoid stepping on the OS's own `<hostname>.local`. Renaming a server is one setting.
- It's **unauthenticated**: anyone on the LAN can announce "Null Drive on nas". That's acceptable because discovery only pre-fills an address. Trust comes afterwards from TLS (or the LAN being trusted), the login or pairing code, and the device token.

**Manual LAN test checklist** (CI can't do multicast):
1. Server on a LAN host (binary or Docker with `network_mode: host`). Settings → LAN discovery shows "Announcing <your LAN IP>".
2. From another machine: `avahi-browse -rt _nulldrive._tcp` (Linux) or `dns-sd -B _nulldrive._tcp` then `dns-sd -L "Null Drive on <name>" _nulldrive._tcp` (macOS/Windows with Bonjour). Expect SRV port and TXT `setup=done`.
3. Fresh desktop client: the setup screen lists the server within ~1.5 s, and clicking it fills the address.
4. Rename the server in Settings and click *Scan again*: the new name appears.
5. Stop the server: `avahi-browse` reports it removed straight away (goodbye packet).
6. Toggle discovery off: the client no longer finds it.
7. Multi-NIC host: query from each subnet. Each sees only its own subnet's address.
8. `NULLDRIVE_MDNS_LIVE=1 go test ./shared/mdns -run Live` runs the real multicast round trip on any machine with a multicast route.

### Pairing codes: threat model

**What it is for:** adding a device without typing the account password on it (a shared or borrowed machine, a remote relative's PC), and off-LAN onboarding where discovery can't help.

**Flow:** an authenticated **session** (device tokens can't do this) issues a code → the user enters it on the new device → `POST /pairing/claim` creates a device token. The web dialog polls status and shows "Paired with <device>"; closing it cancels an unused code.

**Assets:** the code (worth one device token for this account) and the claim endpoint (unauthenticated by necessity).

| Threat | Mitigation |
|---|---|
| **Online brute force** | 6 Crockford-base32 chars = 32⁶ ≈ 1.07 × 10⁹ codes. Claims are limited to 5/min per address and 30/min globally, checked *before* parsing, so malformed guesses cost a token too. With at most 5 live codes per user, one 5-minute window allows ≤150 guesses: P(hit) ≈ 150 × 5 / 1.07 × 10⁹ ≈ **7 × 10⁻⁷**. Even if a code were live 24/7 for a year (it isn't; codes exist for the minutes someone is pairing), the global cap allows 15.8 M guesses ≈ 1.5%. |
| **Distributed brute force** (botnet, many IPs) | That's what the global bucket is for: the per-address limit alone wouldn't stop it. The trade-off: an attacker can make pairing return 429 for everyone (DoS). That's acceptable (wait a minute, or sign in with the password) and logged. |
| **Oracle** | Unknown, expired, cancelled and used codes give the same `400 invalid_code`. Throttled requests return 429 even for the right code, so a guesser can't tell a near-miss from a throttle. |
| **Replay / race** | Single-use is enforced by `UPDATE … WHERE claimed_at IS NULL` inside an immediate (write-locked) SQLite transaction. `TestPairingConcurrentClaimsOnlyOneWins` races 4 claims and exactly 1 succeeds. |
| **Database theft** | Codes are stored as SHA-256. That's not a password-grade KDF, deliberately: a stolen hash of a code that expired 5 minutes ago is worthless, and anyone holding the DB already has everything else. |
| **Shoulder surfing / screenshot** | 5-minute lifetime, single use, cancel on close, and a warning in the dialog. |
| **Stolen device token mints more devices** | Issuing codes needs a session (`403 session_required` for device tokens), same rule as `POST /devices`. |
| **Phishing a code** ("read me your code") | Not solvable by code length. The dialog says not to share it, and every paired device is listed with `added_via=pairing`, its IP and first-seen time, and can be revoked. |

The **QR code / pairing link** (`nulldrive://pair?server=…&code=…`) carries the server address as the browser sees it, so off-LAN pairing needs no typing at all. Pasting the link into the desktop app's code box fills both fields. The QR is rendered as an inline SVG path (no canvas, no `data:` URL), so it works under the strict CSP. It's always dark-on-white with a quiet zone, because scanners need the contrast whatever the theme.

### TLS awareness and TOFU

Null Drive speaks plain HTTP and expects a reverse proxy for HTTPS (Phase 3). The desktop client now makes the transport's security visible:

- **Plain HTTP to a non-LAN address gets a clear warning** before sign-in: the password, device token and files would cross the internet unencrypted. "LAN" (`shared/netutil`) means loopback, RFC 1918, ULA `fc00::/7`, link-local, CGNAT `100.64/10` (Tailscale and other overlay VPNs, which already encrypt), single-label names, and `.local`/`.lan`/`.home.arpa`/`.internal`. A public DNS name that resolves only to private addresses (split-horizon DNS) also counts as local.
- **Self-signed HTTPS: trust on first use, pinned.** If the certificate doesn't verify against the system roots, the client shows its **SHA-256 fingerprint**, subject and expiry, and asks the user to compare it with the server's (`openssl x509 -fingerprint -sha256`). Accepting stores the fingerprint in `config.json` (it's public, not a secret). From then on the client's TLS config **replaces chain and hostname verification with an exact-leaf match**. That's stronger than either check for this one server: a homelab cert often names `localhost` or a bare IP, and a pin also rejects a different certificate from a CA the OS happens to trust. A changed certificate stops sync with a clear "certificate changed since you trusted it" error, never a silent re-trust.
- A certificate that verifies normally isn't pinned, so Let's Encrypt renewals every 60–90 days keep working.
- The pin covers both the HTTPS client and the `wss://` notifier (`ws.DialOptions.TLSConfig`).

**TOFU's known weakness** is the first connection: an attacker present at that moment gets pinned instead. The fingerprint comparison prompt is the mitigation, and on a home LAN the window is small. Showing the fingerprint in the server's own Settings (if it terminates TLS itself) is a Phase 7 item.

### Other decisions

- **Client IP behind a proxy.** `NULLDRIVE_TRUST_PROXY=on` takes the *last* `X-Forwarded-For` entry (the one our proxy appended). Earlier entries are client-controlled and spoofable. Off by default, because trusting the header without a proxy would let anyone dodge the per-IP pairing limit.
- **Device details** (`last_ip`, `client_version` from `User-Agent: NullDrive/<v> (<os>/<arch>)`, `added_via`) are informational only, never used for authorization. They're written at most once a minute unless the address changes, so an active client doesn't turn every request into a DB write.
- **Browser realtime** uses the session cookie (browsers can't set headers on a WebSocket handshake). WebSockets are exempt from CORS and `SameSite` doesn't help against same-site attackers (another app on the same NAS hostname), so the handshake checks `Origin == Host`: **cross-site WebSocket hijacking** protection. Requests without `Origin` are non-browser clients, which must present a bearer token.

### Deliberately not handled yet

- The Go WebSocket client doesn't use HTTP proxies (`HTTP(S)_PROXY`). If one is required, the notifier falls back to polling; normal sync requests do honour the proxy.
- No mDNS name probing or conflict renaming.
- The server doesn't terminate TLS itself, so there's no server-side fingerprint display yet.
- Login (password) attempts aren't rate limited yet, only pairing claims. That's part of the Phase 7 security pass.

---

## Phase 6 — Chunking, deduplication, version history

Goal: editing part of a large file moves only the changed part, identical data is stored once, and every file has a restorable history. Headline result (details in [BENCHMARKS.md](BENCHMARKS.md)): a 1 MiB edit to a 1 GiB file uploads **4.5 MiB instead of 1024 MiB**, and a 4 KiB *insertion* uploads **2.3 MiB**.

### Content-defined chunking (FastCDC)

Files are split into chunks of 512 KiB to 8 MiB (2 MiB on average) at **content-defined** boundaries. A rolling hash runs over the bytes, and a boundary is wherever its high bits are all zero.

```
fixed-size blocks: boundaries every 2 MiB, counted from the start of the file

  original   |AAAA|BBBB|CCCC|DDDD|EEEE|
  insert x   |xAAA|ABBB|BCCC|CDDD|DEEE|E      every block after the insertion changed:
                                               ~the whole file is re-sent

content-defined: boundaries where hash(last 64 bytes) has its top N bits zero

  original   |AAAA|BBBBBB|CC|DDDDD|EEE|
  insert x   |xAAAA|BBBBBB|CC|DDDDD|EEE|      boundaries are found again at the same
              ^^^^^                           content right after the edit: only the
              1 new chunk                     chunk containing it is new
```

The implementation (`shared/chunker`) follows the FastCDC paper (Xia et al., USENIX ATC 2016):
- **Gear hash**: `fp = fp<<1 + G[byte]`, i.e. one shift, one add and one lookup per byte. Testing the *high* bits makes each decision depend on the last 64 bytes.
- **Cut-point skipping**: no hashing in the first 512 KiB of a chunk (no boundary is allowed there anyway).
- **Normalized chunking**: a stricter mask (more bits must be zero) before the 2 MiB average and a looser one after it. This narrows the size distribution towards 2 MiB compared with a single mask.
- The 256-entry gear table comes from SplitMix64 with a fixed seed. **The parameters and table are part of the storage format**: changing them would make new uploads chunk differently from existing data and lose dedup. `TestGolden` pins the boundaries.

Measured: 1.2–1.4 GB/s for boundary detection, ~500 MB/s including SHA-256 of each chunk, on 2 vCPUs. After any small edit to 64 MiB, 28 of 29 chunks stay identical (`TestStability`).

**Why 2 MiB on average?** Smaller chunks dedup more finely, but each one costs a manifest entry, a database row and an HTTP request. At 2 MiB a 1 GB file is ~450 chunks (a ~40 KB manifest), and an edit re-sends ~2–5 MB, which is negligible next to the 1 GB it replaces.

### Manifest model

```
files ──version_id──▶ file_versions ──1:n──▶ version_chunks ──n:1──▶ chunks ──▶ blobs/ab/cd/<sha256>
(current state +      (every content a       (ordered manifest:       (hash, size,
 change feed)          path has had)          seq, hash, size)         refcount)
```

- A **version** is an ordered list of chunk hashes (the manifest) plus the whole-file SHA-256, size, device and time.
- `files` keeps its Phase 1 role (current state + change feed) and points at its current version. **Tombstones keep their pointer**, which is what makes the recently-deleted bin work.
- **Chunks are the blobs.** The Phase 1 store was already content-addressed by SHA-256, which is exactly a chunk's key. Migration `0004` turns every existing file into a one-chunk version **in place, without copying data**. New uploads are chunked.
- **Reference counts are maintained by SQLite triggers** on `version_chunks` insert/delete, so no code path can forget them. A chunk used twice in one manifest counts twice.
- **Renames** move a file's history with it (`file_versions.path` is updated in the move transaction), and the old path's tombstone drops its pointer, so a rename never shows up as a deletion.

### Protocol v2 (and keeping v1 working)

```
client                                             server
  chunk file locally (FastCDC + SHA-256)
  POST /api/v2/chunks/check  {hashes}      ──▶     which of these can't this user use yet?
                                           ◀──     {missing}
  PUT  /api/v2/chunks/{hash} × missing     ──▶     verify SHA-256, store if new (else discard bytes)
       (4 in parallel, retried on network errors)
  PUT  /api/v2/files/{path} {size, sha256,  ──▶    one transaction: precondition (If-Match → 409/428),
       chunks}  If-Match: "<version>"              chunks usable by this user, sizes add up,
                                           ◀──     new version, refcounts, change feed bump, push
```

Download is the mirror image: `GET …/manifest`, re-chunk the old local copy, copy the chunks that match, `GET /api/v2/chunks/{hash}` the rest, verify each chunk and then the whole file, and rename into place.

- **v1 is unchanged on the wire.** `PUT /api/v1/files/{path}` now streams the body through the same chunker on the server, so web uploads and old clients get dedup and history too. `GET /api/v1/files/{path}` serves a manifest through a seekable multi-chunk reader, so `Range` requests and `http.ServeContent` keep working. Phase 4/5 desktop clients keep syncing.
- **Capability detection**: the client probes `POST /api/v2/chunks/check` with an empty list once (cached 10 minutes) and falls back to v1 on a 404. Tested with a proxy that hides `/api/v2` (`TestFallbackToV1`).
- **No conflict semantics changed**: the commit applies exactly the Phase 1 precondition rules, so the sync engine's three-way merge and conflict copies work as before.
- **Path operations are parsed from the right**: `GET /api/v2/files/a/manifest` is always the manifest *of* `a`. The manifest of a file literally named `manifest` is `…/manifest/manifest`. v2 has no plain GET on a bare path, so this is unambiguous.

### Dedup without a cross-user side channel

Storage is global: a chunk is stored once whoever uploads it. The **check is scoped per user**: a chunk counts as "present" only if this user has uploaded it (pending commit) or already references it in one of their versions. Otherwise anyone could ask "does the server have chunk X?" and learn whether *another* user stores a given file. That is the classic dedup side channel, and it enables confirm-a-file and learn-the-remaining-bytes attacks. For the same reason:
- a **commit** may only reference chunks usable by this user (`422 missing_chunks` otherwise), so you can't claim someone else's data just by knowing its hash;
- `GET /chunks/{hash}` requires that the chunk is part of one of the caller's versions;
- uploading bytes someone else already stored is verified by hash and then discarded, so the data is still stored once.

The cost: the second user to upload a popular file sends the bytes once. Their server-side storage is still free.

The whole-file SHA-256 in a commit is **client-asserted**. Every chunk is verified on upload, so the bytes are always exactly what the manifest names. The whole-file hash is metadata the user's own clients use for change detection, and the download path verifies it end to end. A lying client can only confuse its own account.

### Resumable transfers

- **Uploads** resume for free. Chunks sent before an interruption are recorded in `chunk_uploads` (per user), so the next attempt's check reports them present (`TestChunkedUploadResumes`: 11 MiB of 30 MiB already on the server after an interruption).
- **Downloads** write into `<name>.nulldrive-part` at each chunk's offset and save a small `.nulldrive-part.state` (which chunks are done, for which version) every 2 s. A later attempt at the *same version* continues from there; a different version starts over. The scan keeps a part file that has a fresh state file (< 7 days) and removes everything else (`TestChunkedDownloadResumes`: 11 MiB reused, 20 MiB fetched).
- Each chunk request is retried up to 3 times with backoff on network errors, so one dropped connection doesn't fail a 5 GB transfer.

### Garbage collection

Runs hourly (`NULLDRIVE_GC_INTERVAL`) and on demand (`POST /api/v2/admin/gc`):

1. **Retention** prunes old versions (below). Their manifest rows go, so refcounts drop.
2. **Expired uploads**: `chunk_uploads` entries older than the grace period are forgotten.
3. **Unreferenced chunks** (`refcount = 0`) whose `touched_at` is older than the **grace period (24 h)** are deleted: the row first, then the blob.
4. **Orphan sweep**: blob files with no row at all (a crash between writing a blob and recording it) are deleted once older than the grace period.

**Safety against in-flight uploads.** A client checks, uploads, then commits. Every chunk it touches (uploaded, or reported present by a check) gets `touched_at = now`, so it survives at least 24 h before the commit. The race that remains is "GC deletes a chunk while an upload of the same chunk is writing it". It is closed by a **per-chunk lock** (64 striped mutexes) held across both multi-step operations:

```
upload: lock(h) → write blob if absent → upsert row (touched_at = now) → unlock
GC:     lock(h) → DELETE row WHERE refcount = 0 AND touched_at < now - grace → delete blob → unlock
```

A row therefore never points at a missing blob, and a blob being uploaded is never deleted. Commits need no lock: they run in a SQLite write transaction that requires every chunk row to exist, and once committed the refcount protects the chunk. If a commit loses the race anyway (its chunk was collected after the check), it gets `422 missing_chunks` and the client re-uploads that chunk and retries once. Tests: `TestV2RetentionAndGC` (a pending upload survives inside the grace period and is collected after it; current versions are never touched), `TestOrphanSweep`, `TestRefcountsFollowVersions`.

### Retention

Default **keep every version 30 days, then one per day for 90 more days** (both configurable in Settings → Storage):

```
age:   0 ────────── 30 days ──────────────── 120 days ──────▶
       every version │ newest version of each │ dropped
                     │ UTC day                │
```

- The **current version of a live file is always kept**, whatever its age.
- A **deleted file** goes to the trash (Recently deleted) for `trash_days`, 30 by default, separate from the history policy since the Notes-backup phase (see below). The UI shows "N days left". Afterwards all of its versions are dropped.
- Pruning is a pure function (`prunable`, unit-tested on a hand-built timeline) applied per path, in batches of 500 so a large prune doesn't hold the write lock.
- **Restore never rewrites history**: it commits the old manifest as a *new* version (`origin = restore`). No data is copied, the restore itself can be undone, and other devices receive it like any other change. Restoring a deleted file recreates its folders.

### Web UI and desktop

- **History** (file menu → *History*, or `/files/<folder>?history=<name>`): a timeline of versions with time, device ("Web" for the browser) and size. Each version can be downloaded (`/api/v2/files/…/versions/{v}/content`) or restored (with confirmation).
- **Recently deleted** (`/trash`, in the sidebar and bottom bar): deleted files with folder, size, time deleted and days left. Restore, or open a file's full history.
- **Storage** (Settings): logical size, size including all versions, distinct bytes actually stored, and the share saved by chunking and dedup. Admins also see server totals, edit retention, and can run cleanup now.
- **Desktop**: each activity row has a history button that opens that file's history in the browser, and Settings links to Recently deleted. During a transfer the status card shows bytes for the current file ("412 MB of 980 MB"), counting only the chunks that actually travel.

### Deliberately not handled yet

- The old local copy is **re-chunked on every delta download** (CPU instead of bandwidth; see BENCHMARKS.md). Caching each file's manifest in the local index would skip that for unchanged copies.
- **No compression** of chunks (most large files people sync, such as media and archives, are already compressed). Could be added per chunk, with a flag in the chunk row.
- **No server-side chunk verification at rest** (scrubbing). A periodic re-hash of the blobs would detect bit rot.
- Chunks are **not encrypted at rest**: that's Phase 7. Note that client-side encryption with random keys defeats cross-user dedup, which is a trade-off to decide there.
- Small files (< 512 KiB) are single chunks, so there is no sub-file dedup for them, by design.

## Phase 7 — End-to-end encryption and the security pass

The full threat model (who is defended against, and where it stops) is in
[SECURITY.md](SECURITY.md). This section is about *why* it's built this way.

### Overview

```
 desktop / browser                                   server
 ─────────────────                                   ──────
 passphrase ─Argon2id─┐                              user_keys: bundle (opaque JSON)
 recovery key ─HKDF───┼─unwrap─▶ master key            passphrase_wrapped, recovery_wrapped,
                      │            │                    salts, KDF params, fingerprint
                      │          HKDF                   + SHA-256(auth key)
                      │   ┌────┬───┴───┬────────┬────┐
                      │ name  chunk  file-hash  auth  fingerprint
                      │   │     │        │
 "Taxes/2026.pdf" ────┴──▶ "~kZ3…/~Qa9…"          files.path, file_versions.path
 file bytes ──FastCDC──▶ chunks ──seal──▶ blobs    chunk id = SHA-256(sealed bytes)
 SHA-256(file) ─────────────────HMAC──────▶        files.sha256
```

The server's chunk store, refcounts, garbage collector, version history,
trash, moves, conflict detection and realtime push are **unchanged**. They
already worked on opaque hashes and paths; E2EE only changes what the
client puts in them. That was the main design goal: encryption as a layer
in the client, not a second storage engine in the server.

### Key hierarchy (and the one place it differs from the brief)

- **Master key:** 32 random bytes, created on the client.
- **Passphrase → KEK:** Argon2id (t=3, m=64 MiB, p=1). The parameters are
  stored with the bundle so they can be raised later. p=1 because
  libsodium in the browser only supports one lane and both sides must
  derive the same key; 64 MiB keeps unlocking around a second on a phone.
- **Recovery key → KEK:** 160 random bits shown once as 8 groups of
  Crockford base32 (no I/L/O/U, so it survives handwriting). It's random,
  so plain HKDF is enough; no stretching.
- **Wrapping:** XChaCha20-Poly1305 with random nonces and associated data
  naming the purpose (`…/wrap/passphrase`, `…/wrap/recovery`), so one wrap
  can't be passed off as the other. Two wraps of the same master key means
  changing the passphrase is a re-wrap: no file is re-encrypted.
- **Subkeys:** HKDF with a purpose label each. Separate keys for name
  encryption, name nonces, chunk encryption, chunk nonces, the file hash,
  the auth key and the fingerprint.

**Per-file keys → per-content keys.** The brief sketches "master key →
per-file keys". Per-file keys break two things Phase 6 built: if file A
and file B contain the same chunk, it's stored once, so it can only be
encrypted one way, and a per-file key can't decrypt the other file's
copy; and a renamed or copied file would have to be re-encrypted. Instead
each chunk's encryption is derived from the account keys *and its
content* (next section). Every distinct piece of content gets its own
effective nonce, chunks stay shareable between files and versions, and
delta sync keeps working. (Per-file keys matter for *sharing* single files
with other users, which Null Drive doesn't do; if it ever does, a per-share
key wrapping layer can be added on top.)

### Deterministic encryption: the SIV construction

Chunks and names are encrypted *deterministically*: the same plaintext
always gives the same ciphertext for the same account. The nonce is not
random but synthetic: `nonce = HMAC-SHA256(siv-key, ad ‖ 0x00 ‖ plaintext)
[:24]`, then `XChaCha20-Poly1305(enc-key, nonce, plaintext, ad)`. This is
the SIV pattern (RFC 5297) built from standard primitives:

- different plaintexts get unrelated nonces, so the keystream is never
  reused for two different messages (the failure mode of reusing a nonce);
- the nonce is authenticated by Poly1305 like any AEAD nonce, and after
  decryption it's recomputed and compared;
- the only thing it reveals is *equality* of plaintexts, within the account.

Why deterministic at all:

- **Chunks:** the chunk ID is the SHA-256 of the *sealed* bytes. Same data →
  same sealed chunk → same ID, so the server's existing "which chunks do
  you lack?", dedup, resume and delta download all keep working, and the
  server can still verify that an uploaded chunk matches its ID (integrity
  of what it stores) without seeing plaintext.
- **Names:** to open `Documents/report.pdf` the client must compute its
  server path without listing everything; renames, moves, conflicts and
  the If-Match protocol all work on paths.

### Why not convergent encryption

Convergent encryption derives the key from the content alone
(`key = H(chunk)`), so two *different users* uploading the same file
produce the same ciphertext, and cross-user dedup survives encryption.
It has well-known problems:

- **Confirmation of a file:** anyone who has a candidate file (a leaked
  document, a known image) can compute its ciphertext and ask "is this
  stored here?". The server, or anyone who gets the database, can tell who
  stores it.
- **Learn-the-remaining-information:** for files that are mostly known
  (a form letter with a 4-digit PIN), an attacker can enumerate the unknown
  part and test each guess.

Null Drive's SIV keys come from the *account's* master key, so the same
file encrypts to unrelated bytes in different accounts. Within one account
the server sees equality (it can't use that to confirm anything without the
key). Cost: no cross-user dedup for encrypted accounts. For a homelab with a
handful of users that's a few duplicate photos. Standard accounts keep
cross-user dedup.

### Names

Each path segment is padded (ISO 7816-4, to a multiple of 16 bytes, so
lengths leak only to 16-byte buckets), sealed with the SIV construction,
and written as `~` + base64url. `Taxes/2026.pdf` becomes two encrypted
segments; the tree shape stays.

*Why per segment, without the parent folder as associated data:* binding
each name to its folder (as Cryptomator does with per-folder IDs) would
hide that two folders contain a file with the same name, but moving a
folder would then require re-encrypting every name inside it, and the
server's atomic folder move (one SQL statement, history carried along)
would become a client-side walk of thousands of renames. The trade-off
taken: equal names are linkable within the account; folder moves stay
atomic and instant.

The `~` prefix and exact length rules (`e2ee.IsEncryptedName`) let the
server refuse plaintext names on encrypted accounts without knowing any
key. A 255-byte name becomes 396 characters, so path validation allows
longer segments when (and only when) a segment has that shape.

### The file hash

Files carry a whole-file hash for change detection (the three-way merge
compares local, base and remote hashes). A plain SHA-256 would let the
server confirm known files, so encrypted accounts store `HMAC(file-hash
key, SHA-256(plaintext))`. The sync engine gained one hook,
`Engine.ContentHash`, so local scans hash into the same space; nothing
else in the merge changed.

### Sizes and the server's integrity check

A commit's `size` is the plaintext size; each stored chunk is exactly
`e2ee.ChunkOverhead` (41) bytes larger. The server checks
`Σ(stored chunk sizes) − 41 × chunks = size` for encrypted accounts, so it
still rejects inconsistent manifests. The exact size is therefore visible
to the server; hiding it would need padding chunks, and sizes already leak
to within one chunk overhead from the ciphertext anyway.

### Client architecture

```
 sync.Engine ──RemoteAPI/ChunkedRemote──▶ e2ee.Remote ──▶ client.Client ──▶ server
     │ plaintext paths & hashes              │ encrypts paths, decrypts listings,
     │ (ContentHash = keys.FileHash)          │ sets TransferOptions.Codec = keys
                                             ▼
                       client.UploadFile / DownloadFile (FastCDC, 4 parallel,
                       resume): seal/open each chunk through the Codec
```

`e2ee.Remote` makes an encrypted account look like a plain one to the
engine, which is why the conflict handling, renames and mass-delete guard
from Phase 4 needed no changes. The web app has the same layer in
TypeScript (`ui/src/lib/drive.ts`), and its own FastCDC port cuts at the
same byte offsets as Go, so the browser and the desktop produce the same
chunks for the same file.

Whole-file v1 transfers are disabled for encrypted accounts (on the client
*and* refused by the server): a v1 upload hands the server plaintext to
chunk, and a v1 download would return concatenated ciphertext.

### Turning it on, and the migration

Order matters:

1. Keys are created on the client and the **recovery key is shown and
   confirmed (type its last group, tick "saved") before anything is sent**,
   so an account can never be encrypted with a recovery key nobody saved.
2. The bundle goes to the server. An empty account is `on` immediately; one
   with files becomes `migrating`, and from that moment the server refuses
   any new plaintext name.
3. The client downloads each plaintext file through the chunk API and
   re-uploads it encrypted under its encrypted path. Resumable: files
   already present under their encrypted path are overwritten or skipped.
4. `POST /e2ee/finish` (with the auth key) deletes every non-encrypted row:
   files, folders, tombstones and *all their history* (old versions are
   plaintext too). Chunks that lost their last reference are deleted
   immediately, then the database is rebuilt with `secure_delete` on and
   the WAL truncated. Tests scan the data dir afterwards.

If a name can't be encrypted (over 255 bytes), the migration stops
*before* finishing and lists it: never delete what wasn't copied.

Old clients: listings of an encrypted account require the
`X-NullDrive-E2EE: 1` header. A Phase 4–6 desktop client doesn't send it,
gets `409 e2ee_client_required` and stops with a message, instead of
downloading ciphertext as files called `~kZ3…`. A current client that
isn't unlocked yet doesn't send it either.

When a desktop app adopts the encrypted account, its index is reset
(`Index.UseKeyFingerprint`): base hashes from the plaintext era would make
every file look changed on both sides and produce a conflict copy of
everything. After the reset, local and remote files are re-matched by
content (in the new hash space): no transfers, no conflicts (tested).

### New devices and the pairing flow

The new device receives the **wrapped** bundle in the pairing response and
unlocks it locally with the passphrase or recovery key. The tempting
alternative, "encrypt the master key with the pairing code and hand it
over", doesn't work: the code has ~30 bits, so the server relaying it
could brute-force it in milliseconds. Transferring the key itself without
the passphrase needs either a password-authenticated key exchange (PAKE;
no vetted Go + browser pair is available, and the brief rules out homemade
crypto) or an X25519 exchange with the user comparing a short
authentication string on both screens, with a commitment step so the
server can't grind matching strings. That's a reasonable future addition;
typing the passphrase once per device is the simple, safe version.

The desktop app keeps the master key in the OS keychain after unlocking
(same place as the device token). That adds nothing for someone holding the
unlocked laptop, who can read the synced plaintext anyway. The browser
keeps it only in memory: never `localStorage`/`sessionStorage`, which
browsers may persist to disk for session restore.

### Proof of key possession

Replacing the bundle (new passphrase, new recovery key) or finishing the
migration requires `auth_key = HKDF(master, "auth")`; the server stores its
SHA-256. A stolen session can therefore not swap in a bundle under the
attacker's passphrase or destroy the wraps, and the server can't fake it
(HKDF is one-way). The bundle's fingerprint must also stay the same: you
can re-wrap the key, never replace it.

### The web app: what in-browser decryption can and can't promise

The web app derives the key with libsodium (WebAssembly; the CSP allows
`'wasm-unsafe-eval'`, which permits compiling WebAssembly but not
`eval()`), and encrypts and decrypts in the tab. It protects against a
server that stores and serves data, not one that serves malicious
JavaScript. That's stated in the threat model and in Settings, and the
desktop app is the answer for anyone who needs the stronger guarantee.
Thumbnails are off for encrypted files (they were plain `<img>` links).

### Security pass

- **Lockout with backoff, per username**, on top of a per-address limit.
  Backoff (30 s doubling to 15 min) rather than a hard lock, so knowing the
  admin's username isn't enough to lock them out; unknown usernames are
  counted too, so the lock doesn't reveal which accounts exist. In memory:
  a restart clears it, which an attacker can't trigger.
- **TOTP** on top of `crypto/hmac` (about 30 lines, RFC 6238 vectors in the
  tests) rather than a dependency that would also pull in a QR library (the
  UI already renders QR codes). Replay protection stores the last used step
  and updates it with a conditional `UPDATE`, so two concurrent logins with
  the same code can't both succeed. Recovery codes are random and
  single-use, stored as SHA-256 (no Argon2 needed for 50 random bits plus
  rate limiting).
- **CSRF tokens** as HMAC(server key, session hash): stateless, bound to the
  session, delivered in the login and `/me` responses because the cookie is
  `HttpOnly`. Plus an Origin/`Sec-Fetch-Site` guard for requests that have
  no session yet (login, setup). Bearer-token clients are exempt: browsers
  never attach those on their own.
- **Headers:** `default-src 'none'; sandbox` CSP and `no-store` on API
  responses, COOP/CORP same-origin, Permissions-Policy, and HSTS only when
  the request really came over TLS (or through a proxy marked as trusted):
  HSTS over plain HTTP is ignored by browsers, and a spoofable
  `X-Forwarded-Proto` must not turn it on. No `includeSubDomains`: the
  owner's other subdomains may legitimately be HTTP.
- **Audit log** for sign-ins, lockouts, devices, pairing failures, settings,
  2FA and encryption changes. Never file names. Pruned at 180 days.
- **Revocation is immediate**: revoking a device closes its WebSocket (Phase
  5); signing out and changing the password now also close browser
  sessions' sockets.
- **CI:** govulncheck (call-graph based, so findings are real), npm audit
  (production dependencies at high, all at critical), CodeQL with the
  security-extended queries, weekly schedule; releases wait for the
  security workflow.

### Deliberately not handled yet

- Master-key rotation (re-encrypting everything), turning E2EE off in place.
- Device-to-device key transfer without the passphrase (SAS-verified X25519).
- Hiding sizes/structure (padding chunks, flat namespace).
- Multi-user administration (the per-user isolation underneath is done).
- WebAuthn/passkeys as a second factor.

## Phase 8 — Packaging (in progress)

### Logos: one mark, two colors

The mark is the "null" glyph, a circle crossed by a **double slash** (`//`), in a drive bay with a pink activity LED. The slashes carry the identity:

- **cyan `//`**: the desktop app (app icon, tray icon, installer, its UI);
- **red `//`**: the server and the web app it serves (favicon, Docker and NAS app-store icons).

On a taskbar, a NAS dashboard and a browser tab at the same time, you can tell which piece you're looking at. Red is otherwise reserved for alerts in the Neon Lab palette, so it gets its own token (`--brand-server`) instead of borrowing `--danger`: a status color changing must never recolor the logo. The slashes are steeper than 45° so they read as a typed `//`, not an X. Below 48 px the bay and LED turn to mush, so small icon sizes and tray icons use the glyph alone.

Everything is drawn from one definition, `scripts/gen-icons.py` (Pillow, 8× supersampled, `make icons`): SVGs, PNGs, the multi-size Windows `.ico` (16–256 px, PNG-compressed entries), tray icons per sync state, and the installer artwork. The Svelte `Logo` component uses the same 32-unit coordinates, and each app picks its color with one CSS variable (`--logo-slash`), so there's no hand-edited icon file to drift.

### Windows: cross-compiled, one script

`scripts/build-windows.sh` builds the Windows app and its installer from any OS, and CI runs it on Linux.

- **Why not `wails build` on a Windows runner?** Wails' Windows target is pure Go: WebView2 is loaded at runtime and there's no cgo, so `go build -tags desktop,production -ldflags -H=windowsgui` produces the same program. What `wails build` adds is resource compilation (icon, manifest, version info), done here by `go-winres` from `client/build/windows/winres.json`. One script builds identical files on a laptop, in CI and in this repo's sandbox, the installer comes out of the same step, and Linux runners are cheaper and faster to start than Windows ones. CI still builds and tests natively on Windows (`desktop` job), so Windows-only code paths run on Windows.
- **Manifest:** per-monitor-v2 DPI awareness (crisp on mixed-DPI setups), Common Controls v6, `asInvoker` (never elevates), long-path aware (deep synced trees).

### The installer

NSIS, per-user, into `%LOCALAPPDATA%\Programs\Null Drive`.

- **Per-user, no admin.** A sync client belongs to one person (Dropbox and VS Code's user installer do the same). No UAC prompt, updates never need elevation, and it works on locked-down work PCs where users can't elevate. The trade-off: each Windows account installs its own copy, which is what you want for a per-account sync folder anyway.
- **NSIS over MSI/WiX or Inno Setup:** it builds on Linux (`makensis`), so packaging stays in the same cross-platform script. It's also what Wails uses and what winget handles natively (`InstallerType: nullsoft`, `/S`). An MSIX for the Microsoft Store is a separate package (Store-signed, sandboxed install), next in Phase 8.
- **Closing a running copy cleanly.** The installer can't replace `NullDrive.exe` while it runs, and killing it mid-sync is unfriendly. Since the window hides to the tray on close, a WM_CLOSE doesn't quit it either. So the installer runs `NullDrive.exe --quit`, which sends the running copy a message through the channel Wails' single-instance lock already listens on: a `WM_COPYDATA` (tag 1542) with the arguments as JSON, sent to Wails' hidden message-only window. The running copy then calls `onSecondInstance`, sees `--quit`, stops the sync agent and exits. `--quit` does this **without starting Wails**, because Wails checks for WebView2 first and would show a download prompt that would hang a silent uninstall. Only if the exe is still locked after ~10 s does the installer force-close it. (Tested under Wine against a stand-in that creates the same window and decodes the message the way Wails does.)
- **WebView2:** shipped with Windows 11 and current Windows 10. The installer checks the registry (per-machine and per-user Evergreen keys) and, if it's missing, offers Microsoft's download page instead of bundling a 2 MB bootstrapper that would be out of date by the next release. The app itself also prompts (Wails' default) as a second line.
- **Uninstall never touches the sync folder.** It removes the app, the Start menu entry, the "start on login" Run key and the Apps & features entry. Settings and the saved sign-in are removed only if the user says yes; `NullDrive.exe --forget` deletes the device token and encryption key from Credential Manager (the uninstaller can't enumerate those itself) and the settings folder. Silent uninstall keeps them, so `winget upgrade` style reinstalls don't sign you out.

### Code signing and SmartScreen

Unsigned executables get a SmartScreen warning until they build download reputation. The build signs the exe and the installer with `osslsigncode` (works on Linux) when a certificate is provided, timestamping both so signatures outlive the certificate. Certificates cost money and, since 2023, live on hardware tokens or cloud HSMs, so the realistic options are Azure Trusted Signing (cheap, needs an identity check) or an OV certificate on a cloud HSM. The build's hook takes a `.pfx` through optional CI secrets (client/README.md), and nothing breaks without them; with Azure Trusted Signing, that one step would be swapped for Microsoft's signing action.

### winget

Every build writes the three winget manifests (version, installer, default locale; schema 1.6) with the release URL and installer hash, and the release attaches them, so submitting a version is a copy into a `winget-pkgs` pull request. The manifest declares the `Microsoft.EdgeWebView2Runtime` dependency and user scope.

## Cross-product search (Null franchise, Phase 3)

Null Drive's side of the shared search contract (`GET /search`, see API.md › Search). Null Search, a public web app, calls it from the user's browser alongside the Null Notes local agent and merges the results. Code: `server/internal/search` (index, extraction, query) and `server/internal/api/handlers_search.go` (endpoint, CORS, token scope).

### One index file per user

Each account's index is its own SQLite database, `<data dir>/indexes/<user id>.db` (mode 0600), never a table shared by users with a `user_id` column. With a shared table, isolation is a `WHERE` clause that every query must get right forever; here the only input to "which file" is the authenticated user's numeric id from the token (`strconv.FormatInt`, no request data, no path joining of user input), so a bug in a query can't leak another account's rows. It also makes "forget this user's index" a file delete, which matters for encryption (below), and keeps one big account from slowing everyone's searches.

### Following the change feed instead of hooking every write

The server already has a monotonic change feed (`ListFiles(since)`) that sync clients use. The index stores the last feed version it applied and pulls what changed since, so every write path is covered without touching it: v1 and v2 uploads, moves, folder deletes, restores from history and Recently deleted, retention. It also means the index survives crashes and restarts (it catches up), and deleting `indexes/` or bumping the schema version simply rebuilds from version 0. Freshness: every change notification (`Server.changed`) also *kicks* the user's index (debounced 300 ms, in the background), and each search first lets the index catch up for at most 700 ms, then answers from what's indexed. Null Search gives Drive 1.2 s, so a large first build (thousands of PDFs) returns partial results instead of timing out, and fills in on the next search.

Renames and moves keep the extracted text: rows are keyed by the file's stable id, and when the content hash hasn't changed only the name and path are updated.

### Stable file ids

The contract wants an `id` that identifies a file over time (and `nulldrive://open?id=`). Database row ids don't work: a move writes a new row at the destination. Migration 0006 adds `files.uid` (128-bit random, hex) that moves with the file (move = carry the uid to the destination, give the source tombstone a fresh one), survives overwrites, and is revived by "restore" from history or Recently deleted; a *new* file at a deleted file's path gets a new id. The same id is now in `GET /files` listings (`id`).

### Matching: FTS5 trigram plus two fixes

The contract names FTS5 trigram as the reference: case-insensitive substring matching, so `libre` finds `LibreAI`. With `remove_diacritics 1`, `resume` finds `résumé`. Two gaps closed on top:

- **Separators.** The query is split into words on anything that isn't a letter or digit, and all words must match (in the name or the text): `libre ai`, `libre-ai`, `LibreAI` all find each other. Names are also indexed *squashed* (letters and digits only) and the squashed query is matched against that, so `libreai` finds `Libre AI.md`.
- **Short words.** Trigrams need 3 characters; `ai` or `30` alone would find nothing. Short words are matched with `LIKE` within the user's index (a scan, fine at personal scale) and ANDed with the rest.

User text is always quoted as an FTS5 string (and `LIKE`-escaped), so it can't inject FTS5 syntax. Ranking: name matches first, then BM25 with the name weighted 10× over the text. It's substring matching, not typo tolerance: `lirbe` doesn't find `LibreAI`. That's what the reference implementation does too.

### What gets indexed

Every file by name. Text from `.md`/`.markdown` (frontmatter stripped; its `id:` kept for `synced_id`), `.txt`, and `.pdf` up to 64 MB, capped at 512 KiB of text per file (the trigram index is ~3× the text). PDF text comes from `github.com/ledongthuc/pdf` (pure Go, BSD, no dependencies; CGO-free like the rest of the binary). Parsers of hostile files panic or loop, so extraction runs with `recover` and a 20 s limit; a PDF that fails is indexed by name. Skipped entirely, per the Null Notes on-disk format: locked notes (`*.lock` that start with `NNLOCK1\n`; an ordinary `yarn.lock` is still indexed), `*.nntmp`, and everything under `.nullnotes/`, except that `.nullnotes/ids.json` is read for the ids of non-Markdown notes.

`synced_id` for Markdown is the frontmatter id wherever the file is (since the Notes-backup phase: a backup may put a vault anywhere, and a UUID equal to a note's id means it is that note). Other note types need their vault's `.nullnotes/ids.json`, so they only get one inside a synced vault.

### Tokens: a search-only scope

Null Search keeps the Drive token in the browser's storage for its origin, which is a weaker place than the OS keychain the desktop app uses. So the web app's *Connect Null Search* issues a device token with scope `search` (migration 0006: `devices.scope`): it can call `GET /search` and `GET /me`, and gets `403 insufficient_scope` everywhere else, checked once in `requireAuth`. A leaked search token reveals file names and snippets but can't download, change or delete anything, and it's revocable like any device. Full tokens and sessions can search too (curl, scripts).

### CORS and Private Network Access

Only `/search` speaks CORS, only for origins in `NULLDRIVE_SEARCH_ORIGINS` (default `https://search.nullgrid.dev`; validated as bare origins at startup), only `GET` with an `Authorization` header, and never with credentials. The session cookie is `SameSite=Strict` anyway, but not sending `Allow-Credentials` means a listed origin can't ride a signed-in web session even if that changed. Preflights get `Access-Control-Allow-Private-Network: true` when asked, since Null Search is public and a home server is usually on a private address. Chrome is replacing that header with Local Network Access (a permission prompt), which needs nothing from the server. The browser still has to reach Drive over HTTPS, because an HTTPS page can't fetch `http://192.168.x.x` (mixed content). That's a deployment requirement (INSTALL.md), not something the server can fix.

### End-to-end encrypted accounts

An index is plaintext names and text. For an encrypted account it would undo the encryption, and the server can't read the files anyway. So: turning encryption on deletes the account's index files (`-wal`/`-shm` included) in the same request; every index sync checks the account's state first and deletes instead of building; and search answers `[]` (with `X-NullDrive-Search: encrypted`). The sync test suite runs with search on, builds an index before encrypting, checks the plaintext scan *does* find the index's text then, and checks the full data directory scan finds nothing afterwards.

### More than one account

Per-user indexes only mean something with several users. `nulldrive add-user <name>` creates a non-admin account with a random password (same CLI style as `reset-password`). Each account has its own files, devices, index and encryption; chunks are still deduplicated across accounts as before (Phase 6) without letting one account probe another's.

### Deliberately not handled yet

- Office documents (`.docx`, `.odt`), OCR of scanned PDFs, and typo tolerance.
- Results are per account; files shared between accounts don't exist yet.

## Opening links: nulldrive:// (Null franchise, Phase 5)

A search result's `open` field is `nulldrive://open?id=<file id>`. Clicking it in Null Search opens the file from the computer's sync folder. Code: `client/link.go` (parsing, the command line), `client/openlink.go` (resolving and opening), `client/shell_windows.go` and `client/ipc_windows.go` (Windows), `GET /api/v2/ids/{id}` on the server, `/open?id=` in the web app.

### A URL scheme is an attack surface

Any web page can try to launch a registered scheme. Browsers ask the user first, but a user who clicked "Allow always" for Null Search's origin has also allowed every link Null Search shows. So the handler treats a link as untrusted input:

- **One action, one strict id.** `parseLink` accepts `open` and an `id` of exactly 32 hex characters, nothing else: no paths, no user info, no fragments, no duplicate ids. Nothing from the link reaches a shell or the filesystem. The *server* turns the id into a path, for the account this computer syncs, and the client joins that path (after `pathutil.Clean`) under the sync folder.
- **Nothing on the command line but the link.** Every registration runs `NullDrive.exe --url "%1"`. When the arguments contain a link, `main` ignores all other flags, so a crafted URL that breaks out of the quotes can't add `--forget` (delete settings) or `--quit`. Tested in Wine with a link that does exactly that.
- **Links never run programs.** Opening a document is the point; opening `setup.exe` from a link is how URL handlers become remote code execution. Files whose type can run code (`.exe`, scripts, installers, shortcuts, `.url`, `.reg`, …, plus anything Windows' own `AssocIsDangerous` flags and, on macOS and Linux, files with the executable bit) are **revealed** in their folder instead, as are symlinks and other non-regular files. The user can still open them; the link alone can't.
- **ShellExecute, not `explorer.exe <path>`.** Explorer parses its command line (a comma starts a switch), and a synced file can be named anything. `ShellExecuteW` and `SHOpenFolderAndSelectItems` take the path as data.
- **One link at a time.** A page can't queue a burst of launches.

### Delivery: first launch or running copy

If Null Drive isn't running, the link starts it in the tray and is handled once the window has loaded (dialogs need it). If it is running, which is the usual case for a sync client, the new process must hand the link over and exit. On Windows it does so **without starting Wails**: it finds the running copy's single-instance window and sends the arguments over the same `WM_COPYDATA` channel Wails uses (the `--quit` mechanism from Phase 8, generalized), after `AllowSetForegroundWindow` so the file the running copy opens comes to the front instead of flashing in the taskbar. That takes tens of milliseconds instead of a WebView2 start. On macOS links arrive as an Apple event (`OnUrlOpen`); elsewhere Wails' single-instance lock forwards them.

Two details from testing this under Wine:

- **Mutex first, then the window.** The app checks Wails' single-instance mutex: if a copy runs but its window isn't found yet (it may still be starting), it keeps looking for up to 2 s instead of starting a second copy, which would then go through Wails' slower path.
- **`SendMessageTimeout`, not `SendMessage`.** If the running copy hangs, a plain `SendMessage` hangs the new process with it: a link that silently never finishes, or an installer stuck on `--quit`. With a 5 s timeout (and `SMTO_ABORTIFHUNG`) it gives up instead. (Found because a test stand-in that didn't pump messages on its window's thread did exactly that.)

### Resolving: ask the server

The desktop app keeps no id → path map (its index is keyed by path), and the server's answer is always current: the id survives renames and moves made elsewhere since the last sync. `GET /api/v2/ids/{id}` returns the file (deleted ones too, marked, so the app can say "it's in Recently deleted" instead of "not found"). Another account's id is a plain 404, same as a missing one. Search-only tokens can't call it. Encrypted accounts get the encrypted path back and the app decrypts it with the key it already has.

Then: the file is in the sync folder → open (or reveal). It's on the server but not here yet (a link to something just uploaded elsewhere) → sync now and wait up to 20 s. Otherwise (deleted, not synced, another account's, server unreachable, app not set up) the app says which, in a native dialog, and offers the **web app's `/open?id=`** page, which resolves the same id after sign-in and shows the file highlighted in its folder.

### Registration on each platform

| Build | How `nulldrive://` is registered | Removed by |
|---|---|---|
| Installer | NSIS writes `HKCU\Software\Classes\nulldrive` (per user, no admin), notifies the shell | the uninstaller, only if the command still points at this install |
| Portable zip | the app registers itself at start, but only if no other existing copy owns the scheme, so running a portable copy once doesn't take links from the installed one | — |
| Microsoft Store | `uap3:Protocol` in the manifest, with `Parameters="--url &quot;%1&quot;"` | Windows, with the package |
| macOS (future bundle) | `CFBundleURLTypes`, generated by Wails from `wails.json` › `info.protocols` | — |
| Linux (future packages) | `MimeType=x-scheme-handler/nulldrive` in `client/build/linux/nulldrive.desktop` | — |

### Microsoft Store (MSIX)

The Store build is the same `NullDrive.exe` in an MSIX (`client/build/windows/msix/AppxManifest.xml`, `scripts/build-msix.sh`). Choices:

- **Full trust, not a sandboxed app.** Null Drive is a Win32 program (Wails, WebView2, a tray icon, a user-chosen sync folder, Credential Manager), so it's packaged with `runFullTrust` like other desktop sync clients in the Store. The submission has to justify that restricted capability (docs/STORE.md).
- **Startup task instead of the Run key.** Inside a package, writes to `HKCU` (and new files under `%AppData%`) go to a private per-package location, and a Run entry written there is ignored at sign-in. The manifest declares a `windows.startupTask` (`Enabled="true"`: on after the first launch, controlled in Windows Settings). The app detects the package (`GetCurrentPackageFullName`) and replaces its own switch with a pointer to that page.
- **Real paths for Explorer.** Because `%AppData%` is redirected, "Open logs" maps the log folder to `%LocalAppData%\Packages\<family>\LocalCache\…`, which Explorer can see.
- **Built on Windows CI with Microsoft's tools.** `MakePri` indexes the scaled icons (`scale-200`, `targetsize-*`, unplated taskbar variants) and `MakeAppx` packs. On Linux, `makemsix` (Microsoft's open-source packer) builds a package with the 100% icons and validates the manifest against the schemas; that's how this phase was verified here, including negative tests (a misspelled attribute, a three-part version, an invalid protocol name are all rejected).
- **Unsigned for the Store, signed for sideloading.** The Store signs uploads. `MSIX_CERT_PFX` signs a test build with `osslsigncode` (which supports MSIX since 2.6) for installing on a PC that trusts the certificate.
- **Identity from Partner Center** comes in through repository variables, so the manifest in git has no account-specific values.
- **Version** is the tag with a fourth part of 0, which the Store requires.

### Deliberately not handled yet

- `nullnotes://` is the Null Notes app's (its own codebase); the shared rules for both schemes are in `docs/OPEN-LINKS.md` (also the Project doc `null-grid/uri-scheme-contract.md`).
- No "open with" choice for a file type the user hasn't associated: Windows shows its own picker.
- The Store build hasn't been through certification yet (needs the Partner Center account and a demo server for the testers).

## Null Notes backup: the Drive side (Null franchise, Phase 6)

Null Notes can back a vault up into the user's Drive. Drive already had most of it (folders, versioned uploads, a change feed, soft deletes); this phase fills the gaps a mirroring tool hits. The calls a backup makes are in `docs/NOTES-BACKUP.md` (also the Project doc `null-grid/notes-backup-drive-api.md`).

### `If-Match: *`: replace by path

Sync clients must say which version they're replacing (`If-Match: <version>`), so concurrent edits become conflicts instead of silent overwrites. A backup is different: the vault is the source of truth and the Drive copy just follows it. Without a way to say so, it would have to fetch every file's version first. `If-Match: *` (HTTP's "any current representation") means create or overwrite, on both upload APIs and on delete. Nothing is lost: the replaced content is a version in history, restorable for the history window. Numeric `If-Match` and the `428` for a missing one are unchanged for everyone else.

### mtime from the device

`modified_at` is the server's time of the change, which a backup can't compare with a note's file time. Uploads can carry the device's modification time (`X-NullDrive-Mtime`, or `mtime` in a v2 commit). It's stored with the version (a restore brings it back), truncated to milliseconds (the database's precision, so equal times compare equal), and listed as `mtime`. A backup can then skip a file whose size and mtime match without reading it, and fall back to `sha256` when they don't. Re-uploading identical content with a new mtime (a note touched without edits) updates the mtime in place: no new version, no change-feed entry, so sync clients don't download anything.

Encrypted accounts can send it too. It's metadata in plaintext like sizes and `modified_at`, and the client decides whether to send it.

### `?prefix=`

`GET /files?prefix=Backups/Null Notes/Work` lists one subtree (with `since`, just its changes) using the same `(user_id, path)` range scan as folder operations, instead of the whole account.

### A real trash: 30 days, separate from history

Deletes were already soft (tombstone + Recently deleted), but how long a deleted file stayed was the version-history window (30 + 90 = 120 days by default), which made the bin's lifetime a side effect of an unrelated setting. Now `trash_days` (default 30, 1–3650) is its own setting:

- A deleted file's last version is protected from pruning for `trash_days`, even when the history window is shorter.
- After that, **every** version of the path is purged and the chunks they alone used are collected. The tombstone row stays (without its restore pointer), so sync clients still learn about the delete from the change feed.
- The trash listing hides expired items at once, and `purge_at` = deleted at + `trash_days`.

Nothing is ever removed immediately by `DELETE`, which is what a backup that mirrors deletions needs: deleting a note by mistake in the vault can be undone from Drive for a month.

### `synced_id`, stored

The server reads the note id from Markdown frontmatter (`id: <uuid>`, Null Notes' format) on every upload and stores it on the file and the version (migration 0007), so listings and the id lookup return it, not just search. On a v1 upload it's read from the first 64 KiB of the body as it streams in; on a v2 commit from the stored chunks, after the commit has proved the user owns them. A later version without frontmatter clears it; a restore brings back the old one. Encrypted accounts are skipped: the server sees only ciphertext. The parser is shared with search (`server/internal/noteid`).

## Two-way sync with Null Notes: the Drive side (Null franchise, Phase 7)

Null Notes' sync engine matches files by note id, then Drive file id, then path, and makes every write conditional on the version it last saw. Drive already had almost everything it needs (versions as ETags, `409 conflict` with the current file, `428` for unconditional overwrites, `404` for vanished paths, `target_exists` for occupied move destinations). The answers, with exact shapes, are in `docs/NOTES-SYNC.md` (Project: `null-grid/notes-sync-drive-api.md`). Three gaps were closed:

- **Moves looked like deletes.** A move leaves a tombstone at the old path so path-based clients (the desktop app) remove it. But that tombstone was indistinguishable from a delete and still carried the note's `synced_id`. A client matching by note id could see the same note "deleted" and "alive" in one feed, and pick wrong. Migration 0008 adds `files.moved_to`: a move's tombstones name the new path and drop `synced_id`, so a note id appears on exactly one row. Any later write at that path clears the marker (`upsert` resets it). The desktop engine ignores the field and behaves as before.
- **Create-only had only a Drive-specific spelling** (`If-Match: 0`). Uploads now also take HTTP's own `If-None-Match: *`, on both APIs. Combining it with `If-Match`, or giving it any other value, is a `400` instead of a guess.
- **Downloads didn't say everything a sync client stores.** Besides `ETag` and `X-Content-SHA256`, a download now returns the file id, `mtime` and `synced_id` as headers, so the client needn't list just to learn them.

Moves also accept the expected version as an `If-Match` header, like PUT and DELETE (the body field stays; if both are sent they must agree).

Mismatches stay `409 conflict`, not `412`. The status says "your view is stale", and the body (`current`) gives the state needed to re-plan without another request; every Drive client already relies on it. Identical content is still a successful no-op whatever the precondition, which is what makes retries after a lost response safe, but only when Drive knows the hash before reading the body (`X-Content-SHA256`). Without it, a create-only retry is a `409`, the price of failing doomed uploads before the bytes are sent.

## Null Notes in encrypted accounts: the Drive side (Null franchise, Phase 8)

Null Notes wants to back up and sync into accounts with end-to-end encryption, which so far answered it with `409 e2ee_required`. Nothing about the server had to change for that: since Phase 7 (Drive's own) the server only stores opaque names, chunks and a key bundle, and the desktop app and the web app are already two independent clients of that protocol. What was missing was the protocol written down for someone who isn't us, plus evidence that the write-up is enough. The answer is in `docs/NOTES-E2EE.md` (Project: `null-grid/notes-e2ee-drive-api.md`).

- **A spec is only as good as its second implementation.** `scripts/e2ee-reference/nulldrive_e2ee.py` is a small Python client built on libsodium (PyNaCl) and the standard library. It follows the doc step by step and shares no code with the Go and TypeScript implementations. It reproduces both vector files and runs the Notes flow against a real server (`make e2ee-ref`): unlock, create-only upload, conflict, list, download, move, delete, change feed, search. Any ambiguity in the doc would have shown up as a mismatch there. The one gap it found was in the doc, not the server: an empty file has no chunks (a chunk must carry at least one byte), and the doc now says so.
- **A worked example beats a format description.** `shared/e2ee/testdata/notes-vector.json` walks one real note path and one real note through every step: subkeys, padded names, synthetic nonces, the sealed chunk, its hash, the keyed file hash and the commit body. Another implementer can find the exact step where they diverge. `vectors.json` (shared with the web UI's tests) is untouched. The new file uses the same master key, so the existing bundle vector covers unlocking.
- **One API addition, additive:** v2 manifests now carry `id`, `mtime` and `synced_id`. On a plain account the v1 download returns those as headers (Phase 7). An encrypted client can't use the v1 download, so without this addition it would have needed a listing to learn a file's id and mtime. Existing clients ignore unknown JSON fields; the desktop engine's tests are unchanged.
- **`synced_id` stays absent on encrypted accounts, deliberately.** It was derived server-side from frontmatter, which the server can no longer read. Letting the client set it would put a stable, cross-app identifier back on the server in plaintext, which is the kind of linkable metadata E2EE exists to remove. Notes can match by Drive's file id and the decrypted path, and learns note ids from content it downloads anyway. Search likewise stays off (the index would be plaintext); `X-NullDrive-Search: encrypted` lets Null Search say why it found nothing.
- **What the doc is honest about.** The server still sees plaintext size, `mtime` if it is sent, tree shape and equal names (deterministic names are what make "find this path" and `?prefix=` work without decrypting the whole account). The doc says which of these a client can avoid (`mtime` is optional). It also tells clients to pin the key fingerprint: a server can't forge a bundle that unlocks with the user's passphrase, but pinning makes an unexpected key change a hard stop rather than a silent fork.

The raw-HTTP counterpart in Go is `TestNotesE2EEFlow`. It uses only the `e2ee` package's primitives, never the desktop client's transfer code, so it tests the protocol as a stranger would use it. It also covers the cases the Python run doesn't: refusals for every plaintext route (including folder creation and move sources), the size check, `428`, plaintext leftovers in a migrating account's listing, trash and `ids`.
