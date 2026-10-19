package e2ee

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/Null0Grid/null-drive/shared/api"
	"github.com/Null0Grid/null-drive/shared/client"
)

// Account-level operations shared by the desktop app and the CLI: turn
// encryption on, unlock, change the passphrase, migrate existing files.

// ParseBundle decodes the bundle from an E2EEStatus.
func ParseBundle(st api.E2EEStatus) (Bundle, error) {
	if st.State == api.E2EEOff || len(st.Bundle) == 0 {
		return Bundle{}, errors.New("e2ee: encryption is not turned on for this account")
	}
	var b Bundle
	if err := json.Unmarshal(st.Bundle, &b); err != nil {
		return Bundle{}, ErrBundle
	}
	if b.Fingerprint != st.Fingerprint {
		return Bundle{}, ErrBundle
	}
	return b, nil
}

// TurnOn creates keys for the account behind c (a session token) and
// registers the bundle. The account moves to "migrating" if it has files,
// else straight to "on". The recovery key must be shown to the user.
func TurnOn(ctx context.Context, c *client.Client, passphrase string, kdf KDF) (*Keys, string, api.E2EEStatus, error) {
	b, keys, rk, err := Enable(passphrase, kdf)
	if err != nil {
		return nil, "", api.E2EEStatus{}, err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return nil, "", api.E2EEStatus{}, err
	}
	st, err := c.EnableE2EE(ctx, api.E2EEEnableRequest{Bundle: raw, Fingerprint: b.Fingerprint, AuthKey: keys.AuthKey()})
	if err != nil {
		return nil, "", st, err
	}
	return keys, rk, st, nil
}

// Unlock fetches the bundle and opens it with a passphrase or, if
// recovery is set, a recovery key.
func Unlock(ctx context.Context, c *client.Client, secret string, recovery bool) (*Keys, api.E2EEStatus, error) {
	st, err := c.E2EEStatus(ctx)
	if err != nil {
		return nil, st, err
	}
	b, err := ParseBundle(st)
	if err != nil {
		return nil, st, err
	}
	var k *Keys
	if recovery {
		k, err = b.UnlockRecovery(secret)
	} else {
		k, err = b.UnlockPassphrase(secret)
	}
	return k, st, err
}

// ChangePassphrase re-wraps the master key under a new passphrase. No file
// is re-encrypted; the recovery key stays valid.
func ChangePassphrase(ctx context.Context, c *client.Client, keys *Keys, passphrase string, kdf KDF) error {
	st, err := c.E2EEStatus(ctx)
	if err != nil {
		return err
	}
	b, err := ParseBundle(st)
	if err != nil {
		return err
	}
	nb, err := b.WithPassphrase(keys, passphrase, kdf)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(nb)
	if err != nil {
		return err
	}
	_, err = c.UpdateE2EEBundle(ctx, api.E2EEBundleRequest{AuthKey: keys.AuthKey(), Bundle: raw, Fingerprint: nb.Fingerprint})
	return err
}

// MigrateProgress reports migration progress.
type MigrateProgress struct {
	Done, Total int
	Path        string
}

// MigrateReport summarises a migration.
type MigrateReport struct {
	Files, Folders int
	Bytes          int64
	// TooLong lists names the encrypted format can't hold (over 255
	// bytes). If any exist, the plaintext is NOT deleted: rename them and
	// run Migrate again.
	TooLong []string
	Finish  api.E2EEFinishResponse
}

// ErrNamesTooLong stops a migration before the plaintext is deleted.
var ErrNamesTooLong = errors.New("e2ee: some names are too long to encrypt; rename them and run the migration again")

// Migrate encrypts an account's existing files: each plaintext file is
// downloaded, re-uploaded encrypted under its encrypted path, and when
// everything is copied the server is told to delete the plaintext
// (history and Recently deleted included: they are unencrypted copies).
//
// It is resumable: files already present under their encrypted path with
// the same content are skipped, so running it again after an interruption
// continues where it stopped. tmpDir holds one plaintext file at a time.
func Migrate(ctx context.Context, c *client.Client, keys *Keys, tmpDir string, progress func(MigrateProgress)) (MigrateReport, error) {
	var rep MigrateReport
	c.E2EE = true
	list, err := c.ListFiles(ctx, nil)
	if err != nil {
		return rep, err
	}
	existing := map[string]api.File{}
	var todo []api.File
	for _, f := range list.Files {
		if f.Deleted {
			continue
		}
		if IsEncryptedPath(f.Path) {
			existing[f.Path] = f
		} else {
			todo = append(todo, f)
		}
	}
	// Parents before children, so folder creation never races a file.
	sort.Slice(todo, func(i, j int) bool { return todo[i].Path < todo[j].Path })
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return rep, err
	}
	tmp := filepath.Join(tmpDir, "nulldrive-migrate.part")
	defer os.Remove(tmp)
	defer os.Remove(tmp + client.PartStateSuffix)

	for i, f := range todo {
		if progress != nil {
			progress(MigrateProgress{Done: i, Total: len(todo), Path: f.Path})
		}
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		ep, err := keys.EncryptPath(f.Path)
		if err != nil {
			rep.TooLong = append(rep.TooLong, f.Path)
			continue
		}
		if f.IsDir {
			if _, ok := existing[ep]; !ok {
				if _, err := c.CreateFolder(ctx, ep); err != nil && !client.IsCode(err, "already_exists") {
					return rep, fmt.Errorf("%s: %w", f.Path, err)
				}
			}
			rep.Folders++
			continue
		}
		_ = os.Remove(tmp + client.PartStateSuffix)
		if _, _, err := c.DownloadFile(ctx, f.Path, tmp, "", client.TransferOptions{}); err != nil {
			return rep, fmt.Errorf("download %s: %w", f.Path, err)
		}
		var ifMatch *int64
		if cur, ok := existing[ep]; ok {
			ifMatch = &cur.Version // overwrite a partial earlier attempt
		}
		if _, _, err := c.UploadFile(ctx, ep, tmp, ifMatch, client.TransferOptions{Codec: keys}); err != nil {
			return rep, fmt.Errorf("upload %s: %w", f.Path, err)
		}
		rep.Files++
		rep.Bytes += f.Size
	}
	if progress != nil {
		progress(MigrateProgress{Done: len(todo), Total: len(todo)})
	}
	if len(rep.TooLong) > 0 {
		return rep, ErrNamesTooLong // never delete what we could not copy
	}
	fin, err := c.FinishE2EE(ctx, keys.AuthKey())
	rep.Finish = fin
	return rep, err
}
