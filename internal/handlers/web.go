package handlers

import (
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/postman17/gophprofile/internal/models"
	"github.com/postman17/gophprofile/internal/service"
)

const htmlContentType = "text/html; charset=UTF-8"

type webAvatar struct {
	ID        string
	FileName  string
	Status    string
	CreatedAt string
	ThumbURL  string
	FullURL   string
}

type webPageData struct {
	Title         string
	UserID        string
	MaxFileSizeMB int64
	Error         string
	Avatars       []webAvatar
}

type WebHandler struct {
	svc         *service.AvatarService
	maxFileSize int64
	uploadPage  *template.Template
	galleryPage *template.Template
}

func NewWebHandler(svc *service.AvatarService, maxFileSize int64, templates fs.FS) (*WebHandler, error) {
	upload, err := template.ParseFS(templates, "templates/layout.html", "templates/upload.html")
	if err != nil {
		return nil, fmt.Errorf("parse upload templates: %w", err)
	}
	gallery, err := template.ParseFS(templates, "templates/layout.html", "templates/gallery.html")
	if err != nil {
		return nil, fmt.Errorf("parse gallery templates: %w", err)
	}
	if maxFileSize <= 0 {
		maxFileSize = DefaultMaxFileSize
	}
	return &WebHandler{
		svc:         svc,
		maxFileSize: maxFileSize,
		uploadPage:  upload,
		galleryPage: gallery,
	}, nil
}

func (h *WebHandler) UploadForm(c *echo.Context) error {
	return h.render(c, h.uploadPage, http.StatusOK, webPageData{
		Title:         "Загрузка аватарки",
		UserID:        c.QueryParam("user_id"),
		MaxFileSizeMB: h.maxFileSizeMB(),
	})
}

func (h *WebHandler) Upload(c *echo.Context) error {
	userID := strings.TrimSpace(c.FormValue("user_id"))

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return h.renderUploadError(c, userID, "Выберите файл для загрузки", http.StatusBadRequest)
	}

	src, err := fileHeader.Open()
	if err != nil {
		return h.renderUploadError(c, userID, "Не удалось прочитать файл", http.StatusBadRequest)
	}
	defer func() { _ = src.Close() }()

	avatar, err := h.svc.Upload(c.Request().Context(), userID, fileHeader.Filename, src)
	if err != nil {
		return h.renderUploadError(c, userID, webErrorMessage(err), webErrorStatus(err))
	}

	return c.Redirect(http.StatusSeeOther, "/web/gallery/"+url.PathEscape(avatar.UserID))
}

func (h *WebHandler) Gallery(c *echo.Context) error {
	userID := c.Param("user_id")

	avatars, err := h.svc.List(c.Request().Context(), userID)
	if err != nil {
		return h.render(c, h.galleryPage, webErrorStatus(err), webPageData{
			Title:         "Галерея",
			UserID:        userID,
			MaxFileSizeMB: h.maxFileSizeMB(),
			Error:         webErrorMessage(err),
		})
	}

	items := make([]webAvatar, 0, len(avatars))
	for _, avatar := range avatars {
		items = append(items, webAvatar{
			ID:        avatar.ID,
			FileName:  avatar.FileName,
			Status:    string(avatar.ProcessingStatus),
			CreatedAt: avatar.CreatedAt.Format("02.01.2006 15:04"),
			ThumbURL:  models.ThumbnailURL(avatar.ID, models.ThumbnailSize100),
			FullURL:   models.AvatarURL(avatar.ID),
		})
	}

	return h.render(c, h.galleryPage, http.StatusOK, webPageData{
		Title:         "Галерея " + userID,
		UserID:        userID,
		MaxFileSizeMB: h.maxFileSizeMB(),
		Avatars:       items,
	})
}

func (h *WebHandler) renderUploadError(c *echo.Context, userID, message string, status int) error {
	return h.render(c, h.uploadPage, status, webPageData{
		Title:         "Загрузка аватарки",
		UserID:        userID,
		MaxFileSizeMB: h.maxFileSizeMB(),
		Error:         message,
	})
}

func (h *WebHandler) render(c *echo.Context, page *template.Template, status int, data webPageData) error {
	response := c.Response()
	response.Header().Set("Content-Type", htmlContentType)
	response.WriteHeader(status)
	return page.ExecuteTemplate(response, "layout", data)
}

func (h *WebHandler) maxFileSizeMB() int64 {
	return h.maxFileSize / (1024 * 1024)
}

func webErrorMessage(err error) string {
	switch {
	case errors.Is(err, models.ErrMissingUserID):
		return "Укажите User ID"
	case errors.Is(err, models.ErrInvalidFormat):
		return "Неподдерживаемый формат. Допустимы JPEG, PNG, WebP"
	case errors.Is(err, models.ErrFileTooLarge):
		return "Файл слишком большой"
	case errors.Is(err, models.ErrNotFound):
		return "Ничего не найдено"
	default:
		return "Не удалось обработать запрос, попробуйте позже"
	}
}

func webErrorStatus(err error) int {
	switch {
	case errors.Is(err, models.ErrMissingUserID), errors.Is(err, models.ErrInvalidFormat):
		return http.StatusBadRequest
	case errors.Is(err, models.ErrFileTooLarge):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, models.ErrNotFound):
		return http.StatusNotFound
	default:
		return http.StatusInternalServerError
	}
}
