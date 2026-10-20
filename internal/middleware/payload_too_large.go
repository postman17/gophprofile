package middleware

import (
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/postman17/gophprofile/internal/models"
)

func PayloadTooLarge(maxSize int64) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			err := next(c)
			if err == nil {
				return nil
			}
			if echo.StatusCode(err) != http.StatusRequestEntityTooLarge {
				return err
			}
			return c.JSON(http.StatusRequestEntityTooLarge, models.SizeErrorResponse{
				Error:   "File too large",
				MaxSize: maxSize,
			})
		}
	}
}
