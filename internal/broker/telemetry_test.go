package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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

type recordingProducer struct {
	sarama.SyncProducer
	messages []*sarama.ProducerMessage
	err      error
}

func (p *recordingProducer) SendMessage(message *sarama.ProducerMessage) (int32, int64, error) {
	p.messages = append(p.messages, message)
	return 0, 0, p.err
}

type recordingSession struct {
	sarama.ConsumerGroupSession
	marked bool
}

func (s *recordingSession) Context() context.Context                        { return context.Background() }
func (s *recordingSession) MarkMessage(_ *sarama.ConsumerMessage, _ string) { s.marked = true }

func TestProcessMessageSeparatesDecodeAndHandlerErrors(t *testing.T) {
	handlerFailure := errors.New("processing failed")
	dlqFailure := errors.New("dlq unavailable")
	for _, test := range []struct {
		name, body, message     string
		handlerErr, producerErr error
		wantHandler, wantMarked bool
	}{
		{"malformed", "{", "malformed kafka message", nil, nil, false, true},
		{"handler error", `{"event_id":"event-123"}`, "event handling failed", handlerFailure, nil, true, true},
		{"dlq failure", "{", "malformed kafka message", nil, dlqFailure, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&output, nil))
			producer := &recordingProducer{err: test.producerErr}
			kafka := &Kafka{config: Config{TopicUploaded: "uploaded", TopicDLQ: "dlq"}, producer: producer, logger: logger}
			called := false
			handler := &groupHandler{kafka: kafka, logger: logger, handler: func(context.Context, Event) error { called = true; return test.handlerErr }}
			session := &recordingSession{}
			message := &sarama.ConsumerMessage{Topic: "uploaded", Partition: 2, Offset: 42, Value: []byte(test.body)}
			err := handler.processMessage(session, message)
			if !errors.Is(err, test.producerErr) {
				t.Fatalf("error = %v", err)
			}
			if called != test.wantHandler || session.marked != test.wantMarked {
				t.Fatalf("handler=%v marked=%v", called, session.marked)
			}
			if len(producer.messages) != 1 || producer.messages[0].Topic != "dlq" {
				t.Fatalf("DLQ messages: %v", producer.messages)
			}
			var record map[string]any
			if err := json.Unmarshal(output.Bytes(), &record); err != nil {
				t.Fatal(err)
			}
			if record["msg"] != test.message {
				t.Fatalf("log = %s", output.String())
			}
			eventID, present := record["event_id"]
			if test.wantHandler {
				if eventID != "event-123" {
					t.Fatalf("missing event ID: %s", output.String())
				}
			} else if present {
				t.Fatalf("malformed message has event ID: %s", output.String())
			}
		})
	}
}
