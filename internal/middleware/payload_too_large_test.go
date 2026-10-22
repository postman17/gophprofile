package middleware_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/require"

	"github.com/postman17/gophprofile/internal/middleware"
)

func TestPayloadTooLargeWritesCustomResponse(t *testing.T) {
	e := echo.New()
	e.POST("/upload", func(*echo.Context) error {
		return echo.ErrStatusRequestEntityTooLarge
	}, middleware.PayloadTooLarge(1024))

	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/upload", nil))

	require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
	require.JSONEq(t, `{"error":"File too large","max_size":1024}`, recorder.Body.String())
}

func TestPayloadTooLargePassesSuccessThrough(t *testing.T) {
	e := echo.New()
	e.POST("/upload", func(c *echo.Context) error {
		return c.NoContent(http.StatusCreated)
	}, middleware.PayloadTooLarge(1024))

	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/upload", nil))

	require.Equal(t, http.StatusCreated, recorder.Code)
	require.Empty(t, recorder.Body.String())
}

func TestPayloadTooLargePassesOtherErrorsThrough(t *testing.T) {
	e := echo.New()
	e.POST("/upload", func(*echo.Context) error {
		return errors.New("boom")
	}, middleware.PayloadTooLarge(1024))

	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/upload", nil))

	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	require.Contains(t, recorder.Body.String(), "Internal Server Error")
}
