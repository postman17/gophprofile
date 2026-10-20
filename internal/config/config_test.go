package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/postman17/gophprofile/internal/config"
)

func setValidEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://gophprofile:gophprofile@localhost:5437/gophprofile?sslmode=disable")
	t.Setenv("KAFKA_BROKERS", "localhost:9092, localhost:9093")
	t.Setenv("S3_ENDPOINT", "http://localhost:9000")
	t.Setenv("S3_ACCESS_KEY", "gophprofile")
	t.Setenv("S3_SECRET_KEY", "gophprofile")
}

func TestLoadDefaults(t *testing.T) {
	setValidEnv(t)

	cfg, err := config.Load()
	require.NoError(t, err)

	require.Equal(t, ":8080", cfg.HTTPAddr)
	require.Equal(t, int64(10*1024*1024), cfg.MaxFileSize)
	require.Equal(t, []string{"localhost:9092", "localhost:9093"}, cfg.KafkaBrokers)
	require.Equal(t, "avatar.uploaded", cfg.KafkaTopicUploaded)
	require.Equal(t, "avatar.delete", cfg.KafkaTopicDeleted)
	require.Equal(t, "avatar.dlq", cfg.KafkaTopicDLQ)
	require.Equal(t, "avatar-worker", cfg.KafkaConsumerGroup)
	require.Equal(t, "avatars", cfg.S3Bucket)
	require.Equal(t, 3, cfg.WorkerRetries)
	require.Equal(t, time.Second, cfg.WorkerBaseDelay)
}

func TestLoadCustomValues(t *testing.T) {
	setValidEnv(t)
	t.Setenv("HTTP_ADDR", ":3200")
	t.Setenv("MAX_FILE_SIZE", "2048")
	t.Setenv("S3_BUCKET", "custom-bucket")
	t.Setenv("KAFKA_TOPIC_UPLOADED", "custom.uploaded")
	t.Setenv("KAFKA_CONSUMER_GROUP", "custom-group")
	t.Setenv("WORKER_RETRIES", "7")
	t.Setenv("WORKER_BASE_DELAY", "250ms")

	cfg, err := config.Load()
	require.NoError(t, err)
	require.Equal(t, ":3200", cfg.HTTPAddr)
	require.Equal(t, int64(2048), cfg.MaxFileSize)
	require.Equal(t, "custom-bucket", cfg.S3Bucket)
	require.Equal(t, "custom.uploaded", cfg.KafkaTopicUploaded)
	require.Equal(t, "custom-group", cfg.KafkaConsumerGroup)
	require.Equal(t, 7, cfg.WorkerRetries)
	require.Equal(t, 250*time.Millisecond, cfg.WorkerBaseDelay)
}

func TestLoadValidation(t *testing.T) {
	cases := []struct {
		name    string
		envKey  string
		message string
	}{
		{name: "database", envKey: "DATABASE_URL", message: "DATABASE_URL"},
		{name: "kafka", envKey: "KAFKA_BROKERS", message: "KAFKA_BROKERS"},
		{name: "s3 endpoint", envKey: "S3_ENDPOINT", message: "S3_ENDPOINT"},
		{name: "s3 access key", envKey: "S3_ACCESS_KEY", message: "S3_ACCESS_KEY"},
		{name: "s3 secret key", envKey: "S3_SECRET_KEY", message: "S3_SECRET_KEY"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			setValidEnv(t)
			t.Setenv(testCase.envKey, "")

			_, err := config.Load()
			require.Error(t, err)
			require.Contains(t, err.Error(), testCase.message)
		})
	}
}

func TestLoadRejectsNegativeLimits(t *testing.T) {
	setValidEnv(t)
	t.Setenv("MAX_FILE_SIZE", "-1")

	_, err := config.Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "MAX_FILE_SIZE")
}

func TestLoadIgnoresInvalidNumbers(t *testing.T) {
	setValidEnv(t)
	t.Setenv("MAX_FILE_SIZE", "not-a-number")
	t.Setenv("WORKER_RETRIES", "nope")
	t.Setenv("WORKER_BASE_DELAY", "soon")

	cfg, err := config.Load()
	require.NoError(t, err)
	require.Equal(t, int64(config.DefaultMaxFileSize), cfg.MaxFileSize)
	require.Equal(t, config.DefaultWorkerRetries, cfg.WorkerRetries)
	require.Equal(t, config.DefaultWorkerBaseDelay, cfg.WorkerBaseDelay)
}
