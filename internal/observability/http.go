package observability

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/felixge/httpsnoop"
	"github.com/labstack/echo/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type httpObservationKey struct{}
type httpObservation struct{ method, route string }

// HTTP captures Echo's route template and errors without changing the middleware
// contract. Pair it with HTTPHandler around Echo to observe the rendered response.
func HTTP(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		if observation, ok := ctx.Value(httpObservationKey{}).(*httpObservation); ok {
			if route := c.Path(); route != "" {
				observation.route = route
			}
			span := trace.SpanFromContext(ctx)
			span.SetName(observation.method + " " + observation.route)
			span.SetAttributes(attribute.String("http.route", observation.route))
		}
		err := next(c)
		if err != nil {
			trace.SpanFromContext(ctx).RecordError(err)
		}
		return err
	}
}

// HTTPHandler observes the entire request, including Echo's centralized error
// handler and outer middleware. httpsnoop preserves optional writer interfaces.
func HTTPHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.Method
		switch method {
		case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE":
		default:
			method = "OTHER"
		}
		observation := &httpObservation{method: method, route: "unmatched"}
		parent := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := Start(parent, method+" unmatched", trace.SpanKindServer, attribute.String("http.request.method", method))
		defer span.End()
		ctx = context.WithValue(ctx, httpObservationKey{}, observation)
		w.Header().Set("X-Trace-ID", span.SpanContext().TraceID().String())
		metrics := httpsnoop.CaptureMetrics(next, w, r.WithContext(ctx))
		status := metrics.Code
		span.SetAttributes(attribute.Int("http.response.status_code", status), attribute.String("http.route", observation.route))
		if status >= 500 {
			span.SetStatus(codes.Error, http.StatusText(status))
		}
		HTTPRequests.WithLabelValues(method, observation.route, strconv.Itoa(status)).Inc()
		HTTPDuration.WithLabelValues(method, observation.route).Observe(metrics.Duration.Seconds())
		level := slog.LevelInfo
		if status >= 500 {
			level = slog.LevelError
		}
		slog.Log(ctx, level, "http request", "method", method, "route", observation.route, "status", status, "duration_seconds", metrics.Duration.Seconds())
	})
}
