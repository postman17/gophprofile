package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/postman17/gophprofile/internal/app"
	"github.com/postman17/gophprofile/internal/config"
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
	defer stop()

	postgres := repo.NewPostgresStorage(ctx, cfg.DatabaseURL)
	defer postgres.Close()

	storage := s3.NewS3Client(ctx, cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3Bucket)
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
		kafka, err := app.ConnectKafka(ctx, cfg, "gophprofile-worker")
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			return err
		}

		consumeErr := kafka.Consume(ctx, eventWorker.HandleWithRetry)
		_ = kafka.Close()

		if ctx.Err() != nil {
			break
		}
		slog.Error("kafka consumer stopped, reconnecting", "error", consumeErr)

		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}

	slog.Info("worker stopped")
	return nil
}
