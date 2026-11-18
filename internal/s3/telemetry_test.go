package s3

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestS3SpansUseAWSAttributes(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous); _ = provider.Shutdown(context.Background()) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, "image")
		}
	}))
	defer server.Close()
	ctx := context.Background()
	store, err := NewS3Client(ctx, server.URL, "access", "secret", "avatars")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.EnsureBucket(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.Put(ctx, "avatar.png", "image/png", strings.NewReader("image"), 5); err != nil {
		t.Fatal(err)
	}
	reader, _, err := store.Get(ctx, "avatar.png")
	if err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	if err = store.Delete(ctx, "avatar.png"); err != nil {
		t.Fatal(err)
	}
	if err = store.Health(ctx); err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{"s3.EnsureBucket": "HeadBucket", "s3.Put": "PutObject", "s3.Get": "GetObject", "s3.Delete": "DeleteObject", "s3.Health": "HeadBucket"}
	for _, span := range recorder.Ended() {
		method, ok := expected[span.Name()]
		if !ok {
			continue
		}
		attributes := map[string]string{}
		for _, attr := range span.Attributes() {
			key := string(attr.Key)
			if strings.HasPrefix(key, "db.") {
				t.Errorf("S3 span has database attribute %s", key)
			}
			attributes[key] = attr.Value.AsString()
		}
		if attributes["rpc.system"] != "aws-api" || attributes["rpc.service"] != "S3" || attributes["rpc.method"] != method || attributes["aws.s3.bucket"] != "avatars" {
			t.Errorf("unexpected attributes for %s: %v", span.Name(), attributes)
		}
		delete(expected, span.Name())
	}
	if len(expected) != 0 {
		t.Fatalf("missing S3 spans: %v", expected)
	}
}
