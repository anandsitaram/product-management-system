package integration

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"pms/internal/config"
	"pms/internal/security"
)

func TestAuthLifecycle(t *testing.T) {
	e := setup(t)

	// invalid credentials are generic and identical for unknown user / wrong password
	a := e.login("admin", "wrong-password-1")
	b := e.login("nobody", "wrong-password-1")
	require.Equal(t, http.StatusUnauthorized, a.Status)
	require.Equal(t, http.StatusUnauthorized, b.Status)
	require.Equal(t, a.str("error", "message"), b.str("error", "message"))

	// login by email works; username is case-insensitive
	require.Equal(t, http.StatusOK, e.login("ADMIN@example.com", adminPassword).Status)

	// no / malformed / garbage / wrong-signature tokens → 401
	require.Equal(t, http.StatusUnauthorized, e.do("GET", "/api/v1/products", "", nil).Status)
	require.Equal(t, http.StatusUnauthorized, e.do("GET", "/api/v1/products", "garbage", nil).Status)
	forged := security.NewTokenManager("some-other-secret-some-other-secret-12", e.cfg.JWTIssuer, time.Minute)
	tok, _, _ := forged.IssueAccess(uuid.New(), "ADMIN")
	require.Equal(t, http.StatusUnauthorized, e.do("GET", "/api/v1/users", tok, nil).Status)
	// expired token
	expired := security.NewTokenManager(e.cfg.JWTAccessSecret, e.cfg.JWTIssuer, -time.Minute)
	tok, _, _ = expired.IssueAccess(uuid.New(), "ADMIN")
	require.Equal(t, http.StatusUnauthorized, e.do("GET", "/api/v1/users", tok, nil).Status)

	// refresh rotation: old refresh token stops working, new one works
	l := e.login("admin", adminPassword)
	rt1 := l.str("data", "refresh_token")
	r := e.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": rt1})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	rt2 := r.str("data", "refresh_token")
	require.NotEqual(t, rt1, rt2)
	require.Equal(t, http.StatusOK, e.do("GET", "/api/v1/users", r.str("data", "access_token"), nil).Status)

	// reuse of a rotated token is rejected and revokes the whole token family
	require.Equal(t, http.StatusUnauthorized, e.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": rt1}).Status)
	require.Equal(t, http.StatusUnauthorized, e.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": rt2}).Status)

	// logout revokes the refresh token; only the owner's token can be revoked
	l = e.login("admin", adminPassword)
	at, rt := l.str("data", "access_token"), l.str("data", "refresh_token")
	require.Equal(t, http.StatusNoContent, e.do("POST", "/api/v1/auth/logout", at, map[string]string{"refresh_token": rt}).Status)
	require.Equal(t, http.StatusUnauthorized, e.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": rt}).Status)
	require.Equal(t, http.StatusUnauthorized, e.do("POST", "/api/v1/auth/logout", "", map[string]string{"refresh_token": rt}).Status)

	// deactivating a user takes effect immediately for existing access tokens and sessions
	tokU, idU := e.createUser("temp", "MANAGER")
	require.Equal(t, http.StatusOK, e.do("GET", "/api/v1/products", tokU, nil).Status)
	lu := e.login("temp", "Passw0rd-temp")
	require.Equal(t, http.StatusOK, e.do("PATCH", "/api/v1/users/"+idU, e.admin, map[string]any{"is_active": false}).Status)
	require.Equal(t, http.StatusUnauthorized, e.do("GET", "/api/v1/products", tokU, nil).Status)
	require.Equal(t, http.StatusUnauthorized, e.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": lu.str("data", "refresh_token")}).Status)
	require.Equal(t, http.StatusUnauthorized, e.login("temp", "Passw0rd-temp").Status)

	// refresh tokens are stored hashed, never in clear
	var n int
	require.NoError(t, e.queryRow(`SELECT count(*) FROM refresh_tokens WHERE token_hash = $1`, rt).Scan(&n))
	require.Zero(t, n)
}

func TestUserManagementRules(t *testing.T) {
	e := setup(t)
	// validation
	r := e.do("POST", "/api/v1/users", e.admin, map[string]any{"username": "x", "email": "bad", "password": "short", "role": "GOD"})
	require.Equal(t, http.StatusBadRequest, r.Status)
	require.Equal(t, "VALIDATION_ERROR", r.str("error", "code"))
	require.GreaterOrEqual(t, len(r.list("error", "details")), 4)
	// duplicates → 409
	_, _ = e.createUser("dup", "VIEWER")
	r = e.do("POST", "/api/v1/users", e.admin, map[string]any{"username": "dup", "email": "other@example.com", "password": "Passw0rd-dup", "role": "VIEWER"})
	require.Equal(t, http.StatusConflict, r.Status)
	// the response never contains password material
	require.False(t, contains(string(r.Raw), "argon2"))
	lst := e.do("GET", "/api/v1/users", e.admin, nil)
	require.Equal(t, http.StatusOK, lst.Status)
	require.False(t, contains(string(lst.Raw), "password"))
	// admin cannot lock themselves out
	me := e.login("admin", adminPassword).str("data", "user", "id")
	require.Equal(t, http.StatusUnprocessableEntity, e.do("PATCH", "/api/v1/users/"+me, e.admin, map[string]any{"role": "VIEWER"}).Status)
	require.Equal(t, http.StatusUnprocessableEntity, e.do("PATCH", "/api/v1/users/"+me, e.admin, map[string]any{"is_active": false}).Status)
	// unknown fields are rejected, wrong content type is 415
	require.Equal(t, http.StatusBadRequest, e.do("POST", "/api/v1/users", e.admin, map[string]any{"bogus": 1}).Status)
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/v1/users", strings.NewReader("x"))
	req.Header.Set("Authorization", "Bearer "+e.admin)
	req.Header.Set("Content-Type", "text/plain")
	require.Equal(t, http.StatusUnsupportedMediaType, e.send(req).Status)
	// non-UUID ids are 404
	require.Equal(t, http.StatusNotFound, e.do("GET", "/api/v1/users/not-a-uuid", e.admin, nil).Status)
}

// TestRBACMatrix exercises every protected operation for every role (section 4 of the design).
func TestRBACMatrix(t *testing.T) {
	e := setup(t)
	tokens := map[string]string{"ADMIN": e.admin}
	tokens["MANAGER"], _ = e.createUser("manager1", "MANAGER")
	tokens["VIEWER"], _ = e.createUser("viewer1", "VIEWER")

	id := uuid.NewString()
	type op struct {
		name, method, path string
		body               any
		allowed            map[string]bool
	}
	all := map[string]bool{"ADMIN": true, "MANAGER": true, "VIEWER": true}
	mgrUp := map[string]bool{"ADMIN": true, "MANAGER": true}
	admin := map[string]bool{"ADMIN": true}
	prod := map[string]any{"sku": "S", "name": "N", "price": 1}
	ops := []op{
		{"list users", "GET", "/api/v1/users", nil, admin},
		{"create user", "POST", "/api/v1/users", map[string]any{}, admin},
		{"get user", "GET", "/api/v1/users/" + id, nil, admin},
		{"update user", "PATCH", "/api/v1/users/" + id, map[string]any{"is_active": true}, admin},
		{"list products", "GET", "/api/v1/products", nil, all},
		{"create product", "POST", "/api/v1/products", prod, mgrUp},
		{"get product", "GET", "/api/v1/products/" + id, nil, all},
		{"put product", "PUT", "/api/v1/products/" + id, prod, mgrUp},
		{"patch product", "PATCH", "/api/v1/products/" + id, map[string]any{"name": "x"}, mgrUp},
		{"delete product", "DELETE", "/api/v1/products/" + id, nil, admin},
		{"list categories", "GET", "/api/v1/categories", nil, all},
		{"create category", "POST", "/api/v1/categories", map[string]any{"name": "c"}, mgrUp},
		{"get category", "GET", "/api/v1/categories/" + id, nil, all},
		{"patch category", "PATCH", "/api/v1/categories/" + id, map[string]any{"name": "c"}, mgrUp},
		{"delete category", "DELETE", "/api/v1/categories/" + id, nil, mgrUp},
		{"list labels", "GET", "/api/v1/labels", nil, all},
		{"create label", "POST", "/api/v1/labels", map[string]any{"name": "l"}, mgrUp},
		{"patch label", "PATCH", "/api/v1/labels/" + id, map[string]any{"name": "l"}, mgrUp},
		{"delete label", "DELETE", "/api/v1/labels/" + id, nil, mgrUp},
		{"list images", "GET", "/api/v1/products/" + id + "/images", nil, all},
		{"set primary", "PATCH", "/api/v1/products/" + id + "/images/" + id, map[string]any{"is_primary": true}, mgrUp},
		{"delete image", "DELETE", "/api/v1/products/" + id + "/images/" + id, nil, mgrUp},
		{"audit logs", "GET", "/api/v1/audit-logs", nil, admin},
	}
	for _, o := range ops {
		for role, tok := range tokens {
			r := e.do(o.method, o.path, tok, o.body)
			if o.allowed[role] {
				require.NotEqual(t, http.StatusForbidden, r.Status, "%s should be allowed for %s", o.name, role)
				require.NotEqual(t, http.StatusUnauthorized, r.Status, "%s: %s", o.name, role)
			} else {
				require.Equal(t, http.StatusForbidden, r.Status, "%s should be forbidden for %s: %s", o.name, role, string(r.Raw))
			}
		}
		// anonymous is always 401
		require.Equal(t, http.StatusUnauthorized, e.do(o.method, o.path, "", o.body).Status, "%s anonymous", o.name)
	}
	// upload (multipart) role check
	for role, tok := range tokens {
		r := e.upload(tok, id, "a.png", pngBytes(t), "")
		if mgrUp[role] {
			require.Equal(t, http.StatusNotFound, r.Status, "upload for %s reaches handler (unknown product)", role)
		} else {
			require.Equal(t, http.StatusForbidden, r.Status)
		}
	}
}

func TestOperationalEndpointsAndErrors(t *testing.T) {
	e := setup(t)
	r := e.do("GET", "/health", "", nil)
	require.Equal(t, http.StatusOK, r.Status)
	require.NotEmpty(t, r.Header.Get("X-Request-ID"))
	require.Equal(t, r.Header.Get("X-Request-ID"), r.str("meta", "request_id"))
	require.Equal(t, "nosniff", r.Header.Get("X-Content-Type-Options"))
	rd := e.do("GET", "/ready", "", nil)
	require.Equal(t, http.StatusOK, rd.Status)
	require.Equal(t, "ok", rd.str("data", "checks", "database"))
	m, err := http.Get(e.srv.URL + "/metrics")
	require.NoError(t, err)
	m.Body.Close()
	require.Equal(t, http.StatusOK, m.StatusCode)
	nf := e.do("GET", "/api/v1/nope", e.admin, nil)
	require.Equal(t, http.StatusNotFound, nf.Status)
	require.Equal(t, "NOT_FOUND", nf.str("error", "code"))
	// caller-supplied request id is honoured
	req, _ := http.NewRequest("GET", e.srv.URL+"/health", nil)
	req.Header.Set("X-Request-ID", "client-req-12345")
	require.Equal(t, "client-req-12345", e.send(req).Header.Get("X-Request-ID"))
}

func TestLoginRateLimit(t *testing.T) {
	e := setup(t, func(c *config.Config) { c.RateLimitAuthPerMin = 3 })
	got429 := false
	for i := 0; i < 8; i++ {
		if e.login("admin", "bad-password-123").Status == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	require.True(t, got429, "login must be rate limited")
}
