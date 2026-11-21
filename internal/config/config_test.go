package config

import (
	"strings"
	"testing"
)

func TestLoadValidation(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("JWT_ACCESS_SECRET", "short")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") || !strings.Contains(err.Error(), "JWT_ACCESS_SECRET") {
		t.Fatalf("expected both errors, got %v", err)
	}
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("JWT_ACCESS_SECRET", strings.Repeat("s", 40))
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.StorageDriver != "local" || c.MaxUploadSize != 5*1024*1024 || c.MaxImagesPerProduct != 5 || !c.DocsEnabled {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	t.Setenv("APP_ENV", "production")
	t.Setenv("JWT_ACCESS_SECRET", "replace-with-a-long-random-secret-value-xx")
	if _, err = Load(); err == nil {
		t.Fatal("placeholder secret must be rejected in production")
	}
	t.Setenv("JWT_ACCESS_SECRET", strings.Repeat("s", 40))
	t.Setenv("STORAGE_DRIVER", "s3")
	if _, err = Load(); err == nil {
		t.Fatal("s3 without credentials must fail")
	}
}
