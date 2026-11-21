// Package integration holds API-level tests that run against a real PostgreSQL.
//
// Set TEST_DATABASE_URL to a disposable database (its public schema is DROPPED and recreated):
//
//	TEST_DATABASE_URL=postgres://pms_app:local-only-password@localhost:5432/pms_test?sslmode=disable go test ./tests/integration/...
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"pms/db"
	"pms/internal/config"
	"pms/internal/platform/database"
	"pms/internal/platform/storage"
	"pms/internal/security"
	"pms/internal/server"
)

const adminPassword = "AdminPassw0rd!"

var fastArgon = security.Argon2Params{Memory: 1024, Time: 1, Threads: 1, KeyLen: 32, SaltLen: 16}

type env struct {
	pool  *pgxpool.Pool
	t     *testing.T
	srv   *httptest.Server
	store *storage.Local
	cfg   config.Config
	admin string // access token
}

type resp struct {
	Status int
	Header http.Header
	Raw    []byte
	JSON   map[string]any
}

func setup(t *testing.T, mutate ...func(*config.Config)) *env {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration tests")
	}
	ctx := context.Background()
	pool, err := database.Connect(ctx, url)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`)
	require.NoError(t, err)
	_, err = database.MigrateUp(ctx, pool, db.Migrations, "migrations")
	require.NoError(t, err)

	cfg := config.Config{
		Env: "test", Port: "0", DatabaseURL: url,
		JWTIssuer: "pms-test", JWTAccessSecret: "test-secret-test-secret-test-secret-123456",
		JWTAccessTTL: 15 * time.Minute, JWTRefreshTTL: 24 * time.Hour,
		StorageDriver: "local", StorageSignedURLTTL: time.Minute,
		MaxUploadSize: 200 * 1024, MaxImagesPerProduct: 5,
		RateLimitAuthPerMin: 0, DocsEnabled: true,
		BootstrapAdminUsername: "admin", BootstrapAdminEmail: "admin@example.com", BootstrapAdminPassword: adminPassword,
	}
	for _, m := range mutate {
		m(&cfg)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// The signed-URL base must point at the httptest server, which only exists after it starts.
	var ts *httptest.Server
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { ts.Config.Handler.ServeHTTP(w, r) })
	ts = httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	cfg.PublicBaseURL = ts.URL
	local, err := storage.NewLocal(t.TempDir(), cfg.PublicBaseURL, cfg.JWTAccessSecret)
	require.NoError(t, err)

	require.NoError(t, server.BootstrapAdmin(ctx, pool, cfg, fastArgon, log))
	s, err := server.New(server.Deps{Cfg: cfg, Log: log, Pool: pool, Store: local, Argon: fastArgon})
	require.NoError(t, err)
	ts.Config.Handler = s.Handler

	e := &env{pool: pool, t: t, srv: ts, store: local, cfg: cfg}
	e.admin = e.login("admin", adminPassword).str("data", "access_token")
	return e
}

func (r resp) get(path ...string) any {
	var cur any = r.JSON
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

func (r resp) str(path ...string) string { s, _ := r.get(path...).(string); return s }

func (r resp) list(path ...string) []any { l, _ := r.get(path...).([]any); return l }

func (e *env) do(method, path, token string, body any) resp {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(e.t, err)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rd)
	require.NoError(e.t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return e.send(req)
}

func (e *env) send(req *http.Request) resp {
	e.t.Helper()
	res, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	require.NoError(e.t, err)
	out := resp{Status: res.StatusCode, Header: res.Header, Raw: raw}
	_ = json.Unmarshal(raw, &out.JSON)
	return out
}

func (e *env) login(user, pass string) resp {
	return e.do("POST", "/api/v1/auth/token", "", map[string]string{"username": user, "password": pass})
}

// createUser makes a user as ADMIN and returns its access token and ID.
func (e *env) createUser(username, role string) (token, id string) {
	e.t.Helper()
	pw := "Passw0rd-" + username
	r := e.do("POST", "/api/v1/users", e.admin, map[string]any{
		"username": username, "email": username + "@example.com", "password": pw, "role": role})
	require.Equal(e.t, http.StatusCreated, r.Status, string(r.Raw))
	l := e.login(username, pw)
	require.Equal(e.t, http.StatusOK, l.Status, string(l.Raw))
	return l.str("data", "access_token"), r.str("data", "id")
}

func (e *env) upload(token, productID, filename string, data []byte, primary string) resp {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	h.Set("Content-Type", "application/octet-stream")
	fw, err := mw.CreatePart(h)
	require.NoError(e.t, err)
	_, _ = fw.Write(data)
	if primary != "" {
		require.NoError(e.t, mw.WriteField("is_primary", primary))
	}
	require.NoError(e.t, mw.Close())
	req, err := http.NewRequest("POST", e.srv.URL+"/api/v1/products/"+productID+"/images", &buf)
	require.NoError(e.t, err)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	return e.send(req)
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(1, 1, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, img))
	return b.Bytes()
}

func jpegBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil))
	return b.Bytes()
}

// webpBytes builds a minimal RIFF/WEBP container (header-valid, enough for signature checks).
func webpBytes() []byte {
	chunk := append([]byte("VP8 "), 10, 0, 0, 0)
	chunk = append(chunk, make([]byte, 10)...)
	body := append([]byte("WEBP"), chunk...)
	size := len(body)
	out := []byte("RIFF")
	out = append(out, byte(size), byte(size>>8), byte(size>>16), byte(size>>24))
	return append(out, body...)
}

func (e *env) createProduct(token string, body map[string]any) resp {
	return e.do("POST", "/api/v1/products", token, body)
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func (e *env) queryRow(sql string, args ...any) pgx.Row {
	return e.pool.QueryRow(context.Background(), sql, args...)
}
