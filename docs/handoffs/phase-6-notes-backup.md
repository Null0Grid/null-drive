# HANDOFF: Null Notes backup, Drive side (Phase 6)

**Status:** done. The Drive API now covers everything a Null Notes backup needs:

| Need | How | Was |
|---|---|---|
| Folder create | `POST /api/v1/folders`, idempotent (`mkdir -p`; 201 new, 200 existing) | already there; now tested in the backup flow |
| Upload/replace by path | `PUT /api/v1/files/{path}` (and v2 commits) with **`If-Match: *`**: create or replace without knowing the version; the old content stays in history | overwriting needed the current version number |
| Hash or mtime listing | `GET /api/v1/files?prefix=<folder>[&since=]` lists one subtree with `sha256`, `size` and the new **`mtime`** (sent by the device as `X-NullDrive-Mtime`, or `mtime` in a v2 commit) | whole-account listings only, server time only |
| 30-day trash | `DELETE` goes to Recently deleted for **`trash_days` (default 30)**, then every version is purged; the tombstone stays for sync clients; `purge_at` in the trash listing; setting in the API and in Settings › Storage | kept 120 days, tied to the history policy |
| `synced_id` | read from Markdown frontmatter (`id: <uuid>`) on every upload, **stored** on the file and version; returned by listings, `GET /api/v2/ids/{id}` and search (Markdown no longer needs a detected vault) | computed only inside search, only inside a vault |

**For Null Notes:** the recipe its backup should follow (setup, what to upload, the calls in order, errors) is `docs/NOTES-BACKUP.md`, also in the Project as `null-grid/notes-backup-drive-api.md`.
**Previous handoff:** open links / Microsoft Store in `docs/handoffs/phase-5-open-links.md`.

## First thing to do (owner)

```bash
make test && make lint && make e2e && make smoke
```

Migration `0007_backup.sql` runs on start: it adds nullable `mtime` / `synced_id` columns to `files` and `file_versions`, which is instant. Existing deleted files switch from the 120-day to the 30-day rule at the next hourly cleanup. Anything deleted more than 30 days ago is purged then, so raise `trash_days` first (Settings › Storage › *Keep deleted files*) if you want to keep them.

## What was built and where

| Path | What |
|---|---|
| `server/internal/db/migrations/0007_backup.sql` | `mtime`, `synced_id` on files and versions |
| `server/internal/meta/files.go` | `Precondition.Any` (`If-Match: *`) in all checks; `File.Mtime/SyncedID`; `ListFilesUnder` (prefix range scan); `SetSyncedID`; moves carry both |
| `server/internal/meta/versions.go` | `Commit.Mtime/SyncedID` stored per version, restored with it; same-content upload with a new mtime updates it without a version; `Retention.TrashDays` + `trash_days` setting; pruning protects the trash and purges expired trash items' whole history; `Trash()` hides expired items, computes `PurgeAt` |
| `server/internal/noteid/` | Frontmatter note-id parser, shared by uploads and search |
| `server/internal/api/` | `If-Match: *` parsing; `X-NullDrive-Mtime` (RFC 3339, 1970–9999, ms); body tee for v1 Markdown uploads; post-commit note id for v2 commits (plain accounts); `?prefix=`; `trash_days` in settings/stats; `mtime`/`synced_id` in file JSON |
| `server/internal/search/index.go` | Markdown `synced_id` regardless of vault; other note types still via `ids.json` |
| `shared/api/types.go` | `File.Mtime`, `File.SyncedID`, `CommitRequest.Mtime`, `HeaderMtime`, `TrashDays` |
| `ui/` | Settings › Storage: *Keep deleted files (days)*; trash "days left" now follows `trash_days` |
| Docs | `docs/NOTES-BACKUP.md` (new); API.md (prefix, `If-Match: *`, mtime, synced_id, trash_days); DESIGN.md › Null Notes backup; retention text in DESIGN/INSTALL/README; screenshots regenerated |

## How it was tested

- `api/backup_test.go` › **TestNotesBackupFlow** runs the whole backup workflow over HTTP:
  - folders created twice; create, then replace with `If-Match: *`, with history kept;
  - numeric `If-Match` and `428` unchanged;
  - mtime truncated to milliseconds in UTC; a touch with the same content creates no version;
  - three invalid mtimes rejected;
  - prefix listing (no leak into another folder or into a `Work2/` sibling, `since` within the prefix, `../` refused);
  - `synced_id` set, cleared by a version without frontmatter, brought back by restore, kept by a move;
  - a multi-chunk v2 commit with `mtime` gets its `synced_id` from the chunks;
  - delete with `If-Match: *` lands in the trash with `purge_at` = +30 days.
- **TestTrashDaysSetting:** default 30, update to 14, 0 refused, trash and stats follow.
- `meta`:
  - **TestTrashKeepsDeletedFilesForTrashDays:** a 2-day history window doesn't shorten the 30-day trash. On day 29 the file is listed and restorable; on day 31 it's hidden and every version purged, the tombstone is kept, and the chunks are collectable. A live file's current version survives 400 days.
  - **TestRetentionSettings.**
- `noteid`: 8 frontmatter cases. Search: Markdown outside a vault now gets its id, a `.txt` there doesn't.
- Full suites:
  - all Go packages under `-race` (incl. the E2EE data-dir plaintext scans); golangci-lint 0;
  - client tests; Vitest 132; svelte-check 0/0;
  - Playwright e2e 8/8 and screenshots 7/7; smoke test.

## Known issues / next steps

- **Encrypted accounts** can't take a plaintext backup (`409 e2ee_required`), and get no `synced_id` (the server can't read them). Null Notes would need Drive's E2EE client for that.
- `mtime` is client-reported metadata. It's stored in plaintext for every account, like `modified_at` and sizes.
- Stats' "N recently deleted" count can include an expired item for up to an hour, until the next cleanup (the trash listing itself hides them at once).
- The Null Notes side (the backup itself) is built in the Notes conversation, against `null-grid/notes-backup-drive-api.md`.

## Draft resume bullet

- Extended a self-hosted sync server's API for a companion notes app's backup:
  - idempotent replace-by-path (`If-Match: *`) on a version-checked store;
  - device modification times and subtree listings for cheap change detection;
  - a 30-day trash decoupled from version retention, with whole-history purge and change-feed tombstones;
  - server-side extraction of note ids from Markdown frontmatter, used for cross-app search deduplication.
