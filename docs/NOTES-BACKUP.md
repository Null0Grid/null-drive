# Backing up Null Notes into Null Drive: the API recipe

How a Null Notes backup should call Null Drive (server 0.8+, migration 0007). Null Drive implements this side; Null Notes owns the client. Full reference: [API.md](API.md). Why it's built this way: [DESIGN.md › Null Notes backup](DESIGN.md#null-notes-backup-the-drive-side-null-franchise-phase-6).

## Setup

- **Server URL + token.** Ask the user for their Drive address and a token: in the Drive web app, *Settings → Devices → Add device → Create an API token instead*, scope **Everything**, named e.g. "Null Notes backup". (A *Search only* token can't write.) Send it as `Authorization: Bearer <token>`. Keep it like Null Notes keeps its own secrets; the user can revoke it in the same list.
- **Plain accounts only.** An account with end-to-end encryption refuses plaintext uploads (`409 e2ee_required`); Null Notes would need Drive's encryption client to write there. Check `GET /api/v1/me` → `user.e2ee == "off"` and tell the user otherwise.
- **Backup root.** One folder per vault, e.g. `Null Notes/<vault name>`. Paths use `/`, are relative, and each segment is URL-encoded in the request path (`Null%20Notes/Work/Plan.md`). Drive rejects names Windows can't store (`<>:"|?*`, trailing dot/space, `CON`, `NUL`, …) with `400 invalid_path`: report those notes and skip them.

## What to upload

- Every note and image the vault lists (`.md/.markdown`, `.txt`, `.html/.htm`, `.canvas`, `.png`, `.jpg/.jpeg`), and locked notes (`*.lock`) as they are (ciphertext; Drive never indexes them).
- **`.nullnotes/ids.json`**: needed so `.txt` / `.html` / `.canvas` notes keep their ids in Drive (search dedupe). `lock.json` and the per-vault settings are worth backing up too, since they're needed to restore a locked vault.
- **Never** `*.nntmp` (half-written saves).

## A backup run

1. **Root folder** (idempotent, like `mkdir -p`):
   ```
   POST /api/v1/folders   {"path": "Null Notes/Work"}       → 201 created, 200 already there
   ```
2. **What Drive has**, for this vault only:
   ```
   GET /api/v1/files?prefix=Null%20Notes/Work
   → { "version": 812, "files": [ { "path": "Null Notes/Work/Plan.md", "is_dir": false, "size": 2048,
         "sha256": "…", "mtime": "2026-10-02T09:00:00.000Z", "synced_id": "3f2504e0-…", "version": 790, … } ] }
   ```
   Keep `version` (the server's change counter) if you want later runs to ask only for changes: `?prefix=…&since=812` returns rows changed after that, deleted ones included.
3. **Upload what changed.** For each local file: if Drive has nothing at that path, or `size` differs, or `mtime` differs from the file's modification time, upload it. When only the mtime differs, hashing the file and comparing `sha256` first saves the transfer:
   ```
   PUT /api/v1/files/Null%20Notes/Work/Plan.md
   If-Match: *                                        create or replace, whatever is there
   X-NullDrive-Mtime: 2026-10-02T09:00:00.123Z        the note's modification time (RFC 3339)
   X-Content-SHA256: <hex>                            optional: Drive verifies the upload against it
   <raw bytes>
   → 201 created / 200 replaced: the file, with its new "version", "mtime" and, for Markdown, "synced_id"
   ```
   - The replaced content stays in Drive's version history (restorable in the web app).
   - Same content with a newer mtime: `200`, mtime recorded, no new version.
   - Missing parent folders are created automatically.
   - Big files: the same request streams any size. Drive's chunked API (`/api/v2/…`, see API.md) uploads only the changed parts and takes `"mtime"` in the commit body, if Null Notes ever wants that.
4. **Delete what's gone.** Paths under the root that Drive has but the vault doesn't:
   ```
   DELETE /api/v1/files/Null%20Notes/Work/Old%20idea.md
   If-Match: *
   → 200 { "deleted": [ … ] }
   ```
   Nothing is erased: it goes to **Recently deleted for 30 days** (`trash_days`, set by the Drive admin), restorable with its history, then it's purged. Deleting a folder deletes its contents the same way.
5. **Renames** (if Null Notes knows about them) keep Drive's file id and history:
   ```
   POST /api/v1/files/move   {"from": "Null Notes/Work/a.md", "to": "Null Notes/Work/Archive/a.md"}
   ```
   Otherwise upload-new plus delete-old works too: the note id travels in the frontmatter anyway.

## Errors worth handling

| Response | Meaning | Do |
|---|---|---|
| `401` | token revoked or wrong | ask the user for a new one |
| `400 invalid_path` | a name Drive can't store | skip, report |
| `400 invalid_mtime` | not RFC 3339, or before 1970 | send UTC like `2026-10-02T09:00:00Z` |
| `409 path_collision` | a file where a folder is (or the reverse) | report; the user resolves it |
| `409 e2ee_required` | encrypted account | see Setup |
| `5xx`, network errors | | retry with backoff; uploads are idempotent |

## What Drive does with it

- **`synced_id`.** For Markdown, Drive reads `id: <uuid>` from the frontmatter on upload and stores it. Listings, `GET /api/v2/ids/{id}` and search return it, so Null Search shows the note once (from Notes) instead of twice. For other note types, Drive search looks the id up in the uploaded `.nullnotes/ids.json`.
- **Search.** Backed-up notes are searchable in Drive (names and text of `.md`/`.txt`, not locked notes) and in Null Search's Drive section, deduplicated against Notes by `synced_id`.
- **History.** Every replaced version is kept by Drive's retention policy (default: every version for 30 days, then one per day for 90 more).
