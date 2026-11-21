// Package server wires configuration, modules and middleware into the HTTP handler.
package server

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"pms/api"
	"pms/internal/config"
	"pms/internal/middleware/auth"
	"pms/internal/middleware/logging"
	"pms/internal/middleware/ratelimit"
	"pms/internal/middleware/rbac"
	"pms/internal/middleware/recovery"
	"pms/internal/middleware/requestid"
	"pms/internal/middleware/secure"
	"pms/internal/modules/audit"
	authmod "pms/internal/modules/auth"
	"pms/internal/modules/categories"
	"pms/internal/modules/labels"
	"pms/internal/modules/media"
	"pms/internal/modules/products"
	"pms/internal/modules/users"
	"pms/internal/platform/metrics"
	"pms/internal/platform/storage"
	"pms/internal/security"
	"pms/internal/shared/apperrors"
	"pms/internal/shared/response"
)

type Deps struct {
	Cfg     config.Config
	Log     *slog.Logger
	Pool    *pgxpool.Pool
	Store   storage.Storage
	Argon   security.Argon2Params
	Metrics *metrics.Registry
}

// Server holds the handler and background resources that need to be stopped.
type Server struct {
	Handler http.Handler
	limiter *ratelimit.Limiter
}

// Start launches background maintenance (rate-limiter eviction) until ctx is cancelled.
func (s *Server) Start(ctx context.Context) {
	if s.limiter != nil {
		go s.limiter.Cleanup(ctx)
	}
}

func New(d Deps) (*Server, error) {
	cfg := d.Cfg
	if d.Metrics == nil {
		d.Metrics = metrics.New()
	}

	rec := audit.NewRecorder(d.Pool)
	tokens := security.NewTokenManager(cfg.JWTAccessSecret, cfg.JWTIssuer, cfg.JWTAccessTTL)

	usersRepo := users.NewRepository(d.Pool)
	usersSvc := users.NewService(usersRepo, rec, d.Argon)
	authSvc, err := authmod.NewService(authmod.NewRepository(d.Pool), usersRepo, tokens, rec, cfg.JWTRefreshTTL, d.Argon)
	if err != nil {
		return nil, err
	}
	catSvc := categories.NewService(categories.NewRepository(d.Pool), rec)
	lblSvc := labels.NewService(labels.NewRepository(d.Pool), rec)
	prodSvc := products.NewService(products.NewRepository(d.Pool), rec)
	mediaSvc := media.NewService(media.NewRepository(d.Pool), d.Store, rec, media.Config{
		MaxSize: cfg.MaxUploadSize, MaxPerProduct: cfg.MaxImagesPerProduct, URLTTL: cfg.StorageSignedURLTTL})

	authH := authmod.NewHandler(authSvc)
	usersH := users.NewHandler(usersSvc)
	catH := categories.NewHandler(catSvc)
	lblH := labels.NewHandler(lblSvc)
	prodH := products.NewHandler(prodSvc)
	mediaH := media.NewHandler(mediaSvc, cfg.MaxUploadSize)
	auditH := audit.NewHandler(rec)

	var limiter *ratelimit.Limiter
	if cfg.RateLimitAuthPerMin > 0 {
		limiter = ratelimit.New(cfg.RateLimitAuthPerMin)
	}
	authLimit := ratelimit.Middleware(limiter, "auth", cfg.TrustProxy)
	authn := auth.Middleware(tokens, usersRepo)
	need := rbac.Require

	r := chi.NewRouter()
	r.Use(requestid.Middleware)
	r.Use(logging.Middleware(d.Log, d.Metrics.Observe))
	r.Use(recovery.Middleware)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) { response.Error(w, r, apperrors.NotFound("Resource")) })
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		response.Error(w, r, apperrors.New(http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed"))
	})

	ops := opsGuard(cfg.OpsToken)
	r.Group(func(r chi.Router) {
		r.Use(secure.Headers(cfg.IsProduction()))
		r.Get("/health", func(w http.ResponseWriter, req *http.Request) {
			response.JSON(w, req, http.StatusOK, map[string]string{"status": "ok"})
		})
		r.With(ops).Get("/ready", readyHandler(d.Pool, d.Store))
		r.With(ops).Method(http.MethodGet, "/metrics", d.Metrics.Handler())
	})

	if cfg.DocsEnabled {
		r.Get("/openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
			_, _ = w.Write(api.OpenAPI)
		})
		r.Get("/docs", docsHandler)
	}
	if local, ok := d.Store.(*storage.Local); ok {
		r.Method(http.MethodGet, storage.LocalRoutePrefix+"*", local.Handler())
	}

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(secure.Headers(cfg.IsProduction()))
		r.Use(chimw.Timeout(30 * time.Second))
		r.Use(rejectMalformedQuery)

		r.Route("/auth", func(r chi.Router) {
			r.With(authLimit).Post("/token", authH.Token)
			r.With(authLimit).Post("/refresh", authH.Refresh)
			r.With(authn, authLimit).Post("/logout", authH.Logout)
		})

		r.Group(func(r chi.Router) {
			r.Use(authn)

			r.Route("/users", func(r chi.Router) {
				r.Use(need(rbac.UserManage))
				r.Get("/", usersH.List)
				r.Post("/", usersH.Create)
				r.Get("/{userId}", usersH.Get)
				r.Patch("/{userId}", usersH.Update)
			})

			r.Route("/products", func(r chi.Router) {
				r.With(need(rbac.CatalogRead)).Get("/", prodH.List)
				r.With(need(rbac.ProductWrite)).Post("/", prodH.Create)
				r.Route("/{productId}", func(r chi.Router) {
					r.With(need(rbac.CatalogRead)).Get("/", prodH.Get)
					r.With(need(rbac.ProductWrite)).Put("/", prodH.Put)
					r.With(need(rbac.ProductWrite)).Patch("/", prodH.Patch)
					r.With(need(rbac.ProductDelete)).Delete("/", prodH.Delete)

					r.Route("/images", func(r chi.Router) {
						r.With(need(rbac.CatalogRead)).Get("/", mediaH.List)
						r.With(need(rbac.MediaManage)).Post("/", mediaH.Upload)
						r.With(need(rbac.MediaManage)).Patch("/{imageId}", mediaH.SetPrimary)
						r.With(need(rbac.MediaManage)).Delete("/{imageId}", mediaH.Delete)
					})
				})
			})

			r.Route("/categories", func(r chi.Router) {
				r.With(need(rbac.CatalogRead)).Get("/", catH.List)
				r.With(need(rbac.CatalogManage)).Post("/", catH.Create)
				r.With(need(rbac.CatalogRead)).Get("/{categoryId}", catH.Get)
				r.With(need(rbac.CatalogManage)).Patch("/{categoryId}", catH.Update)
				r.With(need(rbac.CatalogManage)).Delete("/{categoryId}", catH.Delete)
			})

			r.Route("/labels", func(r chi.Router) {
				r.With(need(rbac.CatalogRead)).Get("/", lblH.List)
				r.With(need(rbac.CatalogManage)).Post("/", lblH.Create)
				r.With(need(rbac.CatalogRead)).Get("/{labelId}", lblH.Get)
				r.With(need(rbac.CatalogManage)).Patch("/{labelId}", lblH.Update)
				r.With(need(rbac.CatalogManage)).Delete("/{labelId}", lblH.Delete)
			})

			r.With(need(rbac.AuditRead)).Get("/audit-logs", auditH.List)
		})
	})

	return &Server{Handler: r, limiter: limiter}, nil
}

// rejectMalformedQuery returns 400 for query strings Go cannot parse (e.g. a stray ';'), instead of
// silently dropping the offending parameters.
func rejectMalformedQuery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := url.ParseQuery(r.URL.RawQuery); err != nil {
			response.Error(w, r, apperrors.BadRequest("Malformed query string"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// opsGuard protects operational endpoints with a static bearer token when OPS_TOKEN is set.
func opsGuard(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if token == "" {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				response.Error(w, r, apperrors.Unauthorized("Missing or invalid operations token"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func readyHandler(pool *pgxpool.Pool, store storage.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		checks := map[string]string{"database": "ok", "storage": "ok"}
		status := http.StatusOK
		if err := pool.Ping(ctx); err != nil {
			checks["database"], status = "unavailable", http.StatusServiceUnavailable
			slog.Warn("readiness: database", "error", err)
		}
		if err := store.Ping(ctx); err != nil {
			checks["storage"], status = "unavailable", http.StatusServiceUnavailable
			slog.Warn("readiness: storage", "error", err)
		}
		st := "ready"
		if status != http.StatusOK {
			st = "not_ready"
		}
		w.Header().Set("Cache-Control", "no-store")
		if status == http.StatusOK {
			response.JSON(w, r, status, map[string]any{"status": st, "checks": checks})
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"data":{"status":"not_ready","checks":{"database":"` + checks["database"] + `","storage":"` + checks["storage"] + `"}}}` + "\n"))
	}
}

const docsPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Product Management API</title>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui.css"></head>
<body><div id="ui"></div>
<script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
<script>window.ui = SwaggerUIBundle({url: "/openapi.yaml", dom_id: "#ui", persistAuthorization: true});</script>
</body></html>`

func docsHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; script-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; img-src 'self' data: https://cdn.jsdelivr.net; connect-src 'self'")
	_, _ = w.Write([]byte(docsPage))
}

// BootstrapAdmin creates the first administrator from BOOTSTRAP_ADMIN_* when no admin exists.
func BootstrapAdmin(ctx context.Context, pool *pgxpool.Pool, cfg config.Config, p security.Argon2Params, log *slog.Logger) error {
	svc := users.NewService(users.NewRepository(pool), audit.NewRecorder(pool), p)
	have := cfg.BootstrapAdminUsername != "" && cfg.BootstrapAdminEmail != "" && cfg.BootstrapAdminPassword != ""
	if !have {
		n, err := users.NewRepository(pool).CountAdmins(ctx)
		if err != nil {
			return err
		}
		if n == 0 {
			log.Warn("no administrator exists; set BOOTSTRAP_ADMIN_USERNAME, BOOTSTRAP_ADMIN_EMAIL and BOOTSTRAP_ADMIN_PASSWORD and restart")
		}
		return nil
	}
	created, err := svc.Bootstrap(ctx, cfg.BootstrapAdminUsername, cfg.BootstrapAdminEmail, cfg.BootstrapAdminPassword)
	if err != nil {
		return err
	}
	if created {
		log.Info("bootstrap administrator created", "username", strings.ToLower(cfg.BootstrapAdminUsername))
	}
	return nil
}
