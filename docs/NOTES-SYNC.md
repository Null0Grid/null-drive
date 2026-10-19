# Null Notes two-way sync against Null Drive: the API facts

Answers to `null-grid/notes-sync-needs-from-drive.md`, for Null Notes Phase 7. Applies to Null Drive server 0.8.2+ (migration 0008). Everything below is covered by `TestNotesSyncFlow` (`server/internal/api/notes_sync_test.go`). Setup (token, plain accounts only, path rules) is the same as for backup: see [NOTES-BACKUP.md](NOTES-BACKUP.md). Full reference: [API.md](API.md).

**Short version:** writes are conditional on `If-Match: <version>`. A mismatch is always **`409` with code `conflict`** and the server's current file in the body. Create-only is **`If-None-Match: *`**. A move keeps `id` and `synced_id`, gets a new `version`, and leaves a tombstone marked **`moved_to`**, so it can never be mistaken for a delete. Without `If-Match` (or `If-None-Match`), replacing an existing file is refused (`428`), so nothing is ever overwritten silently.

## The version

- Every file and folder row has an integer **`version`**: the value of Drive's global change counter when that row last changed. It is also the `ETag`, quoted: `"812"`.
- It changes on: a new upload, a move (the moved row *and* its tombstone get the move's version), a delete (the tombstone gets the delete's version), a restore.
- It does **not** change when only the `mtime` of identical content is updated, or when Drive records the `synced_id` it read from the upload.
- Versions only grow. For a client they are opaque except "equal or not".

## 1. Download

```
GET /api/v1/files/{path}          (HEAD: headers only)
Authorization: Bearer <token>
```
Each path segment is URL-encoded. The response is `200`, with the bytes **streamed** from storage (any size), and these headers:

| Header | Value |
|---|---|
| `ETag` | `"<version>"`: the version to send back as `If-Match` |
| `X-Content-SHA256` | lowercase hex SHA-256 of the bytes |
| `X-NullDrive-Mtime` | the mtime the uploader sent, `2026-10-18T08:00:00.000Z` (UTC, ms); absent if none was sent |
| `X-NullDrive-File-Id` | the stable file id (32 hex) |
| `X-NullDrive-Synced-Id` | Null Notes note id from the Markdown frontmatter; absent if none |
| `Last-Modified` | server time of the last change (not the mtime) |
| `Content-Length`, `Content-Type`, `Accept-Ranges: bytes` | standard |

- `If-None-Match: "<version>"` gives `304 Not Modified` if unchanged. `Range: bytes=…` gives `206`.
- Errors: `404 not_found` (no live file at the path), `400 is_folder`.
- The headers and bytes always describe the same version.

## 2. Listing

```
GET /api/v1/files?prefix=Null%20Notes/Work              live files and folders under the prefix, sorted by path
GET /api/v1/files?prefix=Null%20Notes/Work&since=812    rows changed after version 812, tombstones included, oldest first
→ 200 { "version": 840, "files": [ … ] }
```

`version` at the top is the server's counter when the listing was taken: pass it as the next `since`. Each row:

```json
{
  "id": "8d4e8fe1c0314cc61cba165be07c753b",
  "path": "Null Notes/Work/Plan.md",
  "is_dir": false,
  "size": 56,
  "sha256": "655d225e…",
  "version": 813,
  "modified_at": "2026-10-18T13:10:02.511Z",
  "deleted": false,
  "mtime": "2026-10-18T08:00:00Z",
  "synced_id": "3f2504e0-4f89-11d3-9a0c-0305e82c3301"
}
```

| Field | Meaning |
|---|---|
| `id` | stable file id, 32 lowercase hex. Survives replace, move/rename and restore. A *new* file at a path whose old file was deleted or moved away gets a new id. |
| `version` | see above |
| `synced_id` | note id from Markdown frontmatter `id: <uuid>` (lowercase). Omitted when there is none, and for non-Markdown files. Those notes' ids are in `.nullnotes/ids.json`, which Notes reads itself. |
| `mtime` | uploader's mtime, omitted if none |
| `deleted`, `deleted_at`, `moved_to` | tombstones only (`since` listings) |

Folder rows (`is_dir: true`) are listed too, with versions.

### Tombstones in `?since=`

The feed returns the **current state of every path that changed**, not a log of events: one row per path. A path deleted and then re-created shows up only as the new live file.

**Deleted file** (to the trash, restorable for `trash_days`):
```json
{ "id": "<same id as before>", "path": "Null Notes/Work/Old.md", "deleted": true, "deleted_at": "2026-10-18T13:20:00Z",
  "version": 851, "synced_id": "<same note id>", "size": 56, "sha256": "…", … }
```

**Moved file**: two rows with the move's version:
```json
{ "id": "<a different, throwaway id>", "path": "Null Notes/Work/Plan.md", "deleted": true, "deleted_at": "…",
  "version": 845, "moved_to": "Null Notes/Work/Archive/Plan.md" }
{ "id": "8d4e8fe1…", "path": "Null Notes/Work/Archive/Plan.md", "deleted": false, "version": 845,
  "synced_id": "3f2504e0-…", … }
```
- A row with `moved_to` is **not a delete**. Ignore it, or use it to update a path mapping. The file is the live row at `moved_to`, with its original `id` and `synced_id`.
- The move tombstone has no `synced_id`, so a note id never appears on two rows.
- A folder move produces this pair for the folder and for every file in it.
- If the moved file has changed again since, the live row shows its later state (and version). Matching by `id` handles that.

## 3. Conditional writes

### Replace
```
PUT /api/v1/files/{path}
If-Match: "<version>"            (or bare 812)
X-NullDrive-Mtime: <RFC 3339>    optional
X-Content-SHA256: <hex>          optional, recommended (see retries)
<bytes>
→ 200 <file>                     ETag: "<new version>"
```

### Delete
```
DELETE /api/v1/files/{path}
If-Match: "<version>"
→ 200 { "version": 851, "deleted": [ <tombstone>, … ] }    a folder: itself and everything inside
```

### Move
```
POST /api/v1/files/move
Content-Type: application/json
If-Match: "<version>"                                  either the header…
{ "from": "Null Notes/Work/Plan.md", "to": "Null Notes/Work/Archive/Plan.md", "if_match": 813 }   …or "if_match", or both if equal
→ 200 { "version": 845, "moved": [ { "from": "Null Notes/Work/Plan.md", "file": <the file at its new path> } ] }
```
The version guards the source: the file's, or for a folder, the folder row's.

### When the version doesn't match

All three return the same response, with nothing changed:
```
409 Conflict
ETag: "<current version>"
{ "error": { "code": "conflict", "message": "version conflict: current version is 830",
             "current": { <the file as it is now: id, path, version, sha256, size, mtime, synced_id, …> } } }
```

| Situation | Status | `error.code` |
|---|---|---|
| `If-Match` version isn't the current one | `409` | `conflict` (+ `current`) |
| replacing an existing file with no `If-Match` / `If-None-Match` | `428` | `precondition_required` |
| nothing live at the path (DELETE, move `from`), e.g. already deleted or moved away | `404` | `not_found` |
| replacing with `If-Match: <v>` a path that has no live file (deleted since) | `409` | `conflict` (`current` = the tombstone, or absent if nothing was ever there) |
| move `to` is taken (live file or folder at `to`, or a file in the way of its folders) | `409` | `target_exists` |
| upload where a folder is (or a file where a folder must go) | `409` | `path_collision` |
| malformed `If-Match`, `If-Match` with `If-None-Match`, body/header `if_match` disagree | `400` | `invalid_if_match` |
| move into itself / onto itself | `400` | `invalid_move` |

There's no `412`: Drive uses `409 conflict` throughout, because the body carries the current state a client needs to re-plan.

**Same content counts as success.** If the bytes you upload are identical to what's already there, the write is a no-op `200`, whatever the precondition. Drive knows that before reading the body only when you send `X-Content-SHA256`. So a retried request whose first attempt succeeded (but whose response was lost) comes back `200`, not `409`. Send the hash.

## 4. Create-only

```
PUT /api/v1/files/{path}
If-None-Match: *                 (If-Match: 0 means the same)
→ 201 Created <file>
```
- Something live at the path: `409 conflict`, `current` = what's there.
- A path whose file was deleted or moved away counts as empty: `201`, with a new `id`.
- Identical content already there, with `X-Content-SHA256` sent: `200` (an idempotent retry).
- `If-None-Match` with any value but `*`, or together with `If-Match`: `400 invalid_if_match`.
- The chunked API (`PUT /api/v2/files/{path}`) takes the same headers.

That's the right call for Notes' conflict copies (`Title (conflict 2026-10-18).md`).

## 5. Moves

| Question | Answer |
|---|---|
| keeps `id`? | yes |
| keeps `synced_id`? | yes (the old path's tombstone has none) |
| keeps `mtime`, content, history? | yes; no bytes are copied |
| bumps `version`? | yes: the moved row gets a new version (the response's `version`); use it for the next `If-Match` |
| destination exists? | `409 target_exists`, nothing moved. Delete it first (conditionally) if that's intended |
| destination is a deleted/moved-away path? | allowed |
| destination folders missing? | created |
| source gone? | `404 not_found` |

## Sanity check against Notes' plan

- **Matching** by note id, then file id, then path: works. Ids survive everything except delete-and-recreate, and move tombstones carry `moved_to` instead of the note id.
- **Every write conditional; skip and re-plan on mismatch:** `409 conflict` (with `current`), `404 not_found` and `409 target_exists` are the "re-plan" answers. Treat `428` as a bug in the client.
- **Both sides edited:**
  1. Download Drive's version.
  2. Upload it as the conflict copy with `If-None-Match: *` (a new note id in its frontmatter gives it its own `synced_id`).
  3. Upload the local version with `If-Match: <Drive's version>`.

  All supported.
- **Deletes** with `If-Match`: an edit on Drive after the local delete makes the delete `409 conflict`, so Notes downloads the edited file instead. Nothing is lost either way: deletes go to the 30-day trash.
- **`.nullnotes/ids.json`** with `If-Match: *`: supported (create or replace whatever is there).
- **Polling:** `?prefix=…&since=<last version>` is one indexed query and returns only changes. Listing the whole prefix every couple of minutes also works for a vault-sized folder. Drive also pushes "something changed" over a WebSocket (`GET /api/v1/ws`, see API.md) if Notes ever wants to drop polling.
