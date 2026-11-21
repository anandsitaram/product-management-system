#!/usr/bin/env bash
# End-to-end smoke test of the acceptance scenario against a RUNNING API.
#   BASE_URL=http://localhost:8080 ADMIN_USER=admin ADMIN_PASS='ChangeMe-123456' ./scripts/smoke.sh
# Requires: bash, curl, python3. Creates uniquely named data; safe to re-run.
set -euo pipefail
BASE_URL="${BASE_URL:-http://localhost:8080}"
ADMIN_USER="${ADMIN_USER:-admin}"
ADMIN_PASS="${ADMIN_PASS:-ChangeMe-123456}"
API="$BASE_URL/api/v1"
RUN="$(date +%s)$RANDOM"
PASS=0; FAIL=0

jget() { python3 -c 'import json,sys
d=json.load(sys.stdin)
for k in sys.argv[1].split("."):
    d=d[int(k)] if isinstance(d,list) else d.get(k) if isinstance(d,dict) else None
print("" if d is None else str(d).lower() if isinstance(d,bool) else d)' "$1"; }

# call METHOD PATH TOKEN [JSON]  -> sets $BODY and $CODE
call() {
  local m="$1" p="$2" t="${3:-}" b="${4:-}" args=(-s -o /tmp/smoke.body -w '%{http_code}' -X "$1")
  [ -n "$t" ] && args+=(-H "Authorization: Bearer $t")
  [ -n "$b" ] && args+=(-H 'Content-Type: application/json' -d "$b")
  CODE=$(curl "${args[@]}" "$API$p"); BODY=$(cat /tmp/smoke.body)
}
expect() { # description expected-code
  if [ "$CODE" = "$2" ]; then PASS=$((PASS+1)); printf '  ok   %s (%s)\n' "$1" "$CODE"
  else FAIL=$((FAIL+1)); printf '  FAIL %s: expected %s got %s\n       %s\n' "$1" "$2" "$CODE" "$BODY"; fi
}

echo "== health"; code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE_URL/health"); [ "$code" = 200 ] && echo "  ok   /health" || { echo "  FAIL /health ($code) - is the API running at $BASE_URL?"; exit 1; }

echo "== 1. admin logs in, creates a MANAGER and a VIEWER"
call POST /auth/token "" "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}"; expect "admin login" 200
ADMIN=$(echo "$BODY" | jget data.access_token)
call POST /users "$ADMIN" "{\"username\":\"mgr$RUN\",\"email\":\"mgr$RUN@example.com\",\"password\":\"Manager-Pass-1\",\"role\":\"MANAGER\"}"; expect "create manager" 201
call POST /users "$ADMIN" "{\"username\":\"viewer$RUN\",\"email\":\"viewer$RUN@example.com\",\"password\":\"Viewer-Pass-1\",\"role\":\"VIEWER\"}"; expect "create viewer" 201

echo "== 2. manager creates category, labels, product"
call POST /auth/token "" "{\"username\":\"mgr$RUN\",\"password\":\"Manager-Pass-1\"}"; expect "manager login" 200
MGR=$(echo "$BODY" | jget data.access_token)
call POST /auth/token "" "{\"username\":\"viewer$RUN\",\"password\":\"Viewer-Pass-1\"}"; VIEW=$(echo "$BODY" | jget data.access_token)
call POST /categories "$MGR" "{\"name\":\"Cat $RUN\"}"; expect "create category" 201; CAT=$(echo "$BODY" | jget data.id)
call POST /labels "$MGR" "{\"name\":\"label-$RUN\"}"; expect "create label" 201; LBL=$(echo "$BODY" | jget data.id)
call POST /products "$MGR" "{\"sku\":\"SKU-$RUN\",\"name\":\"Widget $RUN\",\"price\":19.99,\"stock_quantity\":5,\"status\":\"ACTIVE\",\"category_id\":\"$CAT\",\"label_ids\":[\"$LBL\"]}"; expect "create product" 201
PID=$(echo "$BODY" | jget data.id)
call POST /products "$MGR" "{\"sku\":\"SKU-$RUN\",\"name\":\"dup\",\"price\":1}"; expect "duplicate SKU rejected" 409

echo "== 3. manager uploads images, marks one primary"
python3 - <<'PY'
import base64
# 1x1 PNG and a minimal valid JPEG
open('/tmp/smoke1.png','wb').write(base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=='))
open('/tmp/smoke2.png','wb').write(open('/tmp/smoke1.png','rb').read())
open('/tmp/smoke.txt','w').write('not an image')
PY
upload() { CODE=$(curl -s -o /tmp/smoke.body -w '%{http_code}' -H "Authorization: Bearer $MGR" -F "file=@$1" ${2:+-F "is_primary=$2"} "$API/products/$PID/images"); BODY=$(cat /tmp/smoke.body); }
upload /tmp/smoke1.png; expect "upload image 1" 201; IMG1=$(echo "$BODY" | jget data.id)
upload /tmp/smoke2.png; expect "upload image 2" 201; IMG2=$(echo "$BODY" | jget data.id)
upload /tmp/smoke.txt;  expect "non-image rejected" 415
call PATCH "/products/$PID/images/$IMG2" "$MGR" '{"is_primary":true}'; expect "mark image 2 primary" 200

echo "== 4. viewer can read"
call GET "/products?search=Widget&limit=5" "$VIEW"; expect "viewer lists products" 200
call GET "/products/$PID" "$VIEW"; expect "viewer gets product" 200
call GET "/products/$PID/images" "$VIEW"; expect "viewer lists images" 200
URL=$(echo "$BODY" | jget data.0.url); code=$(curl -s -o /dev/null -w '%{http_code}' "$URL"); [ "$code" = 200 ] && { PASS=$((PASS+1)); echo "  ok   signed image URL downloads"; } || { FAIL=$((FAIL+1)); echo "  FAIL signed URL -> $code ($URL)"; }

echo "== 5. viewer is forbidden to write"
call POST /products "$VIEW" '{"sku":"nope","name":"nope","price":1}'; expect "viewer cannot create" 403
call PATCH "/products/$PID" "$VIEW" '{"name":"hack"}'; expect "viewer cannot update" 403

echo "== 6. manager updates, admin soft-deletes"
call PATCH "/products/$PID" "$MGR" '{"name":"Widget v2","price":24.5}'; expect "manager updates" 200
call DELETE "/products/$PID" "$MGR"; expect "manager cannot delete" 403
call DELETE "/products/$PID" "$ADMIN"; expect "admin deletes" 204

echo "== 7. deleted product is hidden"
call GET "/products/$PID" "$VIEW"; expect "deleted product is 404" 404
call GET "/products?search=SKU-$RUN" "$VIEW"; expect "list ok" 200
[ "$(echo "$BODY" | jget pagination.total)" = 0 ] && { PASS=$((PASS+1)); echo "  ok   not in listing"; } || { FAIL=$((FAIL+1)); echo "  FAIL still listed"; }

echo "== 8. audit trail"
call GET "/audit-logs?action=product.delete&entity_id=$PID" "$ADMIN"; expect "admin reads audit logs" 200
[ "$(echo "$BODY" | jget pagination.total)" = 1 ] && { PASS=$((PASS+1)); echo "  ok   product.delete audited"; } || { FAIL=$((FAIL+1)); echo "  FAIL audit entry missing"; }
call GET /audit-logs "$MGR"; expect "manager cannot read audit logs" 403

echo; echo "passed: $PASS  failed: $FAIL"; [ "$FAIL" = 0 ]
