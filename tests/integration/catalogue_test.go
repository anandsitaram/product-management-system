package integration

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProductValidationAndConflicts(t *testing.T) {
	e := setup(t)
	ok := map[string]any{"sku": "SKU-1", "name": "One", "price": 10}
	r := e.createProduct(e.admin, ok)
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	pid := r.str("data", "id")
	require.Equal(t, "DRAFT", r.str("data", "status"), "default status")
	require.EqualValues(t, 0, r.get("data", "stock_quantity"))
	require.Equal(t, "/api/v1/products/"+pid, r.Header.Get("Location"))

	// duplicate SKU → 409
	require.Equal(t, http.StatusConflict, e.createProduct(e.admin, ok).Status)

	bad := []struct {
		name string
		body map[string]any
		code int
		sub  string
	}{
		{"negative price", map[string]any{"sku": "A", "name": "A", "price": -1}, 400, "price"},
		{"missing price", map[string]any{"sku": "A", "name": "A"}, 400, "price"},
		{"3 decimals", map[string]any{"sku": "A", "name": "A", "price": 1.234}, 400, "price"},
		{"negative stock", map[string]any{"sku": "A", "name": "A", "price": 1, "stock_quantity": -3}, 400, "stock_quantity"},
		{"bad status", map[string]any{"sku": "A", "name": "A", "price": 1, "status": "LIVE"}, 400, "status"},
		{"blank sku", map[string]any{"sku": "  ", "name": "A", "price": 1}, 400, "sku"},
		{"long name", map[string]any{"sku": "A", "name": string(make([]byte, 201)), "price": 1}, 400, "name"},
		{"bad category uuid", map[string]any{"sku": "A", "name": "A", "price": 1, "category_id": "xyz"}, 400, "category_id"},
		{"unknown category", map[string]any{"sku": "A", "name": "A", "price": 1, "category_id": "11111111-1111-1111-1111-111111111111"}, 422, "category_id"},
		{"unknown label", map[string]any{"sku": "A", "name": "A", "price": 1, "label_ids": []string{"11111111-1111-1111-1111-111111111111"}}, 422, "label_ids"},
	}
	for _, b := range bad {
		r := e.createProduct(e.admin, b.body)
		require.Equal(t, b.code, r.Status, "%s: %s", b.name, string(r.Raw))
		require.Contains(t, string(r.Raw), b.sub, b.name)
		require.Equal(t, "VALIDATION_ERROR", r.str("error", "code"), b.name)
	}

	// inactive category / label cannot be newly assigned
	cat := e.do("POST", "/api/v1/categories", e.admin, map[string]any{"name": "Old"})
	lbl := e.do("POST", "/api/v1/labels", e.admin, map[string]any{"name": "old"})
	cid, lid := cat.str("data", "id"), lbl.str("data", "id")
	require.Equal(t, http.StatusNoContent, e.do("DELETE", "/api/v1/categories/"+cid, e.admin, nil).Status)
	require.Equal(t, http.StatusNoContent, e.do("DELETE", "/api/v1/labels/"+lid, e.admin, nil).Status)
	require.Equal(t, 422, e.createProduct(e.admin, map[string]any{"sku": "B", "name": "B", "price": 1, "category_id": cid}).Status)
	require.Equal(t, 422, e.createProduct(e.admin, map[string]any{"sku": "B", "name": "B", "price": 1, "label_ids": []string{lid}}).Status)
	require.Equal(t, false, e.do("GET", "/api/v1/categories/"+cid, e.admin, nil).get("data", "is_active"))

	// PATCH semantics: absent = unchanged, null clears, [] clears labels
	cat2 := e.do("POST", "/api/v1/categories", e.admin, map[string]any{"name": "Live"}).str("data", "id")
	lab2 := e.do("POST", "/api/v1/labels", e.admin, map[string]any{"name": "live"}).str("data", "id")
	p := e.do("PATCH", "/api/v1/products/"+pid, e.admin, map[string]any{"category_id": cat2, "label_ids": []string{lab2}, "description": "d"})
	require.Equal(t, http.StatusOK, p.Status, string(p.Raw))
	require.Equal(t, "Live", p.str("data", "category", "name"))
	require.Len(t, p.list("data", "labels"), 1)
	p = e.do("PATCH", "/api/v1/products/"+pid, e.admin, map[string]any{"stock_quantity": 9})
	require.Equal(t, "Live", p.str("data", "category", "name"), "unchanged when absent")
	require.Equal(t, "d", p.str("data", "description"))
	p = e.do("PATCH", "/api/v1/products/"+pid, e.admin, map[string]any{"category_id": nil, "description": nil, "label_ids": []string{}})
	require.Nil(t, p.get("data", "category_id"))
	require.Nil(t, p.get("data", "description"))
	require.Empty(t, p.list("data", "labels"))
	require.EqualValues(t, 9, p.get("data", "stock_quantity"))
	require.Equal(t, http.StatusBadRequest, e.do("PATCH", "/api/v1/products/"+pid, e.admin, map[string]any{}).Status)
	// PATCH to a duplicate SKU → 409
	e.createProduct(e.admin, map[string]any{"sku": "SKU-2", "name": "Two", "price": 2})
	require.Equal(t, http.StatusConflict, e.do("PATCH", "/api/v1/products/"+pid, e.admin, map[string]any{"sku": "SKU-2"}).Status)
	// PUT replaces everything, resetting omitted optional fields
	p = e.do("PUT", "/api/v1/products/"+pid, e.admin, map[string]any{"sku": "SKU-1", "name": "Replaced", "price": 3.5})
	require.Equal(t, http.StatusOK, p.Status, string(p.Raw))
	require.EqualValues(t, 0, p.get("data", "stock_quantity"))
	require.Equal(t, "DRAFT", p.str("data", "status"))
	// deleted products cannot be modified
	require.Equal(t, http.StatusNoContent, e.do("DELETE", "/api/v1/products/"+pid, e.admin, nil).Status)
	require.Equal(t, http.StatusNotFound, e.do("PATCH", "/api/v1/products/"+pid, e.admin, map[string]any{"name": "z"}).Status)
	require.Equal(t, http.StatusNotFound, e.do("DELETE", "/api/v1/products/"+pid, e.admin, nil).Status)
	// duplicate names for categories/labels → 409
	require.Equal(t, http.StatusConflict, e.do("POST", "/api/v1/categories", e.admin, map[string]any{"name": "Live"}).Status)
	require.Equal(t, http.StatusConflict, e.do("POST", "/api/v1/labels", e.admin, map[string]any{"name": "live"}).Status)
}

func TestProductSearchFilterSortPaginate(t *testing.T) {
	e := setup(t)
	catA := e.do("POST", "/api/v1/categories", e.admin, map[string]any{"name": "A"}).str("data", "id")
	catB := e.do("POST", "/api/v1/categories", e.admin, map[string]any{"name": "B"}).str("data", "id")
	lbl := e.do("POST", "/api/v1/labels", e.admin, map[string]any{"name": "hot"}).str("data", "id")
	for i := 1; i <= 25; i++ {
		body := map[string]any{"sku": fmt.Sprintf("P-%03d", i), "name": fmt.Sprintf("Item %02d", i),
			"description": "generic thing", "price": float64(i), "status": "ACTIVE", "category_id": catA}
		if i%5 == 0 {
			body["category_id"], body["status"], body["label_ids"] = catB, "INACTIVE", []string{lbl}
		}
		if i == 7 {
			body["name"], body["description"] = "100% Cotton_Shirt", "special"
		}
		require.Equal(t, http.StatusCreated, e.createProduct(e.admin, body).Status)
	}
	total := func(q string) (int, []any) {
		r := e.do("GET", "/api/v1/products"+q, e.admin, nil)
		require.Equal(t, http.StatusOK, r.Status, q+": "+string(r.Raw))
		return int(r.get("pagination", "total").(float64)), r.list("data")
	}
	n, items := total("")
	require.Equal(t, 25, n)
	require.Len(t, items, 20, "default limit 20")
	r := e.do("GET", "/api/v1/products?page=2&limit=10", e.admin, nil)
	require.EqualValues(t, 3, r.get("pagination", "total_pages"))
	require.Len(t, r.list("data"), 10)
	n, items = total("?page=3&limit=10")
	require.Equal(t, 25, n)
	require.Len(t, items, 5)

	n, _ = total("?category_id=" + catB)
	require.Equal(t, 5, n)
	n, _ = total("?label_id=" + lbl)
	require.Equal(t, 5, n)
	n, _ = total("?status=INACTIVE")
	require.Equal(t, 5, n)
	n, _ = total("?min_price=10&max_price=12")
	require.Equal(t, 3, n)
	n, _ = total("?search=P-01")
	require.Equal(t, 10, n, "SKU substring")
	n, items = total("?search=100%25")
	require.Equal(t, 1, n, "LIKE wildcards are escaped")
	require.Equal(t, "100% Cotton_Shirt", items[0].(map[string]any)["name"])
	n, _ = total("?search=cotton_s")
	require.Equal(t, 1, n)
	n, _ = total("?search=GENERIC")
	require.Equal(t, 24, n, "case-insensitive description search")
	n, _ = total("?category_id=" + catB + "&status=INACTIVE&label_id=" + lbl + "&min_price=5&max_price=15&search=item")
	require.Equal(t, 3, n)

	_, items = total("?sort_by=price&order=asc&limit=3")
	require.EqualValues(t, 1, items[0].(map[string]any)["price"])
	_, items = total("?sort_by=price&order=desc&limit=3")
	require.EqualValues(t, 25, items[0].(map[string]any)["price"])
	_, items = total("?sort_by=sku&limit=2")
	require.Equal(t, "P-001", items[0].(map[string]any)["sku"], "non-date sorts default to ascending")
	_, items = total("?limit=1")
	require.Equal(t, "P-025", items[0].(map[string]any)["sku"], "default is newest first")

	for _, q := range []string{"?limit=101", "?limit=0", "?page=0", "?page=abc", "?sort_by=password", "?sort_by=price;drop",
		"?order=sideways", "?status=LIVE", "?min_price=abc", "?min_price=10&max_price=5", "?category_id=xx", "?label_id=xx"} {
		require.Equal(t, http.StatusBadRequest, e.do("GET", "/api/v1/products"+q, e.admin, nil).Status, q)
	}
	// SQL-injection-shaped input is just data
	n, _ = total("?search=%27%3B+DROP+TABLE+products%3B--")
	require.Equal(t, 0, n)
	n, _ = total("")
	require.Equal(t, 25, n)

	// labels are attached in list results
	_, items = total("?label_id=" + lbl + "&limit=1")
	require.Len(t, items[0].(map[string]any)["labels"], 1)
}
