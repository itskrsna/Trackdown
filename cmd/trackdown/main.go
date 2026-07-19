// Command trackdown is Trackdown's single binary: a Sentry-wire-compatible
// error tracking server with its own storage, requiring no external
// services (no Docker, no separate database) to self-host.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/itskrsna/Trackdown/internal/alert"
	"github.com/itskrsna/Trackdown/internal/auth"
	"github.com/itskrsna/Trackdown/internal/config"
	"github.com/itskrsna/Trackdown/internal/ingest"
	"github.com/itskrsna/Trackdown/internal/ratelimit"
	"github.com/itskrsna/Trackdown/internal/server"
	"github.com/itskrsna/Trackdown/internal/store"
)

const (
	shutdownTimeout = 5 * time.Second

	// http.Server timeouts. Unset defaults (Go's zero value = no timeout) are
	// a real slow-loris exposure for a server reachable from the internet —
	// these are sized generously enough for maxEnvelopeSize's 20 MiB cap,
	// not tuned to the byte.
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 60 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 120 * time.Second

	// authFailureRate/authFailureBurst throttle repeated failed Basic Auth
	// attempts per IP — a crude brute-force mitigation. Deliberately much
	// stricter than the ingest rate limit: 5 attempts, refilling one every
	// 12 seconds, is plenty for a legitimate admin who fat-fingers a
	// password, and is a nuisance rather than a shortcut for an attacker.
	authFailureRate  = 1.0 / 12.0
	authFailureBurst = 5
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "serve":
		if err := serve(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	case "gc":
		if err := gc(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	case "backup":
		if err := backup(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: trackdown serve [-addr :8080] [-db trackdown.db] [-log-format text|json] [-insecure-no-auth] [-ingest-rate-limit 20] [-config path.json] [-retention-days 0]")
	fmt.Fprintf(os.Stderr, "  admin credentials come from %s (default \"admin\") and %s (required unless -insecure-no-auth)\n", auth.EnvUser, auth.EnvPassword)
	fmt.Fprintln(os.Stderr, "  -config points to a JSON file configuring SMTP/webhook alerting; omit it to disable alerting")
	fmt.Fprintln(os.Stderr, "  -retention-days > 0 deletes events older than that many days, checked once at startup and then daily; 0 (default) disables retention entirely")
	fmt.Fprintln(os.Stderr, "       trackdown gc -db trackdown.db -retention-days N     one-shot cleanup for external cron/Task Scheduler")
	fmt.Fprintln(os.Stderr, "       trackdown backup -db trackdown.db <dest-path>       consistent point-in-time backup via VACUUM INTO")
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":8080", "HTTP listen address")
	dbPath := fs.String("db", "trackdown.db", "path to the SQLite database file")
	logFormat := fs.String("log-format", "text", "log output format: text or json")
	insecureNoAuth := fs.Bool("insecure-no-auth", false,
		"disable admin authentication entirely (development only -- never use this in production)")
	ingestRateLimit := fs.Float64("ingest-rate-limit", 20,
		"max envelope-ingest requests per second, per client IP (burst allowance is 3x this)")
	configPath := fs.String("config", "", "path to a JSON config file for SMTP/webhook alerting (omit to disable alerting)")
	retentionDays := fs.Int("retention-days", 0,
		"delete events older than N days, checked at startup and then daily (0 = disabled; never deletes data by default)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	logger, err := newLogger(*logFormat)
	if err != nil {
		return err
	}

	var notifier alert.Notifier
	if *configPath != "" {
		cfg, err := config.Load(*configPath)
		if err != nil {
			return fmt.Errorf("loading config: %w", err)
		}
		notifier = cfg.BuildNotifier()
		if notifier == nil {
			logger.Warn("config file has no smtp or webhooks configured; alerting stays disabled", "path", *configPath)
		}
	}

	var wrapManagement func(http.Handler) http.Handler
	if *insecureNoAuth {
		logger.Warn("starting with -insecure-no-auth: the management API and web UI are NOT password protected")
	} else {
		authCfg, err := auth.FromEnv()
		if err != nil {
			return fmt.Errorf("%w (or pass -insecure-no-auth for local development only)", err)
		}
		authMW := auth.New(authCfg)
		authMW.Limiter = ratelimit.New(authFailureRate, authFailureBurst)
		wrapManagement = authMW.Require
	}

	st, err := store.Open(*dbPath)
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer st.Close()

	ingestLimiter := ratelimit.New(*ingestRateLimit, int(*ingestRateLimit*3))

	webUI, err := server.New(st)
	if err != nil {
		return fmt.Errorf("building web UI: %w", err)
	}

	// Ingest mounts directly on the root mux: Handler.Register already
	// applies wrapManagement selectively per-route (envelope ingest is
	// never wrapped; its own management endpoints always are), so it
	// composes safely alongside the web UI without any path collision --
	// their route spaces (/api/... vs. /, /projects/..., /static/...)
	// don't overlap, and Go's mux picks the most specific match regardless
	// of registration order.
	root := http.NewServeMux()
	(&ingest.Handler{Store: st, Logger: logger, RateLimiter: ingestLimiter, Notifier: notifier}).Register(root, wrapManagement)
	root.HandleFunc("GET /healthz", healthzHandler(st))

	// The web UI has no unauthenticated routes of its own -- wrap the whole
	// thing uniformly on its own sub-mux, then mount that at "/" (Go's mux
	// treats registration order across pattern specificities correctly, so
	// ingest's more specific /api/... patterns still win for those paths).
	uiMux := http.NewServeMux()
	webUI.Register(uiMux)
	var uiHandler http.Handler = uiMux
	if wrapManagement != nil {
		uiHandler = wrapManagement(uiMux)
	}
	root.Handle("/", uiHandler)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           root,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if *retentionDays > 0 {
		go runRetentionLoop(ctx, st, logger, time.Duration(*retentionDays)*24*time.Hour)
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("trackdown listening", "addr", *addr, "db", *dbPath)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// runRetentionLoop deletes events older than retention, once immediately
// (so retention takes effect right away rather than only after the first
// tick) and then once every 24 hours until ctx is canceled (at shutdown).
func runRetentionLoop(ctx context.Context, st *store.Store, logger *slog.Logger, retention time.Duration) {
	runOnce := func() {
		deleted, err := st.DeleteOldEvents(ctx, retention)
		if err != nil {
			logger.Error("retention cleanup failed", "error", err)
			return
		}
		if deleted > 0 {
			logger.Info("retention cleanup", "deleted_events", deleted, "retention", retention.String())
		}
	}
	runOnce()

	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce()
		}
	}
}

// gc is a one-shot event-retention cleanup for operators who prefer
// external cron/Task Scheduler triggering over the in-process daily loop
// serve() runs when -retention-days is set.
func gc(args []string) error {
	fs := flag.NewFlagSet("gc", flag.ExitOnError)
	dbPath := fs.String("db", "trackdown.db", "path to the SQLite database file")
	retentionDays := fs.Int("retention-days", 0, "delete events older than N days (required, must be > 0)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *retentionDays <= 0 {
		return fmt.Errorf("-retention-days must be greater than 0")
	}

	st, err := store.Open(*dbPath)
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer st.Close()

	deleted, err := st.DeleteOldEvents(context.Background(), time.Duration(*retentionDays)*24*time.Hour)
	if err != nil {
		return fmt.Errorf("deleting old events: %w", err)
	}
	fmt.Printf("deleted %d event(s) older than %d day(s)\n", deleted, *retentionDays)
	return nil
}

// backup writes a consistent point-in-time snapshot of the database to
// dest, using SQLite's VACUUM INTO -- a single SQL statement that produces
// a valid, self-contained copy without needing to stop the server or risk
// the torn/inconsistent snapshot a raw file copy could produce.
func backup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	dbPath := fs.String("db", "trackdown.db", "path to the SQLite database file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: trackdown backup -db trackdown.db <dest-path>")
	}
	dest := fs.Arg(0)

	st, err := store.Open(*dbPath)
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer st.Close()

	if err := st.BackupTo(context.Background(), dest); err != nil {
		return fmt.Errorf("backing up database: %w", err)
	}
	fmt.Printf("backed up %s to %s\n", *dbPath, dest)
	return nil
}

// newLogger builds the process-wide structured logger. "text" (the default)
// is easiest to read directly in a terminal or a simple log file; "json"
// suits log aggregation pipelines that expect structured records.
func newLogger(format string) (*slog.Logger, error) {
	switch format {
	case "text":
		return slog.New(slog.NewTextHandler(os.Stderr, nil)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(os.Stderr, nil)), nil
	default:
		return nil, fmt.Errorf("invalid -log-format %q: must be text or json", format)
	}
}

// healthzHandler reports whether the store is reachable. Deliberately
// unauthenticated — health checks (load balancers, container orchestrators,
// uptime monitors) shouldn't need credentials to ask "are you alive."
func healthzHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := st.Ping(r.Context()); err != nil {
			http.Error(w, "unhealthy: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}
}
