# Product Management System (Go + PostgreSQL)

Complete implementation of `Product_Management_System_Architecture_Design.docx`: a modular-monolith REST API with
JWT auth + refresh rotation, RBAC (ADMIN / MANAGER / VIEWER), products with search/filter/sort/pagination, categories,
labels, product images (local disk **or** S3-compatible/MinIO with signed URLs), soft delete, audit logging,
OpenAPI docs, health/readiness/metrics, migrations, Docker and tests.

## Quick start

Pick one. Each gives you the API on <http://localhost:8080> (Swagger UI at `/docs`).

### A. Everything in Docker (PostgreSQL + MinIO + API)
```bash
docker compose up --build
```
Login with `admin` / `ChangeMe-123456` (override via `BOOTSTRAP_ADMIN_*`). MinIO console: <http://localhost:9001> (`minioadmin`/`minioadmin`).

### B. Go on your host, PostgreSQL in Docker (best for development)
```bash
make setup        # creates .env, runs `go mod tidy` (generates go.sum)  -- needs internet once
make deps-up      # PostgreSQL 16 (+ pms_test database for tests)
make run          # applies migrations, creates the admin, serves :8080 using local-disk image storage
```

### C. Go on your host, your own PostgreSQL
Create a database/user, put `DATABASE_URL` and a 32+ char `JWT_ACCESS_SECRET` in `.env`, then `make setup && make run`.

Requirements: Go 1.22+, PostgreSQL 14+ (16 tested), Docker only for options A/B.

### Verify it works
```bash
make smoke        # runs the whole acceptance scenario against the running API (27 checks)
make test         # vet + unit + integration tests (integration needs the pms_test DB from `make deps-up`)
```

### Try it by hand
```bash
TOKEN=$(curl -s localhost:8080/api/v1/auth/token -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"ChangeMe-123456"}' | python3 -c 'import sys,json;print(json.load(sys.stdin)["data"]["access_token"])')

curl -s localhost:8080/api/v1/products -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"sku":"SKU-1","name":"Widget","price":19.99,"stock_quantity":5,"status":"ACTIVE"}'

curl -s "localhost:8080/api/v1/products?search=widget&sort_by=price&order=asc" -H "Authorization: Bearer $TOKEN"
curl -s localhost:8080/api/v1/products/<id>/images -H "Authorization: Bearer $TOKEN" -F file=@photo.jpg -F is_primary=true
```
In Swagger UI click **Authorize** and paste the `access_token`.

## Roles
| Capability | ADMIN | MANAGER | VIEWER |
|---|:-:|:-:|:-:|
| Read products, categories, labels, image metadata | ✔ | ✔ | ✔ |
| Create/update products, categories, labels; upload/delete/primary images | ✔ | ✔ | – |
| Soft-delete products | ✔ | – | – |
| Manage users, read audit logs | ✔ | – | – |

## Endpoints (all under `/api/v1`, JSON envelope `{data, meta}` / `{error, meta}`)
`POST /auth/token|refresh|logout` · `GET|POST /users`, `GET|PATCH /users/{id}` · `GET|POST /products`,
`GET|PUT|PATCH|DELETE /products/{id}` · `GET|POST /products/{id}/images`, `PATCH|DELETE /products/{id}/images/{imageId}` ·
`GET|POST /categories`, `GET|PATCH|DELETE /categories/{id}` · `GET|POST /labels`, `GET|PATCH|DELETE /labels/{id}` ·
`GET /audit-logs` · plus `/health`, `/ready`, `/metrics`, `/docs`, `/openapi.yaml`. Full contract: `api/openapi.yaml`.

Product list query: `search, category_id, label_id, status, min_price, max_price, sort_by (name|sku|price|created_at|updated_at), order, page, limit (≤100)`.

## Layout
```
cmd/api        server            cmd/migrate   up/down migrations      cmd/reconcile  find/delete orphaned image objects
db/migrations  SQL (embedded)    api/           OpenAPI (embedded)      tests/integration  API tests on real PostgreSQL
internal/
  config  server(router wiring)  middleware/{requestid,logging,recovery,secure,ratelimit,auth,rbac}
  platform/{database,logger,metrics,storage(+s3store)}   security/{password,jwt}   shared/{apperrors,response,request,validation,pagination}
  modules/{auth,users,products,categories,labels,media,audit}
```

## Design decisions & deviations from the document (please review)
- **One Go package per module** (`handler/service/repository` split by file, not by sub-package) – idiomatic Go, same layering. Small modules (categories, labels) keep all three layers in one file.
- **Hand-written SQL with pgx** (the doc allowed "sqlc or a thin repository layer"); migrations run by a small built-in runner (`schema_migrations` table, advisory lock, up/down).
- **`labels` gained `is_active` and `updated_at`**, because the API table says `DELETE /labels/{id}` *deactivates* but the baseline DDL had no such column. Inactive categories/labels remain on existing products but can't be newly assigned (422).
- **Storage**: `STORAGE_DRIVER=local` (default; HMAC-signed expiring URLs served by the API) or `s3` (AWS S3/MinIO/R2 presigned URLs). Object keys are generated (`products/{id}/{uuid}.ext`); client file names are never used for paths.
- **Image safety**: type decided by magic bytes (JPEG/PNG/WebP), JPEG/PNG headers decoded to reject truncated files and >50 MP images, size cap, max 5 per product, serialised per product with a row lock; failed DB writes delete the uploaded object; `cmd/reconcile` cleans anything left over.
- **Auth**: Argon2id passwords (64 MiB, t=2); 15-min HS256 access tokens; opaque refresh tokens stored only as SHA-256, rotated on every use, reuse of a rotated token revokes the user's sessions. The user is re-loaded on every request, so deactivation/role changes apply immediately. Admins cannot demote/deactivate themselves or the last admin.
- **Passwords**: 10–128 chars with a letter and a digit. Usernames/emails are stored lower-case.
- **PUT vs PATCH**: PUT replaces all writable fields (omitted optional fields reset); PATCH changes only supplied fields, `null` clears `description`/`category_id`.
- **SKUs stay unique even after soft delete** (you cannot reuse the SKU of a deleted product); easy to change to a partial unique index if you prefer.
- **Audit** events are written right after the mutation succeeds (best effort, logged on failure) and redact keys containing `password|token|secret|hash`.
- **Rate limiting** is in-memory per client IP on `/auth/*` (`RATE_LIMIT_AUTH_PER_MIN`). With several instances, enforce it at the proxy as well. Set `TRUST_PROXY=true` only behind your own proxy.
- Extras not in the endpoint table: `GET /categories/{id}`, `GET /labels/{id}`, `PATCH …/images/{imageId}` (set primary), malformed query strings → 400.

## Operations
- `/ready` checks DB + storage; `/metrics` is Prometheus text (request count/latency by route). Set `OPS_TOKEN` to require a bearer token for both.
- Logs are structured (`slog`; text in development, JSON otherwise) with `request_id`; query strings are never logged (they can contain signed-URL signatures).
- `make migrate-up | migrate-down [N=2]`; set `AUTO_MIGRATE=false` if your pipeline migrates separately.
- Production checklist: strong unique `JWT_ACCESS_SECRET`, change the bootstrap password, `APP_ENV=production` (hides docs, enables HSTS), TLS at the proxy, `OPS_TOKEN`, backups for PostgreSQL and the bucket, `DOCS_ENABLED` as needed.
- Load test: `k6 run -e PASS=... scripts/loadtest.js`.

## Tests
- Unit tests: password/JWT, RBAC matrix, rate limiter, config, audit redaction, validation, local storage confinement, S3 driver (against an in-process fake S3 endpoint).
- Integration tests (`tests/integration`, need `TEST_DATABASE_URL`, **drops the public schema**): the document's acceptance scenario (§11.3), auth lifecycle (rotation, reuse, logout, deactivation), full role×endpoint matrix, validation/conflict rules, search/filter/sort/pagination (incl. LIKE-wildcard and injection-shaped input), image signature/size/limit/primary/signed-URL rules, concurrent uploads.

## Troubleshooting
- `missing go.sum entry` → run `make setup` (or `go mod tidy`).
- `JWT_ACCESS_SECRET is required…` → at least 32 characters.
- `bucket … is not accessible` → create it or set `STORAGE_AUTO_CREATE_BUCKET=true`.
- Image URLs from the Docker stack don't open → they use `STORAGE_PUBLIC_ENDPOINT` (`http://localhost:9000`); make sure port 9000 is published.
- Port busy → change `APP_PORT`.
