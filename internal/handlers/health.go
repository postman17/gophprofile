package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/postman17/gophprofile/internal/models"
)

type Checker func(ctx context.Context) error

type HealthHandler struct {
	checkers map[string]Checker
	timeout  time.Duration
}

func NewHealthHandler(checkers map[string]Checker) *HealthHandler {
	return &HealthHandler{checkers: checkers, timeout: 3 * time.Second}
}

func (h *HealthHandler) Health(c *echo.Context) error {
	ctx, cancel := context.WithTimeout(c.Request().Context(), h.timeout)
	defer cancel()

	components := make(map[string]string, len(h.checkers))
	status := "ok"
	code := http.StatusOK

	for name, check := range h.checkers {
		if check == nil {
			components[name] = "skipped"
			continue
		}
		if err := check(ctx); err != nil {
			components[name] = "error: " + err.Error()
			status = "degraded"
			code = http.StatusServiceUnavailable
			continue
		}
		components[name] = "ok"
	}

	return c.JSON(code, models.HealthResponse{Status: status, Components: components})
}
