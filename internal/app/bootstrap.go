package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/postman17/gophprofile/internal/broker"
	"github.com/postman17/gophprofile/internal/config"
	"github.com/postman17/gophprofile/internal/s3"
)

const (
	connectAttempts = 15
	connectDelay    = 2 * time.Second
)

func ConnectKafka(ctx context.Context, cfg config.Config, clientID string) (*broker.Kafka, error) {
	opts := broker.Config{
		Brokers:       cfg.KafkaBrokers,
		TopicUploaded: cfg.KafkaTopicUploaded,
		TopicDeleted:  cfg.KafkaTopicDeleted,
		TopicDLQ:      cfg.KafkaTopicDLQ,
		ConsumerGroup: cfg.KafkaConsumerGroup,
		ClientID:      clientID,
		NumPartitions: 3,
	}

	var lastErr error
	for attempt := 1; attempt <= connectAttempts; attempt++ {
		client, err := broker.NewKafka(opts, slog.Default())
		if err == nil {
			if err := client.EnsureTopics(ctx); err == nil {
				return client, nil
			} else {
				lastErr = err
				_ = client.Close()
			}
		} else {
			lastErr = err
		}
		slog.Warn("kafka is not ready, retrying", "attempt", attempt, "error", lastErr)
		if !sleep(ctx) {
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("connect kafka: %w", lastErr)
}

func EnsureBucket(ctx context.Context, store *s3.S3Store) error {
	var lastErr error
	for attempt := 1; attempt <= connectAttempts; attempt++ {
		if err := store.EnsureBucket(ctx); err == nil {
			return nil
		} else {
			lastErr = err
		}
		slog.Warn("storage is not ready, retrying", "attempt", attempt, "error", lastErr)
		if !sleep(ctx) {
			return ctx.Err()
		}
	}
	return fmt.Errorf("ensure bucket: %w", lastErr)
}

func sleep(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(connectDelay):
		return true
	}
}
