package broker

import (
	"context"
	"testing"

	"github.com/IBM/sarama"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func TestKafkaTraceRoundTrip(t *testing.T) {
	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1, 2, 3}, SpanID: trace.SpanID{4, 5, 6}, TraceFlags: trace.FlagsSampled})
	sent := &sarama.ProducerMessage{Headers: []sarama.RecordHeader{{Key: []byte(HeaderEventType), Value: []byte("uploaded")}}}
	injectTrace(trace.ContextWithSpanContext(context.Background(), sc), sent)
	received := &sarama.ConsumerMessage{}
	for i := range sent.Headers {
		received.Headers = append(received.Headers, &sent.Headers[i])
	}
	restored := trace.SpanContextFromContext(extractTrace(context.Background(), received))
	if restored.TraceID() != sc.TraceID() || restored.SpanID() != sc.SpanID() || !restored.IsRemote() {
		t.Fatalf("context not propagated: %v", restored)
	}
	if headerValue(received.Headers, HeaderEventType) != "uploaded" {
		t.Fatal("event header lost")
	}
}
