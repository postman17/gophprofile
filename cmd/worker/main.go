package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/postman17/gophprofile/internal/app"
	"github.com/postman17/gophprofile/internal/broker"
	"github.com/postman17/gophprofile/internal/config"
	"github.com/postman17/gophprofile/internal/observability"
	"github.com/postman17/gophprofile/internal/repo"
	"github.com/postman17/gophprofile/internal/s3"
	"github.com/postman17/gophprofile/internal/worker"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if err := run(); err != nil {
		slog.Error("worker stopped with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	var (
		shutdownTelemetry, stopMetrics func()
		postgres                       *repo.PostgresStorage
		kafka                          *broker.Kafka
	)
	// Keep cancellation and resource shutdown in one explicit sequence, including
	// partial initialization failures. Telemetry stays alive until cleanup ends.
	defer func() {
		stop()
		if kafka != nil {
			if err := kafka.Close(); err != nil {
				slog.Error("close kafka", "error", err)
			}
		}
		if postgres != nil {
			postgres.Close()
		}
		if stopMetrics != nil {
			stopMetrics()
		}
		if shutdownTelemetry != nil {
			shutdownTelemetry()
		}
	}()
	shutdownTelemetry, err = observability.Init(ctx, "gophprofile-worker")
	if err != nil {
		return err
	}
	stopMetrics, err = observability.ServeMetrics(context.WithoutCancel(ctx), cfg.MetricsAddr)
	if err != nil {
		return fmt.Errorf("metrics listener: %w", err)
	}

	postgres, err = repo.NewPostgresStorage(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("initialize postgres: %w", err)
	}
	postgres.RegisterMetrics()

	storage, err := s3.NewS3Client(ctx, cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3Bucket)
	if err != nil {
		return fmt.Errorf("initialize s3: %w", err)
	}
	if err := app.EnsureBucket(ctx, storage); err != nil {
		return err
	}

	eventWorker := worker.New(postgres, storage, worker.Options{
		Retries:   cfg.WorkerRetries,
		BaseDelay: cfg.WorkerBaseDelay,
		Logger:    slog.Default(),
	})

	slog.Info("worker starting",
		"brokers", cfg.KafkaBrokers,
		"group", cfg.KafkaConsumerGroup,
		"topics", []string{cfg.KafkaTopicUploaded, cfg.KafkaTopicDeleted},
	)

	for ctx.Err() == nil {
		kafka, err = app.ConnectKafka(ctx, cfg, "gophprofile-worker")
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			return err
		}

		consumeErr := kafka.Consume(ctx, eventWorker.HandleWithRetry)
		if ctx.Err() != nil {
			break
		}
		if err := kafka.Close(); err != nil {
			slog.Error("close kafka before reconnect", "error", err)
		}
		kafka = nil
		slog.Error("kafka consumer stopped, reconnecting", "error", consumeErr)

		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}

	slog.Info("worker stopped")
	return nil
}
