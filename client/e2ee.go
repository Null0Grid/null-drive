package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/Null0Grid/null-drive/shared/api"
	"github.com/Null0Grid/null-drive/shared/client"
	"github.com/Null0Grid/null-drive/shared/e2ee"
	nsync "github.com/Null0Grid/null-drive/shared/sync"
)

// End-to-end encryption on the desktop (Phase 7).
//
// The master key is unwrapped once, with the passphrase or recovery key the
// user types into the app, and then kept in the OS keychain next to the
// device token, so syncing survives restarts without asking again. That is
// the same protection the synced files themselves have on this computer:
// they sit decrypted in the sync folder anyway. Signing out removes it.

// E2EE states shown by the UI.
const (
	e2eeOff       = "off"       // plain account
	e2eeUnlocked  = "unlocked"  // syncing encrypted
	e2eeLocked    = "locked"    // encrypted account, no key on this computer yet
	e2eeMigrating = "migrating" // encryption being turned on elsewhere; sync waits
)

var (
	errE2EELocked    = errors.New("this account uses end-to-end encryption: enter your encryption passphrase to start syncing")
	errE2EEMigrating = errors.New("end-to-end encryption is being turned on from another device; syncing resumes when it has finished")
)

func keyAccount(c Config) string { return keychainAccount(c) + "|e2ee" }

func saveMasterKey(c Config, k *e2ee.Keys) error {
	return keyring.Set(keychainService, keyAccount(c), hex.EncodeToString(k.Master()))
}

func loadMasterKey(c Config) (*e2ee.Keys, error) {
	s, err := keyring.Get(keychainService, keyAccount(c))
	if err != nil {
		return nil, err
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, err
	}
	return e2ee.FromMaster(b)
}

func deleteMasterKey(c Config) {
	_ = keyring.Delete(keychainService, keyAccount(c)) // absent is fine
}

// resolveKeys decides how this account syncs: plain (nil keys), encrypted
// with the key from the keychain, or not at all yet (locked/migrating).
// The fingerprint recorded in the config lets it keep working offline.
func (a *App) resolveKeys(ctx context.Context, cfg Config, c *client.Client) (*e2ee.Keys, string, error) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	st, err := c.E2EEStatus(cctx)
	if err != nil {
		if client.IsStatus(err, 404) {
			return nil, e2eeOff, nil // server older than Phase 7
		}
		// Offline: go with what we knew. The agent retries the network.
		if cfg.E2EEFingerprint == "" {
			return nil, e2eeOff, nil
		}
		k, kerr := loadMasterKey(cfg)
		if kerr != nil || k.Fingerprint() != cfg.E2EEFingerprint {
			return nil, e2eeLocked, errE2EELocked
		}
		return k, e2eeUnlocked, nil
	}
	switch st.State {
	case api.E2EEOff:
		return nil, e2eeOff, nil
	case api.E2EEMigrating:
		return nil, e2eeMigrating, errE2EEMigrating
	}
	k, err := loadMasterKey(cfg)
	if err != nil || k.Fingerprint() != st.Fingerprint {
		return nil, e2eeLocked, errE2EELocked
	}
	return k, e2eeUnlocked, nil
}

// applyKeys switches an engine to encrypted mode and resets the index if
// the key changed (see Index.UseKeyFingerprint).
func applyKeys(ctx context.Context, eng *nsync.Engine, plain *client.Client, k *e2ee.Keys) (bool, error) {
	fp := ""
	if k != nil {
		eng.Remote = e2ee.NewRemote(plain, k)
		eng.ContentHash = k.FileHash
		fp = k.Fingerprint()
	}
	return eng.Index.UseKeyFingerprint(ctx, fp)
}

// UnlockE2EE (bound): unwrap the account key with the passphrase, or the
// recovery key when recovery is true, store it in the keychain and start
// syncing.
func (a *App) UnlockE2EE(secret string, recovery bool) error {
	a.mu.Lock()
	cfg := a.cfg
	a.mu.Unlock()
	token, err := loadToken(cfg)
	if err != nil {
		return err
	}
	c := client.NewPinned(cfg.ServerURL, token, cfg.CertSHA256)
	c.UserAgent = userAgent(a.version)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	k, _, err := e2ee.Unlock(ctx, c, strings.TrimSpace(secret), recovery)
	if errors.Is(err, e2ee.ErrWrongKey) {
		if recovery {
			return errors.New("that recovery key doesn't match")
		}
		return errors.New("that passphrase doesn't match")
	}
	if err != nil {
		return friendlyErr(err)
	}
	if err := saveMasterKey(cfg, k); err != nil {
		return fmt.Errorf("could not save the key to the system keychain: %w", err)
	}
	cfg.E2EEFingerprint = k.Fingerprint()
	if err := saveConfig(cfg); err != nil {
		return err
	}
	a.mu.Lock()
	a.cfg = cfg
	a.mu.Unlock()
	a.log.Info("end-to-end encryption unlocked", "fingerprint", cfg.E2EEFingerprint)
	a.stopSync()
	return a.startSync()
}

// RetrySync (bound) re-checks the account (after encryption was turned on
// or finished elsewhere) and restarts syncing.
func (a *App) RetrySync() error {
	a.stopSync()
	return a.startSync()
}

func (a *App) setE2EEState(s string) {
	a.mu.Lock()
	a.e2eeState = s
	a.mu.Unlock()
}

// watchForE2EE: if the account is switched to encryption while this
// computer syncs in plaintext, the server answers e2ee_client_required;
// stop and ask for the passphrase instead of retrying forever.
func (a *App) watchForE2EE(s nsync.Status) {
	if s.State != nsync.StateError || !strings.Contains(s.Message, "e2ee_client_required") {
		return
	}
	a.mu.Lock()
	state := a.e2eeState
	a.mu.Unlock()
	if state != e2eeOff {
		return
	}
	go func() {
		if err := a.RetrySync(); err != nil {
			a.log.Info("account switched to end-to-end encryption", "err", err)
		}
	}()
}
