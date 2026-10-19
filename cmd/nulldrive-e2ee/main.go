// Command nulldrive-e2ee manages end-to-end encryption for a Null Drive
// account from a terminal: turn it on (and encrypt existing files), check
// the passphrase, change it, or replace the recovery key.
//
// It runs on *your* machine and talks to the server like the desktop app
// does; the passphrase, recovery key and master key never leave it. Use it
// on headless setups, or when the account holds too much data to migrate
// comfortably from a browser tab.
//
//	nulldrive-e2ee -server https://drive.example.com -user alice enable
//	nulldrive-e2ee -server ... -user alice migrate     # resume an interrupted migration
//	nulldrive-e2ee -server ... -user alice status
//	nulldrive-e2ee -server ... -user alice verify
//	nulldrive-e2ee -server ... -user alice passphrase
//	nulldrive-e2ee -server ... -user alice recovery-key
//
// Secrets are prompted for without echo. For scripts (and the smoke test)
// they can come from NULLDRIVE_PASSWORD, NULLDRIVE_TOTP and
// NULLDRIVE_E2EE_PASSPHRASE instead.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"golang.org/x/term"

	"github.com/Null0Grid/null-drive/shared/api"
	"github.com/Null0Grid/null-drive/shared/client"
	"github.com/Null0Grid/null-drive/shared/e2ee"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

var stdin = bufio.NewReader(os.Stdin)

func prompt(label, env string) (string, error) {
	if v := os.Getenv(env); v != "" {
		return v, nil
	}
	fmt.Fprint(os.Stderr, label)
	if term.IsTerminal(int(os.Stdin.Fd())) { //nolint:gosec // fd fits in int
		b, err := term.ReadPassword(int(os.Stdin.Fd())) //nolint:gosec // fd fits in int
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	s, err := stdin.ReadString('\n')
	return strings.TrimRight(s, "\r\n"), err
}

func newPassphrase() (string, error) {
	if v := os.Getenv("NULLDRIVE_E2EE_PASSPHRASE"); v != "" {
		return v, e2ee.CheckPassphrase(v)
	}
	p1, err := prompt(fmt.Sprintf("New encryption passphrase (at least %d characters, NOT your login password): ", e2ee.MinPassphraseRunes), "")
	if err != nil {
		return "", err
	}
	if err := e2ee.CheckPassphrase(p1); err != nil {
		return "", err
	}
	p2, err := prompt("Repeat it: ", "")
	if err != nil {
		return "", err
	}
	if p1 != p2 {
		return "", errors.New("the passphrases don't match")
	}
	return p1, nil
}

func run() error {
	server := flag.String("server", os.Getenv("NULLDRIVE_SERVER"), "server URL (or NULLDRIVE_SERVER)")
	user := flag.String("user", os.Getenv("NULLDRIVE_USER"), "username (or NULLDRIVE_USER)")
	tmp := flag.String("tmp", os.TempDir(), "directory for one file at a time during migration")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: nulldrive-e2ee -server URL -user NAME <status|enable|migrate|verify|passphrase|recovery-key>")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 || *server == "" || *user == "" {
		flag.Usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	base, err := client.NormalizeURL(*server)
	if err != nil {
		return err
	}
	pw, err := prompt("Login password for "+*user+": ", "NULLDRIVE_PASSWORD")
	if err != nil {
		return err
	}
	anon := client.New(base, "", nil)
	anon.UserAgent = "nulldrive-e2ee"
	sess, err := anon.LoginTOTP(ctx, *user, pw, os.Getenv("NULLDRIVE_TOTP"))
	if client.IsCode(err, "totp_required") {
		code, perr := prompt("Authenticator code: ", "NULLDRIVE_TOTP")
		if perr != nil {
			return perr
		}
		sess, err = anon.LoginTOTP(ctx, *user, pw, code)
	}
	if err != nil {
		return err
	}
	c := client.New(base, sess.Token, nil)
	c.UserAgent = "nulldrive-e2ee"
	c.E2EE = true
	defer func() { _ = c.Logout(context.WithoutCancel(ctx)) }()

	switch cmd := flag.Arg(0); cmd {
	case "status":
		st, err := c.E2EEStatus(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("state: %s\n", st.State)
		if st.State != api.E2EEOff {
			fmt.Printf("key fingerprint: %s\nturned on: %s\n", st.Fingerprint, st.EnabledAt.Local().Format("2006-01-02 15:04"))
		}
		if st.State == api.E2EEMigrating {
			fmt.Printf("still unencrypted: %d files and folders (run: migrate)\n", st.PlaintextFiles)
		}
		return nil

	case "enable":
		fmt.Fprintln(os.Stderr, "End-to-end encryption: files and names are encrypted on your devices; the server")
		fmt.Fprintln(os.Stderr, "can't read them. If you lose BOTH the passphrase and the recovery key, your files")
		fmt.Fprintln(os.Stderr, "can't be recovered by anyone, including the server's administrator.")
		pass, err := newPassphrase()
		if err != nil {
			return err
		}
		keys, rk, st, err := e2ee.TurnOn(ctx, c, pass, e2ee.DefaultKDF())
		if err != nil {
			return err
		}
		fmt.Println()
		fmt.Println("RECOVERY KEY (write it down or print it, and keep it somewhere safe):")
		fmt.Println()
		fmt.Println("    " + rk)
		fmt.Println()
		if st.State == api.E2EEMigrating {
			return migrate(ctx, c, keys, *tmp)
		}
		fmt.Println("Encryption is on.")
		return nil

	case "migrate":
		keys, err := unlock(ctx, c)
		if err != nil {
			return err
		}
		return migrate(ctx, c, keys, *tmp)

	case "verify":
		keys, err := unlock(ctx, c)
		if err != nil {
			return err
		}
		fmt.Printf("OK: that unlocks key %s\n", keys.Fingerprint())
		return nil

	case "passphrase":
		keys, err := unlock(ctx, c)
		if err != nil {
			return err
		}
		os.Unsetenv("NULLDRIVE_E2EE_PASSPHRASE") //nolint:errcheck,gosec // force a prompt for the new one
		pass, err := newPassphrase()
		if err != nil {
			return err
		}
		if err := e2ee.ChangePassphrase(ctx, c, keys, pass, e2ee.DefaultKDF()); err != nil {
			return err
		}
		fmt.Println("Passphrase changed. Your recovery key still works.")
		return nil

	case "recovery-key":
		keys, err := unlock(ctx, c)
		if err != nil {
			return err
		}
		st, err := c.E2EEStatus(ctx)
		if err != nil {
			return err
		}
		b, err := e2ee.ParseBundle(st)
		if err != nil {
			return err
		}
		nb, rk, err := b.WithNewRecoveryKey(keys)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(nb)
		if _, err := c.UpdateE2EEBundle(ctx, api.E2EEBundleRequest{AuthKey: keys.AuthKey(), Bundle: raw, Fingerprint: nb.Fingerprint}); err != nil {
			return err
		}
		fmt.Println("NEW RECOVERY KEY (the old one no longer works):")
		fmt.Println()
		fmt.Println("    " + rk)
		return nil

	default:
		flag.Usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func unlock(ctx context.Context, c *client.Client) (*e2ee.Keys, error) {
	secret, err := prompt("Encryption passphrase (or recovery key): ", "NULLDRIVE_E2EE_PASSPHRASE")
	if err != nil {
		return nil, err
	}
	// A recovery key has a fixed shape; anything else is a passphrase.
	if _, perr := e2ee.ParseRecoveryKey(secret); perr == nil {
		keys, _, err := e2ee.Unlock(ctx, c, secret, true)
		return keys, err
	}
	keys, _, err := e2ee.Unlock(ctx, c, secret, false)
	return keys, err
}

func migrate(ctx context.Context, c *client.Client, keys *e2ee.Keys, tmp string) error {
	fmt.Println("Encrypting existing files (safe to interrupt; run 'migrate' to continue)...")
	rep, err := e2ee.Migrate(ctx, c, keys, tmp, func(p e2ee.MigrateProgress) {
		if p.Total > 0 && p.Path != "" {
			fmt.Fprintf(os.Stderr, "\r[%d/%d] %-60.60s", p.Done+1, p.Total, p.Path)
		}
	})
	fmt.Fprintln(os.Stderr)
	if errors.Is(err, e2ee.ErrNamesTooLong) {
		for _, p := range rep.TooLong {
			fmt.Fprintln(os.Stderr, "  name too long:", p)
		}
	}
	if err != nil {
		return err
	}
	fmt.Printf("Done: %d files, %d folders encrypted; %d unencrypted items and %d chunks deleted from the server.\n",
		rep.Files, rep.Folders, rep.Finish.PurgedPaths, rep.Finish.ChunksDeleted)
	return nil
}
