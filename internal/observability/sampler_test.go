package observability

import (
	"context"
	"encoding/binary"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestSamplerConfiguration(t *testing.T) {
	for _, test := range []struct {
		name, ratio string
		sampled     bool
	}{
		{"always_on", "", true}, {"always_off", "", false},
		{"traceidratio", "0", false}, {"traceidratio", "1", true},
		{"parentbased_traceidratio", "0", false}, {"parentbased_traceidratio", "1", true},
		{"parentbased_always_on", "", true}, {"parentbased_always_off", "", false}, {"", "", true},
	} {
		t.Run(test.name+test.ratio, func(t *testing.T) {
			t.Setenv("OTEL_TRACES_SAMPLER", test.name)
			t.Setenv("OTEL_TRACES_SAMPLER_ARG", test.ratio)
			sampler, err := samplerFromEnv()
			if err != nil {
				t.Fatal(err)
			}
			result := sampler.ShouldSample(sdktrace.SamplingParameters{ParentContext: context.Background(), TraceID: trace.TraceID{1}})
			if (result.Decision == sdktrace.RecordAndSample) != test.sampled {
				t.Fatalf("decision = %v", result.Decision)
			}
		})
	}
	for _, value := range []string{"-0.1", "1.1", "NaN", "+Inf", "invalid"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("OTEL_TRACES_SAMPLER", "parentbased_traceidratio")
			t.Setenv("OTEL_TRACES_SAMPLER_ARG", value)
			if _, err := samplerFromEnv(); err == nil {
				t.Fatal("expected invalid ratio error")
			}
		})
	}
	t.Setenv("OTEL_TRACES_SAMPLER", "unsupported")
	if _, err := samplerFromEnv(); err == nil {
		t.Fatal("expected unsupported sampler error")
	}
}

func TestParentBasedSamplingRatioAndPropagation(t *testing.T) {
	t.Setenv("OTEL_TRACES_SAMPLER", "parentbased_traceidratio")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "0.1")
	sampler, err := samplerFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	// Distribute trace IDs uniformly over the SDK's 63-bit sampling space.
	// Unlike a random test, the expected fraction is reproducible.
	sampled := 0
	const total = 10000
	for i := 0; i < total; i++ {
		id := trace.TraceID{1}
		binary.BigEndian.PutUint64(id[8:], uint64(i)*(1<<63/total)<<1)
		result := sampler.ShouldSample(sdktrace.SamplingParameters{ParentContext: context.Background(), TraceID: id})
		if result.Decision == sdktrace.RecordAndSample {
			sampled++
		}
	}
	if sampled < 999 || sampled > 1001 {
		t.Fatalf("sampled %d/%d, expected 10%%", sampled, total)
	}
	for _, sampledParent := range []bool{false, true} {
		flags := trace.TraceFlags(0)
		if sampledParent {
			flags = trace.FlagsSampled
		}
		parent := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}, TraceFlags: flags, Remote: true})
		result := sampler.ShouldSample(sdktrace.SamplingParameters{ParentContext: trace.ContextWithRemoteSpanContext(context.Background(), parent), TraceID: parent.TraceID()})
		if (result.Decision == sdktrace.RecordAndSample) != sampledParent {
			t.Fatalf("parent decision was not preserved: %v", sampledParent)
		}
	}
}

func TestInitAppliesSamplerToProvider(t *testing.T) {
	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	previousErrorHandler := otel.GetErrorHandler()
	previousLogger := slog.Default()
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
		otel.SetErrorHandler(previousErrorHandler)
		slog.SetDefault(previousLogger)
	})
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_TRACES_SAMPLER", "parentbased_traceidratio")
	for _, ratio := range []string{"0", "1"} {
		t.Run(ratio, func(t *testing.T) {
			t.Setenv("OTEL_TRACES_SAMPLER_ARG", ratio)
			shutdown, err := Init(context.Background(), "sampler-test")
			if err != nil {
				t.Fatal(err)
			}
			defer shutdown()
			_, span := otel.Tracer("test").Start(context.Background(), "root")
			defer span.End()
			if span.SpanContext().IsSampled() != (ratio == "1") {
				t.Fatalf("provider ignored sampling ratio %s", ratio)
			}
		})
	}
}
