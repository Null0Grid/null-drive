// Package sync keeps a local folder and a Null Drive server in sync.
//
// The engine is a three-way merge run per path:
//
//	Local  what is on disk now
//	Base   what both sides agreed on at the last successful sync
//	Remote what the server has now (a mirror kept fresh from the change feed)
//
// Comparing each side against Base tells us *who* changed a path since the
// last sync, which a two-way comparison (local vs remote) cannot: "the file
// is missing locally" could mean "deleted here" or "created on the server
// while we were offline", and the right actions are opposite. Base is what
// lets a client that was offline for a week come back without resurrecting
// deleted files or deleting new ones.
//
// The package depends only on the standard library and shared/*; the
// desktop app plugs in a file watcher, keychain and UI around it.
package sync

// Local is a file or folder found by scanning the sync folder.
type Local struct {
	Path    string // server-style path: relative, '/'-separated
	IsDir   bool
	Size    int64
	MtimeNs int64
	SHA256  string // empty for folders
}

// Entry is the base state: what was last synced for a path.
type Entry struct {
	Path    string
	IsDir   bool
	Size    int64
	MtimeNs int64  // local mtime when synced (lets the scan skip re-hashing)
	SHA256  string // content hash, equal on both sides at sync time
	Version int64  // server version at sync time (sent as If-Match)
}

// Remote is a live entry on the server (from the mirrored change feed).
type Remote struct {
	Path    string
	IsDir   bool
	Size    int64
	SHA256  string
	Version int64
}

// Kind is what to do for one path.
type Kind int

const (
	Noop Kind = iota
	// UpdateBase: content unchanged on both sides; only remember the
	// server's newer version number.
	UpdateBase
	// Converge: both sides changed in the same way (same content, or both
	// created the same folder); just record it as synced.
	Converge
	// Forget: deleted on both sides; drop the base entry.
	Forget
	Upload            // local created/edited a file → send it
	CreateRemoteDir   // local created a folder
	DeleteRemote      // local deleted; server unchanged
	Download          // server created/edited a file → fetch it
	CreateLocalDir    // server created a folder
	DeleteLocal       // server deleted; local unchanged
	RestoreRemote     // local edited, server deleted → re-upload (edit wins)
	RestoreLocal      // local deleted, server edited → download (edit wins)
	Conflict          // both changed differently → keep both
	MoveRemote        // local rename detected → server-side move (planner)
	MoveLocal         // server rename detected → local rename (planner)
	ReplaceRemoteType // local turned a file into a folder or vice versa
	ReplaceLocalType  // server turned a file into a folder or vice versa
)

var kindNames = map[Kind]string{
	Noop: "noop", UpdateBase: "update-base", Converge: "converge", Forget: "forget",
	Upload: "upload", CreateRemoteDir: "create-remote-dir", DeleteRemote: "delete-remote",
	Download: "download", CreateLocalDir: "create-local-dir", DeleteLocal: "delete-local",
	RestoreRemote: "restore-remote", RestoreLocal: "restore-local", Conflict: "conflict",
	MoveRemote: "move-remote", MoveLocal: "move-local",
	ReplaceRemoteType: "replace-remote-type", ReplaceLocalType: "replace-local-type",
}

func (k Kind) String() string { return kindNames[k] }

// sameLocal reports whether the local item is what Base recorded.
func sameLocal(l *Local, b *Entry) bool {
	if l == nil || b == nil {
		return l == nil && b == nil
	}
	return l.IsDir == b.IsDir && (l.IsDir || l.SHA256 == b.SHA256)
}

// sameRemote reports whether the server item is what Base recorded.
// Content is compared by hash, not version: a version bump with identical
// content (e.g. an idempotent re-upload) is not a change worth acting on.
func sameRemote(r *Remote, b *Entry) bool {
	if r == nil || b == nil {
		return r == nil && b == nil
	}
	return r.IsDir == b.IsDir && (r.IsDir || r.SHA256 == b.SHA256)
}

// Decide is the three-way merge for one path. nil means "absent". It is a
// pure function: all I/O happens in the executor, which makes this the
// easiest part of the system to test exhaustively (see decide_test.go).
func Decide(l *Local, b *Entry, r *Remote) Kind {
	localChanged := !sameLocal(l, b)
	remoteChanged := !sameRemote(r, b)

	switch {
	case !localChanged && !remoteChanged:
		if b != nil && r != nil && r.Version != b.Version {
			return UpdateBase
		}
		return Noop

	case localChanged && !remoteChanged:
		switch {
		case l == nil:
			return DeleteRemote
		case b != nil && l.IsDir != b.IsDir:
			return ReplaceRemoteType
		case l.IsDir:
			return CreateRemoteDir
		default:
			return Upload
		}

	case !localChanged && remoteChanged:
		switch {
		case r == nil:
			return DeleteLocal
		case b != nil && r.IsDir != b.IsDir:
			return ReplaceLocalType
		case r.IsDir:
			return CreateLocalDir
		default:
			return Download
		}
	}

	// Both sides changed since the last sync.
	switch {
	case l == nil && r == nil:
		return Forget
	case l != nil && r != nil && l.IsDir == r.IsDir && (l.IsDir || l.SHA256 == r.SHA256):
		return Converge
	case l != nil && r == nil:
		// Edited here, deleted there: the edit wins (deleting someone's
		// fresh work is the one unrecoverable outcome). Folders that still
		// exist locally are simply recreated on the server.
		if l.IsDir {
			return CreateRemoteDir
		}
		return RestoreRemote
	case l == nil && r != nil:
		// Deleted here, edited there: same rule, the edit wins.
		if r.IsDir {
			return CreateLocalDir
		}
		return RestoreLocal
	default:
		// Different content (or a file on one side and a folder on the
		// other): never overwrite either; keep both.
		return Conflict
	}
}
