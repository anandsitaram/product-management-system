package integration

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAcceptanceScenario follows section 11.3 of the design document step by step.
func TestAcceptanceScenario(t *testing.T) {
	e := setup(t)

	// 1. ADMIN creates a MANAGER user and activates the account.
	r := e.do("POST", "/api/v1/users", e.admin, map[string]any{
		"username": "mgr", "email": "mgr@example.com", "password": "ManagerPass123", "role": "MANAGER", "is_active": false})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	mgrID := r.str("data", "id")
	require.Equal(t, http.StatusUnauthorized, e.login("mgr", "ManagerPass123").Status, "inactive accounts cannot log in")
	r = e.do("PATCH", "/api/v1/users/"+mgrID, e.admin, map[string]any{"is_active": true})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	viewer, _ := e.createUser("view", "VIEWER")

	// 2. MANAGER authenticates, creates a category and labels, then a product.
	l := e.login("mgr", "ManagerPass123")
	require.Equal(t, http.StatusOK, l.Status)
	mgr := l.str("data", "access_token")
	cat := e.do("POST", "/api/v1/categories", mgr, map[string]any{"name": "Gadgets", "description": "Small devices"})
	require.Equal(t, http.StatusCreated, cat.Status, string(cat.Raw))
	l1 := e.do("POST", "/api/v1/labels", mgr, map[string]any{"name": "new"})
	l2 := e.do("POST", "/api/v1/labels", mgr, map[string]any{"name": "sale"})
	require.Equal(t, http.StatusCreated, l1.Status)
	require.Equal(t, http.StatusCreated, l2.Status)
	p := e.createProduct(mgr, map[string]any{
		"sku": "SKU-001", "name": "Widget", "description": "A fine widget", "price": 19.99, "stock_quantity": 5,
		"category_id": cat.str("data", "id"), "status": "ACTIVE",
		"label_ids": []string{l1.str("data", "id"), l2.str("data", "id")}})
	require.Equal(t, http.StatusCreated, p.Status, string(p.Raw))
	pid := p.str("data", "id")
	require.Equal(t, "Gadgets", p.str("data", "category", "name"))
	require.Len(t, p.list("data", "labels"), 2)
	require.Equal(t, 19.99, p.get("data", "price"))

	// 3. MANAGER uploads multiple images and marks one as primary.
	i1 := e.upload(mgr, pid, "a.png", pngBytes(t), "")
	require.Equal(t, http.StatusCreated, i1.Status, string(i1.Raw))
	require.Equal(t, true, i1.get("data", "is_primary"), "first image becomes primary")
	i2 := e.upload(mgr, pid, "b.jpg", jpegBytes(t), "")
	require.Equal(t, http.StatusCreated, i2.Status, string(i2.Raw))
	require.Equal(t, false, i2.get("data", "is_primary"))
	i3 := e.upload(mgr, pid, "c.webp", webpBytes(), "true")
	require.Equal(t, http.StatusCreated, i3.Status, string(i3.Raw))
	require.Equal(t, true, i3.get("data", "is_primary"))
	pm := e.do("PATCH", "/api/v1/products/"+pid+"/images/"+i2.str("data", "id"), mgr, map[string]any{"is_primary": true})
	require.Equal(t, http.StatusOK, pm.Status, string(pm.Raw))

	// 4. VIEWER can list and view the product and image metadata.
	require.Equal(t, http.StatusOK, e.do("GET", "/api/v1/products", viewer, nil).Status)
	require.Equal(t, http.StatusOK, e.do("GET", "/api/v1/products/"+pid, viewer, nil).Status)
	imgs := e.do("GET", "/api/v1/products/"+pid+"/images", viewer, nil)
	require.Equal(t, http.StatusOK, imgs.Status)
	list := imgs.list("data")
	require.Len(t, list, 3)
	first := list[0].(map[string]any)
	require.Equal(t, true, first["is_primary"])
	require.Equal(t, i2.str("data", "id"), first["id"])
	require.NotEmpty(t, first["url"])
	// the signed URL really serves the bytes
	dl, err := http.Get(first["url"].(string))
	require.NoError(t, err)
	dl.Body.Close()
	require.Equal(t, http.StatusOK, dl.StatusCode)

	// 5. VIEWER attempts product creation/update and receives 403.
	require.Equal(t, http.StatusForbidden, e.createProduct(viewer, map[string]any{"sku": "X", "name": "X", "price": 1}).Status)
	require.Equal(t, http.StatusForbidden, e.do("PATCH", "/api/v1/products/"+pid, viewer, map[string]any{"name": "Hacked"}).Status)

	// 6. MANAGER updates product details; ADMIN soft-deletes the product.
	up := e.do("PATCH", "/api/v1/products/"+pid, mgr, map[string]any{"name": "Widget v2", "price": 24.5})
	require.Equal(t, http.StatusOK, up.Status, string(up.Raw))
	require.Equal(t, "Widget v2", up.str("data", "name"))
	require.Equal(t, http.StatusForbidden, e.do("DELETE", "/api/v1/products/"+pid, mgr, nil).Status, "managers cannot delete")
	require.Equal(t, http.StatusNoContent, e.do("DELETE", "/api/v1/products/"+pid, e.admin, nil).Status)

	// 7. Normal product listing no longer returns the deleted product.
	ls := e.do("GET", "/api/v1/products", viewer, nil)
	require.Equal(t, http.StatusOK, ls.Status)
	require.Empty(t, ls.list("data"))
	require.EqualValues(t, 0, ls.get("pagination", "total"))
	require.Equal(t, http.StatusNotFound, e.do("GET", "/api/v1/products/"+pid, viewer, nil).Status)
	require.Equal(t, http.StatusNotFound, e.do("GET", "/api/v1/products/"+pid+"/images", viewer, nil).Status)

	// 8. ADMIN can inspect audit events for the mutations.
	au := e.do("GET", "/api/v1/audit-logs?limit=100", e.admin, nil)
	require.Equal(t, http.StatusOK, au.Status, string(au.Raw))
	actions := map[string]bool{}
	for _, it := range au.list("data") {
		m := it.(map[string]any)
		actions[m["action"].(string)] = true
		if m["user_id"] != nil { // startup bootstrap has no HTTP request, hence no request id
			require.NotEmpty(t, m["request_id"], "audit rows carry the request id")
		}
		require.NotContains(t, string(au.Raw), "ManagerPass123")
	}
	for _, a := range []string{"user.create", "user.update", "category.create", "label.create", "product.create",
		"product.update", "product.delete", "image.upload", "image.set_primary", "auth.login"} {
		require.True(t, actions[a], "missing audit action %s", a)
	}
	require.Equal(t, http.StatusForbidden, e.do("GET", "/api/v1/audit-logs", mgr, nil).Status)
	byActor := e.do("GET", "/api/v1/audit-logs?action=product.delete", e.admin, nil)
	require.Len(t, byActor.list("data"), 1)
}
