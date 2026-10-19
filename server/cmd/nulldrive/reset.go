package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nulldrive/nulldrive/server/internal/auth"
	"github.com/nulldrive/nulldrive/server/internal/config"
	"github.com/nulldrive/nulldrive/server/internal/db"
	"github.com/nulldrive/nulldrive/server/internal/meta"
)

// resetPassword is the recovery path for a forgotten password: whoever can
// run commands on the server (docker exec) owns the data anyway, so that is
// the right trust boundary. A random password is generated rather than read
// from the terminal, which avoids echoing it and works in a distroless
// container without a TTY. All web sessions of the user are ended; device
// tokens keep syncing.
func resetPassword(getenv func(string) string, username string, out io.Writer) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	// SQLite in WAL mode with a busy timeout is safe to open from a second
	// process while the server is running.
	database, err := db.Open(filepath.Join(cfg.DataDir, "nulldrive.db"))
	if err != nil {
		return err
	}
	defer database.Close()
	ctx := context.Background()
	if _, err := db.Migrate(ctx, database); err != nil {
		return err
	}
	store := meta.New(database, nil)
	u, err := store.UserByUsername(ctx, username)
	if errors.Is(err, meta.ErrNotFound) {
		return fmt.Errorf("no user named %q", username)
	}
	if err != nil {
		return err
	}
	pw, err := randomPassword()
	if err != nil {
		return err
	}
	hash, err := auth.Hasher{P: auth.DefaultParams}.Hash(pw)
	if err != nil {
		return err
	}
	// keepSessionID 0 matches no session: every session is ended.
	if err := store.UpdatePassword(ctx, u.ID, hash, 0); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Password for %q reset. New password:\n\n    %s\n\nSign in and change it under Settings.\n", u.Username, pw)
	return err
}

// randomPassword returns 4 groups of 5 characters from an alphabet without
// look-alikes (no 0/O, 1/l/I): ~100 bits of entropy, easy to type.
func randomPassword() (string, error) {
	const alphabet = "abcdefghijkmnpqrstuvwxyz23456789"
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	var sb strings.Builder
	for i, c := range b {
		if i > 0 && i%5 == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(alphabet[int(c)%len(alphabet)])
	}
	return sb.String(), nil
}

// disableTOTP turns off two-factor authentication for a user who lost both
// the authenticator and the recovery codes. Shell access to the server is
// the proof of authority here, as for reset-password.
func disableTOTP(getenv func(string) string, username string, out io.Writer) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	database, err := db.Open(filepath.Join(cfg.DataDir, "nulldrive.db"))
	if err != nil {
		return err
	}
	defer database.Close()
	ctx := context.Background()
	if _, err := db.Migrate(ctx, database); err != nil {
		return err
	}
	store := meta.New(database, nil)
	u, err := store.UserByUsername(ctx, username)
	if errors.Is(err, meta.ErrNotFound) {
		return fmt.Errorf("no user named %q", username)
	}
	if err != nil {
		return err
	}
	if err := store.DisableTOTP(ctx, u.ID); err != nil {
		return err
	}
	_ = store.AddAudit(ctx, meta.AuditEvent{Event: meta.AuditTOTPDisabled, UserID: u.ID, Username: u.Username,
		Detail: map[string]string{"by": "command line"}})
	_, err = fmt.Fprintf(out, "Two-factor authentication turned off for %q. Sign in with the password and set it up again in Settings.\n", u.Username)
	return err
}

var usernameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// addUser creates a regular account with a random password. Each account
// has its own files, history and search index; only the setup wizard's
// first account is an administrator.
func addUser(getenv func(string) string, username string, out io.Writer) error {
	if !usernameRE.MatchString(username) {
		return errors.New("username must be 1-64 characters: letters, digits, '.', '_' or '-'")
	}
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	database, err := db.Open(filepath.Join(cfg.DataDir, "nulldrive.db"))
	if err != nil {
		return err
	}
	defer database.Close()
	ctx := context.Background()
	if _, err := db.Migrate(ctx, database); err != nil {
		return err
	}
	store := meta.New(database, nil)
	if done, err := store.SetupComplete(ctx); err != nil {
		return err
	} else if !done {
		return errors.New("finish the setup wizard first (it creates the administrator)")
	}
	pw, err := randomPassword()
	if err != nil {
		return err
	}
	hash, err := auth.Hasher{P: auth.DefaultParams}.Hash(pw)
	if err != nil {
		return err
	}
	u, err := store.CreateUser(ctx, username, hash)
	if err != nil {
		return err
	}
	_ = store.AddAudit(ctx, meta.AuditEvent{Event: meta.AuditUserAdded, UserID: u.ID, Username: u.Username,
		Detail: map[string]string{"by": "command line"}})
	_, err = fmt.Fprintf(out, "User %q created. Password:\n\n    %s\n\nThey can sign in and change it under Settings.\n", u.Username, pw)
	return err
}
