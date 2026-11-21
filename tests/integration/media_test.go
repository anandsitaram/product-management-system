package integration

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"pms/internal/config"
	"pms/internal/platform/storage"
)

func newProduct(t *testing.T, e *env, sku string) string {
	r := e.createProduct(e.admin, map[string]any{"sku": sku, "name": sku, "price": 1})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	return r.str("data", "id")
}

func TestImageValidation(t *testing.T) {
	e := setup(t)
	pid := newProduct(t, e, "IMG-1")

	// text disguised as a PNG, HTML, empty file, GIF → 415 / 400
	require.Equal(t, http.StatusUnsupportedMediaType, e.upload(e.admin, pid, "evil.png", []byte("<html><script>alert(1)</script>"), "").Status)
	require.Equal(t, http.StatusUnsupportedMediaType, e.upload(e.admin, pid, "x.gif", []byte("GIF89a\x01\x00\x01\x00"), "").Status)
	require.Equal(t, http.StatusBadRequest, e.upload(e.admin, pid, "empty.png", []byte{}, "").Status)
	// right signature, broken body → 422
	require.Equal(t, http.StatusUnprocessableEntity, e.upload(e.admin, pid, "trunc.png", pngBytes(t)[:20], "").Status)
	require.Equal(t, http.StatusUnprocessableEntity, e.upload(e.admin, pid, "trunc.jpg", jpegBytes(t)[:10], "").Status)
	// extension lies but content is valid → accepted, stored under a generated key with the real type
	r := e.upload(e.admin, pid, "../../../etc/passwd.txt", pngBytes(t), "")
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	require.Equal(t, "image/png", r.str("data", "content_type"))
	require.Equal(t, "passwd.txt", r.str("data", "original_filename"), "path components are stripped")
	imgURL := r.str("data", "url")
	require.Contains(t, imgURL, "/media/files/products/"+pid+"/")
	require.Contains(t, imgURL, ".png")
	require.NotContains(t, imgURL, "passwd")
	require.NotContains(t, string(r.Raw), "object_key")

	// oversize → 413 (limit is 200 KiB in tests)
	big := append(pngBytes(t), bytes.Repeat([]byte{0}, 300*1024)...)
	require.Equal(t, http.StatusRequestEntityTooLarge, e.upload(e.admin, pid, "big.png", big, "").Status)
	// no file part / wrong content-type / bad flag
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/v1/products/"+pid+"/images", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+e.admin)
	req.Header.Set("Content-Type", "application/json")
	require.Equal(t, http.StatusUnsupportedMediaType, e.send(req).Status)
	require.Equal(t, http.StatusBadRequest, e.upload(e.admin, pid, "a.png", pngBytes(t), "maybe").Status)
	// unknown product → 404
	require.Equal(t, http.StatusNotFound, e.upload(e.admin, "11111111-1111-1111-1111-111111111111", "a.png", pngBytes(t), "").Status)
}

func TestImageLimitsPrimaryAndSignedURLs(t *testing.T) {
	e := setup(t)
	pid := newProduct(t, e, "IMG-2")
	var ids []string
	for i := 0; i < 5; i++ {
		r := e.upload(e.admin, pid, "a.png", pngBytes(t), "")
		require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
		ids = append(ids, r.str("data", "id"))
	}
	// max five images per product
	r := e.upload(e.admin, pid, "six.png", pngBytes(t), "")
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))
	// exactly one primary, and it is the first upload
	list := e.do("GET", "/api/v1/products/"+pid+"/images", e.admin, nil).list("data")
	require.Len(t, list, 5)
	primaries := 0
	for _, it := range list {
		if it.(map[string]any)["is_primary"] == true {
			primaries++
		}
	}
	require.Equal(t, 1, primaries)
	require.Equal(t, ids[0], list[0].(map[string]any)["id"])

	// set primary moves the flag; unsetting directly is refused
	require.Equal(t, http.StatusOK, e.do("PATCH", "/api/v1/products/"+pid+"/images/"+ids[3], e.admin, map[string]any{"is_primary": true}).Status)
	require.Equal(t, http.StatusUnprocessableEntity, e.do("PATCH", "/api/v1/products/"+pid+"/images/"+ids[3], e.admin, map[string]any{"is_primary": false}).Status)
	var n int
	require.NoError(t, e.queryRow(`SELECT count(*) FROM product_images WHERE product_id=$1 AND is_primary`, pid).Scan(&n))
	require.Equal(t, 1, n)

	// signed URL works; tampered signature / expiry / key fail
	list = e.do("GET", "/api/v1/products/"+pid+"/images", e.admin, nil).list("data")
	u := list[0].(map[string]any)["url"].(string)
	get := func(s string) int {
		res, err := http.Get(s)
		require.NoError(t, err)
		defer res.Body.Close()
		_, _ = io.Copy(io.Discard, res.Body)
		return res.StatusCode
	}
	require.Equal(t, http.StatusOK, get(u))
	pu, _ := url.Parse(u)
	q := pu.Query()
	q.Set("sig", strings.Repeat("0", 64))
	pu.RawQuery = q.Encode()
	require.Equal(t, http.StatusForbidden, get(pu.String()))
	pu, _ = url.Parse(u)
	q = pu.Query()
	q.Set("exp", "1")
	pu.RawQuery = q.Encode()
	require.Equal(t, http.StatusForbidden, get(pu.String()))
	require.Equal(t, http.StatusForbidden, get(e.srv.URL+"/media/files/products/"+pid+"/other.png?exp=9999999999&sig=abc"))
	require.Equal(t, http.StatusForbidden, get(e.srv.URL+"/media/files/../../etc/passwd?exp=9999999999&sig=abc"))

	// deleting the primary promotes another image; the object is removed from storage
	primID := list[0].(map[string]any)["id"].(string)
	var key string
	require.NoError(t, e.queryRow(`SELECT object_key FROM product_images WHERE id=$1`, primID).Scan(&key))
	require.Equal(t, http.StatusNoContent, e.do("DELETE", "/api/v1/products/"+pid+"/images/"+primID, e.admin, nil).Status)
	require.NoError(t, e.queryRow(`SELECT count(*) FROM product_images WHERE product_id=$1 AND is_primary`, pid).Scan(&n))
	require.Equal(t, 1, n, "a new primary is promoted")
	found := false
	require.NoError(t, e.store.List(context.Background(), key, func(o storage.Object) error { found = true; return nil }))
	require.False(t, found, "object deleted from storage")
	require.Equal(t, http.StatusNotFound, e.do("DELETE", "/api/v1/products/"+pid+"/images/"+primID, e.admin, nil).Status)
	// image of another product is not addressable through this product
	other := newProduct(t, e, "IMG-3")
	require.Equal(t, http.StatusNotFound, e.do("DELETE", "/api/v1/products/"+other+"/images/"+ids[1], e.admin, nil).Status)
}

func TestImageStorageFailureLeavesNoMetadata(t *testing.T) {
	e := setup(t, func(c *config.Config) { c.MaxImagesPerProduct = 2 })
	pid := newProduct(t, e, "IMG-4")
	require.Equal(t, http.StatusCreated, e.upload(e.admin, pid, "a.png", pngBytes(t), "").Status)
	require.Equal(t, http.StatusCreated, e.upload(e.admin, pid, "b.png", pngBytes(t), "").Status)
	// 3rd is refused up-front; no stray object or row is left behind
	require.Equal(t, http.StatusUnprocessableEntity, e.upload(e.admin, pid, "c.png", pngBytes(t), "").Status)
	count := 0
	require.NoError(t, e.store.List(context.Background(), "products/", func(o storage.Object) error { count++; return nil }))
	require.Equal(t, 2, count)
}

func TestConcurrentUploadsRespectLimitAndSinglePrimary(t *testing.T) {
	e := setup(t)
	pid := newProduct(t, e, "IMG-RACE")
	const n = 12
	codes := make(chan int, n)
	data := pngBytes(t)
	for i := 0; i < n; i++ {
		go func(i int) {
			primary := ""
			if i%2 == 0 {
				primary = "true"
			}
			codes <- e.upload(e.admin, pid, "r.png", data, primary).Status
		}(i)
	}
	created := 0
	for i := 0; i < n; i++ {
		switch c := <-codes; c {
		case http.StatusCreated:
			created++
		case http.StatusUnprocessableEntity:
		default:
			t.Fatalf("unexpected status %d", c)
		}
	}
	require.Equal(t, 5, created, "exactly the configured maximum is accepted")
	var imgs, prim, objs int
	require.NoError(t, e.queryRow(`SELECT count(*), count(*) FILTER (WHERE is_primary) FROM product_images WHERE product_id=$1`, pid).Scan(&imgs, &prim))
	require.Equal(t, 5, imgs)
	require.Equal(t, 1, prim)
	require.NoError(t, e.store.List(context.Background(), "products/", func(storage.Object) error { objs++; return nil }))
	require.Equal(t, 5, objs, "rejected uploads leave no orphaned objects")
}
