package observability

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func HTTP(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		route := c.Path()
		if route == "" {
			route = "unmatched"
		}
		method := c.Request().Method
		// Bound labels even for arbitrary HTTP method tokens.
		switch method {
		case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE":
		default:
			method = "OTHER"
		}
		parent := otel.GetTextMapPropagator().Extract(c.Request().Context(), propagation.HeaderCarrier(c.Request().Header))
		ctx, span := Start(parent, method+" "+route, trace.SpanKindServer, attribute.String("http.request.method", method), attribute.String("http.route", route))
		defer span.End()
		c.SetRequest(c.Request().WithContext(ctx))
		c.Response().Header().Set("X-Trace-ID", span.SpanContext().TraceID().String())
		started := time.Now()
		err := next(c)
		// Render Echo errors before observing the final response status.
		if err != nil {
			c.Echo().HTTPErrorHandler(c, err)
		}
		status := http.StatusOK
		if response, unwrapErr := echo.UnwrapResponse(c.Response()); unwrapErr == nil && response.Status != 0 {
			status = response.Status
		}
		span.SetAttributes(attribute.Int("http.response.status_code", status))
		if status >= 500 {
			span.SetStatus(codes.Error, http.StatusText(status))
		}
		if err != nil {
			span.RecordError(err)
		}
		HTTPRequests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
		HTTPDuration.WithLabelValues(method, route).Observe(time.Since(started).Seconds())
		level := slog.LevelInfo
		if status >= 500 {
			level = slog.LevelError
		}
		slog.Log(ctx, level, "http request", "method", method, "route", route, "status", status, "duration_seconds", time.Since(started).Seconds())
		return nil
	}
}
