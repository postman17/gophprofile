package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/require"

	"github.com/postman17/gophprofile/internal/handlers"
	"github.com/postman17/gophprofile/internal/imaging"
	"github.com/postman17/gophprofile/internal/models"
	"github.com/postman17/gophprofile/internal/service"
	"github.com/postman17/gophprofile/internal/testutil"
	"github.com/postman17/gophprofile/web"
)

type testApp struct {
	e       *echo.Echo
	repo    *testutil.Repo
	storage *testutil.Storage
	pub     *testutil.Publisher
}

func newApp(t *testing.T, maxFileSize int64, dbErr error, avatars ...*models.Avatar) *testApp {
	t.Helper()

	repo := testutil.NewRepo(avatars...)
	storage := testutil.NewStorage()
	pub := &testutil.Publisher{}
	svc := service.NewAvatarService(repo, storage, pub, service.Options{MaxFileSize: maxFileSize})

	h, err := handlers.NewHandlers(svc, maxFileSize, web.Templates, map[string]handlers.Checker{
		"database": func(context.Context) error { return dbErr },
	})
	require.NoError(t, err)

	e := echo.New()
	v1 := e.Group("/api/v1")
	v1.POST("/avatars", h.Avatars.Upload)
	v1.GET("/avatars/:avatar_id", h.Avatars.Get)
	v1.GET("/avatars/:avatar_id/metadata", h.Avatars.Metadata)
	v1.DELETE("/avatars/:avatar_id", h.Avatars.Delete)
	v1.GET("/users/:user_id/avatar", h.Avatars.GetByUser)
	v1.DELETE("/users/:user_id/avatar", h.Avatars.DeleteByUser)
	v1.GET("/users/:user_id/avatars", h.Avatars.ListByUser)
	e.GET("/health", h.Health.Health)
	e.GET("/web/upload", h.Web.UploadForm)
	e.POST("/web/upload", h.Web.Upload)
	e.GET("/web/gallery/:user_id", h.Web.Gallery)

	return &testApp{e: e, repo: repo, storage: storage, pub: pub}
}

func (a *testApp) do(request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	a.e.ServeHTTP(recorder, request)
	return recorder
}

func uploadRequest(t *testing.T, userID, fileName string, data []byte) *http.Request {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", fileName)
	require.NoError(t, err)
	_, err = part.Write(data)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	request := httptest.NewRequest(http.MethodPost, "/api/v1/avatars", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if userID != "" {
		request.Header.Set("X-User-ID", userID)
	}
	return request
}

func TestUploadHandler(t *testing.T) {
	data := testutil.PNG(t, 24, 24)

	t.Run("created", func(t *testing.T) {
		app := newApp(t, 10*1024*1024, nil)

		recorder := app.do(uploadRequest(t, "user-1", "avatar.png", data))
		require.Equal(t, http.StatusCreated, recorder.Code)

		var payload models.AvatarResponse
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
		require.NotEmpty(t, payload.ID)
		require.Equal(t, "user-1", payload.UserID)
		require.Equal(t, string(models.ProcessingStatusProcessing), payload.Status)
		require.Equal(t, "/api/v1/avatars/"+payload.ID, payload.URL)
	})

	t.Run("missing user id", func(t *testing.T) {
		app := newApp(t, 10*1024*1024, nil)
		recorder := app.do(uploadRequest(t, "", "avatar.png", data))
		require.Equal(t, http.StatusBadRequest, recorder.Code)
		require.Contains(t, recorder.Body.String(), "X-User-ID")
	})

	t.Run("invalid format", func(t *testing.T) {
		app := newApp(t, 10*1024*1024, nil)
		recorder := app.do(uploadRequest(t, "user-1", "avatar.txt", []byte("plain text")))
		require.Equal(t, http.StatusBadRequest, recorder.Code)
		require.Contains(t, recorder.Body.String(), "Invalid file format")
	})

	t.Run("storage unavailable", func(t *testing.T) {
		app := newApp(t, 10*1024*1024, nil)
		app.storage.PutErr = models.ErrStorage

		recorder := app.do(uploadRequest(t, "user-1", "avatar.png", data))
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
		require.Contains(t, recorder.Body.String(), "Storage unavailable")
	})

	t.Run("too large", func(t *testing.T) {
		app := newApp(t, int64(len(data)-1), nil)
		recorder := app.do(uploadRequest(t, "user-1", "avatar.png", data))
		require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)

		var payload models.SizeErrorResponse
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
		require.Equal(t, "File too large", payload.Error)
		require.Equal(t, int64(len(data)-1), payload.MaxSize)
	})

	t.Run("missing file field", func(t *testing.T) {
		app := newApp(t, 10*1024*1024, nil)
		request := httptest.NewRequest(http.MethodPost, "/api/v1/avatars", strings.NewReader(""))
		request.Header.Set("X-User-ID", "user-1")
		request.Header.Set("Content-Type", "multipart/form-data; boundary=xyz")

		recorder := app.do(request)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	})
}

func TestGetHandlers(t *testing.T) {
	pngData := testutil.PNG(t, 16, 16)

	t.Run("image", func(t *testing.T) {
		avatar := testutil.Avatar("avatar-1", "user-1")
		app := newApp(t, 10*1024*1024, nil, avatar)
		app.storage.Objects[avatar.S3Key] = pngData
		app.storage.Types[avatar.S3Key] = imaging.MimePNG

		recorder := app.do(httptest.NewRequest(http.MethodGet, "/api/v1/avatars/avatar-1", nil))
		require.Equal(t, http.StatusOK, recorder.Code)
		require.Equal(t, imaging.MimePNG, recorder.Header().Get("Content-Type"))
		require.Equal(t, `"etag-avatar-1"`, recorder.Header().Get("ETag"))
		require.Contains(t, recorder.Header().Get("Cache-Control"), "max-age=86400")
		require.Equal(t, pngData, recorder.Body.Bytes())
	})

	t.Run("not found", func(t *testing.T) {
		app := newApp(t, 10*1024*1024, nil)
		recorder := app.do(httptest.NewRequest(http.MethodGet, "/api/v1/avatars/missing", nil))
		require.Equal(t, http.StatusNotFound, recorder.Code)
		require.Contains(t, recorder.Body.String(), "Avatar not found")
	})

	t.Run("metadata", func(t *testing.T) {
		avatar := testutil.Avatar("avatar-1", "user-1")
		avatar.ProcessingStatus = models.ProcessingStatusCompleted
		avatar.Thumbnails = map[string]models.Thumbnail{
			models.ThumbnailSize100: {Size: models.ThumbnailSize100, S3Key: "thumb-100.jpg", MimeType: imaging.MimeJPEG, Width: 100, Height: 100},
		}
		app := newApp(t, 10*1024*1024, nil, avatar)

		recorder := app.do(httptest.NewRequest(http.MethodGet, "/api/v1/avatars/avatar-1/metadata", nil))
		require.Equal(t, http.StatusOK, recorder.Code)

		var payload models.AvatarMetadataResponse
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
		require.Equal(t, "avatar-1", payload.ID)
		require.Equal(t, "avatar.png", payload.FileName)
		require.Equal(t, 64, payload.Dimensions.Width)
		require.Len(t, payload.Thumbnails, 1)
		require.Equal(t, models.ThumbnailSize100, payload.Thumbnails[0].Size)
	})

	t.Run("list", func(t *testing.T) {
		app := newApp(t, 10*1024*1024, nil, testutil.Avatar("avatar-1", "user-1"), testutil.Avatar("avatar-2", "user-1"))

		recorder := app.do(httptest.NewRequest(http.MethodGet, "/api/v1/users/user-1/avatars", nil))
		require.Equal(t, http.StatusOK, recorder.Code)

		var payload models.AvatarListResponse
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
		require.Equal(t, "user-1", payload.UserID)
		require.Len(t, payload.Avatars, 2)
	})

	t.Run("user avatar", func(t *testing.T) {
		avatar := testutil.Avatar("avatar-1", "user-1")
		app := newApp(t, 10*1024*1024, nil, avatar)
		app.storage.Objects[avatar.S3Key] = pngData
		app.storage.Types[avatar.S3Key] = imaging.MimePNG

		recorder := app.do(httptest.NewRequest(http.MethodGet, "/api/v1/users/user-1/avatar", nil))
		require.Equal(t, http.StatusOK, recorder.Code)
		require.Equal(t, pngData, recorder.Body.Bytes())
	})
}

func TestDeleteHandlers(t *testing.T) {
	t.Run("deleted", func(t *testing.T) {
		app := newApp(t, 10*1024*1024, nil, testutil.Avatar("avatar-1", "user-1"))

		request := httptest.NewRequest(http.MethodDelete, "/api/v1/avatars/avatar-1", nil)
		request.Header.Set("X-User-ID", "user-1")

		recorder := app.do(request)
		require.Equal(t, http.StatusNoContent, recorder.Code)
		require.Len(t, app.pub.Deletes, 1)
	})

	t.Run("forbidden", func(t *testing.T) {
		app := newApp(t, 10*1024*1024, nil, testutil.Avatar("avatar-1", "user-1"))

		request := httptest.NewRequest(http.MethodDelete, "/api/v1/avatars/avatar-1", nil)
		request.Header.Set("X-User-ID", "user-2")

		recorder := app.do(request)
		require.Equal(t, http.StatusForbidden, recorder.Code)
		require.Contains(t, recorder.Body.String(), "Forbidden")
	})

	t.Run("missing header", func(t *testing.T) {
		app := newApp(t, 10*1024*1024, nil, testutil.Avatar("avatar-1", "user-1"))
		recorder := app.do(httptest.NewRequest(http.MethodDelete, "/api/v1/avatars/avatar-1", nil))
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	})

	t.Run("delete by user", func(t *testing.T) {
		app := newApp(t, 10*1024*1024, nil, testutil.Avatar("avatar-1", "user-1"))

		request := httptest.NewRequest(http.MethodDelete, "/api/v1/users/user-1/avatar", nil)
		request.Header.Set("X-User-ID", "user-1")

		recorder := app.do(request)
		require.Equal(t, http.StatusNoContent, recorder.Code)
	})
}

func TestHealthHandler(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		app := newApp(t, 10*1024*1024, nil)
		recorder := app.do(httptest.NewRequest(http.MethodGet, "/health", nil))
		require.Equal(t, http.StatusOK, recorder.Code)

		var payload models.HealthResponse
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
		require.Equal(t, "ok", payload.Status)
		require.Equal(t, "ok", payload.Components["database"])
	})

	t.Run("degraded", func(t *testing.T) {
		app := newApp(t, 10*1024*1024, errors.New("db is down"))
		recorder := app.do(httptest.NewRequest(http.MethodGet, "/health", nil))
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code)

		var payload models.HealthResponse
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
		require.Equal(t, "degraded", payload.Status)
		require.Contains(t, payload.Components["database"], "db is down")
	})
}

func TestWebHandlers(t *testing.T) {
	t.Run("upload form", func(t *testing.T) {
		app := newApp(t, 10*1024*1024, nil)
		recorder := app.do(httptest.NewRequest(http.MethodGet, "/web/upload?user_id=user-1", nil))
		require.Equal(t, http.StatusOK, recorder.Code)
		require.Contains(t, recorder.Header().Get("Content-Type"), "text/html")
		require.Contains(t, recorder.Body.String(), "Загрузка аватарки")
		require.Contains(t, recorder.Body.String(), "user-1")
	})

	t.Run("upload redirects to gallery", func(t *testing.T) {
		app := newApp(t, 10*1024*1024, nil)

		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		require.NoError(t, writer.WriteField("user_id", "user-1"))
		part, err := writer.CreateFormFile("file", "avatar.png")
		require.NoError(t, err)
		_, err = part.Write(testutil.PNG(t, 12, 12))
		require.NoError(t, err)
		require.NoError(t, writer.Close())

		request := httptest.NewRequest(http.MethodPost, "/web/upload", &body)
		request.Header.Set("Content-Type", writer.FormDataContentType())

		recorder := app.do(request)
		require.Equal(t, http.StatusSeeOther, recorder.Code)
		require.Equal(t, "/web/gallery/user-1", recorder.Header().Get("Location"))
	})

	t.Run("upload without file shows error", func(t *testing.T) {
		app := newApp(t, 10*1024*1024, nil)

		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		require.NoError(t, writer.WriteField("user_id", "user-1"))
		require.NoError(t, writer.WriteField("file", ""))

		request := httptest.NewRequest(http.MethodPost, "/web/upload", &body)
		request.Header.Set("Content-Type", writer.FormDataContentType())

		recorder := app.do(request)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
		require.Contains(t, recorder.Body.String(), "Выберите файл")
	})

	t.Run("upload without user id shows error", func(t *testing.T) {
		app := newApp(t, 10*1024*1024, nil)

		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("file", "avatar.png")
		require.NoError(t, err)
		_, err = part.Write(testutil.PNG(t, 8, 8))
		require.NoError(t, err)
		require.NoError(t, writer.Close())

		request := httptest.NewRequest(http.MethodPost, "/web/upload", &body)
		request.Header.Set("Content-Type", writer.FormDataContentType())

		recorder := app.do(request)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
		require.Contains(t, recorder.Body.String(), "Укажите User ID")
	})

	t.Run("gallery", func(t *testing.T) {
		app := newApp(t, 10*1024*1024, nil, testutil.Avatar("avatar-1", "user-1"))
		recorder := app.do(httptest.NewRequest(http.MethodGet, "/web/gallery/user-1", nil))
		require.Equal(t, http.StatusOK, recorder.Code)
		require.Contains(t, recorder.Body.String(), "Галерея user-1")
		require.Contains(t, recorder.Body.String(), "/api/v1/avatars/avatar-1?size=100x100")
	})

	t.Run("gallery empty", func(t *testing.T) {
		app := newApp(t, 10*1024*1024, nil)
		recorder := app.do(httptest.NewRequest(http.MethodGet, "/web/gallery/user-2", nil))
		require.Equal(t, http.StatusOK, recorder.Code)
		require.Contains(t, recorder.Body.String(), "нет аватарок")
	})
}
