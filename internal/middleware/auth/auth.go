// Package auth validates bearer JWTs and puts the authenticated principal in the context.
package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"pms/internal/security"
	"pms/internal/shared/apperrors"
	"pms/internal/shared/response"
)

type Principal struct {
	UserID   uuid.UUID
	Username string
	Role     string
}

// Loader returns the current, active principal for a user ID, or (nil, nil) if missing/inactive.
type Loader interface {
	LoadPrincipal(ctx context.Context, id uuid.UUID) (*Principal, error)
}

type ctxKey struct{}

func With(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

func From(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// Middleware verifies the access token, then reloads the user so that deactivation and
// role changes take effect immediately rather than when the token expires.
func Middleware(tm *security.TokenManager, loader Loader) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			const prefix = "bearer "
			if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="pms"`)
				response.Error(w, r, apperrors.Unauthorized("Missing or malformed Authorization header"))
				return
			}
			claims, err := tm.VerifyAccess(strings.TrimSpace(h[len(prefix):]))
			if err != nil {
				w.Header().Set("WWW-Authenticate", `Bearer realm="pms", error="invalid_token"`)
				response.Error(w, r, apperrors.Unauthorized("Invalid or expired access token"))
				return
			}
			p, err := loader.LoadPrincipal(r.Context(), claims.UserID)
			if err != nil {
				response.Error(w, r, err)
				return
			}
			if p == nil {
				response.Error(w, r, apperrors.Unauthorized("Invalid or expired access token"))
				return
			}
			next.ServeHTTP(w, r.WithContext(With(r.Context(), *p)))
		})
	}
}
