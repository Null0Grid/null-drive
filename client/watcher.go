package main

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// watch triggers a sync shortly after local changes. Events are only a
// hint: they are debounced (a burst of saves or a big copy causes one
// sync, not hundreds), and the engine always rescans the folder, so a
// missed or overflowed event is harmless. The agent's periodic cycle is
// the safety net.
func watch(ctx context.Context, root string, debounce time.Duration, trigger func(), log *slog.Logger) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	// fsnotify is not recursive on every platform: add each folder.
	addTree := func(dir string) {
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				if werr := w.Add(p); werr != nil {
					log.Debug("watch add failed", "path", p, "err", werr)
				}
			}
			return nil
		})
	}
	addTree(root)

	go func() {
		defer w.Close()
		var timer *time.Timer
		fire := make(chan struct{}, 1)
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if strings.HasSuffix(ev.Name, ".nulldrive-part") {
					continue // our own in-progress downloads
				}
				if ev.Has(fsnotify.Create) {
					if st, err := os.Stat(ev.Name); err == nil && st.IsDir() {
						addTree(ev.Name)
					}
				}
				if timer == nil {
					timer = time.AfterFunc(debounce, func() {
						select {
						case fire <- struct{}{}:
						default:
						}
					})
				} else {
					timer.Reset(debounce)
				}
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				// Typically an event queue overflow: just rescan.
				log.Warn("file watcher error", "err", err)
				trigger()
			case <-fire:
				trigger()
			}
		}
	}()
	return nil
}
