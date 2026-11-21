// Package requestid assigns a request ID to every request.
package requestid

import (
	"context"
	"net/http"
	"regexp"

	"github.com/google/uuid"
)

type ctxKey struct{}

const Header = "X-Request-ID"

var safe = regexp.MustCompile(`^[A-Za-z0-9._-]{8,100}$`)

func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(Header)
		if !safe.MatchString(id) {
			id = uuid.NewString()
		}
		w.Header().Set(Header, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	})
}

func From(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}
