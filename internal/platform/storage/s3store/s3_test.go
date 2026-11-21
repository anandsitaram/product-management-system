package s3store

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"pms/internal/platform/storage"
)

// fakeS3 is a minimal path-style S3 endpoint (HEAD/PUT bucket, PUT/GET/DELETE object, ListObjectsV2).
type fakeS3 struct {
	mu      sync.Mutex
	bucket  bool
	objects map[string][]byte
	ctypes  map[string]string
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	key := ""
	if len(parts) == 2 {
		key = parts[1]
	}
	switch {
	case key == "" && r.Method == http.MethodHead:
		if !f.bucket {
			w.WriteHeader(http.StatusNotFound)
		}
	case key == "" && r.Method == http.MethodPut:
		f.bucket = true
	case key == "" && r.Method == http.MethodGet:
		prefix := r.URL.Query().Get("prefix")
		type content struct {
			Key          string
			Size         int
			LastModified string
		}
		var res struct {
			XMLName     xml.Name `xml:"ListBucketResult"`
			IsTruncated bool
			Contents    []content
		}
		for k, v := range f.objects {
			if strings.HasPrefix(k, prefix) {
				res.Contents = append(res.Contents, content{k, len(v), time.Now().UTC().Format(time.RFC3339)})
			}
		}
		_ = xml.NewEncoder(w).Encode(res)
	case r.Method == http.MethodPut:
		b, _ := io.ReadAll(r.Body)
		f.objects[key] = b
		f.ctypes[key] = r.Header.Get("Content-Type")
	case r.Method == http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func TestStoreAgainstFakeS3(t *testing.T) {
	f := &fakeS3{objects: map[string][]byte{}, ctypes: map[string]string{}}
	ts := httptest.NewServer(f)
	defer ts.Close()
	ctx := context.Background()
	cfg := Config{Endpoint: ts.URL, PublicEndpoint: "http://public.example:9000", Bucket: "bkt", Region: "us-east-1",
		AccessKey: "ak", SecretKey: "sk", PathStyle: true}

	_, err := New(ctx, cfg)
	require.Error(t, err, "missing bucket without auto-create must fail fast")
	cfg.AutoCreate = true
	s, err := New(ctx, cfg)
	require.NoError(t, err)
	require.True(t, f.bucket, "bucket auto-created")
	require.NoError(t, s.Ping(ctx))

	data := []byte("hello-image")
	require.NoError(t, s.Put(ctx, "products/p1/a.png", bytes.NewReader(data), int64(len(data)), "image/png"))
	require.Equal(t, data, f.objects["products/p1/a.png"])
	require.Equal(t, "image/png", f.ctypes["products/p1/a.png"])

	var keys []string
	require.NoError(t, s.List(ctx, "products/", func(o storage.Object) error { keys = append(keys, o.Key); return nil }))
	require.Equal(t, []string{"products/p1/a.png"}, keys)

	signed, err := s.SignedURL(ctx, "products/p1/a.png", 5*time.Minute)
	require.NoError(t, err)
	u, err := url.Parse(signed)
	require.NoError(t, err)
	require.Equal(t, "public.example:9000", u.Host, "presigned URLs use the public endpoint")
	require.Equal(t, "/bkt/products/p1/a.png", u.Path)
	require.Equal(t, "300", u.Query().Get("X-Amz-Expires"))
	require.NotEmpty(t, u.Query().Get("X-Amz-Signature"))

	require.NoError(t, s.Delete(ctx, "products/p1/a.png"))
	require.Empty(t, f.objects)
}
