package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestHTTPPropagationAndFinalStatus(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	oldProvider, oldPropagator := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(oldProvider)
		otel.SetTextMapPropagator(oldPropagator)
		_ = provider.Shutdown(context.Background())
	})
	e := echo.New()
	e.Use(HTTP)
	e.GET("/avatars/:id", func(c *echo.Context) error { return echo.NewHTTPError(http.StatusServiceUnavailable, "unavailable") })
	before := testutil.ToFloat64(HTTPRequests.WithLabelValues("GET", "/avatars/:id", "503"))
	req := httptest.NewRequest(http.MethodGet, "/avatars/private-id", nil)
	req.Header.Set("traceparent", "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
	response := httptest.NewRecorder()
	e.ServeHTTP(response, req)
	if response.Code != 503 {
		t.Fatalf("status = %d", response.Code)
	}
	if got := response.Header().Get("X-Trace-ID"); got != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("trace ID = %q", got)
	}
	if got := testutil.ToFloat64(HTTPRequests.WithLabelValues("GET", "/avatars/:id", "503")); got != before+1 {
		t.Fatalf("counter = %v", got)
	}
	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Parent().SpanID().String() != "0123456789abcdef" || spans[0].SpanKind() != trace.SpanKindServer {
		t.Fatalf("unexpected spans: %v", spans)
	}
	// Unmatched paths must share one label, regardless of the arbitrary URL.
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/random/private-user", nil))
	if testutil.ToFloat64(HTTPRequests.WithLabelValues("GET", "unmatched", "404")) < 1 {
		t.Fatal("missing unmatched metric")
	}
}

func TestLoggerCorrelatesContextAfterWith(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(&traceHandler{Handler: slog.NewJSONHandler(&output, nil)}).With("service", "worker").WithGroup("event")
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}})
	logger.InfoContext(trace.ContextWithSpanContext(context.Background(), sc), "processed", "avatar_id", "a")
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	event := record["event"].(map[string]any)
	if event["trace_id"] != sc.TraceID().String() || event["span_id"] != sc.SpanID().String() {
		t.Fatalf("no correlation: %s", output.String())
	}
}
