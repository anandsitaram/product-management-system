package media

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg" // register decoders for content validation
	_ "image/png"
	"log/slog"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"pms/internal/modules/audit"
	"pms/internal/platform/storage"
	"pms/internal/shared/apperrors"
)

type Config struct {
	MaxSize       int64
	MaxPerProduct int
	URLTTL        time.Duration
}

type Service struct {
	repo  *Repository
	store storage.Storage
	audit *audit.Recorder
	cfg   Config
}

func NewService(r *Repository, s storage.Storage, a *audit.Recorder, c Config) *Service {
	return &Service{repo: r, store: s, audit: a, cfg: c}
}

type kind struct{ contentType, ext string }

// sniff identifies the image type from magic bytes, ignoring client-supplied names and MIME types.
func sniff(b []byte) (kind, bool) {
	switch {
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return kind{"image/jpeg", ".jpg"}, true
	case len(b) >= 8 && bytes.Equal(b[:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return kind{"image/png", ".png"}, true
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return kind{"image/webp", ".webp"}, true
	}
	return kind{}, false
}

const maxPixels = 50_000_000

// validateContent decodes headers (JPEG/PNG) to reject truncated/malformed files and decompression bombs.
func validateContent(k kind, data []byte) error {
	switch k.contentType {
	case "image/webp":
		// RIFF size field must be consistent with the data we received.
		if len(data) < 20 {
			return apperrors.Unprocessable("The image file is malformed")
		}
		declared := int64(data[4]) | int64(data[5])<<8 | int64(data[6])<<16 | int64(data[7])<<24
		if declared+8 > int64(len(data)) {
			return apperrors.Unprocessable("The image file is truncated or malformed")
		}
	default:
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return apperrors.Unprocessable("The image file is malformed")
		}
		if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxPixels {
			return apperrors.Unprocessable("The image dimensions are not supported")
		}
	}
	return nil
}

func cleanFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, name)
	if name == "." || name == "/" || name == "" {
		name = "image"
	}
	if utf8.RuneCountInString(name) > 200 {
		name = string([]rune(name)[:200])
	}
	return name
}

func (s *Service) decorate(ctx context.Context, imgs []Image) {
	for i := range imgs {
		u, err := s.store.SignedURL(ctx, imgs[i].ObjectKey, s.cfg.URLTTL)
		if err != nil {
			slog.Error("sign url", "image_id", imgs[i].ID, "error", err)
			continue
		}
		exp := time.Now().Add(s.cfg.URLTTL).UTC()
		imgs[i].URL, imgs[i].URLExpiresAt = u, &exp
	}
}

func (s *Service) List(ctx context.Context, productID uuid.UUID) ([]Image, error) {
	ok, err := s.repo.ProductExists(ctx, productID)
	if err != nil {
		return nil, apperrors.Internal(err)
	}
	if !ok {
		return nil, apperrors.NotFound("Product")
	}
	imgs, err := s.repo.List(ctx, productID)
	if err != nil {
		return nil, apperrors.Internal(err)
	}
	s.decorate(ctx, imgs)
	return imgs, nil
}

func (s *Service) Upload(ctx context.Context, productID uuid.UUID, filename string, data []byte, primary bool) (*Image, error) {
	if int64(len(data)) > s.cfg.MaxSize {
		return nil, apperrors.PayloadTooLarge(fmt.Sprintf("Image exceeds the maximum size of %d bytes", s.cfg.MaxSize))
	}
	if len(data) == 0 {
		return nil, apperrors.Validation([]apperrors.FieldError{{Field: "file", Message: "is required and must not be empty"}})
	}
	ok, err := s.repo.ProductExists(ctx, productID)
	if err != nil {
		return nil, apperrors.Internal(err)
	}
	if !ok {
		return nil, apperrors.NotFound("Product")
	}
	k, supported := sniff(data)
	if !supported {
		return nil, apperrors.UnsupportedMediaType("Only JPEG, PNG and WebP images are accepted")
	}
	if err := validateContent(k, data); err != nil {
		return nil, err
	}
	if n, err := s.repo.Count(ctx, productID); err != nil {
		return nil, apperrors.Internal(err)
	} else if n >= s.cfg.MaxPerProduct {
		return nil, apperrors.Unprocessable(fmt.Sprintf("A product can have at most %d images", s.cfg.MaxPerProduct))
	}

	key := fmt.Sprintf("products/%s/%s%s", productID, uuid.NewString(), k.ext)
	if err := s.store.Put(ctx, key, bytes.NewReader(data), int64(len(data)), k.contentType); err != nil {
		return nil, apperrors.Unavailable("Image storage is unavailable", err)
	}
	img, err := s.repo.Insert(ctx, Image{ProductID: productID, ObjectKey: key, OriginalFilename: cleanFilename(filename),
		ContentType: k.contentType, FileSize: int64(len(data))}, primary, s.cfg.MaxPerProduct)
	if err != nil {
		// Compensating cleanup so a failed DB write does not leave an orphaned object.
		if derr := s.store.Delete(context.WithoutCancel(ctx), key); derr != nil {
			slog.Error("orphaned object after failed insert", "key", key, "error", derr)
		}
		if err == errTooMany {
			return nil, apperrors.Unprocessable(fmt.Sprintf("A product can have at most %d images", s.cfg.MaxPerProduct))
		}
		return nil, wrap(err)
	}
	s.audit.Record(ctx, audit.Entry{Action: "image.upload", EntityType: "product_image", EntityID: &img.ID,
		Changes: map[string]any{"product_id": productID, "content_type": img.ContentType, "file_size": img.FileSize, "is_primary": img.IsPrimary}})
	out := []Image{*img}
	s.decorate(ctx, out)
	return &out[0], nil
}

func wrap(err error) error {
	if _, ok := err.(*apperrors.Error); ok {
		return err
	}
	return apperrors.Internal(err)
}

func (s *Service) SetPrimary(ctx context.Context, productID, imageID uuid.UUID) (*Image, error) {
	img, err := s.repo.SetPrimary(ctx, productID, imageID)
	if err != nil {
		return nil, wrap(err)
	}
	s.audit.Record(ctx, audit.Entry{Action: "image.set_primary", EntityType: "product_image", EntityID: &imageID,
		Changes: map[string]any{"product_id": productID}})
	out := []Image{*img}
	s.decorate(ctx, out)
	return &out[0], nil
}

func (s *Service) Delete(ctx context.Context, productID, imageID uuid.UUID) error {
	img, err := s.repo.Delete(ctx, productID, imageID)
	if err != nil {
		return wrap(err)
	}
	// Metadata is gone; removing the object is best-effort and orphans are found by `cmd/reconcile`.
	if err := s.store.Delete(context.WithoutCancel(ctx), img.ObjectKey); err != nil {
		slog.Error("object delete failed; run reconcile", "key", img.ObjectKey, "error", err)
	}
	s.audit.Record(ctx, audit.Entry{Action: "image.delete", EntityType: "product_image", EntityID: &imageID,
		Changes: map[string]any{"product_id": productID, "was_primary": img.IsPrimary}})
	return nil
}
