# Null Drive HTTP API (v1)

Base URL: `http://<server>:8080/api/v1`. All request and response bodies are JSON unless noted (file uploads and downloads are raw bytes).

Every path *outside* `/api/` serves the embedded web UI (see DESIGN.md), except `GET /search` (the cross-product search endpoint, see [Search](#search)). Unknown `/api/...` paths always get a JSON `404`.

## Conventions

**Authentication.** Send `Authorization: Bearer <token>`. Two kinds of token exist:

| Prefix | Kind | Obtained from | Lifetime |
|---|---|---|---|
| `nds_` | Session (web UI, scripts) | `POST /setup`, `POST /auth/login` | 30 days |
| `ndd_` | Device (desktop client) | `POST /devices`, `POST /pairing/claim` | Until revoked |

A device token has a **scope**: `full` (everything the user can do; the default) or `search` (only `GET /search` and `GET /me`, for Null Search; anything else gets `403 insufficient_scope`).

Browsers can instead rely on the `nulldrive_session` cookie that login sets (`HttpOnly`, `SameSite=Strict`). Missing or invalid credentials return `401` with a `WWW-Authenticate: Bearer` header.

**CSRF (Phase 7).** A state-changing request (anything but `GET`/`HEAD`/`OPTIONS`) authenticated by the *cookie* must send `X-NullDrive-CSRF: <csrf_token>`, where the token comes from the login/setup response or `GET /me`; otherwise `403 csrf_failed`. Bearer-token requests don't need it. Independently, any state-changing `/api/` request whose `Origin` is another host, or that a browser marks `Sec-Fetch-Site: cross-site` or `same-site`, is refused with `403 bad_origin` (this also covers login and setup). Clients that send no `Origin` (curl, the desktop app) are unaffected.

**End-to-end encrypted accounts (Phase 7).** Clients that can decrypt send `X-NullDrive-E2EE: 1`. For an encrypted account, listings and file operations without it get `409 e2ee_client_required`; whole-file uploads/downloads (`PUT`/`GET /files/{path}`, `GET …/versions/{v}/content`) get `409 e2ee_required`; and every name written (commit, folder, move source and target, restore) must be an encrypted segment (`~` + base64url, see DESIGN.md) or the request gets `409 e2ee_path_required`.

**Security headers.** API responses (and `/search`) carry `Cache-Control: no-store`, `Content-Security-Policy: default-src 'none'; frame-ancestors 'none'; sandbox`, `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, COOP/CORP `same-origin`, and over TLS `Strict-Transport-Security: max-age=31536000`.

**Errors.** Every non-2xx JSON response has this shape:

```json
{ "error": { "code": "conflict", "message": "version conflict: current version is 7", "current": { ... } } }
```

`current` appears only on version conflicts. Error codes:

| Status | `code` | Meaning |
|---|---|---|
| 400 | `invalid_json`, `invalid_path`, `invalid_if_match`, `invalid_sha256`, `hash_mismatch`, `invalid_credentials`, `invalid_name`, `invalid_since`, `invalid_move`, `invalid_id`, `is_folder`, `invalid_code`, `invalid_scope`, `invalid_limit`, `query_too_long` | Bad input |
| 401 | `unauthenticated`, `invalid_credentials` | No/invalid token, or wrong login |
| 403 | `session_required` | Device tokens may not perform this action |
| 403 | `insufficient_scope` | A search-only token used outside `GET /search` / `GET /me` |
| 401 | `totp_required`, `invalid_totp` | Account has 2FA: send `totp`; the code was wrong or already used |
| 403 | `admin_required`, `bad_origin`, `csrf_failed` | Not an administrator; cross-origin request; missing CSRF token |
| 403 | `e2ee_auth_failed` | Wrong auth key for an encryption bundle change |
| 404 | `not_found` | No such file, folder, device or endpoint |
| 405 | `method_not_allowed` | |
| 409 | `conflict` | `If-Match` did not match the current version |
| 409 | `target_exists` | Move destination already exists |
| 409 | `path_collision` | Path would be both a file and a folder |
| 409 | `setup_complete` | Setup was already done |
| 409 | `e2ee_client_required`, `e2ee_required`, `e2ee_path_required` | Encrypted account: see above |
| 409 | `e2ee_already_on`, `e2ee_off`, `totp_enabled`, `totp_disabled`, `totp_not_pending` | State doesn't allow the action |
| 426 | `upgrade_required` | `GET /ws` without a WebSocket upgrade |
| 428 | `precondition_required` | Overwriting an existing file needs `If-Match` |
| 429 | `rate_limited`, `too_many_codes`, `locked_out` | Pairing or sign-in limits, account lockout (see `Retry-After`) |
| 500 | `internal` | Server bug; message includes the request ID |
| 503 | `search_disabled` | The server runs without search indexes (embedding/tests only) |

**Versions.** The server keeps one global, monotonic change counter. Every create, update, delete or move takes the next value, and the affected file rows record it in `version`. A file's `version` doubles as its ETag (`"12"`).

**Paths.** File paths are relative, use `/`, and are validated on every request. Rejected: `..` segments, absolute paths and drive letters, null bytes and control characters, `<>:"|?*`, names ending in a dot or space, Windows reserved names (`CON`, `NUL`, `COM1`…), segments over 255 bytes, paths over 4096 bytes (encrypted segments, `~` + base64url, may be up to 400 bytes and such paths up to 16 KiB). Backslashes become `/`; empty and `.` segments are dropped. In URLs, percent-encode as usual (`My%20Docs/r%C3%A9sum%C3%A9.pdf`).

**Request IDs.** Every response carries `X-Request-ID` (echoed if the client sent one), also logged by the server.

---

## Health and setup

### `GET /health`
No auth. `version` and `commit` are stamped at build time from the Git tag (`dev`/`none` for local builds). Docker's `HEALTHCHECK` uses it via `nulldrive healthcheck`.
```json
200 { "status": "ok", "version": "v0.1.0", "commit": "abc1234" }
```

### `GET /setup/status`
No auth. Tells the UI whether to show the first-run wizard.
```json
200 { "setup_complete": false }
```

### `GET /system/info`
**No auth until setup is complete** (the wizard shows it before an account exists); afterwards any valid token. Reports where data is stored and how much space is left.
```json
200 { "version": "v0.2.0", "data_dir": "/srv/nulldrive/data",
      "disk_total_bytes": 250790436864, "disk_free_bytes": 31138447360 }
```
If free space can't be determined, `disk_error` explains why and the byte counts are `0`.

### `POST /setup`
No auth. Creates the first admin. Works exactly once; afterwards `409 setup_complete`.

Username: 1–64 of `A-Z a-z 0-9 . _ -`. Password: at least 8 characters.
```bash
curl -X POST localhost:8080/api/v1/setup \
  -d '{"username":"admin","password":"correct horse battery"}'
```
```json
201 {
  "token": "nds_3q2+7w…",
  "expires_at": "2026-11-12T07:00:00Z",
  "user": { "id": 1, "username": "admin", "is_admin": true, "created_at": "2026-10-13T07:00:00Z" }
}
```
Also sets the session cookie, so the wizard can continue straight into the app.

---

## Authentication

### `POST /auth/login`
No auth. Same response shape as `/setup` (`200`), including `csrf_token`. Wrong password and unknown user both return `401 invalid_credentials`, with identical timing.
```bash
curl -X POST localhost:8080/api/v1/auth/login -d '{"username":"admin","password":"correct horse battery"}'
```
With two-factor authentication on, the first attempt returns `401 totp_required`; repeat with `"totp": "123456"` (or a recovery code like `"abcde-fghjk"`). A wrong or reused code is `401 invalid_totp`.

Limits: 10 attempts per minute per client address (`429 rate_limited`). After 5 consecutive failures for a username (wrong password *or* code; unknown usernames too), it's locked for 30 s, doubling per further failure up to 15 min (`429 locked_out`, `Retry-After`). A success resets the count.

```json
200 { "token": "nds_…", "expires_at": "…", "csrf_token": "q3V…",
      "user": { "id": 1, "username": "admin", "is_admin": true, "created_at": "…", "totp_enabled": true, "e2ee": "on" } }
```
`e2ee` is `off`, `migrating` or `on`.

### `POST /auth/totp/setup`
Auth: **session only**. Creates a pending secret (replacing an unconfirmed one). `409 totp_enabled` if 2FA is already on.
```json
200 { "secret": "JBSWY3DPEHPK3PXP…", "uri": "otpauth://totp/Null%20Drive:admin?secret=…&issuer=Null+Drive&algorithm=SHA1&digits=6&period=30" }
```

### `POST /auth/totp/enable`
Auth: **session only**. `{"code":"123456"}` from the app confirms the pending secret. Returns 10 single-use recovery codes, shown once (stored hashed). `400 invalid_totp`, `409 totp_not_pending`.
```json
200 { "recovery_codes": ["k3m9q-7xw2p", "…"] }
```

### `POST /auth/totp/disable`
Auth: **session only**. `{"password":"…","code":"123456"}` (or a recovery code). Failures count towards the lockout. `204`.

### `POST /auth/logout`
Auth: any. Invalidates the credential used for the request: a session is deleted; a device token is revoked. Clears the cookie.
```
204 No Content
```

### `POST /auth/password`
Auth: **session only**. Requires the current password even though the caller is logged in.
```bash
curl -X POST localhost:8080/api/v1/auth/password -H "Authorization: Bearer $SESSION" \
  -d '{"current_password":"old one","new_password":"a new passphrase"}'
```
`204` on success. Every *other* session of the user is ended; the calling session and device tokens stay valid. Wrong current password → `401 invalid_credentials`; new password under 8 characters → `400 invalid_credentials`.

### `GET /me`
Auth: any. Who am I?
```json
200 { "user": { "id": 1, "username": "admin", "is_admin": true, "created_at": "…", "totp_enabled": false, "e2ee": "off" },
      "device": { "id": 3, "name": "Desktop PC", "created_at": "…", "last_seen_at": "…" } }
```
`device` is present only when a device token was used; `csrf_token` only for sessions (the browser reads it after a reload, since the cookie is `HttpOnly`).

### `GET /admin/audit`
Auth: **session, admin**. The security log, newest first: `?limit=` (1–200, default 50) and `?before=<id>` to page. Events: `setup_completed`, `login`, `login_failed`, `login_locked`, `logout`, `password_changed`, `totp_enabled`, `totp_disabled`, `totp_recovery_code_used`, `device_added`, `device_revoked`, `pairing_created`, `pairing_claim_failed`, `settings_changed`, `e2ee_enabled`, `e2ee_finished`, `e2ee_keys_changed`, `gc_run`. Never file names. Kept 180 days.
```json
200 { "events": [ { "id": 42, "at": "…", "event": "login_failed", "username": "admin", "ip": "192.168.1.30",
                    "detail": { "reason": "password" } } ],
      "next": 41 }
```

---

## Devices

### `POST /devices`
Auth: **session only** (`403 session_required` for device tokens, so a stolen device token cannot mint more devices). Registers a device and returns its token. **The token is shown only once**; the server keeps only its hash.
```bash
curl -X POST localhost:8080/api/v1/devices -H "Authorization: Bearer $SESSION" -d '{"name":"Desktop PC"}'
```
```json
201 { "device": { "id": 3, "name": "Desktop PC", "created_at": "2026-10-13T07:01:00Z", "scope": "full" },
      "token": "ndd_Zx9…" }
```
Optional `"scope": "search"` creates a search-only token (for Null Search); `"full"` or omitted is a normal token; anything else is `400 invalid_scope`.

### `GET /devices`
Auth: any. Lists the user's devices, including revoked ones (with `revoked_at`).
```json
200 { "devices": [ {
  "id": 3, "name": "Desktop PC", "created_at": "…", "last_seen_at": "…",
  "last_ip": "192.168.1.33", "client_version": "NullDrive/0.5.0 (windows/amd64)",
  "added_via": "pairing", "scope": "full", "online": true
} ] }
```
- `last_seen_at` / `last_ip` are written at most once a minute, or immediately when the address changes. `last_ip` is the TCP peer, or the last `X-Forwarded-For` hop with `NULLDRIVE_TRUST_PROXY=on`.
- `client_version` is the desktop app's `User-Agent` (only `NullDrive/…` agents are recorded).
- `added_via`: `password` (registered with `POST /devices` after signing in) or `pairing` (claimed a pairing code).
- `online` is true while the device holds a realtime connection (`GET /ws`).

Informational only: none of these fields is used for authorization.

### `DELETE /devices/{id}`
Auth: session only. Revokes the device: its token stops working immediately and any open realtime socket is closed (`1008 device revoked`). Idempotent. `404` if the device is not yours.
```json
200 { "id": 3, "name": "Desktop PC", "created_at": "…", "revoked_at": "2026-10-13T08:00:00Z" }
```

---

## Pairing

Adding a device without typing the account password on it. The web UI issues a short code; the new device exchanges it for a device token.

Code format: `NULL-XXXX-XX`, six characters from Crockford base32 (`0-9 A-Z` without `I L O U`). Input is normalized: case, spaces and dashes are ignored, the `NULL` prefix is optional, `O` is read as `0` and `I`/`L` as `1`. Codes are valid for **5 minutes**, **single use**, and stored only as a SHA-256 hash.

### `POST /pairing`
Auth: **session only**. Issues a code. The plaintext code appears only in this response. At most 5 unexpired, unused codes per user (`429 too_many_codes`).
```json
201 { "id": 12, "code": "NULL-7K2Q-9F", "status": "pending", "expires_at": "2026-10-15T13:11:52.511Z" }
```
The web UI also shows a QR code and a copyable link containing the server address and code:
`nulldrive://pair?code=NULL-7K2Q-9F&server=https%3A%2F%2Fdrive.example.com`

### `GET /pairing/{id}`
Auth: session only. Status of one of your codes, polled by the "Add device" dialog. `status` is `pending`, `claimed`, `expired` or `cancelled`; once claimed, `device` is the new device.
```json
200 { "id": 12, "status": "claimed", "expires_at": "…", "device": { "id": 7, "name": "Work laptop", "added_via": "pairing", … } }
```

### `DELETE /pairing/{id}`
Auth: session only. Cancels a pending code (the dialog does this when closed). Returns the final state; cancelling a finished code is a no-op.

### `POST /pairing/claim`
Auth: **none** (the code is the credential). Rate limited to **5 attempts per minute per client address** and **30 per minute in total**, checked before anything else. Over the limit: `429 rate_limited` with `Retry-After`.
```bash
curl -X POST localhost:8080/api/v1/pairing/claim -d '{"code":"null 7k2q 9f","device_name":"Work laptop"}'
```
```json
201 { "device": { "id": 7, "name": "Work laptop", "added_via": "pairing", … },
      "token": "ndd_…", "user": { "id": 1, "username": "admin", … } }
```
For an end-to-end encrypted account the response also has `"e2ee": { "state": "on", "bundle": {…}, "fingerprint": "…" }`: the *wrapped* key bundle, which the new device unlocks with the passphrase or recovery key (the pairing code is never used as a key; see DESIGN.md › Phase 7).

Unknown, expired, cancelled and already-used codes all return the same `400 invalid_code`, so the response never tells a guesser that a code exists. `400 invalid_name` for an empty or >100-character device name.

---

## Realtime

### `GET /ws`
Auth: device token in `Authorization: Bearer` (desktop app) **or** session cookie (browser). WebSocket (RFC 6455) upgrade; a plain GET returns `426 upgrade_required`. Cookie-authenticated handshakes must come from the same origin (`403 bad_origin` otherwise), which blocks cross-site WebSocket hijacking.

Server → client text frames (JSON). Clients send nothing except pings and pongs.

| Message | When |
|---|---|
| `{"type":"hello","version":42}` | Once, right after connecting. `version` is the current change-feed version, so a client that missed notifications while disconnected knows straight away whether to sync. |
| `{"type":"changed","version":43}` | The user's files changed. Sent to all of the user's sockets **except the device that made the change**. Coalesced: a slow client only gets the newest version. |
| `{"type":"presence","device_id":7,"online":true}` | Browser sessions only: a device connected or disconnected. |

Notifications carry no file data. Clients react by calling `GET /files?since=` as usual.

Keepalive: the server pings every 25 s and drops a socket that sends no frame for 62.5 s. Close codes: `1001` server restarting (reconnect after a short random delay), `1008` device revoked or too many connections (32 per user).

---

## Server settings

### `GET /system/settings`
Auth: session only.
```json
200 { "server_name": "nas", "mdns_enabled": true, "mdns_forced_off": false,
      "mdns_active": true, "mdns_addrs": ["192.168.1.20", "fd00::20"] }
```
`mdns_forced_off` is true when `NULLDRIVE_MDNS=off` overrides the setting. `mdns_error` explains why discovery is not running, or warns about a Docker bridge network.

### `PATCH /system/settings`
Auth: **admin session**. Body: any of `server_name` (1–63 characters, no dots: it becomes the DNS-SD instance label) and `mdns_enabled`. Returns the new settings and re-announces on the LAN.
```bash
curl -X PATCH localhost:8080/api/v1/system/settings -H "Authorization: Bearer $SESSION" -d '{"server_name":"attic","mdns_enabled":true}'
```

---

## Files

File object:
```json
{
  "path": "notes/hello.txt",
  "is_dir": false,
  "size": 22,
  "sha256": "c0535e4be2b79ffd93291305436bf889314e4a3faec05ecffcbb7df31ad9e51a",
  "version": 4,
  "modified_at": "2026-10-13T07:02:00.123Z",
  "deleted": false
}
```
Tombstones have `"deleted": true` and `deleted_at`.

**Folders** are rows too (`"is_dir": true`, `size` 0, `sha256` `""`), so empty folders exist and sync. The server keeps every live entry's parent folders in place: uploading `a/b/c.txt` or moving something to `a/b/...` creates `a` and `a/b` if needed, in the same version. Deleting the last file in a folder leaves the empty folder behind. A path is never both a file and a folder (`409 path_collision`).

### `GET /files`
Auth: any.

- Without parameters: all live files and folders, sorted by path.
- `?since=<version>`: the **change feed**. Every row changed after that version, **including tombstones**, oldest change first. It is the current state of each changed path (one row per path), not an event log. Tombstones have `deleted: true` and `deleted_at`; those left by a move also have `moved_to`.
- `?prefix=<folder>`: only that folder and everything below it (`Notes` matches `Notes/a.md`, not `Notes2/…`). Combines with `since`. A backup tool lists just its own folder this way.

Each file has `sha256` and `size`; `modified_at` (the server's time of the change); and, when present, `mtime` (the uploading device's modification time, see `X-NullDrive-Mtime`) and `synced_id` (the Null Notes note id read from a Markdown file's frontmatter `id: <uuid>` on upload).

```bash
curl -H "Authorization: Bearer $TOKEN" 'localhost:8080/api/v1/files?since=3'
```
```json
200 { "version": 5, "files": [ { "path": "old.txt", "deleted": true, "version": 4, … },
                                { "path": "new.txt", "deleted": false, "version": 5, … } ] }
```
`version` is the server's current counter. A sync client stores it and sends it as `since` next time. It is read *before* the rows, so a change can at worst be delivered twice, never skipped.

### `PUT /files/{path}`
Auth: any. Body: raw file bytes. Creates or replaces the file.

Optional headers:

| Header | Meaning |
|---|---|
| `If-Match: "<version>"` (or bare `<version>`) | Only write if the file's current version is this. `0` means "only if it does not exist". |
| `If-None-Match: *` | Create only: `409 conflict` (with `current`) if a live file is at the path. Same as `If-Match: 0`; can't be combined with `If-Match`. |
| `If-Match: *` | Write whatever is there: create, or replace without knowing the version. For tools that mirror a folder (backups). The replaced content stays in version history. |
| `X-NullDrive-Mtime: <RFC 3339>` | The file's modification time on the uploading device, kept as `mtime` (UTC, milliseconds). `400 invalid_mtime` if it isn't RFC 3339 or is outside 1970–9999. Re-uploading identical content with a new mtime records the mtime without a new version. |
| `X-Content-SHA256: <hex>` | Expected SHA-256 of the body. Mismatch → `400 hash_mismatch`, nothing stored. Also lets the server recognise a retried upload before reading the body. |

Rules:

| Current state | `If-Match` | Result |
|---|---|---|
| Doesn't exist / deleted | absent, `0`, `*`, or tombstone's version | `201 Created` |
| Doesn't exist / deleted | other version | `409 conflict` (you edited something that was deleted) |
| Exists, identical content | anything (with `X-Content-SHA256`) | `200`, no new version (idempotent retry) |
| Exists | absent | `428 precondition_required` |
| Exists | matches, or `*` | `200 OK`, new version |
| Exists | doesn't match | `409 conflict` with `current` |
| Parent path is a file, or path is a folder | | `409 path_collision` |

Preconditions are checked before the body is read, so a doomed large upload fails immediately. The response carries the new `ETag`. For `.md`/`.markdown` files the server reads a Null Notes note id from the frontmatter and returns it as `synced_id` (cleared again if a later version has none).

```bash
curl -X PUT --data-binary @report.pdf \
  -H "Authorization: Bearer $TOKEN" \
  -H 'If-Match: "4"' \
  -H "X-Content-SHA256: $(sha256sum report.pdf | cut -d' ' -f1)" \
  localhost:8080/api/v1/files/work/report.pdf
```
```json
200 { "path": "work/report.pdf", "size": 48213, "sha256": "…", "version": 9, "modified_at": "…", "deleted": false }
```
Conflict:
```json
409 { "error": { "code": "conflict", "message": "version conflict: current version is 7",
                 "current": { "path": "work/report.pdf", "version": 7, "sha256": "…", … } } }
```

### `POST /folders`
Auth: any. Creates a folder and any missing parents (`mkdir -p`).
```bash
curl -X POST -H "Authorization: Bearer $TOKEN" localhost:8080/api/v1/folders -d '{"path":"Photos/2026"}'
```
```json
201 { "path": "Photos/2026", "is_dir": true, "size": 0, "sha256": "", "version": 12, "modified_at": "…", "deleted": false }
```
`200` with the existing folder if it already exists (no new version). `409 path_collision` if a file exists at the path or at any parent.

### `GET /files/{path}` and `HEAD /files/{path}`
Auth: any. Returns the bytes. `404` if missing or deleted; `400 is_folder` for a folder.

Response headers: `ETag` (`"<version>"`), `X-Content-SHA256`, `X-NullDrive-File-Id` (stable id), `X-NullDrive-Mtime` (if the uploader sent one), `X-NullDrive-Synced-Id` (Markdown note id, if any), `Content-Type` (by extension), `Last-Modified` (server time of the change), `Content-Disposition: attachment`, `Content-Security-Policy: sandbox`. Supports `Range` (`206`, resumable downloads), `If-None-Match` (`304`) and `If-Match`.

```bash
curl -H "Authorization: Bearer $TOKEN" -o hello.txt localhost:8080/api/v1/files/notes/hello.txt
```

### `DELETE /files/{path}`
Auth: any. Soft-deletes (tombstones) the file into Recently deleted. If `{path}` is a folder, the folder and everything inside it are tombstoned. Optional `If-Match: <version>` (the file's, or the folder row's) → `409 conflict` with `current` on a mismatch; `If-Match: *` deletes whatever is there. All tombstones from one call share one version.
```json
200 { "version": 10, "deleted": [ { "path": "notes/hello.txt", "deleted": true, "deleted_at": "…", "version": 10, … } ] }
```
`404` if nothing live exists at that path (deleting twice is a 404).

### `POST /files/move`
Auth: any. Renames or moves a file **or a folder**. Content is not copied; the new rows point at the same blobs. Old paths become tombstones.
```bash
curl -X POST -H "Authorization: Bearer $TOKEN" localhost:8080/api/v1/files/move \
  -d '{"from":"notes","to":"archive/notes"}'
```
```json
200 { "version": 11, "moved": [ { "from": "notes/hello.txt", "file": { "path": "archive/notes/hello.txt", "version": 11, … } } ] }
```
Optional `"if_match": <version>` in the body, or an `If-Match` header (both only if equal), guards the move with the source's version → `409 conflict` with `current` on a mismatch; a missing source is `404 not_found`. The moved rows keep their `id`, `synced_id`, `mtime` and history and get the move's new `version`; the old paths' tombstones carry `"moved_to": "<new path>"` (and a throwaway id, no `synced_id`), so a change-feed reader can tell a move from a delete. A move never overwrites: if the destination (or any file under a destination folder) exists, `409 target_exists`. Moving a folder into itself or onto the same path is `400 invalid_move`. A file literally named `move` at the root is still reachable with GET/PUT/DELETE; only `POST` is routed to this endpoint.

---

# API v2: chunks, versions, recently deleted (Phase 6)

v2 transfers file content as **content-defined chunks** (FastCDC, 512 KiB–8 MiB, ~2 MiB average; see DESIGN.md › Phase 6), so editing part of a large file moves only the changed chunks. Same authentication as v1 (session cookie/token or device token). **v1 keeps working**: its uploads are chunked on the server, and its downloads are served from chunks, `Range` included.

A chunk's key is the lowercase hex SHA-256 of its bytes. A **manifest** is the ordered list of `{hash, size}` that makes up a file.

Client flow for an upload: chunk locally → `POST /chunks/check` → `PUT /chunks/{hash}` for each missing chunk → `PUT /files/{path}` with the manifest. For a download: `GET /files/{path}/manifest` → reuse chunks you already have locally → `GET /chunks/{hash}` for the rest.

### `POST /api/v2/chunks/check`
Which of these chunks must I upload? Up to 20,000 hashes per request.
```json
{ "hashes": ["9f86d0…", "60303a…"] }
200 { "missing": ["60303a…"] }
```
A chunk counts as present only if **this user** may use it: uploaded by them and not yet committed, or part of one of their versions. Data is stored once across users, but the answer never reveals what *other* users store. Present chunks get their garbage-collection grace period (24 h) refreshed. An empty list is a cheap capability probe: `404` means the server only speaks v1.

### `PUT /api/v2/chunks/{hash}`
Body: the chunk's raw bytes (≤ 16 MiB). The server hashes them and refuses a mismatch (`400 hash_mismatch`). If the chunk is already stored, the verified bytes are discarded.
```json
201 { "hash": "60303a…", "size": 2093411 }
```
Uploaded but uncommitted chunks are kept for the grace period (24 h), which is what makes interrupted uploads resumable.

### `GET /api/v2/chunks/{hash}` (and `HEAD`)
The chunk's bytes, `ETag: "<hash>"`, `Cache-Control: private, max-age=31536000, immutable`. Only chunks of your own files (`404` otherwise): knowing a hash isn't enough to read the data.

### `PUT /api/v2/files/{path}`
Commit a manifest as the new content of `path`. Same preconditions as v1 `PUT`: `If-Match` required to overwrite (`428`), `If-Match: *` replaces whatever is there, stale `If-Match` → `409 conflict` with the current file, identical content → `200` no-op. Missing parent folders are created. Optional `mtime` (RFC 3339) as in `X-NullDrive-Mtime`; Markdown gets `synced_id` as in v1 (plain accounts).
```json
{ "size": 1073741824, "sha256": "<whole-file sha256>", "mtime": "2026-10-18T09:30:00.123Z",
  "chunks": [ { "hash": "9f86d0…", "size": 2350102 }, … ] }
201 { "path": "big.iso", "version": 412, "size": 1073741824, "sha256": "…", … }
```
- `422 missing_chunks` plus `"missing": [hashes]`: upload those and retry.
- `400 invalid_manifest`: the chunk sizes don't add up to `size`, or a chunk's size differs from the stored one.

An empty file has `"chunks": []`.

### `GET /api/v2/files/{path}/manifest`
The current version's manifest, with the file's stable `id`, `mtime` and `synced_id` (each omitted when unset; `synced_id` never appears on encrypted accounts). `…/versions/{version}/manifest` is the same for an older version (`mtime` and `synced_id` of that version, no `id`).
```json
200 { "path": "big.iso", "version": 412, "size": 1073741824, "sha256": "…", "id": "8d4e8fe1c0314cc61cba165be07c753b",
      "mtime": "2026-10-18T09:30:00.123Z", "chunks": [ { "hash": "…", "size": 2350102 }, … ] }
```

### Path operations
Operation suffixes are read **from the right**: `…/a/manifest` is the manifest of `a`. The manifest of a file literally named `manifest` is `…/manifest/manifest`.

## Version history

### `GET /api/v2/files/{path}/versions`
History, newest first. `device` is the name of the device that wrote the version (absent for the web app). `origin` is `upload`, `restore` or `migrated` (stored before Phase 6). Works for deleted files too (`"deleted": true`).
```json
200 { "path": "Documents/Budget 2026.xlsx", "deleted": false, "versions": [
  { "version": 31, "size": 62500, "sha256": "…", "created_at": "…", "device": "Desktop PC", "device_id": 2,
    "origin": "upload", "chunks": 1, "current": true },
  … ] }
```
`404` if the path has no history.

### `GET /api/v2/files/{path}/versions/{version}/content`
Downloads that version (`Content-Disposition: attachment`, `Range` supported). `…/versions/{version}/manifest` returns its manifest.

### `POST /api/v2/files/{path}/versions/{version}/restore`
Makes an old version current by committing its manifest as a **new** version (`origin: "restore"`). History is never rewritten, and no data is copied. Also undeletes: restoring a deleted file recreates it and its folders. An optional `If-Match` guards against a concurrent edit; without it, the server uses the version you were looking at.
```json
200 { "path": "…", "version": 35, … }
```

### `GET /api/v2/ids/{id}`
Auth: any full-scope token (not search-only). Resolves a file's stable id (from `GET /search` results, `nulldrive://open?id=` links, or `id` in listings) to the file as it is now: the current path after renames and moves, or `"deleted": true` if it's in Recently deleted. An unknown id, or another user's, is `404 not_found`; not 32 hex characters is `400 invalid_id`. Encrypted accounts: the path is encrypted, as in listings (needs `X-NullDrive-E2EE: 1`).
```json
200 { "id": "d28fc8c06bfa5b3229e9cada9b33e866", "path": "Projects/LibreAI roadmap.md", "is_dir": false, "size": 35,
      "sha256": "…", "version": 41, "modified_at": "…", "deleted": false }
```

### `GET /api/v2/trash`
Recently deleted files whose content is still retained, most recent first. `version` is what a restore brings back. `purge_at` is when it leaves the trash for good: deleted at + `trash_days` (default 30). Until then it can be restored with its whole history; afterwards every version is purged (the tombstone stays in the change feed).
```json
200 { "items": [ { "path": "Packing list.md", "size": 21, "deleted_at": "…", "version": 27, "versions": 1, "purge_at": "…" } ] }
```
Restore an item with `POST /api/v2/files/{path}/versions/{version}/restore`.

## Storage

### `GET /api/v2/stats`
```json
200 { "files": 18, "logical_bytes": 137363456, "history_bytes": 141557760, "versions": 25,
      "stored_bytes": 35651584, "trash_files": 3,
      "retention_full_days": 30, "retention_daily_days": 90,
      "server_stored_bytes": 35651584, "server_logical_bytes": 141557760,
      "server_chunks": 25, "server_reclaimable_bytes": 0 }
```
- `logical_bytes`: current files.
- `history_bytes`: every retained version at full size, i.e. what a store without chunking would need.
- `stored_bytes`: the distinct chunks behind your versions. Dedup savings are `1 - stored/history`.
- `server_*` fields are only included for administrators.

### `POST /api/v2/admin/gc`
Admin session only. Runs retention and garbage collection now. They also run every `NULLDRIVE_GC_INTERVAL` (default 1 h).
```json
200 { "versions_pruned": 4, "uploads_expired": 0, "chunks_deleted": 6, "orphans_deleted": 0, "bytes_freed": 12582912, "duration_ms": 41 }
```

### Retention settings
`GET/PATCH /api/v1/system/settings` include `retention_full_days` (keep every version, default 30), `retention_daily_days` (then the newest version per day, default 90) and `trash_days` (how long deleted files stay in Recently deleted, default 30, 1–3650). History values 0–3650; `400 invalid_retention` otherwise. The current version of a live file is always kept, and `DELETE` never removes content immediately: it goes to the trash.

### v2 error codes

| Status | `code` | Meaning |
|---|---|---|
| 400 | `hash_mismatch` | Chunk bytes don't match `{hash}` |
| 400 | `invalid_manifest`, `invalid_sha256`, `too_many_hashes` | Malformed commit or check |
| 413 | `too_large` | Chunk over 16 MiB |
| 422 | `missing_chunks` | Commit references chunks you haven't uploaded; see `missing` |

# End-to-end encryption (Phase 7)

Base URL `/api/v2`. The server stores the client-made key bundle verbatim and can't open it; all cryptography is in `shared/e2ee` (Go) and `ui/src/lib/crypto` (browser). The `nulldrive-e2ee` CLI drives these endpoints from a terminal.

### `GET /api/v2/e2ee`
Auth: any.
```json
200 { "state": "migrating", "fingerprint": "8f3a1c0d9b2e4f61", "enabled_at": "…", "plaintext_files": 12,
      "bundle": { "version": 1,
                  "kdf": { "alg": "argon2id", "time": 3, "memory_kib": 65536, "threads": 1, "salt": "base64…" },
                  "passphrase_wrapped": "base64(nonce‖ciphertext‖tag)", "recovery_wrapped": "…",
                  "recovery_salt": "…", "fingerprint": "8f3a1c0d9b2e4f61" } }
```
`state: "off"` has no bundle.

### `POST /api/v2/e2ee`
Auth: **session only**. Turns encryption on: `{ "bundle": {…}, "fingerprint": "<16 hex>", "auth_key": "<64 hex>" }`. The server keeps `SHA-256(auth_key)`. Returns the status (`201`): `on` for an empty account, `migrating` if it has files (from now on plaintext names are refused). `400 invalid_bundle`, `409 e2ee_already_on`.

### `PUT /api/v2/e2ee/bundle`
Auth: any, plus the auth key. `{ "auth_key", "bundle", "fingerprint" }` replaces the bundle (new passphrase or new recovery key). The fingerprint must not change. `403 e2ee_auth_failed`.

### `POST /api/v2/e2ee/finish`
Auth: any, plus `{ "auth_key" }`. After the client re-uploaded every file under its encrypted name: deletes all remaining plaintext rows (files, folders, tombstones, history), deletes the chunks only they used immediately, rebuilds the database, sets `on`. Idempotent.
```json
200 { "state": "on", "purged_paths": 12, "chunks_deleted": 30, "bytes_freed": 48211337 }
```

### Commits on an encrypted account
`PUT /api/v2/files/{path}` works as before with: `path` made of encrypted segments; chunk hashes = SHA-256 of the *sealed* chunk bytes; `size` = plaintext size, checked as `Σ chunk sizes − 41 × number of chunks`; `sha256` = `HMAC-SHA256(file-hash key, SHA-256(plaintext))`. Every chunk holds at least one plaintext byte (an empty file is `"chunks": []`).

Every request that lists or reads files on an encrypted account (listings, manifests, v2 file operations, moves, deletes, trash, `ids`) needs **`X-NullDrive-E2EE: 1`**, or it's `409 e2ee_client_required`; v1 whole-file `GET`/`PUT` and `…/versions/{v}/content` are `409 e2ee_required`; a plaintext name in a write, folder or move source/target is `409 e2ee_path_required`. Full client protocol, with a worked example: [NOTES-E2EE.md](NOTES-E2EE.md).

---

## Search

Null Drive implements the Null franchise search contract (v1), so [Null Search](https://search.nullgrid.dev) can show a user's Drive files next to their notes and the web.

### `GET /search?q=<query>&limit=<n>`
Also at `GET /api/v1/search` (same handler, for proxies that only forward `/api/`). Auth: any token of the user (`Authorization: Bearer`), including a **search-only** device token. The user comes from the token; a request can only ever reach its own user's index.

- `q`: free text, up to 1000 bytes. Case-insensitive substring matching on file **names** and **text content** of `.md`/`.markdown`, `.txt` and `.pdf` files (other files: by name). Words are split on anything that isn't a letter or digit and must all match, so `libre ai`, `libre-ai` and `LibreAI` find each other. Empty `q` → `[]`.
- `limit`: default 20, capped at 100; not a positive number → `400 invalid_limit`.
- Order: name matches first, then by relevance (BM25, name weighted over text).

```json
200 [ {
  "id": "d28fc8c06bfa5b3229e9cada9b33e866",
  "source": "drive",
  "title": "LibreAI roadmap.md",
  "snippet": "# Plan The zebra launch is in March.",
  "path": "Projects/LibreAI roadmap.md",
  "open": "nulldrive://open?id=d28fc8c06bfa5b3229e9cada9b33e866",
  "modified": "2026-10-18T09:53:02Z",
  "synced_id": null
} ]
```
- `id`: the file's stable id (also `id` in `GET /files` listings). It survives renames, moves, overwrites and restores; a new file at a deleted file's path gets a new one.
- `title`: the file name; `path`: the full path inside the drive; `modified`: RFC 3339, UTC.
- `snippet`: up to ~160 characters of text around the match, `…` where cut, `""` when only the name matched.
- `synced_id`: the Null Notes note id: for Markdown, the frontmatter `id:` (wherever the file is); for other notes (`.txt`, `.html`, `.canvas`), their entry in the vault's `.nullnotes/ids.json`, so those need the vault's `.nullnotes/` folder synced too. Otherwise `null`.
- Never returned: Null Notes locked notes (`*.lock` starting `NNLOCK1\n`), `*.nntmp`, anything under `.nullnotes/`, folders.
- **End-to-end encrypted accounts** aren't indexed (the server can't read them): `200 []` with `X-NullDrive-Search: encrypted`.

**CORS.** For origins in `NULLDRIVE_SEARCH_ORIGINS` (default `https://search.nullgrid.dev`), `/search` answers preflights (`204`, `Access-Control-Allow-Methods: GET`, `-Allow-Headers: Authorization`, `-Max-Age: 600`, and `Access-Control-Allow-Private-Network: true` when asked) and adds `Access-Control-Allow-Origin` to responses. No `Allow-Credentials`: cookies never work cross-origin, only a token the page was given. Other origins get `403 bad_origin` on preflight and no CORS headers. No other endpoint sends CORS headers.

```bash
curl -s "https://drive.example.com/search?q=invoice&limit=5" -H "Authorization: Bearer $SEARCH_TOKEN"
```
