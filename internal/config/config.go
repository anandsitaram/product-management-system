// Package config loads and validates configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Env  string
	Port string

	DatabaseURL string
	AutoMigrate bool

	JWTIssuer       string
	JWTAccessSecret string
	JWTAccessTTL    time.Duration
	JWTRefreshTTL   time.Duration

	StorageDriver         string // local | s3
	StorageLocalDir       string
	StorageEndpoint       string
	StoragePublicEndpoint string // endpoint embedded in presigned URLs (defaults to StorageEndpoint)
	StorageBucket         string
	StorageRegion         string
	StorageAccessKey      string
	StorageSecretKey      string
	StorageUsePathStyle   bool
	StorageAutoCreate     bool
	StorageSignedURLTTL   time.Duration
	PublicBaseURL         string // used for locally signed media URLs
	MaxUploadSize         int64
	MaxImagesPerProduct   int

	LogLevel string

	RateLimitAuthPerMin int // 0 disables
	TrustProxy          bool
	OpsToken            string // optional bearer token protecting /ready and /metrics
	DocsEnabled         bool

	BootstrapAdminUsername string
	BootstrapAdminEmail    string
	BootstrapAdminPassword string
}

func (c Config) IsProduction() bool { return strings.EqualFold(c.Env, "production") }

func Load() (Config, error) {
	var errs []error
	c := Config{
		Env:  get("APP_ENV", "development"),
		Port: get("APP_PORT", "8080"),

		DatabaseURL: os.Getenv("DATABASE_URL"),
		AutoMigrate: getBool("AUTO_MIGRATE", true, &errs),

		JWTIssuer:       get("JWT_ISSUER", "pms-api"),
		JWTAccessSecret: os.Getenv("JWT_ACCESS_SECRET"),
		JWTAccessTTL:    getDuration("JWT_ACCESS_TTL", 15*time.Minute, &errs),
		JWTRefreshTTL:   getDuration("JWT_REFRESH_TTL", 168*time.Hour, &errs),

		StorageDriver:         strings.ToLower(get("STORAGE_DRIVER", "local")),
		StorageLocalDir:       get("STORAGE_LOCAL_DIR", "./data/uploads"),
		StorageEndpoint:       os.Getenv("STORAGE_ENDPOINT"),
		StoragePublicEndpoint: os.Getenv("STORAGE_PUBLIC_ENDPOINT"),
		StorageBucket:         get("STORAGE_BUCKET", "pms-images"),
		StorageRegion:         get("STORAGE_REGION", "us-east-1"),
		StorageAccessKey:      os.Getenv("STORAGE_ACCESS_KEY"),
		StorageSecretKey:      os.Getenv("STORAGE_SECRET_KEY"),
		StorageUsePathStyle:   getBool("STORAGE_USE_PATH_STYLE", true, &errs),
		StorageAutoCreate:     getBool("STORAGE_AUTO_CREATE_BUCKET", false, &errs),
		StorageSignedURLTTL:   getDuration("STORAGE_SIGNED_URL_TTL", 15*time.Minute, &errs),
		PublicBaseURL:         strings.TrimRight(get("PUBLIC_BASE_URL", ""), "/"),
		MaxUploadSize:         getInt64("MAX_UPLOAD_SIZE", 5*1024*1024, &errs),
		MaxImagesPerProduct:   int(getInt64("MAX_IMAGES_PER_PRODUCT", 5, &errs)),

		LogLevel: get("LOG_LEVEL", "info"),

		RateLimitAuthPerMin: int(getInt64("RATE_LIMIT_AUTH_PER_MIN", 10, &errs)),
		TrustProxy:          getBool("TRUST_PROXY", false, &errs),
		OpsToken:            os.Getenv("OPS_TOKEN"),

		BootstrapAdminUsername: os.Getenv("BOOTSTRAP_ADMIN_USERNAME"),
		BootstrapAdminEmail:    os.Getenv("BOOTSTRAP_ADMIN_EMAIL"),
		BootstrapAdminPassword: os.Getenv("BOOTSTRAP_ADMIN_PASSWORD"),
	}
	c.DocsEnabled = getBool("DOCS_ENABLED", !c.IsProduction(), &errs)
	if c.PublicBaseURL == "" {
		c.PublicBaseURL = "http://localhost:" + c.Port
	}
	if c.StoragePublicEndpoint == "" {
		c.StoragePublicEndpoint = c.StorageEndpoint
	}

	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}
	if len(c.JWTAccessSecret) < 32 {
		errs = append(errs, errors.New("JWT_ACCESS_SECRET is required and must be at least 32 characters"))
	}
	if c.IsProduction() && strings.HasPrefix(c.JWTAccessSecret, "replace-with") {
		errs = append(errs, errors.New("JWT_ACCESS_SECRET still holds the example placeholder"))
	}
	switch c.StorageDriver {
	case "local":
	case "s3":
		if c.StorageBucket == "" || c.StorageAccessKey == "" || c.StorageSecretKey == "" {
			errs = append(errs, errors.New("STORAGE_BUCKET, STORAGE_ACCESS_KEY and STORAGE_SECRET_KEY are required when STORAGE_DRIVER=s3"))
		}
	default:
		errs = append(errs, fmt.Errorf("STORAGE_DRIVER must be 'local' or 's3', got %q", c.StorageDriver))
	}
	if c.MaxUploadSize <= 0 {
		errs = append(errs, errors.New("MAX_UPLOAD_SIZE must be positive"))
	}
	if c.MaxImagesPerProduct <= 0 {
		errs = append(errs, errors.New("MAX_IMAGES_PER_PRODUCT must be positive"))
	}
	return c, errors.Join(errs...)
}

func get(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func getBool(key string, def bool, errs *[]error) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: invalid boolean %q", key, v))
		return def
	}
	return b
}

func getInt64(key string, def int64, errs *[]error) int64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: invalid integer %q", key, v))
		return def
	}
	return n
}

func getDuration(key string, def time.Duration, errs *[]error) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		*errs = append(*errs, fmt.Errorf("%s: invalid duration %q", key, v))
		return def
	}
	return d
}
