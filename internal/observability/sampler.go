package observability

import (
	"fmt"
	"math"
	"os"
	"strconv"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// samplerFromEnv validates configuration instead of silently falling back to
// 100% sampling when an unsupported name or invalid ratio is supplied.
func samplerFromEnv() (sdktrace.Sampler, error) {
	name := os.Getenv("OTEL_TRACES_SAMPLER")
	switch name {
	case "", "parentbased_always_on":
		return sdktrace.ParentBased(sdktrace.AlwaysSample()), nil
	case "always_on":
		return sdktrace.AlwaysSample(), nil
	case "always_off":
		return sdktrace.NeverSample(), nil
	case "parentbased_always_off":
		return sdktrace.ParentBased(sdktrace.NeverSample()), nil
	case "traceidratio", "parentbased_traceidratio":
		ratio := 1.0
		if value := os.Getenv("OTEL_TRACES_SAMPLER_ARG"); value != "" {
			var err error
			ratio, err = strconv.ParseFloat(value, 64)
			if err != nil || math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 0 || ratio > 1 {
				return nil, fmt.Errorf("OTEL_TRACES_SAMPLER_ARG must be a number between 0 and 1, got %q", value)
			}
		}
		sampler := sdktrace.TraceIDRatioBased(ratio)
		if name == "parentbased_traceidratio" {
			sampler = sdktrace.ParentBased(sampler)
		}
		return sampler, nil
	default:
		return nil, fmt.Errorf("unsupported OTEL_TRACES_SAMPLER %q", name)
	}
}
