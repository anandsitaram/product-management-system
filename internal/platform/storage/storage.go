// Package storage defines the object-storage abstraction and the local-disk driver.
// The S3-compatible driver lives in the storage/s3 sub-package.
package storage

import (
	"context"
	"io"
	"time"
)

type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
}

type Storage interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Delete(ctx context.Context, key string) error
	// SignedURL returns a short-lived URL granting read access to key.
	SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error)
	// List calls fn for every object whose key starts with prefix.
	List(ctx context.Context, prefix string, fn func(Object) error) error
	Ping(ctx context.Context) error
}
