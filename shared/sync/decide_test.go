package sync

import "testing"

// Shorthand constructors: f("A") is a file with content A, d() a folder,
// nil means absent. Versions: base v1; remote v1 = unchanged, v2+ = changed.
func lf(sha string) *Local           { return &Local{Path: "p", SHA256: sha, Size: int64(len(sha))} }
func ld() *Local                     { return &Local{Path: "p", IsDir: true} }
func bf(sha string) *Entry           { return &Entry{Path: "p", SHA256: sha, Version: 1} }
func bd() *Entry                     { return &Entry{Path: "p", IsDir: true, Version: 1} }
func rf(sha string, v int64) *Remote { return &Remote{Path: "p", SHA256: sha, Version: v} }
func rd(v int64) *Remote             { return &Remote{Path: "p", IsDir: true, Version: v} }

// TestDecide is the complete decision table for the three-way merge.
// Rows are grouped by who changed what since the last sync.
func TestDecide(t *testing.T) {
	tests := []struct {
		name string
		l    *Local
		b    *Entry
		r    *Remote
		want Kind
	}{
		// ---- nothing changed ----
		{"never existed anywhere", nil, nil, nil, Noop},
		{"in sync", lf("A"), bf("A"), rf("A", 1), Noop},
		{"folder in sync", ld(), bd(), rd(1), Noop},
		{"server version bumped, same content (idempotent re-upload)", lf("A"), bf("A"), rf("A", 5), UpdateBase},
		{"folder version bumped (moved in place / touched)", ld(), bd(), rd(4), UpdateBase},

		// ---- only local changed ----
		{"new local file", lf("A"), nil, nil, Upload},
		{"edited local file", lf("B"), bf("A"), rf("A", 1), Upload},
		{"new local folder", ld(), nil, nil, CreateRemoteDir},
		{"deleted local file", nil, bf("A"), rf("A", 1), DeleteRemote},
		{"deleted local folder", nil, bd(), rd(1), DeleteRemote},
		{"local file replaced by folder", ld(), bf("A"), rf("A", 1), ReplaceRemoteType},
		{"local folder replaced by file", lf("A"), bd(), rd(1), ReplaceRemoteType},

		// ---- only server changed ----
		{"new server file", nil, nil, rf("A", 2), Download},
		{"edited on server", lf("A"), bf("A"), rf("B", 2), Download},
		{"new server folder", nil, nil, rd(2), CreateLocalDir},
		{"deleted on server (stale client must not resurrect it)", lf("A"), bf("A"), nil, DeleteLocal},
		{"folder deleted on server", ld(), bd(), nil, DeleteLocal},
		{"server file replaced by folder", lf("A"), bf("A"), rd(3), ReplaceLocalType},
		{"server folder replaced by file", ld(), bd(), rf("A", 3), ReplaceLocalType},

		// ---- both changed ----
		{"deleted on both sides", nil, bf("A"), nil, Forget},
		{"folder deleted on both sides", nil, bd(), nil, Forget},
		{"same edit on both sides", lf("B"), bf("A"), rf("B", 2), Converge},
		{"same new file on both sides (e.g. crash after upload)", lf("A"), nil, rf("A", 2), Converge},
		{"same new folder on both sides", ld(), nil, rd(2), Converge},
		{"edit / edit", lf("B"), bf("A"), rf("C", 2), Conflict},
		{"new / new with different content", lf("B"), nil, rf("C", 2), Conflict},
		{"edit here, delete there → edit wins", lf("B"), bf("A"), nil, RestoreRemote},
		{"delete here, edit there → edit wins", nil, bf("A"), rf("B", 2), RestoreLocal},
		{"folder kept here, deleted there", ld(), nil, nil, CreateRemoteDir}, // b nil: plain create
		{"new local folder vs new server file", ld(), nil, rf("A", 2), Conflict},
		{"new local file vs new server folder", lf("A"), nil, rd(2), Conflict},
		{"local edited file, server made it a folder", lf("B"), bf("A"), rd(2), Conflict},
		{"local made folder, server edited file", ld(), bf("A"), rf("B", 2), Conflict},
		{"local folder survives server delete of a file at that path", ld(), bf("A"), nil, CreateRemoteDir},
		{"local file deleted, server made it a folder", nil, bf("A"), rd(2), CreateLocalDir},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Decide(tt.l, tt.b, tt.r); got != tt.want {
				t.Fatalf("Decide = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestDecideNeverLosesData checks the core safety property over every
// combination of states: no decision may discard a side that changed.
func TestDecideNeverLosesData(t *testing.T) {
	locals := []*Local{nil, lf("A"), lf("B"), ld()}
	bases := []*Entry{nil, bf("A"), bd()}
	remotes := []*Remote{nil, rf("A", 1), rf("C", 2), rd(1), rd(2)}
	for _, l := range locals {
		for _, b := range bases {
			for _, r := range remotes {
				k := Decide(l, b, r)
				lChanged := !sameLocal(l, b)
				rChanged := !sameRemote(r, b)
				// Destroying local data is only allowed if local didn't change.
				if (k == DeleteLocal || k == Download || k == ReplaceLocalType) && lChanged && l != nil {
					t.Errorf("l=%v b=%v r=%v: %v would overwrite a local change", l, b, r, k)
				}
				// Destroying remote data is only allowed if remote didn't change.
				if (k == DeleteRemote || k == Upload || k == ReplaceRemoteType) && rChanged && r != nil {
					t.Errorf("l=%v b=%v r=%v: %v would overwrite a remote change", l, b, r, k)
				}
			}
		}
	}
}
