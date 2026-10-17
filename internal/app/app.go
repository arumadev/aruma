// Package app wires the server together from its configuration.
package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/osama300/aurma-project/internal/config"
	"github.com/osama300/aurma-project/internal/platform/metrics"
	"github.com/osama300/aurma-project/internal/server"
	"github.com/osama300/aurma-project/internal/store"
)

// Serve runs the server until ctx is cancelled. It refuses to start on an outdated schema,
// and never migrates it itself (ADR-009).
func Serve(ctx context.Context, cfg *config.Config, logger *slog.Logger, version string) error {
	pool, err := store.Open(ctx, cfg.DatabaseURL.Reveal())
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := store.CheckCurrent(ctx, pool); err != nil {
		return err
	}
	if !cfg.SecureCookies() {
		logger.Warn("public_url is plain http on a non-loopback host: console cookies will not be Secure; put the server behind https")
	}

	m := metrics.New(version)
	srv := server.New(cfg, logger, pool, m, server.Handlers{})
	logger.Info("starting aruma", "version", version, "public_url", cfg.PublicBaseURL().String())
	if err := srv.Run(ctx); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}
