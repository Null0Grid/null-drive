package synctest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Null0Grid/null-drive/shared/api"
	"github.com/Null0Grid/null-drive/shared/client"
	"github.com/Null0Grid/null-drive/shared/e2ee"
)

const passphrase = "a long encryption passphrase"

// Cheap Argon2 for tests; the format and code path are the same.
var testKDF = e2ee.KDF{Alg: "argon2id", Time: 1, MemoryKiB: 8 * 1024, Threads: 1}

// encrypt switches a device to end-to-end encrypted mode: unlock the
// account's key with the passphrase, wrap the remote, hash in the keyed
// space, and reset the index if the key changed (exactly what the desktop
// app does after the unlock prompt).
func (d *device) encrypt() *e2ee.Keys {
	t := d.w.t
	t.Helper()
	c := client.New(d.w.ts.URL, d.token, nil)
	keys, _, err := e2ee.Unlock(context.Background(), c, passphrase, false)
	if err != nil {
		t.Fatal(err)
	}
	d.engine.Remote = e2ee.NewRemote(c, keys)
	d.engine.ContentHash = keys.FileHash
	if _, err := d.ix.UseKeyFingerprint(context.Background(), keys.Fingerprint()); err != nil {
		t.Fatal(err)
	}
	return keys
}

// Markers: names and contents that must never appear in the server's data
// directory once the account is encrypted.
var (
	secretNames = []string{"Tax Returns", "divorce-settlement", "medical", "passport-scan"}
	secretBody  = "THE-QUICK-PLAINTEXT-MARKER-0xC0FFEE"
)

func secretFiles() map[string]string {
	big := bytes.Repeat([]byte(secretBody+"\n"), 300000) // ~11 MB, several chunks
	return map[string]string{
		"Tax Returns/2026/divorce-settlement.pdf": secretBody + " settlement terms",
		"medical/passport-scan.jpg":               string(big),
		"medical/notes.txt":                       "medical notes " + secretBody,
	}
}

// scanForPlaintext reads every file under the data dir (database, WAL,
// blobs) and fails if any marker appears.
func scanForPlaintext(t *testing.T, dir string, markers ...string) {
	t.Helper()
	for _, f := range findPlaintext(t, dir, markers...) {
		t.Error(f)
	}
}

func findPlaintext(t *testing.T, dir string, markers ...string) []string {
	t.Helper()
	var found []string
	files := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files++
		for _, m := range markers {
			if bytes.Contains(b, []byte(m)) {
				rel, _ := filepath.Rel(dir, p)
				found = append(found, fmt.Sprintf("plaintext %q found in %s", m, rel))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files < 3 {
		t.Fatalf("scanned only %d files", files)
	}
	return found
}

func markers() []string {
	return append(append([]string{}, secretNames...), secretBody, "settlement terms", "medical notes")
}

func TestE2EESyncServerSeesNothing(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	_, rk, st, err := e2ee.TurnOn(ctx, w.admin, passphrase, testKDF)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != api.E2EEOn || rk == "" {
		t.Fatalf("empty account should be on at once: %+v", st)
	}
	a, b := w.device("A"), w.device("B")
	a.encrypt()
	b.encrypt()
	for p, c := range secretFiles() {
		a.write(p, c)
	}
	a.mkdir("medical/empty folder")
	settle(t, a, b)
	if !equalTrees(a.tree(), b.tree()) {
		t.Fatalf("trees differ:\nA %v\nB %v", keys(a.tree()), keys(b.tree()))
	}
	// Edits, a rename and a delete travel too.
	b.write("medical/notes.txt", "edited "+secretBody)
	b.mv("Tax Returns/2026/divorce-settlement.pdf", "Tax Returns/2026/settled.pdf")
	b.rm("medical/empty folder")
	settle(t, a, b)
	if got := a.tree()["medical/notes.txt"]; got != "edited "+secretBody {
		t.Fatalf("edit: %q", got)
	}
	if _, ok := a.tree()["Tax Returns/2026/settled.pdf"]; !ok {
		t.Fatalf("rename not synced: %v", keys(a.tree()))
	}
	// A conflict produces a (decrypted) conflict copy.
	a.write("medical/notes.txt", "A's version")
	b.write("medical/notes.txt", "B's version")
	a.sync()
	b.sync()
	settle(t, a, b)
	conflicts := 0
	for p := range b.tree() {
		if strings.Contains(p, "conflict") {
			conflicts++
		}
	}
	if conflicts != 1 || !equalTrees(a.tree(), b.tree()) {
		t.Fatalf("conflict handling: %v", keys(b.tree()))
	}

	// The server's listing holds only encrypted names.
	raw := client.New(w.ts.URL, a.token, nil)
	raw.E2EE = true
	list, err := raw.ListFiles(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range list.Files {
		if !e2ee.IsEncryptedPath(f.Path) {
			t.Fatalf("plaintext path on server: %q", f.Path)
		}
	}
	scanForPlaintext(t, w.dir, markers()...)
}

func TestE2EEServerRefusesPlaintext(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	keys, _, _, err := e2ee.TurnOn(ctx, w.admin, passphrase, testKDF)
	if err != nil {
		t.Fatal(err)
	}
	dev := w.device("old client")
	old := client.New(w.ts.URL, dev.token, nil) // no E2EE header: a Phase 6 client
	if _, err := old.ListFiles(ctx, nil); !client.IsCode(err, "e2ee_client_required") {
		t.Fatalf("old client listing: %v", err)
	}
	// The old engine stops with that error instead of syncing garbage.
	dev.write("hello.txt", "hi")
	if _, err := dev.engine.Sync(ctx); !client.IsCode(err, "e2ee_client_required") {
		t.Fatalf("old engine: %v", err)
	}
	// Even an E2EE-aware client can't write plaintext.
	aware := client.New(w.ts.URL, dev.token, nil)
	aware.E2EE = true
	if _, err := aware.Upload(ctx, "plain.txt", strings.NewReader("x"), 1, "", nil); !client.IsCode(err, "e2ee_required") {
		t.Fatalf("v1 upload: %v", err)
	}
	if _, err := aware.CreateFolder(ctx, "Plain Folder"); !client.IsCode(err, "e2ee_path_required") {
		t.Fatalf("plaintext folder: %v", err)
	}
	tmp := filepath.Join(t.TempDir(), "f")
	_ = os.WriteFile(tmp, []byte("content"), 0o600)
	if _, _, err := aware.UploadFile(ctx, "plain.txt", tmp, nil, client.TransferOptions{}); !client.IsCode(err, "e2ee_path_required") {
		t.Fatalf("plaintext commit: %v", err)
	}
	// An encrypted name with a wrong plaintext size is rejected too.
	ep, _ := keys.EncryptPath("x.txt")
	sealed := keys.SealChunk([]byte("content"))
	id, _ := storedHash(sealed)
	if err := aware.PutChunk(ctx, id, sealed, noWrap); err != nil {
		t.Fatal(err)
	}
	if _, err := aware.Commit(ctx, ep, api.CommitRequest{Size: int64(len(sealed)), SHA256: strings.Repeat("a", 64),
		Chunks: []api.ChunkRef{{Hash: id, Size: int64(len(sealed))}}}, nil); !client.IsCode(err, "invalid_manifest") {
		t.Fatalf("ciphertext size as plaintext size: %v", err)
	}
	if _, err := aware.Commit(ctx, ep, api.CommitRequest{Size: 7, SHA256: strings.Repeat("a", 64),
		Chunks: []api.ChunkRef{{Hash: id, Size: int64(len(sealed))}}}, nil); err != nil {
		t.Fatalf("correct encrypted commit: %v", err)
	}
	// Moves must keep names encrypted at both ends.
	if _, err := aware.Move(ctx, ep, "renamed.txt", nil); !client.IsCode(err, "e2ee_path_required") {
		t.Fatalf("move to plaintext: %v", err)
	}
	// Bundle changes need the auth key and can't swap the key.
	st, _ := aware.E2EEStatus(ctx)
	if _, err := aware.UpdateE2EEBundle(ctx, api.E2EEBundleRequest{AuthKey: strings.Repeat("0", 64), Bundle: st.Bundle, Fingerprint: st.Fingerprint}); !client.IsCode(err, "e2ee_auth_failed") {
		t.Fatalf("bundle change without auth key: %v", err)
	}
	if err := e2ee.ChangePassphrase(ctx, aware, keys, "another long passphrase", testKDF); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e2ee.Unlock(ctx, aware, passphrase, false); err == nil {
		t.Fatal("old passphrase still unlocks")
	}
	if _, _, err := e2ee.Unlock(ctx, aware, "another long passphrase", false); err != nil {
		t.Fatal(err)
	}
	// Turning it on twice is refused.
	if _, _, _, err := e2ee.TurnOn(ctx, w.admin, passphrase, testKDF); !client.IsCode(err, "e2ee_already_on") {
		t.Fatalf("second enable: %v", err)
	}
	// Pairing hands a new device the wrapped bundle (not a key).
	pr := struct {
		Code string `json:"code"`
	}{}
	if err := postJSON(w, "/api/v1/pairing", w.admin.Token(), struct{}{}, &pr); err != nil {
		t.Fatal(err)
	}
	var claim api.ClaimPairingResponse
	if err := postJSON(w, "/api/v1/pairing/claim", "", api.ClaimPairingRequest{Code: pr.Code, DeviceName: "new"}, &claim); err != nil {
		t.Fatal(err)
	}
	if claim.E2EE == nil || claim.E2EE.State != api.E2EEOn {
		t.Fatalf("claim: %+v", claim.E2EE)
	}
	b, err := e2ee.ParseBundle(*claim.E2EE)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.UnlockPassphrase("another long passphrase"); err != nil {
		t.Fatalf("unlock from pairing bundle: %v", err)
	}
}

func TestE2EEMigration(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	plain := w.device("plain")
	for p, c := range secretFiles() {
		plain.write(p, c)
	}
	plain.sync()
	// History and Recently deleted: unencrypted copies the purge must catch.
	plain.write("medical/notes.txt", "an older version "+secretBody)
	plain.sync()
	plain.write("Tax Returns/deleted-later.txt", "deleted "+secretBody)
	plain.sync()
	plain.rm("Tax Returns/deleted-later.txt")
	plain.sync()
	want := plain.tree()
	// Build the search index (it holds names and text in plaintext).
	res, err := searchReq(w, "medical")
	if err != nil || res != http.StatusOK {
		t.Fatalf("search before encryption: %v %d", err, res)
	}
	// Control: the scan does see plaintext (names in the database,
	// contents in the blobs, the search index) before encryption.
	f := findPlaintext(t, w.dir, markers()...)
	if len(f) < 4 {
		t.Fatalf("scan control found too little: %v", f)
	}
	if !strings.Contains(strings.Join(f, "\n"), "indexes") {
		t.Fatalf("scan control: nothing found in the search index: %v", f)
	}

	keys, _, st, err := e2ee.TurnOn(ctx, w.admin, passphrase, testKDF)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != api.E2EEMigrating || st.PlaintextFiles == 0 {
		t.Fatalf("account with files should be migrating: %+v", st)
	}
	// The unencrypted device stops instead of mixing plaintext in.
	if _, err := plain.engine.Sync(ctx); !client.IsCode(err, "e2ee_client_required") {
		t.Fatalf("plain device during migration: %v", err)
	}
	rep, err := e2ee.Migrate(ctx, w.admin, keys, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Files != 3 || rep.Finish.State != api.E2EEOn || rep.Finish.PurgedPaths == 0 || rep.Finish.ChunksDeleted == 0 {
		t.Fatalf("migration report: %+v", rep)
	}
	st, err = w.admin.E2EEStatus(ctx)
	if err != nil || st.State != api.E2EEOn || st.PlaintextFiles != 0 {
		t.Fatalf("status after migration: %+v %v", st, err)
	}

	// A new device gets everything back, decrypted.
	fresh := w.device("fresh")
	fresh.encrypt()
	fresh.sync()
	if !equalTrees(fresh.tree(), want) {
		t.Fatalf("migrated content differs:\n got %v\nwant %v", keys2(fresh.tree()), keys2(want))
	}
	// The old device switches to encrypted mode and adopts its files
	// without conflicts or re-downloads.
	plain.encrypt()
	r := plain.sync()
	if r.Conflicts != 0 || r.Ops["download"] != 0 || r.Ops["upload"] != 0 {
		t.Fatalf("re-adoption did work it shouldn't: %+v %v", r.Ops, w.events)
	}
	settle(t, plain, fresh)
	if !equalTrees(plain.tree(), want) {
		t.Fatal("plain device changed during re-adoption")
	}
	// Nothing readable is left: not the names, the contents, the old
	// version, or the deleted file.
	scanForPlaintext(t, w.dir, append(markers(), "an older version", "deleted-later")...)
}

func equalTrees(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func keys2(m map[string]string) []string { return keys(m) }

func noWrap(_ context.Context, r io.Reader) io.Reader { return r }

func storedHash(b []byte) (string, error) {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:]), nil
}

func postJSON(w *world, path, token string, in, out any) error {
	c := client.New(w.ts.URL, token, nil)
	body, _ := json.Marshal(in)
	req, _ := http.NewRequest(http.MethodPost, w.ts.URL+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	_ = c
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(res.Body)
		return fmt.Errorf("%s: %d %s", path, res.StatusCode, b)
	}
	return json.NewDecoder(res.Body).Decode(out)
}

func searchReq(w *world, q string) (int, error) {
	req, err := http.NewRequest(http.MethodGet, w.ts.URL+"/search?q="+q, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+w.admin.Token())
	res, err := w.ts.Client().Do(req)
	if err != nil {
		return 0, err
	}
	res.Body.Close()
	return res.StatusCode, nil
}
