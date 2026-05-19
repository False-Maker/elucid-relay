package httpserver

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

func (s *Server) cors(next http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, origin := range strings.Split(s.cfg.CORSAllowedOrigins, ",") {
		origin = strings.TrimSpace(origin)
		if origin != "" {
			allowed[origin] = true
		}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if allowed[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-CSRF-Token, X-Request-Id")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.cfg.RateLimitEnabled || s.redis == nil || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		bucket, limit := rateLimitBucket(r)
		window := time.Now().UTC().Unix() / 60
		key := fmt.Sprintf("rl:%s:%s:%d", bucket, clientIP(r), window)

		count, err := s.redis.Incr(r.Context(), key).Result()
		if err != nil {
			slog.Warn("rate limit unavailable", "error", err)
			next.ServeHTTP(w, r)
			return
		}
		if count == 1 {
			_ = s.redis.Expire(r.Context(), key, 2*time.Minute).Err()
		}

		w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%d", limit))
		w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%d", maxInt64(int64(limit)-count, 0)))
		if count > int64(limit) {
			writeError(w, r, appError{status: http.StatusTooManyRequests, code: "rate_limited", message: "Too many requests.", typ: "rate_limit_error"})
			return
		}

		next.ServeHTTP(w, r)
	})
}

func rateLimitBucket(r *http.Request) (string, int) {
	path := r.URL.Path
	if strings.HasPrefix(path, "/v1/") {
		return "northbound", 300
	}
	if strings.Contains(path, "/auth/login") ||
		strings.Contains(path, "/auth/register") ||
		strings.Contains(path, "/auth/password-reset") ||
		strings.Contains(path, "/auth/email-verification") {
		return "auth", 20
	}
	if strings.Contains(path, "/redeem") || strings.Contains(path, "/api-keys") {
		return "sensitive", 60
	}
	return "default", 600
}

func maxInt64(a int64, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
