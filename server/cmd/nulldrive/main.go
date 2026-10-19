// Command nulldrive runs the Null Drive server.
//
//	nulldrive                         run the server (default)
//	nulldrive healthcheck             exit 0 if the local server is healthy (Docker HEALTHCHECK)
//	nulldrive version                 print version and commit
//	nulldrive reset-password <user>   set a new random password for <user>
//	nulldrive add-user <user>         create another account (random password)
//	nulldrive disable-2fa <user>      turn off two-factor authentication for <user>
//
// The subcommands live in the same binary because the container image is
// distroless: there is no shell, curl or sqlite3 inside it.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/nulldrive/nulldrive/server/internal/api"
	"github.com/nulldrive/nulldrive/server/internal/auth"
	"github.com/nulldrive/nulldrive/server/internal/buildinfo"
	"github.com/nulldrive/nulldrive/server/internal/config"
	"github.com/nulldrive/nulldrive/server/internal/db"
	"github.com/nulldrive/nulldrive/server/internal/discovery"
	"github.com/nulldrive/nulldrive/server/internal/meta"
	"github.com/nulldrive/nulldrive/server/internal/realtime"
	"github.com/nulldrive/nulldrive/server/internal/webui"
	"github.com/nulldrive/nulldrive/server/storage"
)

func main() {
	if err := dispatch(os.Args[1:], os.Stdout, os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, "nulldrive:", err)
		os.Exit(1)
	}
}

func dispatch(args []string, stdout io.Writer, getenv func(string) string) error {
	if len(args) == 0 || args[0] == "serve" {
		return serve(getenv)
	}
	switch args[0] {
	case "healthcheck":
		cfg, err := config.Load(getenv)
		if err != nil {
			return err
		}
		return healthcheck(fmt.Sprintf("http://127.0.0.1:%d/api/v1/health", cfg.Port))
	case "version", "--version", "-v":
		_, err := fmt.Fprintf(stdout, "nulldrive %s (commit %s)\n", buildinfo.Version, buildinfo.Commit)
		return err
	case "add-user":
		if len(args) != 2 {
			return errors.New("usage: nulldrive add-user <username>")
		}
		return addUser(getenv, args[1], stdout)
	case "disable-2fa":
		if len(args) != 2 {
			return errors.New("usage: nulldrive disable-2fa <username>")
		}
		return disableTOTP(getenv, args[1], stdout)
	case "reset-password":
		if len(args) != 2 {
			return errors.New("usage: nulldrive reset-password <username>")
		}
		return resetPassword(getenv, args[1], stdout)
	case "help", "--help", "-h":
		_, err := fmt.Fprint(stdout, usage)
		return err
	default:
		return fmt.Errorf("unknown command %q\n\n%s", args[0], usage)
	}
}

const usage = `Usage:
  nulldrive                         run the server
  nulldrive healthcheck             check the local server (exit status 0 = healthy)
  nulldrive version                 print version information
  nulldrive reset-password <user>   set a new random password and sign the user out everywhere
  nulldrive add-user <user>         create another account with its own files (prints a password)
  nulldrive disable-2fa <user>      turn off two-factor authentication (lost phone and recovery codes)

Configuration (environment):
  NULLDRIVE_DATA_DIR   data directory (default ./data)
  NULLDRIVE_PORT       HTTP port (default 8080)
  NULLDRIVE_LOG_LEVEL  debug | info | warn | error (default info)
  NULLDRIVE_TRUST_PROXY     on: client IP from X-Forwarded-For (only behind a proxy)
  NULLDRIVE_MDNS            off: disable LAN discovery regardless of the UI setting
  NULLDRIVE_ADVERTISE_ADDR  comma-separated IPs to announce instead of auto-detected ones
  NULLDRIVE_ADVERTISE_PORT  port to announce (default NULLDRIVE_PORT; for port mappings)
  NULLDRIVE_GC_INTERVAL     how often old versions are pruned and unused chunks deleted (default 1h)
  NULLDRIVE_SEARCH_ORIGINS  web origins allowed to call GET /search, comma-separated, or off
                            (default https://search.nullgrid.dev, the Null Search app)
`

func serve(getenv func(string) string) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	if err := ensureWritable(cfg.DataDir); err != nil {
		return err
	}
	database, err := db.Open(filepath.Join(cfg.DataDir, "nulldrive.db"))
	if err != nil {
		return err
	}
	defer database.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	schema, err := db.Migrate(ctx, database)
	if err != nil {
		return err
	}
	blobs, err := storage.NewDisk(cfg.DataDir)
	if err != nil {
		return err
	}
	ui := webui.New()
	if !ui.Built() {
		logger.Warn("web UI not embedded; serving API only (build with `make build`)")
	}
	hub := realtime.NewHub(logger)
	adv := &discovery.Advertiser{
		Port:     uint16(cfg.AdvertisePort), //nolint:gosec // validated 1-65535 by config.Load
		Override: cfg.AdvertiseAddrs, Log: logger,
	}
	srv, err := api.New(api.Options{
		Store:              meta.New(database, nil),
		Blobs:              blobs,
		Hasher:             auth.Hasher{P: auth.DefaultParams},
		Logger:             logger,
		DataDir:            cfg.DataDir,
		UI:                 ui,
		Hub:                hub,
		TrustProxy:         cfg.TrustProxy,
		Discovery:          adv,
		DiscoveryForcedOff: !cfg.MDNS,
		SearchDir:          filepath.Join(cfg.DataDir, "indexes"),
		SearchOrigins:      cfg.SearchOrigins,
	})
	if err != nil {
		return err
	}
	srv.RefreshDiscovery(ctx)
	// Retention + garbage collection in the background (also on demand:
	// POST /api/v2/admin/gc).
	go srv.Chunks().Run(ctx, cfg.GCInterval, api.DefaultGCGrace)
	defer adv.Stop() // goodbye packets: clients drop us from their lists at once

	httpSrv := &http.Server{
		Addr:    cfg.Addr(),
		Handler: srv,
		// No ReadTimeout/WriteTimeout: they cap whole request/response
		// time and would kill large uploads and downloads. Slowloris-style
		// header dribbling is bounded by ReadHeaderTimeout instead.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	// Shutdown does not wait for hijacked (WebSocket) connections; close
	// them with "going away" so clients reconnect to the new process.
	httpSrv.RegisterOnShutdown(hub.Shutdown)

	errc := make(chan error, 1)
	go func() {
		logger.Info("listening",
			"addr", cfg.Addr(), "data_dir", cfg.DataDir, "schema_version", schema,
			"version", buildinfo.Version, "commit", buildinfo.Commit)
		errc <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	// Graceful shutdown: stop accepting, let in-flight requests (including
	// uploads mid-transfer) finish for up to 30s, then close the DB.
	// `docker stop` waits 10s by default before SIGKILL; compose and the
	// templates set stop_grace_period to 30s to match.
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	if err := srv.Close(); err != nil {
		logger.Warn("close search indexes", "err", err)
	}
	logger.Info("stopped")
	return nil
}

// ensureWritable creates the data dir and proves we can write to it, so a
// permissions problem (typical with Docker bind mounts) fails at startup
// with an actionable message instead of on the first upload.
func ensureWritable(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create data dir %s: %w%s", dir, err, permissionHint(err, dir))
	}
	probe, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return fmt.Errorf("data dir %s is not writable: %w%s", dir, err, permissionHint(err, dir))
	}
	name := probe.Name()
	_ = probe.Close()
	return os.Remove(name)
}

func permissionHint(err error, dir string) string {
	if !errors.Is(err, os.ErrPermission) {
		return ""
	}
	return fmt.Sprintf("\n\nThe server runs as user id %d. Make the data directory writable for it, e.g.\n"+
		"  sudo chown -R %d:%d <host folder mounted at %s>\n"+
		"or run the container as the folder's owner (docker run --user <uid>:<gid> ...).",
		os.Getuid(), os.Getuid(), os.Getgid(), dir)
}

// healthcheck is used by the Docker HEALTHCHECK. Distroless images have no
// curl/wget, so the binary checks itself.
func healthcheck(url string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	res, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("unhealthy: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("unhealthy: %s returned %d", url, res.StatusCode)
	}
	return nil
}
