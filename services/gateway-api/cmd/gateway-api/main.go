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

	"github.com/elucid/elucid-relay/services/gateway-api/internal/cache"
	"github.com/elucid/elucid-relay/services/gateway-api/internal/config"
	"github.com/elucid/elucid-relay/services/gateway-api/internal/db"
	"github.com/elucid/elucid-relay/services/gateway-api/internal/httpserver"
	"github.com/elucid/elucid-relay/services/gateway-api/internal/migrations"
)

func main() {
	if err := run(); err != nil {
		slog.Error("gateway-api failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := config.Load()
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch command {
	case "serve":
		return serve(ctx, cfg)
	case "migrate":
		return migrate(ctx, cfg, os.Args[2:])
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

func serve(ctx context.Context, cfg config.Config) error {
	if err := cfg.ValidateForServe(); err != nil {
		return err
	}

	if cfg.MigrateOnStart {
		slog.Info("running database migrations")
		if err := migrations.Up(ctx, cfg.DatabaseURL); err != nil {
			return err
		}
	}

	database, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer database.Close()

	if err := httpserver.SeedOwner(ctx, cfg, database); err != nil {
		return err
	}

	redisClient, err := cache.OpenRedis(ctx, cfg.RedisAddr)
	if err != nil {
		return err
	}
	defer redisClient.Close()

	server := httpserver.New(cfg, database, redisClient)
	errCh := make(chan error, 1)

	go func() {
		slog.Info("starting gateway-api", "addr", server.Addr)
		errCh <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func migrate(ctx context.Context, cfg config.Config, args []string) error {
	direction := "up"
	if len(args) > 0 {
		direction = args[0]
	}

	if direction != "up" {
		return fmt.Errorf("unsupported migration direction %q", direction)
	}
	if cfg.DatabaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}

	return migrations.Up(ctx, cfg.DatabaseURL)
}

func printUsage() {
	fmt.Println("usage: gateway-api [serve|migrate up]")
}
