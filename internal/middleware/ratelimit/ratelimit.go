// Package ratelimit is an in-memory per-key token-bucket limiter (single-instance).
// Behind multiple instances, enforce limits at the reverse proxy or use a shared store.
package ratelimit

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"pms/internal/shared/apperrors"
	"pms/internal/shared/response"
)

type bucket struct {
	tokens float64
	last   time.Time
}

type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64 // tokens per second
	burst   float64
}

// New allows perMinute requests per key per minute (with a burst of the same size).
func New(perMinute int) *Limiter {
	return &Limiter{buckets: map[string]*bucket{}, rate: float64(perMinute) / 60.0, burst: float64(perMinute)}
}

func (l *Limiter) Allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Cleanup periodically evicts idle buckets until ctx is cancelled.
func (l *Limiter) Cleanup(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.mu.Lock()
			for k, b := range l.buckets {
				if time.Since(b.last) > 10*time.Minute {
					delete(l.buckets, k)
				}
			}
			l.mu.Unlock()
		}
	}
}

// ClientIP returns the peer address; with trustProxy it uses the last X-Forwarded-For entry
// (the one appended by your own proxy, which cannot be spoofed by the client).
func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Middleware is a no-op when l is nil.
func Middleware(l *Limiter, scope string, trustProxy bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if l == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.Allow(scope + "|" + ClientIP(r, trustProxy)) {
				w.Header().Set("Retry-After", "60")
				response.Error(w, r, apperrors.TooManyRequests())
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
