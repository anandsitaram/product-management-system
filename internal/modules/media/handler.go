package media

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"pms/internal/shared/apperrors"
	"pms/internal/shared/request"
	"pms/internal/shared/response"
	"pms/internal/shared/validation"
)

type Handler struct {
	svc     *Service
	maxSize int64
}

func NewHandler(s *Service, maxSize int64) *Handler { return &Handler{svc: s, maxSize: maxSize} }

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	pid, err := request.PathUUID(r, "productId", "Product")
	if err != nil {
		response.Error(w, r, err)
		return
	}
	imgs, err := h.svc.List(r.Context(), pid)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.JSON(w, r, http.StatusOK, imgs)
}

// Upload accepts multipart/form-data with a "file" part and an optional "is_primary" field.
// The body is capped before it is read so oversized uploads never fully buffer.
func (h *Handler) Upload(w http.ResponseWriter, r *http.Request) {
	pid, err := request.PathUUID(r, "productId", "Product")
	if err != nil {
		response.Error(w, r, err)
		return
	}
	mt, _, merr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if merr != nil || mt != "multipart/form-data" {
		response.Error(w, r, apperrors.UnsupportedMediaType("Content-Type must be multipart/form-data"))
		return
	}
	const overhead = 64 << 10 // multipart framing and small fields
	if r.ContentLength > h.maxSize+overhead {
		response.Error(w, r, apperrors.PayloadTooLarge("Request body exceeds the maximum upload size"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.maxSize+overhead)
	mr, err := r.MultipartReader()
	if err != nil {
		response.Error(w, r, apperrors.BadRequest("Malformed multipart request"))
		return
	}
	var (
		data     []byte
		filename string
		haveFile bool
		primary  bool
	)
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			response.Error(w, r, bodyErr(err))
			return
		}
		switch part.FormName() {
		case "file":
			if haveFile {
				response.Error(w, r, apperrors.BadRequest("Only one file may be uploaded per request"))
				return
			}
			b, err := io.ReadAll(io.LimitReader(part, h.maxSize+1))
			if err != nil {
				response.Error(w, r, bodyErr(err))
				return
			}
			if int64(len(b)) > h.maxSize {
				response.Error(w, r, apperrors.PayloadTooLarge("Image exceeds the maximum size of "+strconv.FormatInt(h.maxSize, 10)+" bytes"))
				return
			}
			data, filename, haveFile = b, part.FileName(), true
		case "is_primary":
			b, _ := io.ReadAll(io.LimitReader(part, 16))
			val, perr := strconv.ParseBool(strings.TrimSpace(string(b)))
			if perr != nil {
				response.Error(w, r, apperrors.Validation([]apperrors.FieldError{{Field: "is_primary", Message: "must be true or false"}}))
				return
			}
			primary = val
		default:
			_, _ = io.Copy(io.Discard, io.LimitReader(part, 1<<10))
		}
	}
	if !haveFile {
		v := validation.New()
		v.Add("file", "is required (multipart field named \"file\")")
		response.Error(w, r, v.Err())
		return
	}
	img, err := h.svc.Upload(r.Context(), pid, filename, data, primary)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.JSON(w, r, http.StatusCreated, img)
}

func bodyErr(err error) error {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return apperrors.PayloadTooLarge("Request body exceeds the maximum upload size")
	}
	return apperrors.BadRequest("Malformed multipart request")
}

func (h *Handler) SetPrimary(w http.ResponseWriter, r *http.Request) {
	pid, err := request.PathUUID(r, "productId", "Product")
	if err != nil {
		response.Error(w, r, err)
		return
	}
	iid, err := request.PathUUID(r, "imageId", "Image")
	if err != nil {
		response.Error(w, r, err)
		return
	}
	var in struct {
		IsPrimary *bool `json:"is_primary"`
	}
	if err := request.DecodeJSON(w, r, &in); err != nil {
		response.Error(w, r, err)
		return
	}
	if in.IsPrimary == nil || !*in.IsPrimary {
		response.Error(w, r, apperrors.ValidationUnprocessable([]apperrors.FieldError{
			{Field: "is_primary", Message: "must be true; to change the primary image, mark another image as primary"}}))
		return
	}
	img, err := h.svc.SetPrimary(r.Context(), pid, iid)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.JSON(w, r, http.StatusOK, img)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	pid, err := request.PathUUID(r, "productId", "Product")
	if err != nil {
		response.Error(w, r, err)
		return
	}
	iid, err := request.PathUUID(r, "imageId", "Image")
	if err != nil {
		response.Error(w, r, err)
		return
	}
	if err := h.svc.Delete(r.Context(), pid, iid); err != nil {
		response.Error(w, r, err)
		return
	}
	response.NoContent(w)
}
