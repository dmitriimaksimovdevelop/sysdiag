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
	"syscall"
	"time"

	"github.com/dmitriimaksimovdevelop/melisai/internal/shareapi"
)

func main() {
	var (
		addr         = flag.String("addr", ":8080", "HTTP listen address")
		dbPath       = flag.String("db", "/var/lib/melisai-share-api/store.db", "SQLite database path")
		publicBase   = flag.String("public-base", "https://melisai.dev/r", "Viewer base URL embedded in POST responses")
		maxBodyBytes = flag.Int64("max-body-bytes", shareapi.DefaultMaxBodyBytes, "Max upload size in bytes")
		logLevel     = flag.String("log-level", "info", "Log level: debug, info, warn, error")
	)
	flag.Parse()

	logger := newLogger(*logLevel)

	if err := run(*addr, *dbPath, *publicBase, *maxBodyBytes, logger); err != nil {
		logger.Error("server failed", "err", err)
		os.Exit(1)
	}
}

func run(addr, dbPath, publicBase string, maxBodyBytes int64, log *slog.Logger) error {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return fmt.Errorf("create db dir: %w", err)
	}

	store, err := shareapi.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close()

	srv := shareapi.NewServer(store, publicBase, maxBodyBytes, log)

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

	serveErr := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr, "db", dbPath, "public_base", publicBase)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()

	select {
	case <-ctx.Done():
		log.Info("shutdown signal received")
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("listen: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	log.Info("server stopped")
	return nil
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
