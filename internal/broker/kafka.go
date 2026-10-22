package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/IBM/sarama"

	"github.com/postman17/gophprofile/internal/models"
)

const (
	HeaderEventType    = "event_type"
	HeaderSourceTopic  = "source_topic"
	HeaderError        = "error"
	eventHandlingLimit = 60 * time.Second
)

type Config struct {
	Brokers       []string
	TopicUploaded string
	TopicDeleted  string
	TopicDLQ      string
	ConsumerGroup string
	ClientID      string
	NumPartitions int32
}

type Event struct {
	ID    string
	Type  string
	Topic string
	Body  []byte
}

type Kafka struct {
	config   Config
	client   sarama.Client
	producer sarama.SyncProducer
	logger   *slog.Logger
}

func NewKafka(cfg Config, logger *slog.Logger) (*Kafka, error) {
	if len(cfg.Brokers) == 0 {
		return nil, errors.New("kafka: no brokers configured")
	}
	if logger == nil {
		logger = slog.Default()
	}

	saramaCfg, err := saramaConfig(cfg.ClientID)
	if err != nil {
		return nil, err
	}

	client, err := sarama.NewClient(cfg.Brokers, saramaCfg)
	if err != nil {
		return nil, fmt.Errorf("kafka client: %w", err)
	}
	producer, err := sarama.NewSyncProducerFromClient(client)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("kafka producer: %w", err)
	}

	return &Kafka{config: cfg, client: client, producer: producer, logger: logger}, nil
}

func saramaConfig(clientID string) (*sarama.Config, error) {
	cfg := sarama.NewConfig()
	cfg.ClientID = clientID
	cfg.Version = sarama.V3_6_0_0

	cfg.Producer.RequiredAcks = sarama.WaitForAll
	cfg.Producer.Retry.Max = 5
	cfg.Producer.Return.Successes = true
	cfg.Producer.Return.Errors = true

	cfg.Consumer.Return.Errors = true
	cfg.Consumer.Offsets.Initial = sarama.OffsetOldest
	cfg.Consumer.Group.Rebalance.GroupStrategies = []sarama.BalanceStrategy{sarama.NewBalanceStrategyRange()}
	cfg.Consumer.Group.Session.Timeout = 30 * time.Second
	cfg.Consumer.Group.Heartbeat.Interval = 3 * time.Second

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("kafka config: %w", err)
	}
	return cfg, nil
}

func (k *Kafka) EnsureTopics(ctx context.Context) error {
	saramaCfg, err := saramaConfig(k.config.ClientID)
	if err != nil {
		return err
	}

	adminClient, err := sarama.NewClient(k.config.Brokers, saramaCfg)
	if err != nil {
		return fmt.Errorf("kafka admin client: %w", err)
	}

	admin, err := sarama.NewClusterAdminFromClient(adminClient)
	if err != nil {
		_ = adminClient.Close()
		return fmt.Errorf("kafka admin: %w", err)
	}
	defer func() { _ = admin.Close() }()

	partitions := k.config.NumPartitions
	if partitions <= 0 {
		partitions = 3
	}

	for _, topic := range k.topics() {
		if topic == "" {
			continue
		}
		err := admin.CreateTopic(topic, &sarama.TopicDetail{
			NumPartitions:     partitions,
			ReplicationFactor: 1,
		}, false)
		if err != nil && !isTopicExists(err) {
			return fmt.Errorf("create topic %s: %w", topic, err)
		}
	}
	return nil
}

func (k *Kafka) topics() []string {
	return []string{k.config.TopicUploaded, k.config.TopicDeleted, k.config.TopicDLQ}
}

func (k *Kafka) PublishUpload(ctx context.Context, event models.AvatarUploadEvent) error {
	return k.publish(ctx, k.config.TopicUploaded, models.EventTypeUploaded, event.AvatarID, event)
}

func (k *Kafka) PublishDelete(ctx context.Context, event models.AvatarDeleteEvent) error {
	return k.publish(ctx, k.config.TopicDeleted, models.EventTypeDeleted, event.AvatarID, event)
}

func (k *Kafka) publish(ctx context.Context, topic, eventType, key string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s event: %w", eventType, err)
	}
	message := &sarama.ProducerMessage{
		Topic:     topic,
		Key:       sarama.StringEncoder(key),
		Value:     sarama.ByteEncoder(body),
		Timestamp: time.Now(),
		Headers: []sarama.RecordHeader{
			{Key: []byte(HeaderEventType), Value: []byte(eventType)},
		},
	}
	if _, _, err := k.producer.SendMessage(message); err != nil {
		return fmt.Errorf("publish to %s: %w", topic, err)
	}
	return nil
}

func (k *Kafka) Consume(ctx context.Context, handler func(context.Context, Event) error) error {
	group, err := sarama.NewConsumerGroupFromClient(k.config.ConsumerGroup, k.client)
	if err != nil {
		return fmt.Errorf("kafka consumer group: %w", err)
	}
	defer func() { _ = group.Close() }()

	topics := []string{k.config.TopicUploaded, k.config.TopicDeleted}
	groupHandler := &groupHandler{kafka: k, handler: handler, logger: k.logger}

	for {
		if err := group.Consume(ctx, topics, groupHandler); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("consume: %w", err)
		}
		if ctx.Err() != nil {
			return nil
		}
	}
}

func (k *Kafka) Health(_ context.Context) error {
	if k.client.Closed() {
		return errors.New("kafka client is closed")
	}
	if err := k.client.RefreshMetadata(); err != nil {
		return fmt.Errorf("kafka metadata refresh: %w", err)
	}
	return nil
}

func (k *Kafka) Close() error {
	var errs []error
	if err := k.producer.Close(); err != nil {
		errs = append(errs, err)
	}
	if err := k.client.Close(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (k *Kafka) sendToDLQ(ctx context.Context, original *sarama.ConsumerMessage, cause error) error {
	if k.config.TopicDLQ == "" {
		return nil
	}
	message := &sarama.ProducerMessage{
		Topic:     k.config.TopicDLQ,
		Key:       sarama.ByteEncoder(original.Key),
		Value:     sarama.ByteEncoder(original.Value),
		Timestamp: time.Now(),
		Headers: []sarama.RecordHeader{
			{Key: []byte(HeaderSourceTopic), Value: []byte(original.Topic)},
			{Key: []byte(HeaderError), Value: []byte(cause.Error())},
		},
	}
	if _, _, err := k.producer.SendMessage(message); err != nil {
		return fmt.Errorf("publish to dlq %s: %w", k.config.TopicDLQ, err)
	}
	return nil
}

func (k *Kafka) decodeEvent(message *sarama.ConsumerMessage) (Event, error) {
	var meta struct {
		EventID string `json:"event_id"`
	}
	if err := json.Unmarshal(message.Value, &meta); err != nil {
		return Event{}, fmt.Errorf("decode event: %w", err)
	}

	eventType := headerValue(message.Headers, HeaderEventType)
	if eventType == "" {
		switch message.Topic {
		case k.config.TopicUploaded:
			eventType = models.EventTypeUploaded
		case k.config.TopicDeleted:
			eventType = models.EventTypeDeleted
		default:
			eventType = strings.TrimSpace(message.Topic)
		}
	}

	return Event{ID: meta.EventID, Type: eventType, Topic: message.Topic, Body: message.Value}, nil
}

func headerValue(headers []*sarama.RecordHeader, key string) string {
	for _, header := range headers {
		if header != nil && string(header.Key) == key {
			return string(header.Value)
		}
	}
	return ""
}

func isTopicExists(err error) bool {
	var topicErr *sarama.TopicError
	if errors.As(err, &topicErr) {
		return topicErr.Err == sarama.ErrTopicAlreadyExists
	}
	return errors.Is(err, sarama.ErrTopicAlreadyExists)
}

type groupHandler struct {
	kafka   *Kafka
	handler func(context.Context, Event) error
	logger  *slog.Logger
}

func (h *groupHandler) Setup(sarama.ConsumerGroupSession) error {
	return nil
}

func (h *groupHandler) Cleanup(sarama.ConsumerGroupSession) error {
	return nil
}

func (h *groupHandler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	for {
		select {
		case message, ok := <-claim.Messages():
			if !ok {
				return nil
			}

			event, err := h.kafka.decodeEvent(message)
			if err != nil {
				h.logger.Error("skip malformed kafka message", "topic", message.Topic, "error", err)
				if dlqErr := h.kafka.sendToDLQ(session.Context(), message, err); dlqErr != nil {
					h.logger.Error("failed to publish malformed message to dlq", "error", dlqErr)
				}
				session.MarkMessage(message, "malformed")
				continue
			}

			ctx, cancel := context.WithTimeout(session.Context(), eventHandlingLimit)
			handleErr := h.handler(ctx, event)
			cancel()

			if handleErr != nil {
				h.logger.Error("event handling failed",
					"topic", message.Topic, "event_id", event.ID, "event_type", event.Type, "error", handleErr)
				if dlqErr := h.kafka.sendToDLQ(session.Context(), message, handleErr); dlqErr != nil {
					h.logger.Error("failed to publish event to dlq", "error", dlqErr)
					return dlqErr
				}
			}
			session.MarkMessage(message, "")

		case <-session.Context().Done():
			return nil
		}
	}
}
