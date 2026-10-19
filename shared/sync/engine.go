package sync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/nulldrive/nulldrive/shared/api"
	"github.com/nulldrive/nulldrive/shared/client"
)

// RemoteAPI is the subset of the server API the engine uses. *client.Client
// implements it; tests can wrap it to inject failures.
type RemoteAPI interface {
	ListFiles(ctx context.Context, since *int64) (api.FileList, error)
	Download(ctx context.Context, path string) (io.ReadCloser, string, error)
	Upload(ctx context.Context, path string, body io.Reader, size int64, sha string, ifMatch *int64) (api.File, error)
	Delete(ctx context.Context, path string, ifMatch *int64) (api.DeleteResponse, error)
	Move(ctx context.Context, from, to string, ifMatch *int64) (api.MoveResponse, error)
	CreateFolder(ctx context.Context, path string) (api.File, error)
}

// ChunkedRemote is the optional API v2 (Phase 6) side of RemoteAPI:
// delta transfers by content-defined chunks. *client.Client implements it;
// against an older server it returns client.ErrV2Unsupported and the
// engine falls back to whole-file transfers.
type ChunkedRemote interface {
	UploadFile(ctx context.Context, path, local string, ifMatch *int64, o client.TransferOptions) (api.File, client.TransferStats, error)
	DownloadFile(ctx context.Context, path, part, reuse string, o client.TransferOptions) (api.Manifest, client.TransferStats, error)
}

// ErrMassDelete stops a cycle that would delete a large share of the
// synced files from the server. Local deletions are *inferred* from absence
// during the scan, so accidents (a wiped folder, the wrong folder selected,
// a disk that mounted empty) look exactly like intentional deletes; the
// user must confirm. Server-side deletions arrive as explicit tombstones
// from another device (where this guard already ran), so they are applied
// without asking.
type ErrMassDelete struct {
	Side  string // "server" or "this computer"
	Count int
	Total int
}

func (e *ErrMassDelete) Error() string {
	return fmt.Sprintf("sync paused: it would delete %d of %d files on %s; confirm to continue", e.Count, e.Total, e.Side)
}

// Event describes one completed (or failed) operation, for the activity
// list and the log.
type Event struct {
	Time time.Time `json:"time"`
	Kind string    `json:"kind"`
	Path string    `json:"path"`
	From string    `json:"from,omitempty"`
	Err  string    `json:"error,omitempty"`
}

// Report summarises one Sync call.
type Report struct {
	Ops       map[string]int `json:"ops"`
	Failed    int            `json:"failed"`
	Skipped   []Skipped      `json:"skipped"`
	Conflicts int            `json:"conflicts"`
}

// Engine runs sync cycles for one folder ↔ one server account.
type Engine struct {
	Root   string // local folder (OS path)
	Index  *Index
	Remote RemoteAPI
	Device string // used in conflict copy names

	// Up/Down limit transfer speed (nil = unlimited).
	Up, Down *Limiter

	// Guard against mass deletion: a cycle stops if it would delete more
	// than MassDeleteRatio of the synced files *and* at least
	// MassDeleteMin files on one side, unless AllowMassDelete is set.
	MassDeleteMin   int
	MassDeleteRatio float64
	AllowMassDelete bool

	OnEvent    func(Event)
	OnProgress func(done, total int, path string)
	// OnBytes reports network progress within the current file.
	OnBytes func(path string, done, total int64)
	// OnTransfer reports what each chunked transfer moved (for the log
	// and benchmarks).
	OnTransfer func(path, direction string, st client.TransferStats)
	// Parallel is the number of concurrent chunk requests (default 4).
	Parallel int
	// WholeFileOnly disables chunked transfers (testing, comparisons).
	WholeFileOnly bool
	// ContentHash maps a file's SHA-256 to the value the server records
	// (nil = lowercase hex). Set to e2ee.Keys.FileHash for encrypted
	// accounts, together with an e2ee.Remote as Remote.
	ContentHash func(sum []byte) string

	Now func() time.Time
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Engine) emit(k Kind, p, from string, err error) {
	if e.OnEvent == nil {
		return
	}
	ev := Event{Time: e.now(), Kind: k.String(), Path: p, From: from}
	if err != nil {
		ev.Err = err.Error()
	}
	e.OnEvent(ev)
}

func (e *Engine) full(p string) string { return filepath.Join(e.Root, filepath.FromSlash(p)) }

// errSkip marks an operation that was not done because the world changed
// under it (the server or the file moved on). It is not an error: the next
// cycle re-evaluates the path with fresh state.
var errSkip = errors.New("skipped: changed during sync")

// Sync runs one cycle: refresh the remote mirror, scan, plan, execute.
// It returns early only for problems that make the whole cycle unsafe or
// impossible (server unreachable, folder missing, mass delete); individual
// operation failures are counted in the report and retried next cycle.
func (e *Engine) Sync(ctx context.Context) (*Report, error) {
	if err := e.refreshRemote(ctx); err != nil {
		return nil, fmt.Errorf("fetch changes: %w", err)
	}
	bases, err := e.Index.Bases(ctx)
	if err != nil {
		return nil, err
	}
	remotes, err := e.Index.Remotes(ctx)
	if err != nil {
		return nil, err
	}
	scan, err := ScanWith(e.Root, bases, e.ContentHash)
	if err != nil {
		return nil, err
	}

	skipped := map[string]bool{}
	for _, s := range scan.Skipped {
		skipped[s.Path] = true
	}
	skip := func(p string) bool {
		if scan.Ignore.Match(p, false) || scan.Ignore.Match(p, true) {
			return true
		}
		for s := range skipped {
			if p == s || isWithin(p, s) {
				return true
			}
		}
		return false
	}
	ops := Plan(scan.Items, bases, remotes, skip)
	if err := e.checkMassDelete(ops, bases); err != nil {
		return nil, err
	}

	rep := &Report{Ops: map[string]int{}, Skipped: scan.Skipped}
	x := &executor{e: e, locals: scan.Items, remotes: remotes, ops: ops}
	for i, op := range ops {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		if e.OnProgress != nil {
			e.OnProgress(i, len(ops), op.Path)
		}
		err := x.run(ctx, op)
		switch {
		case err == nil:
			rep.Ops[op.Kind.String()]++
			if op.Kind == Conflict {
				rep.Conflicts++
			}
			// Conflicts emit their own event (with the copy's name).
			if op.Kind != UpdateBase && op.Kind != Converge && op.Kind != Forget && op.Kind != Conflict {
				e.emit(op.Kind, op.Path, op.From, nil)
			}
		case errors.Is(err, errSkip):
			// Re-evaluated next cycle.
		case client.IsNetwork(err) || errors.Is(err, context.Canceled):
			return rep, err
		default:
			rep.Failed++
			e.emit(op.Kind, op.Path, op.From, err)
		}
	}
	if e.OnProgress != nil {
		e.OnProgress(len(ops), len(ops), "")
	}
	e.AllowMassDelete = false // a confirmation covers one cycle only
	return rep, nil
}

func (e *Engine) checkMassDelete(ops []Op, bases map[string]*Entry) error {
	if e.AllowMassDelete {
		return nil
	}
	minN := e.MassDeleteMin
	if minN == 0 {
		minN = 25
	}
	ratio := e.MassDeleteRatio
	if ratio == 0 {
		ratio = 0.5
	}
	total := 0
	for _, b := range bases {
		if !b.IsDir {
			total++
		}
	}
	delRemote := 0
	for _, op := range ops {
		if op.Kind == DeleteRemote && op.Base != nil && !op.Base.IsDir {
			delRemote++
		}
	}
	if delRemote >= minN && float64(delRemote) > ratio*float64(total) {
		return &ErrMassDelete{Side: "the server", Count: delRemote, Total: total}
	}
	return nil
}

// refreshRemote brings the mirror up to date: a full listing on first sync,
// then only the change feed since the stored cursor.
func (e *Engine) refreshRemote(ctx context.Context) error {
	cursor, err := e.Index.Cursor(ctx)
	if err != nil {
		return err
	}
	var since *int64
	if cursor > 0 {
		since = &cursor
	}
	list, err := e.Remote.ListFiles(ctx, since)
	if err != nil {
		return err
	}
	changes := make([]RemoteChange, 0, len(list.Files))
	for _, f := range list.Files {
		changes = append(changes, RemoteChange{Remote: remoteOf(f), Deleted: f.Deleted})
	}
	return e.Index.ApplyFeed(ctx, changes, list.Version, since == nil)
}

func remoteOf(f api.File) Remote {
	return Remote{Path: f.Path, IsDir: f.IsDir, Size: f.Size, SHA256: f.SHA256, Version: f.Version}
}

// ---------------------------------------------------------------------------

type executor struct {
	e       *Engine
	locals  map[string]*Local
	remotes map[string]*Remote
	ops     []Op
}

func i64(v int64) *int64 { return &v }

// remoteChangedErr reports server answers that mean "someone else changed
// this since our snapshot": not a failure, re-evaluate next cycle.
func remoteChangedErr(err error) bool {
	return client.IsCode(err, "conflict") || client.IsCode(err, "precondition_required") ||
		client.IsCode(err, "target_exists") || client.IsCode(err, "hash_mismatch")
}

func (x *executor) run(ctx context.Context, op Op) error {
	e := x.e
	ix := e.Index
	switch op.Kind {
	case UpdateBase:
		b := *op.Base
		b.Version = op.Remote.Version
		return ix.PutBase(ctx, b)

	case Converge:
		b := Entry{Path: op.Path, IsDir: op.Local.IsDir, Version: op.Remote.Version}
		if !op.Local.IsDir {
			b.Size, b.MtimeNs, b.SHA256 = op.Local.Size, op.Local.MtimeNs, op.Local.SHA256
		}
		return ix.PutBase(ctx, b)

	case Forget:
		return ix.DeleteBase(ctx, op.Path)

	case Upload:
		var ifm *int64
		if op.Base != nil {
			ifm = i64(op.Base.Version)
		} else {
			ifm = i64(0) // create only: never overwrite something we haven't seen
		}
		return x.upload(ctx, op.Path, op.Local, ifm)

	case RestoreRemote:
		return x.upload(ctx, op.Path, op.Local, i64(0))

	case CreateRemoteDir:
		f, err := e.Remote.CreateFolder(ctx, op.Path)
		if err != nil {
			if client.IsCode(err, "path_collision") {
				return errSkip
			}
			return err
		}
		return x.recordRemote(ctx, f, &Entry{Path: op.Path, IsDir: true, Version: f.Version})

	case Download, RestoreLocal:
		return x.download(ctx, op.Path, op.Remote, op.Local)

	case CreateLocalDir:
		if err := os.MkdirAll(e.full(op.Path), 0o755); err != nil {
			return err
		}
		return ix.PutBase(ctx, Entry{Path: op.Path, IsDir: true, Version: op.Remote.Version})

	case DeleteRemote:
		return x.deleteRemote(ctx, op)

	case DeleteLocal:
		return x.deleteLocal(ctx, op)

	case MoveRemote:
		return x.moveRemote(ctx, op)

	case MoveLocal:
		return x.moveLocal(ctx, op)

	case Conflict:
		return x.conflict(ctx, op)

	case ReplaceRemoteType:
		return x.replaceRemoteType(ctx, op)

	case ReplaceLocalType:
		return x.replaceLocalType(ctx, op)
	}
	return fmt.Errorf("unknown op %v", op.Kind)
}

// recordRemote stores a server change we just made in the mirror (and the
// base, if given), so the next feed doesn't look like someone else's change.
func (x *executor) recordRemote(ctx context.Context, f api.File, base *Entry) error {
	r := remoteOf(f)
	x.remotes[f.Path] = &r
	if err := x.e.Index.PutRemote(ctx, RemoteChange{Remote: r}); err != nil {
		return err
	}
	if base != nil {
		return x.e.Index.PutBase(ctx, *base)
	}
	return nil
}

// statMatches reports whether the file on disk still is what the scan saw.
func (x *executor) statMatches(p string, l *Local) bool {
	st, err := os.Stat(x.e.full(p))
	if l == nil {
		return errors.Is(err, os.ErrNotExist)
	}
	if err != nil {
		return false
	}
	if l.IsDir {
		return st.IsDir()
	}
	return !st.IsDir() && st.Size() == l.Size && st.ModTime().UnixNano() == l.MtimeNs
}

func (x *executor) upload(ctx context.Context, p string, l *Local, ifm *int64) error {
	e := x.e
	f, err := os.Open(e.full(p))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errSkip
		}
		return err
	}
	defer f.Close()
	if !x.statMatches(p, l) {
		return errSkip // edited since the scan; upload the new content next cycle
	}
	var file api.File
	if cr, ok := e.Remote.(ChunkedRemote); ok && !e.WholeFileOnly {
		var st client.TransferStats
		file, st, err = cr.UploadFile(ctx, p, e.full(p), ifm, e.transferOptions(p, e.Up))
		if err == nil {
			if e.OnTransfer != nil {
				e.OnTransfer(p, "up", st)
			}
			if file.SHA256 != l.SHA256 {
				// Edited between the scan and the upload: the server has
				// the newer content. Record the remote side only; the next
				// scan reconciles the local base.
				r := remoteOf(file)
				x.remotes[file.Path] = &r
				if err := x.e.Index.PutRemote(ctx, RemoteChange{Remote: r}); err != nil {
					return err
				}
				return errSkip
			}
		}
		if errors.Is(err, client.ErrV2Unsupported) {
			file, err = e.Remote.Upload(ctx, p, e.Up.Reader(ctx, f), l.Size, l.SHA256, ifm)
		}
	} else {
		file, err = e.Remote.Upload(ctx, p, e.Up.Reader(ctx, f), l.Size, l.SHA256, ifm)
	}
	if err != nil {
		if remoteChangedErr(err) || client.IsCode(err, "path_collision") {
			return errSkip
		}
		return err
	}
	return x.recordRemote(ctx, file, &Entry{Path: p, Size: l.Size, MtimeNs: l.MtimeNs, SHA256: l.SHA256, Version: file.Version})
}

// download fetches r into place atomically: write to "<name>.nulldrive-part"
// next to the target, verify the hash, then rename over the target. A crash
// leaves at most a .nulldrive-part file, which the next scan removes.
//
// expect is what the scan saw at the target path. If the file there changed
// while we were downloading, the local version is kept as a conflict copy.
func (x *executor) download(ctx context.Context, p string, r *Remote, expect *Local) error {
	e := x.e
	target := e.full(p)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	tmp := target + partSuffix
	chunked := false
	if cr, ok := e.Remote.(ChunkedRemote); ok && !e.WholeFileOnly {
		// Delta download: chunks already in the old local copy (or in an
		// interrupted earlier attempt) are reused, only the rest is fetched.
		reuse := ""
		if expect != nil && !expect.IsDir {
			reuse = target
		}
		m, st, err := cr.DownloadFile(ctx, p, tmp, reuse, e.transferOptions(p, e.Down))
		switch {
		case err == nil && (m.Version != r.Version || m.SHA256 != r.SHA256):
			// Newer than our mirror: refetch after the next feed update.
			removePart(tmp)
			return errSkip
		case err == nil:
			chunked = true
			if e.OnTransfer != nil {
				e.OnTransfer(p, "down", st)
			}
		case errors.Is(err, client.ErrV2Unsupported):
		case errors.Is(err, client.ErrDownloadChanged), client.IsCode(err, "not_found"):
			removePart(tmp)
			return errSkip
		default:
			return err // the partial file + state stay for a resume
		}
	}
	if !chunked {
		if err := e.downloadWhole(ctx, p, tmp, r); err != nil {
			return err
		}
	}

	// Last check before replacing: has someone touched the target?
	if !x.statMatches(p, expect) {
		if _, err := os.Lstat(target); err == nil {
			if err := x.keepLocalCopy(ctx, p); err != nil {
				_ = os.Remove(tmp)
				return err
			}
		}
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	st, err := os.Stat(target)
	if err != nil {
		return err
	}
	return e.Index.PutBase(ctx, Entry{Path: p, Size: st.Size(), MtimeNs: st.ModTime().UnixNano(), SHA256: r.SHA256, Version: r.Version})
}

// downloadWhole is the v1 path: one GET of the whole file.
func (e *Engine) downloadWhole(ctx context.Context, p, tmp string, r *Remote) error {
	rc, _, err := e.Remote.Download(ctx, p)
	if err != nil {
		if client.IsCode(err, "not_found") || client.IsCode(err, "is_folder") {
			return errSkip
		}
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644) //nolint:gosec // inside the sync root
	if err != nil {
		return err
	}
	h := sha256.New()
	_, cerr := io.Copy(io.MultiWriter(out, h), e.Down.Reader(ctx, rc))
	serr := out.Sync()
	clErr := out.Close()
	if err := errors.Join(cerr, serr, clErr); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != r.SHA256 {
		_ = os.Remove(tmp)
		return errSkip // changed on the server mid-download; fetch again next cycle
	}
	return nil
}

func removePart(tmp string) {
	_ = os.Remove(tmp)
	_ = os.Remove(tmp + client.PartStateSuffix)
}

func (e *Engine) transferOptions(p string, lim *Limiter) client.TransferOptions {
	o := client.TransferOptions{
		Parallel: e.Parallel,
		Wrap:     func(ctx context.Context, r io.Reader) io.Reader { return lim.Reader(ctx, r) },
	}
	if e.OnBytes != nil {
		o.Progress = func(done, total int64) { e.OnBytes(p, done, total) }
	}
	return o
}

// keepLocalCopy renames the local item at p to a conflict copy name and
// records the conflict. The copy is uploaded right away if it's a file (a
// failure there is fine: the next cycle sees it as a new local file).
func (x *executor) keepLocalCopy(ctx context.Context, p string) error {
	e := x.e
	cp := ConflictName(p, e.Device, e.now(), func(c string) bool {
		if _, ok := x.locals[c]; ok {
			return true
		}
		if _, ok := x.remotes[c]; ok {
			return true
		}
		_, err := os.Lstat(e.full(c))
		return err == nil
	})
	if err := os.Rename(e.full(p), e.full(cp)); err != nil {
		return err
	}
	if _, err := e.Index.AddConflict(ctx, ConflictRecord{Path: p, ConflictPath: cp, Device: e.Device, At: e.now()}); err != nil {
		return err
	}
	e.emit(Conflict, p, cp, nil)
	// Best effort: upload the copy now so other devices see it promptly.
	// If this fails for any reason, the next cycle finds it as a new local
	// file and uploads it then; the conflict itself is already safe.
	if st, err := os.Stat(e.full(cp)); err == nil && !st.IsDir() {
		if sum, herr := hashFile(e.full(cp), e.ContentHash); herr == nil {
			l := &Local{Path: cp, Size: st.Size(), MtimeNs: st.ModTime().UnixNano(), SHA256: sum}
			x.locals[cp] = l
			_ = x.upload(ctx, cp, l, i64(0))
		}
	}
	return nil
}

func (x *executor) conflict(ctx context.Context, op Op) error {
	e := x.e
	if err := x.keepLocalCopy(ctx, op.Path); err != nil {
		return err
	}
	if op.Remote.IsDir {
		if err := os.MkdirAll(e.full(op.Path), 0o755); err != nil {
			return err
		}
		return e.Index.PutBase(ctx, Entry{Path: op.Path, IsDir: true, Version: op.Remote.Version})
	}
	return x.download(ctx, op.Path, op.Remote, nil)
}

func (x *executor) deleteRemote(ctx context.Context, op Op) error {
	e := x.e
	b := op.Base
	if b.IsDir {
		// Only delete a server folder if nothing inside it would survive:
		// a file someone else added there must not be deleted with it.
		for p := range x.remotes {
			if isWithin(p, op.Path) && !x.deletingRemote(p) {
				return e.Index.DeleteBase(ctx, op.Path) // keep the remote folder
			}
		}
		if _, err := e.Remote.Delete(ctx, op.Path, nil); err != nil && !client.IsCode(err, "not_found") {
			return err
		}
	} else if _, err := e.Remote.Delete(ctx, op.Path, i64(b.Version)); err != nil {
		switch {
		case client.IsCode(err, "not_found"):
		case remoteChangedErr(err):
			return errSkip
		default:
			return err
		}
	}
	for p := range x.remotes {
		if p == op.Path || isWithin(p, op.Path) {
			delete(x.remotes, p)
		}
	}
	if err := e.Index.DeleteRemoteTree(ctx, op.Path); err != nil {
		return err
	}
	return e.Index.DeleteBase(ctx, op.Path)
}

// deletingRemote reports whether this cycle deletes p from the server.
func (x *executor) deletingRemote(p string) bool {
	for _, op := range x.ops {
		if op.Path == p && op.Kind == DeleteRemote {
			return true
		}
		if op.Kind == MoveRemote && op.From == p {
			return true
		}
	}
	return false
}

func (x *executor) deleteLocal(ctx context.Context, op Op) error {
	e := x.e
	full := e.full(op.Path)
	if op.Base.IsDir {
		if err := removeDirIfOnlyIgnored(full, e.Root); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return e.Index.DeleteBase(ctx, op.Path)
	}
	if !x.statMatches(op.Path, op.Local) {
		return errSkip // edited since the scan: the edit wins next cycle
	}
	if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return e.Index.DeleteBase(ctx, op.Path)
}

// removeDirIfOnlyIgnored deletes a folder the server deleted, but only if
// what's left in it is junk the user never saw synced (Thumbs.db,
// desktop.ini, .DS_Store...). Anything else stays, and becomes a new
// local item on the next cycle.
func removeDirIfOnlyIgnored(dir, root string) error {
	ig := NewIgnore(nil)
	onlyJunk := true
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if ig.Match(filepath.ToSlash(rel), d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			onlyJunk = false
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !onlyJunk {
		return nil
	}
	return os.RemoveAll(dir)
}

func (x *executor) moveRemote(ctx context.Context, op Op) error {
	e := x.e
	res, err := e.Remote.Move(ctx, op.From, op.Path, i64(op.FromBase.Version))
	if err != nil {
		if !remoteChangedErr(err) && !client.IsCode(err, "not_found") && !client.IsCode(err, "path_collision") {
			return err
		}
		// Fall back to upload + delete (each with its own safety checks).
		if uerr := x.upload(ctx, op.Path, op.Local, i64(0)); uerr != nil {
			return uerr
		}
		return x.deleteRemote(ctx, Op{Kind: DeleteRemote, Path: op.From, Base: op.FromBase})
	}
	if err := e.Index.DeleteRemoteTree(ctx, op.From); err != nil {
		return err
	}
	delete(x.remotes, op.From)
	if err := e.Index.DeleteBase(ctx, op.From); err != nil {
		return err
	}
	for _, m := range res.Moved {
		if m.File.Path != op.Path {
			continue
		}
		return x.recordRemote(ctx, m.File, &Entry{Path: op.Path, Size: op.Local.Size, MtimeNs: op.Local.MtimeNs, SHA256: op.Local.SHA256, Version: m.File.Version})
	}
	return nil
}

func (x *executor) moveLocal(ctx context.Context, op Op) error {
	e := x.e
	from, to := e.full(op.From), e.full(op.Path)
	fallback := func() error {
		if err := x.download(ctx, op.Path, op.Remote, nil); err != nil {
			return err
		}
		return x.deleteLocal(ctx, Op{Kind: DeleteLocal, Path: op.From, Base: op.FromBase, Local: op.Local})
	}
	if !x.statMatches(op.From, op.Local) {
		return fallback()
	}
	if _, err := os.Lstat(to); err == nil {
		return fallback()
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	if err := os.Rename(from, to); err != nil {
		return fallback()
	}
	st, err := os.Stat(to)
	if err != nil {
		return err
	}
	if err := e.Index.DeleteBase(ctx, op.From); err != nil {
		return err
	}
	return e.Index.PutBase(ctx, Entry{Path: op.Path, Size: st.Size(), MtimeNs: st.ModTime().UnixNano(), SHA256: op.Remote.SHA256, Version: op.Remote.Version})
}

// replaceRemoteType: locally, a file became a folder or a folder a file.
func (x *executor) replaceRemoteType(ctx context.Context, op Op) error {
	e := x.e
	if op.Local.IsDir { // file → folder
		if _, err := e.Remote.Delete(ctx, op.Path, i64(op.Base.Version)); err != nil && !client.IsCode(err, "not_found") {
			if remoteChangedErr(err) {
				return errSkip
			}
			return err
		}
		f, err := e.Remote.CreateFolder(ctx, op.Path)
		if err != nil {
			return err
		}
		return x.recordRemote(ctx, f, &Entry{Path: op.Path, IsDir: true, Version: f.Version})
	}
	// folder → file: only if the server folder has nothing else in it.
	for p := range x.remotes {
		if isWithin(p, op.Path) && !x.deletingRemote(p) {
			return x.keepLocalCopy(ctx, op.Path)
		}
	}
	if _, err := e.Remote.Delete(ctx, op.Path, nil); err != nil && !client.IsCode(err, "not_found") {
		return err
	}
	_ = e.Index.DeleteRemoteTree(ctx, op.Path)
	return x.upload(ctx, op.Path, op.Local, i64(0))
}

// replaceLocalType: on the server, a file became a folder or vice versa.
func (x *executor) replaceLocalType(ctx context.Context, op Op) error {
	e := x.e
	full := e.full(op.Path)
	if op.Remote.IsDir { // local file → folder
		if !x.statMatches(op.Path, op.Local) {
			return errSkip
		}
		if err := os.Remove(full); err != nil {
			return err
		}
		if err := os.MkdirAll(full, 0o755); err != nil {
			return err
		}
		return e.Index.PutBase(ctx, Entry{Path: op.Path, IsDir: true, Version: op.Remote.Version})
	}
	// local folder → file: remove the folder only if it's empty (its
	// children are handled by their own ops); otherwise keep it as a copy.
	if err := os.Remove(full); err != nil {
		if err := x.keepLocalCopy(ctx, op.Path); err != nil {
			return err
		}
	}
	return x.download(ctx, op.Path, op.Remote, nil)
}
