// melisai-share-api is the HTTP backend for short share links.
//
// It accepts gzip-compressed melisai report payloads on POST /api/r,
// stores them in a local SQLite database, and serves them back by
// 8-character short code on GET /api/r/{code}. The static viewer page
// at https://melisai.dev/r/{code} fetches the payload and renders it
// client-side.
//
// Designed to run as a sidecar next to the melisai-site nginx pod,
// sharing a PVC for the database file.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/dmitriimaksimovdevelop/melisai/internal/shareapi"
)

func main() {
	var (
		addr             = flag.String("addr", ":8080", "HTTP listen address")
		dbPath           = flag.String("db", "/var/lib/melisai-share-api/store.db", "SQLite database path")
		publicBase       = flag.String("public-base", "https://melisai.dev/r", "Viewer base URL embedded in POST responses")
		maxBodyBytes     = flag.Int64("max-body-bytes", shareapi.DefaultMaxBodyBytes, "Max compressed upload size in bytes")
		maxInflatedBytes = flag.Int64("max-inflated-bytes", shareapi.DefaultMaxInflatedBytes, "Max decompressed payload size in bytes (defends against gzip bombs)")
		logLevel         = flag.String("log-level", "info", "Log level: debug, info, warn, error")
		retention        = flag.Duration("retention", 0, "Delete reports older than this; 0 disables retention (e.g. 2160h for 90 days)")
		cleanupInterval  = flag.Duration("cleanup-interval", time.Hour, "How often the retention sweep runs (ignored when --retention=0)")
		deleteOnStartup  = flag.String("delete-on-startup", "", "Comma-separated codes to delete from the store at startup, then continue normally")
	)
	flag.Parse()

	logger := newLogger(*logLevel)

	if err := shareapi.ValidatePublicBase(*publicBase); err != nil {
		logger.Error("invalid --public-base", "err", err)
		os.Exit(2)
	}

	if err := run(*addr, *dbPath, *publicBase, *maxBodyBytes, *maxInflatedBytes, *retention, *cleanupInterval, *deleteOnStartup, logger); err != nil {
		logger.Error("server failed", "err", err)
		os.Exit(1)
	}
}

func run(addr, dbPath, publicBase string, maxBodyBytes, maxInflatedBytes int64, retention, cleanupInterval time.Duration, deleteOnStartup string, log *slog.Logger) error {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return fmt.Errorf("create db dir: %w", err)
	}

	store, err := shareapi.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() {
		if cerr := store.Close(); cerr != nil {
			log.Error("store close", "err", cerr)
		}
	}()

	if deleteOnStartup != "" {
		if err := startupDelete(context.Background(), store, deleteOnStartup, log); err != nil {
			return fmt.Errorf("delete-on-startup: %w", err)
		}
	}

	srv, err := shareapi.NewServer(store, publicBase, maxBodyBytes, log)
	if err != nil {
		return fmt.Errorf("init server: %w", err)
	}
	srv = srv.WithMaxInflatedBytes(maxInflatedBytes)

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if retention > 0 {
		go runRetention(ctx, store, retention, cleanupInterval, log)
	}

	// Buffered so the listen goroutine never blocks on send; we drain
	// in the shutdown branch below to surface a late ListenAndServe
	// error that races with the signal.
	serveErr := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr, "db", dbPath, "public_base", publicBase, "retention", retention)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	select {
	case <-ctx.Done():
		log.Info("shutdown signal received")
	case err := <-serveErr:
		return fmt.Errorf("listen: %w", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	// Drain any error that the listen goroutine produced after we
	// already lost the select race.
	select {
	case err := <-serveErr:
		log.Error("late listen error", "err", err)
	default:
	}

	log.Info("server stopped")
	return nil
}

// startupDelete removes the given comma-separated codes from the store
// once before the HTTP server starts accepting traffic. Used to take
// down malicious or accidentally-shared payloads without invoking
// kubectl exec — set --delete-on-startup=Xxxx,Yyyy on the next deploy,
// observe the log line, then remove the flag.
//
// Codes are validated against the same shape as GenerateCode produces,
// so an operator typo cannot end up logging arbitrary control bytes
// or silently no-op'ing a delete against a malformed code.
func startupDelete(ctx context.Context, store *shareapi.Store, codes string, log *slog.Logger) error {
	for _, code := range strings.Split(codes, ",") {
		code = strings.TrimSpace(code)
		if code == "" {
			continue
		}
		if !shareapi.ValidCode(code) {
			return fmt.Errorf("invalid code %q: must be %d base62 characters", code, shareapi.CodeLength)
		}
		n, err := store.DeleteByCode(ctx, code)
		if err != nil {
			return fmt.Errorf("delete %q: %w", code, err)
		}
		log.Info("delete-on-startup", "code", code, "rows", n)
	}
	return nil
}

// runRetention drops rows older than `age` on the given interval until
// ctx is cancelled. The first sweep fires immediately on startup to
// shrink any backlog from a previous run with longer retention.
func runRetention(ctx context.Context, store *shareapi.Store, age, interval time.Duration, log *slog.Logger) {
	sweep := func() {
		cutoff := time.Now().Add(-age)
		deleted, err := store.DeleteOlderThan(ctx, cutoff)
		if err != nil {
			log.Error("retention sweep", "err", err)
			return
		}
		if deleted > 0 {
			log.Info("retention sweep", "deleted", deleted, "cutoff", cutoff)
		}
	}
	sweep()

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sweep()
		}
	}
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
