package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/nulldrive/nulldrive/shared/api"
	"github.com/nulldrive/nulldrive/shared/client"
	"github.com/nulldrive/nulldrive/shared/e2ee"
	"github.com/nulldrive/nulldrive/shared/pathutil"
)

// Opening nulldrive://open?id=<id> links (Null Search results).
//
// The id is resolved by the server, for the account this computer syncs,
// into the file's current path; the file is then opened from the sync
// folder (programs and scripts are shown in their folder instead). If it
// isn't there (not synced yet, deleted, another account's file, server
// unreachable) the user is told why and offered the web app, which can
// open the same link (/open?id=) after signing in.

// How long to wait for a file that exists on the server but hasn't been
// downloaded yet (a link to something just uploaded elsewhere).
var linkSyncWait = 20 * time.Second

// queueLink handles a link once the app is ready (the window exists, so
// dialogs can be shown); links that arrive earlier wait.
func (a *App) queueLink(raw string) {
	a.mu.Lock()
	if !a.ready {
		a.pendingLink = raw
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()
	a.handleLink(raw)
}

// domReady runs once the window has loaded: links that came with the
// launch are handled now.
func (a *App) domReady(context.Context) {
	a.mu.Lock()
	a.ready = true
	raw := a.pendingLink
	a.pendingLink = ""
	a.mu.Unlock()
	if raw != "" {
		a.handleLink(raw)
	}
}

func (a *App) handleLink(raw string) {
	l, err := parseLink(raw)
	if err != nil {
		a.log.Warn("ignored a link", "err", err)
		return
	}
	// One at a time: a page can't queue up a burst of launches.
	if !a.linkBusy.CompareAndSwap(false, true) {
		a.log.Info("link ignored: another one is being opened")
		return
	}
	go func() {
		defer a.linkBusy.Store(false)
		a.openByID(l.ID)
	}()
}

// linkOutcome is what opening a link did, for logs and tests.
type linkOutcome string

const (
	linkOpened    linkOutcome = "opened"
	linkRevealed  linkOutcome = "revealed"
	linkNotSetUp  linkOutcome = "not_set_up"
	linkLocked    linkOutcome = "locked"
	linkNotFound  linkOutcome = "not_found"
	linkDeleted   linkOutcome = "deleted"
	linkNotSynced linkOutcome = "not_synced"
	linkFailed    linkOutcome = "failed"
)

func (a *App) openByID(id string) {
	a.mu.Lock()
	cfg, remote, keys := a.cfg, a.remote, a.keys
	a.mu.Unlock()
	env := linkEnv{
		folder: cfg.Folder, configured: cfg.Configured(), decrypt: decryptFunc(keys),
		syncNow: a.SyncNow, wait: linkSyncWait,
		open: shellOpen, reveal: shellReveal,
	}
	if remote != nil { // (a nil *client.Client in the interface would not be nil)
		env.remote = remote
	}
	res := resolveLink(context.Background(), env, id)
	a.log.Info("link", "id", id, "outcome", res.outcome, "path", res.path, "err", res.err)

	server := cfg.ServerURL
	switch res.outcome {
	case linkOpened, linkRevealed:
		return
	case linkNotSetUp:
		a.Show()
		a.info("Set up Null Drive first", "This computer doesn't sync a Null Drive account yet. Set it up, then open the link again.")
	case linkLocked:
		a.Show()
		a.info("Null Drive is locked", "Unlock end-to-end encryption on this computer, then open the link again.")
	case linkNotFound:
		a.offerWeb(server, id, "File not found",
			fmt.Sprintf("This file isn't in the Null Drive account this computer syncs (%s on %s). It may belong to another account.\n\nOpen it in the web app instead?",
				cfg.Username, hostOf(server)))
	case linkDeleted:
		a.offerWeb(server, id, "File was deleted",
			fmt.Sprintf("“%s” was deleted. You can restore it from Recently deleted in the web app.\n\nOpen the web app?", path.Base(res.path)))
	case linkNotSynced:
		a.offerWeb(server, id, "Not on this computer yet",
			fmt.Sprintf("“%s” hasn't finished syncing to this computer.\n\nOpen it in the web app instead?", path.Base(res.path)))
	default:
		msg := "Null Drive couldn't open this link."
		if res.err != nil {
			msg += "\n\n" + res.err.Error()
		}
		a.offerWeb(server, id, "Couldn't open the file", msg+"\n\nTry the web app instead?")
	}
}

// decryptFunc turns an encrypted server path into the plain one (nil for
// plain accounts).
func decryptFunc(k *e2ee.Keys) func(string) (string, error) {
	if k == nil {
		return nil
	}
	return k.DecryptPath
}

func hostOf(u string) string {
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Host
	}
	return u
}

func (a *App) info(title, msg string) {
	if a.ctx == nil {
		return
	}
	_, _ = wruntime.MessageDialog(a.ctx, wruntime.MessageDialogOptions{Type: wruntime.InfoDialog, Title: title, Message: msg})
}

// offerWeb asks whether to open the link in the web app (Yes/No: Windows
// message boxes can't have custom button labels).
func (a *App) offerWeb(server, id, title, msg string) {
	if a.ctx == nil {
		return
	}
	a.Show()
	if server == "" {
		a.info(title, msg)
		return
	}
	choice, err := wruntime.MessageDialog(a.ctx, wruntime.MessageDialogOptions{
		Type: wruntime.QuestionDialog, Title: title, Message: msg,
		Buttons: []string{"Yes", "No"}, DefaultButton: "Yes", CancelButton: "No",
	})
	if err == nil && choice == "Yes" {
		wruntime.BrowserOpenURL(a.ctx, webOpenURL(server, id))
	}
}

// webOpenURL is the web app's page for a link (the id is validated hex).
func webOpenURL(server, id string) string {
	return strings.TrimRight(server, "/") + "/open?id=" + url.QueryEscape(id)
}

// ---- resolving (separate from the UI, so it can be tested) ----

type linkEnv struct {
	folder     string
	configured bool
	remote     interface {
		FileByID(context.Context, string) (api.File, error)
	}
	decrypt func(string) (string, error) // server path → plain path (E2EE), or nil
	syncNow func()
	wait    time.Duration
	open    func(string) error
	reveal  func(string) error
}

type linkResult struct {
	outcome linkOutcome
	path    string // server path (plain)
	local   string
	err     error
}

func resolveLink(ctx context.Context, env linkEnv, id string) linkResult {
	if !env.configured || env.folder == "" {
		return linkResult{outcome: linkNotSetUp}
	}
	if env.remote == nil {
		return linkResult{outcome: linkLocked}
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	f, err := env.remote.FileByID(cctx, id)
	cancel()
	switch {
	case client.IsStatus(err, 404):
		return linkResult{outcome: linkNotFound, err: err}
	case err != nil:
		return linkResult{outcome: linkFailed, err: err}
	}
	p := f.Path
	if env.decrypt != nil {
		if p, err = env.decrypt(p); err != nil {
			return linkResult{outcome: linkFailed, err: fmt.Errorf("decrypt the file name: %w", err)}
		}
	}
	clean, err := pathutil.Clean(p)
	if err != nil {
		return linkResult{outcome: linkFailed, err: err}
	}
	if f.Deleted {
		return linkResult{outcome: linkDeleted, path: clean}
	}
	local := filepath.Join(env.folder, filepath.FromSlash(clean))
	st, err := os.Lstat(local)
	if errors.Is(err, fs.ErrNotExist) && env.wait > 0 {
		// On the server but not here yet: sync now and give it a moment.
		if env.syncNow != nil {
			env.syncNow()
		}
		deadline := time.Now().Add(env.wait)
		for errors.Is(err, fs.ErrNotExist) && time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return linkResult{outcome: linkFailed, path: clean, err: ctx.Err()}
			case <-time.After(250 * time.Millisecond):
			}
			st, err = os.Lstat(local)
		}
	}
	if errors.Is(err, fs.ErrNotExist) {
		return linkResult{outcome: linkNotSynced, path: clean, local: local}
	}
	if err != nil {
		return linkResult{outcome: linkFailed, path: clean, local: local, err: err}
	}
	res := linkResult{path: clean, local: local}
	switch {
	case st.IsDir():
		res.outcome, res.err = linkOpened, env.open(local)
	case !st.Mode().IsRegular() || isDangerous(st.Name()) || (runtime.GOOS != "windows" && st.Mode()&0o111 != 0):
		// Programs, scripts, shortcuts, links, devices: shown, never run.
		res.outcome, res.err = linkRevealed, env.reveal(local)
	default:
		res.outcome, res.err = linkOpened, env.open(local)
	}
	if res.err != nil {
		res.outcome = linkFailed
	}
	return res
}
