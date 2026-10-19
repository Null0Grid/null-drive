package sync

import (
	"sort"
)

// Op is one planned action.
type Op struct {
	Kind   Kind
	Path   string
	From   string // MoveRemote / MoveLocal: the old path
	Local  *Local
	Base   *Entry
	Remote *Remote
	// FromBase is the base entry of the old path for moves.
	FromBase *Entry
}

// Plan runs Decide for every path known to any side and turns the result
// into an ordered list of operations.
//
// skip(p) excludes paths that must not be touched this cycle: ignored paths
// and files the scan couldn't read. Without it, a file that is merely
// locked (or newly ignored) would look "deleted locally" and be deleted
// from the server.
func Plan(locals map[string]*Local, bases map[string]*Entry, remotes map[string]*Remote, skip func(string) bool) []Op {
	paths := map[string]struct{}{}
	for p := range locals {
		paths[p] = struct{}{}
	}
	for p := range bases {
		paths[p] = struct{}{}
	}
	for p := range remotes {
		paths[p] = struct{}{}
	}

	var ops []Op
	for p := range paths {
		if skip != nil && skip(p) {
			continue
		}
		l, b, r := locals[p], bases[p], remotes[p]
		if k := Decide(l, b, r); k != Noop {
			ops = append(ops, Op{Kind: k, Path: p, Local: l, Base: b, Remote: r})
		}
	}
	ops = detectRenames(ops)
	sortOps(ops)
	return ops
}

// detectRenames turns "delete X + create Y with identical content" into a
// move, on either side. A local rename then becomes a server-side move
// (no re-upload, and other devices see it as a rename too), and a remote
// rename becomes a local rename (no re-download).
//
// Matching is by content hash. When several candidates share a hash the
// one with the same file name (moved to another folder) is preferred,
// then the first in path order, so results are deterministic.
func detectRenames(ops []Op) []Op {
	deletedRemote := map[string][]int{} // sha → indexes of DeleteRemote ops (local side deleted)
	deletedLocal := map[string][]int{}  // sha → indexes of DeleteLocal ops (server side deleted)
	for i, op := range ops {
		if op.Base == nil || op.Base.IsDir || op.Base.SHA256 == "" {
			continue
		}
		switch op.Kind {
		case DeleteRemote:
			deletedRemote[op.Base.SHA256] = append(deletedRemote[op.Base.SHA256], i)
		case DeleteLocal:
			deletedLocal[op.Base.SHA256] = append(deletedLocal[op.Base.SHA256], i)
		}
	}
	for _, m := range []map[string][]int{deletedRemote, deletedLocal} {
		for _, idx := range m {
			sort.Slice(idx, func(a, b int) bool { return ops[idx[a]].Path < ops[idx[b]].Path })
		}
	}
	used := map[int]bool{}
	pick := func(list []int, name string) int {
		best := -1
		for _, i := range list {
			if used[i] {
				continue
			}
			if baseName(ops[i].Path) == name {
				return i
			}
			if best < 0 {
				best = i
			}
		}
		return best
	}

	// Iterate in path order for determinism.
	order := make([]int, len(ops))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return ops[order[a]].Path < ops[order[b]].Path })

	for _, i := range order {
		op := &ops[i]
		switch {
		case op.Kind == Upload && op.Base == nil && op.Local != nil:
			if j := pick(deletedRemote[op.Local.SHA256], baseName(op.Path)); j >= 0 {
				used[j] = true
				*op = Op{Kind: MoveRemote, Path: op.Path, From: ops[j].Path, Local: op.Local, FromBase: ops[j].Base, Remote: op.Remote}
			}
		case op.Kind == Download && op.Base == nil && op.Local == nil && op.Remote != nil:
			if j := pick(deletedLocal[op.Remote.SHA256], baseName(op.Path)); j >= 0 {
				used[j] = true
				*op = Op{Kind: MoveLocal, Path: op.Path, From: ops[j].Path, Remote: op.Remote, FromBase: ops[j].Base, Local: ops[j].Local}
			}
		}
	}
	out := ops[:0]
	for i, op := range ops {
		if !used[i] {
			out = append(out, op)
		}
	}
	return out
}

func baseName(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

// phase orders operations so each one's preconditions hold:
//
//	0 create folders (parents before children)
//	1 moves (their targets' folders now exist)
//	2 transfers and bookkeeping
//	3 deletions (children before parents, after moves took what they need)
func phase(k Kind) int {
	switch k {
	case CreateLocalDir, CreateRemoteDir:
		return 0
	case MoveRemote, MoveLocal:
		return 1
	case DeleteLocal, DeleteRemote:
		return 3
	default:
		return 2
	}
}

func sortOps(ops []Op) {
	sort.SliceStable(ops, func(a, b int) bool {
		pa, pb := phase(ops[a].Kind), phase(ops[b].Kind)
		if pa != pb {
			return pa < pb
		}
		if pa == 3 {
			return ops[a].Path > ops[b].Path // deepest first
		}
		return ops[a].Path < ops[b].Path
	})
}
