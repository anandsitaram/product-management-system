package server

import (
	"context"
	"fmt"

	"pms/internal/config"
	"pms/internal/platform/storage"
	"pms/internal/platform/storage/s3store"
)

// NewStorage builds the configured object-storage driver (local disk or S3-compatible).
func NewStorage(ctx context.Context, cfg config.Config) (storage.Storage, error) {
	switch cfg.StorageDriver {
	case "s3":
		return s3store.New(ctx, s3store.Config{
			Endpoint: cfg.StorageEndpoint, PublicEndpoint: cfg.StoragePublicEndpoint, Bucket: cfg.StorageBucket,
			Region: cfg.StorageRegion, AccessKey: cfg.StorageAccessKey, SecretKey: cfg.StorageSecretKey,
			PathStyle: cfg.StorageUsePathStyle, AutoCreate: cfg.StorageAutoCreate,
		})
	case "local":
		return storage.NewLocal(cfg.StorageLocalDir, cfg.PublicBaseURL, cfg.JWTAccessSecret)
	}
	return nil, fmt.Errorf("unsupported storage driver %q", cfg.StorageDriver)
}
