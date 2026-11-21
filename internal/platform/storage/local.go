package storage

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Local stores objects on disk and serves them through HMAC-signed, expiring URLs.
type Local struct {
	dir     string
	baseURL string
	secret  []byte
}

const LocalRoutePrefix = "/media/files/"

func NewLocal(dir, publicBaseURL, secret string) (*Local, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return nil, err
	}
	return &Local{dir: abs, baseURL: strings.TrimRight(publicBaseURL, "/"), secret: []byte("media-url:" + secret)}, nil
}

func (l *Local) resolve(key string) (string, error) {
	clean := path.Clean("/" + key)
	if clean == "/" || strings.Contains(key, "..") || strings.ContainsRune(key, '\\') {
		return "", errors.New("invalid object key")
	}
	return filepath.Join(l.dir, filepath.FromSlash(clean)), nil
}

func (l *Local) Put(ctx context.Context, key string, r io.Reader, _ int64, _ string) error {
	p, err := l.resolve(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

func (l *Local) Delete(ctx context.Context, key string) error {
	p, err := l.resolve(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (l *Local) sign(key string, exp int64) string {
	m := hmac.New(sha256.New, l.secret)
	fmt.Fprintf(m, "%s|%d", key, exp)
	return hex.EncodeToString(m.Sum(nil))
}

func (l *Local) SignedURL(_ context.Context, key string, ttl time.Duration) (string, error) {
	exp := time.Now().Add(ttl).Unix()
	q := url.Values{"exp": {strconv.FormatInt(exp, 10)}, "sig": {l.sign(key, exp)}}
	return l.baseURL + LocalRoutePrefix + key + "?" + q.Encode(), nil
}

func (l *Local) List(ctx context.Context, prefix string, fn func(Object) error) error {
	return filepath.WalkDir(l.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), ".upload-") {
			return nil
		}
		rel, err := filepath.Rel(l.dir, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if !strings.HasPrefix(key, prefix) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return fn(Object{Key: key, Size: info.Size(), LastModified: info.ModTime()})
	})
}

func (l *Local) Ping(context.Context) error {
	info, err := os.Stat(l.dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("storage path is not a directory")
	}
	return nil
}

// Handler serves GET {LocalRoutePrefix}{key}?exp=&sig= after verifying the signature.
func (l *Local) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, LocalRoutePrefix)
		exp, err := strconv.ParseInt(r.URL.Query().Get("exp"), 10, 64)
		sig := r.URL.Query().Get("sig")
		if err != nil || time.Now().Unix() > exp || !hmac.Equal([]byte(sig), []byte(l.sign(key, exp))) {
			http.Error(w, "invalid or expired link", http.StatusForbidden)
			return
		}
		p, err := l.resolve(key)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		f, err := os.Open(p)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if ct := mime.TypeByExtension(filepath.Ext(p)); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "private, max-age=60")
		http.ServeContent(w, r, "", info.ModTime(), f)
	})
}
