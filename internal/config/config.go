package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultHTTPAddr    = ":8080"
	DefaultMaxFileSize = 10 * 1024 * 1024
	DefaultS3Bucket    = "avatars"

	DefaultKafkaTopicUploaded  = "avatar.uploaded"
	DefaultKafkaTopicDeleted   = "avatar.delete"
	DefaultKafkaTopicDLQ       = "avatar.dlq"
	DefaultKafkaConsumerGroup  = "avatar-worker"
	DefaultWorkerRetries       = 3
	DefaultWorkerBaseDelay     = time.Second
	DefaultGracefulTimeout     = 15 * time.Second
	DefaultShutdownWaitTimeout = 10 * time.Second
)

type Config struct {
	HTTPAddr    string
	DatabaseURL string
	MaxFileSize int64

	KafkaBrokers       []string
	KafkaTopicUploaded string
	KafkaTopicDeleted  string
	KafkaTopicDLQ      string
	KafkaConsumerGroup string

	S3Endpoint  string
	S3AccessKey string
	S3SecretKey string
	S3Bucket    string

	WorkerRetries   int
	WorkerBaseDelay time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:    getenv("HTTP_ADDR", DefaultHTTPAddr),
		DatabaseURL: getenv("DATABASE_URL", ""),
		MaxFileSize: getenvInt64("MAX_FILE_SIZE", DefaultMaxFileSize),

		KafkaBrokers:       getenvList("KAFKA_BROKERS", nil),
		KafkaTopicUploaded: getenv("KAFKA_TOPIC_UPLOADED", DefaultKafkaTopicUploaded),
		KafkaTopicDeleted:  getenv("KAFKA_TOPIC_DELETED", DefaultKafkaTopicDeleted),
		KafkaTopicDLQ:      getenv("KAFKA_TOPIC_DLQ", DefaultKafkaTopicDLQ),
		KafkaConsumerGroup: getenv("KAFKA_CONSUMER_GROUP", DefaultKafkaConsumerGroup),

		S3Endpoint:  getenv("S3_ENDPOINT", ""),
		S3AccessKey: getenv("S3_ACCESS_KEY", ""),
		S3SecretKey: getenv("S3_SECRET_KEY", ""),
		S3Bucket:    getenv("S3_BUCKET", DefaultS3Bucket),

		WorkerRetries:   int(getenvInt64("WORKER_RETRIES", DefaultWorkerRetries)),
		WorkerBaseDelay: getenvDuration("WORKER_BASE_DELAY", DefaultWorkerBaseDelay),
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	if c.DatabaseURL == "" {
		return errors.New("config: DATABASE_URL is required")
	}
	if len(c.KafkaBrokers) == 0 {
		return errors.New("config: KAFKA_BROKERS is required")
	}
	if c.S3Endpoint == "" {
		return errors.New("config: S3_ENDPOINT is required")
	}
	if c.S3AccessKey == "" || c.S3SecretKey == "" {
		return errors.New("config: S3_ACCESS_KEY and S3_SECRET_KEY are required")
	}
	if c.S3Bucket == "" {
		return errors.New("config: S3_BUCKET is required")
	}
	if c.MaxFileSize <= 0 {
		return fmt.Errorf("config: MAX_FILE_SIZE must be positive, got %d", c.MaxFileSize)
	}
	if c.WorkerRetries <= 0 {
		return fmt.Errorf("config: WORKER_RETRIES must be positive, got %d", c.WorkerRetries)
	}
	if c.WorkerBaseDelay <= 0 {
		return fmt.Errorf("config: WORKER_BASE_DELAY must be positive, got %s", c.WorkerBaseDelay)
	}
	return nil
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func getenvInt64(key string, def int64) int64 {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return def
	}
	return n
}

func getenvBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return b
}

func getenvDuration(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return d
}

func getenvList(key string, def []string) []string {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
