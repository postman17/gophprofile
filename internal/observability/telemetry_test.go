package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	HTTPHandler(e).ServeHTTP(response, req)
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
	HTTPHandler(e).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/random/private-user", nil))
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

func TestHTTPPreservesErrorContractAndObservesRenderedStatus(t *testing.T) {
	failure := echo.NewHTTPError(http.StatusBadRequest, "bad upload")
	for _, test := range []struct {
		name            string
		custom, consume bool
		expectedStatus  int
	}{
		{name: "framework handler", expectedStatus: 400},
		{name: "custom error handler", custom: true, expectedStatus: 422},
		{name: "outer middleware consumes error", consume: true, expectedStatus: 409},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := echo.New()
			var observed error
			e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
				return func(c *echo.Context) error {
					observed = next(c)
					if test.consume {
						return c.NoContent(http.StatusConflict)
					}
					return observed
				}
			}, HTTP)
			errorHandlerCalls := 0
			if test.custom {
				e.HTTPErrorHandler = func(c *echo.Context, err error) {
					errorHandlerCalls++
					if !errors.Is(err, failure) {
						t.Errorf("error handler received %v", err)
					}
					if writeErr := c.NoContent(http.StatusUnprocessableEntity); writeErr != nil {
						t.Error(writeErr)
					}
				}
			}
			e.GET("/contract", func(c *echo.Context) error { return failure })
			before := testutil.ToFloat64(HTTPRequests.WithLabelValues("GET", "/contract", strconv.Itoa(test.expectedStatus)))
			response := httptest.NewRecorder()
			HTTPHandler(e).ServeHTTP(response, httptest.NewRequest("GET", "/contract", nil))
			if !errors.Is(observed, failure) {
				t.Fatalf("outer middleware received %v", observed)
			}
			if response.Code != test.expectedStatus {
				t.Fatalf("status = %d", response.Code)
			}
			after := testutil.ToFloat64(HTTPRequests.WithLabelValues("GET", "/contract", strconv.Itoa(test.expectedStatus)))
			if after != before+1 {
				t.Fatalf("final status counted %v times", after-before)
			}
			if test.custom && errorHandlerCalls != 1 {
				t.Fatalf("error handler called %d times", errorHandlerCalls)
			}
		})
	}
}
