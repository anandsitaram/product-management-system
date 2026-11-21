// Command api runs the Product Management System REST API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"pms/db"
	"pms/internal/config"
	"pms/internal/platform/database"
	"pms/internal/platform/logger"
	"pms/internal/platform/metrics"
	"pms/internal/security"
	"pms/internal/server"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	log := logger.New(cfg.LogLevel, cfg.Env)
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if cfg.AutoMigrate {
		applied, err := database.MigrateUp(ctx, pool, db.Migrations, "migrations")
		if err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		if len(applied) > 0 {
			log.Info("migrations applied", "versions", applied)
		}
	}

	store, err := server.NewStorage(ctx, cfg)
	if err != nil {
		return fmt.Errorf("storage: %w", err)
	}

	argon := security.DefaultArgon2
	if err := server.BootstrapAdmin(ctx, pool, cfg, argon, log); err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}

	srv, err := server.New(server.Deps{Cfg: cfg, Log: log, Pool: pool, Store: store, Argon: argon, Metrics: metrics.New()})
	if err != nil {
		return err
	}
	srv.Start(ctx)

	httpSrv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("api listening", "addr", httpSrv.Addr, "env", cfg.Env, "storage", cfg.StorageDriver, "docs", cfg.DocsEnabled)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(sctx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	log.Info("stopped")
	return nil
}

// healthcheck is used by the container HEALTHCHECK (the runtime image has no curl).
func healthcheck() int {
	port := os.Getenv("APP_PORT")
	if port == "" {
		port = "8080"
	}
	c := http.Client{Timeout: 3 * time.Second}
	res, err := c.Get("http://127.0.0.1:" + port + "/health")
	if err != nil || res.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
