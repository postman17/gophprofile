package handlers

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/postman17/gophprofile/internal/models"
	"github.com/postman17/gophprofile/internal/service"
)

const (
	userIDHeader          = "X-User-ID"
	DefaultMaxFileSize    = 10 * 1024 * 1024
	cacheControlImage     = "public, max-age=86400"
	supportedFormatsHuman = "jpeg, png, webp"
	supportedSizesHuman   = "100x100, 300x300, original"
)

type AvatarHandler struct {
	svc         *service.AvatarService
	maxFileSize int64
	logger      *slog.Logger
}

func NewAvatarHandler(svc *service.AvatarService, maxFileSize int64) *AvatarHandler {
	if maxFileSize <= 0 {
		maxFileSize = DefaultMaxFileSize
	}
	return &AvatarHandler{svc: svc, maxFileSize: maxFileSize, logger: slog.Default()}
}

func (h *AvatarHandler) Upload(c *echo.Context) error {
	userID := c.Request().Header.Get(userIDHeader)
	if strings.TrimSpace(userID) == "" {
		return c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "Missing required header: " + userIDHeader,
		})
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		if echo.StatusCode(err) == http.StatusRequestEntityTooLarge {
			return err
		}
		return c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid request",
			Details: "Make sure 'file' field is present in multipart/form-data",
		})
	}

	src, err := fileHeader.Open()
	if err != nil {
		return c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid request",
			Details: "Cannot read uploaded file",
		})
	}
	defer func() { _ = src.Close() }()

	avatar, err := h.svc.Upload(c.Request().Context(), userID, fileHeader.Filename, src)
	if err != nil {
		return h.writeError(c, err)
	}
	return c.JSON(http.StatusCreated, models.NewAvatarResponse(avatar))
}

func (h *AvatarHandler) Get(c *echo.Context) error {
	img, err := h.svc.Open(c.Request().Context(), c.Param("avatar_id"), c.QueryParam("size"), c.QueryParam("format"))
	if err != nil {
		return h.writeError(c, err)
	}
	return h.writeImage(c, img)
}

func (h *AvatarHandler) GetByUser(c *echo.Context) error {
	img, err := h.svc.OpenByUser(c.Request().Context(), c.Param("user_id"), c.QueryParam("size"), c.QueryParam("format"))
	if err != nil {
		return h.writeError(c, err)
	}
	return h.writeImage(c, img)
}

func (h *AvatarHandler) Metadata(c *echo.Context) error {
	avatar, err := h.svc.Get(c.Request().Context(), c.Param("avatar_id"))
	if err != nil {
		return h.writeError(c, err)
	}
	return c.JSON(http.StatusOK, models.NewAvatarMetadataResponse(avatar))
}

func (h *AvatarHandler) ListByUser(c *echo.Context) error {
	userID := c.Param("user_id")
	avatars, err := h.svc.List(c.Request().Context(), userID)
	if err != nil {
		return h.writeError(c, err)
	}
	return c.JSON(http.StatusOK, models.NewAvatarListResponse(userID, avatars))
}

func (h *AvatarHandler) Delete(c *echo.Context) error {
	userID := c.Request().Header.Get(userIDHeader)
	if strings.TrimSpace(userID) == "" {
		return c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "Missing required header: " + userIDHeader,
		})
	}
	if err := h.svc.Delete(c.Request().Context(), c.Param("avatar_id"), userID); err != nil {
		return h.writeError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *AvatarHandler) DeleteByUser(c *echo.Context) error {
	userID := c.Request().Header.Get(userIDHeader)
	if strings.TrimSpace(userID) == "" {
		return c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "Missing required header: " + userIDHeader,
		})
	}
	if err := h.svc.DeleteByUser(c.Request().Context(), c.Param("user_id"), userID); err != nil {
		return h.writeError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *AvatarHandler) writeImage(c *echo.Context, img *service.Image) error {
	defer func() { _ = img.Reader.Close() }()

	response := c.Response()
	headers := response.Header()
	headers.Set("Content-Type", img.MimeType)
	headers.Set("Cache-Control", cacheControlImage)
	if img.ETag != "" {
		headers.Set("ETag", `"`+img.ETag+`"`)
	}
	if img.Size > 0 {
		headers.Set("Content-Length", strconv.FormatInt(img.Size, 10))
	}
	response.WriteHeader(http.StatusOK)

	if _, err := io.Copy(response, img.Reader); err != nil {
		h.logger.Error("failed to stream image", "error", err, "path", c.Request().URL.Path)
	}
	return nil
}

func (h *AvatarHandler) writeError(c *echo.Context, err error) error {
	switch {
	case errors.Is(err, models.ErrNotFound):
		return c.JSON(http.StatusNotFound, models.ErrorResponse{Error: "Avatar not found"})
	case errors.Is(err, models.ErrForbidden):
		return c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error:   "Forbidden",
			Details: "You can only delete your own avatars",
		})
	case errors.Is(err, models.ErrMissingUserID):
		return c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "Missing required header: " + userIDHeader,
		})
	case errors.Is(err, models.ErrInvalidFormat):
		return c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid file format",
			Details: "Supported formats: " + supportedFormatsHuman,
		})
	case errors.Is(err, models.ErrFileTooLarge):
		return c.JSON(http.StatusRequestEntityTooLarge, models.SizeErrorResponse{
			Error:   "File too large",
			MaxSize: h.maxFileSize,
		})
	case errors.Is(err, models.ErrNotReady):
		return c.JSON(http.StatusConflict, models.ErrorResponse{
			Error:   "Avatar is still processing",
			Details: "Thumbnails are not ready yet, try again later",
		})
	case errors.Is(err, models.ErrInvalidSize):
		return c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid size",
			Details: "Supported sizes: " + supportedSizesHuman,
		})
	case errors.Is(err, models.ErrUnsupportedFormat):
		return c.JSON(http.StatusUnsupportedMediaType, models.ErrorResponse{
			Error:   "Unsupported output format",
			Details: "Supported output formats: jpeg, png",
		})
	default:
		h.logger.Error("request failed", "error", err, "path", c.Request().URL.Path)
		return c.JSON(http.StatusInternalServerError, models.ErrorResponse{Error: "Internal server error"})
	}
}
