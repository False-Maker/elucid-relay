package httpserver

import (
	"context"
	"log/slog"
	"time"
)

const expiredNorthboundCleanupInterval = 10 * time.Minute

func (s *Server) startExpiredNorthboundCleanup(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = expiredNorthboundCleanupInterval
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.cleanupExpiredNorthboundState(ctx)
			}
		}
	}()
}

func (s *Server) cleanupExpiredNorthboundState(ctx context.Context) {
	if s.db == nil {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(cleanupCtx, "DELETE FROM northbound_idempotency_keys WHERE expires_at < now()"); err != nil {
		slog.WarnContext(ctx, "northbound idempotency cleanup failed", "error", err)
	}
	if _, err := s.db.ExecContext(cleanupCtx, "DELETE FROM northbound_route_affinities WHERE expires_at < now()"); err != nil {
		slog.WarnContext(ctx, "northbound route affinity cleanup failed", "error", err)
	}
}
