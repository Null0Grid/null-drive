# HANDOFF: Null Notes two-way sync, Drive side (Phase 7)

**Status:** done. Every question in `null-grid/notes-sync-needs-from-drive.md` is answered in **`docs/NOTES-SYNC.md`**, published to the Project as **`null-grid/notes-sync-drive-api.md`**. Most of it already existed; three gaps are closed, so every write can be conditional and nothing is overwritten silently:

| Need | Answer | Was |
|---|---|---|
| Download | `GET /api/v1/files/{path}`, streamed; `ETag: "<version>"`, `X-Content-SHA256`, **`X-NullDrive-Mtime`, `X-NullDrive-File-Id`, `X-NullDrive-Synced-Id`**; `304` / `206` supported | id, mtime, note id not in headers |
| Listing fields | `id` (32 hex, survives replace/move/restore), `version` (int; **changes on a move**), `synced_id`, `mtime`, `sha256` | already there |
| Tombstones in `?since=` | delete: `deleted: true`, same `id` and `synced_id`; move: old path `deleted: true` + **`moved_to`**, throwaway id, no `synced_id`; plus the live row at the new path | a move's tombstone looked exactly like a delete and kept the note id |
| Conditional PUT / DELETE / move | `If-Match: <version>` on all three (move: header **or** `if_match` in the body); mismatch → **`409` `conflict`** with `current` (+ `ETag`); no precondition on an existing file → `428`; gone → `404 not_found` | move took only the body field |
| Create-only | **`If-None-Match: *`** (or `If-Match: 0`) → `409 conflict` with `current` if something is there; deleted/moved-away paths count as empty | only `If-Match: 0` |
| Moves | keep `id`, `synced_id`, `mtime`, history; new `version`; destination taken → `409 target_exists` | already, now documented and tested |

**For Null Notes:** use `409 conflict` (not 412) as the "re-plan" signal, and treat rows with `moved_to` as moves, not deletes. Send `X-Content-SHA256` on uploads so a retried write whose first attempt went through comes back `200`, not `409`.
**Previous handoff:** Notes backup in `docs/handoffs/phase-6-notes-backup.md`.

## First thing to do (owner)

```bash
make test && make lint && make e2e && make smoke
```

Migration `0008_moved_to.sql` adds one nullable column; existing move tombstones simply have no `moved_to`.

## What was built and where

| Path | What |
|---|---|
| `server/internal/db/migrations/0008_moved_to.sql` | `files.moved_to` |
| `server/internal/meta/files.go` | `File.MovedTo`; moves set it on their tombstones and clear their `synced_id`; any write at the path resets it |
| `server/internal/api/respond.go` | `parseWritePrecondition`: `If-Match` or `If-None-Match: *` (not both); `moved_to` in file JSON |
| `server/internal/api/handlers_files.go`, `handlers_v2.go` | create-only on v1 and v2 uploads; download headers; move `If-Match` header |
| `shared/api/types.go`, `ui/src/lib/api/types.ts` | `MovedTo`, `HeaderFileID`, `HeaderSyncedID` |
| `server/internal/api/notes_sync_test.go` | **TestNotesSyncFlow** |
| Docs | `docs/NOTES-SYNC.md` (new); API.md (download headers, `If-None-Match`, move `If-Match`, `moved_to`, feed semantics); DESIGN.md › Two-way sync with Null Notes |

## How it was tested

- **TestNotesSyncFlow** runs over HTTP:
  - **Create-only:** `201`; a retry with the hash returns `200`, without it `409`; different content `409` with `current` and `ETag`; `If-Match: 0` behaves the same; both headers or a bad `If-None-Match` are rejected with `400`; a deleted path counts as empty.
  - **Download:** body, all five headers, `304`, `206`, `HEAD`.
  - **Listing:** `id`, `version`, `synced_id`, `sha256`.
  - **Replace:** a stale version gives `409` (the current version comes back); a missing precondition gives `428`; the fresh version gives `200`, keeping the `id`.
  - **Move:**
    - a stale version gives `409`, both via the body and via the header;
    - a body and header that disagree give `400`;
    - a taken destination gives `409 target_exists`; a missing source gives `404`;
    - success keeps `id` and `synced_id` and bumps the version;
    - the feed shows a tombstone with `moved_to`, a different id and no note id, plus the live row with the original id;
    - a new file at the old path gets a new id and no marker.
  - **Delete:** a stale version gives `409`; success gives `200`; deleting again gives `404`; replacing the deleted file gives `409` with the tombstone as `current`; the feed tombstone keeps `id` and `synced_id` and has no `moved_to`.
  - The conflict-copy upload works, and v2 commits take `If-None-Match: *`.
- **Full suites:**
  - all Go packages under `-race`, including the desktop sync engine's integration tests (unchanged behavior with the new tombstone field); golangci-lint 0;
  - client tests; svelte-check 0/0.

## Known issues / next steps

- The change feed is state, not an event log: one row per path. If a file is moved away and a new file appears at the old path before the next poll, Notes sees only the new file there, and the moved file at its new path (same id). Matching by id handles it.
- `409 conflict` on an identical-content retry when no `X-Content-SHA256` is sent: by design (preconditions are checked before the body is read).
- Encrypted accounts: as in Phase 6, out of scope for Notes sync.

## Draft resume bullet

- Specified and hardened the sync contract between two apps:
  - conditional writes everywhere (ETag versions, `If-None-Match: *` create-only, `409` with the current state for re-planning);
  - change-feed tombstones that distinguish moves from deletes;
  - stable file and note ids across renames;
  - each claim pinned by an end-to-end HTTP test.
