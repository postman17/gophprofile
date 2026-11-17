package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/postman17/gophprofile/internal/observability"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/postman17/gophprofile/internal/models"
)

type S3Store struct {
	bucket string
	client *s3.Client
}

func NewS3Client(ctx context.Context, endpoint, accessKey, secretKey, bucket string) (*S3Store, error) {
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion("ru-1"),
		config.WithHTTPClient(&http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport), Timeout: 60 * time.Second}),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("load s3 config: %w", err)
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
	return &S3Store{bucket: bucket, client: client}, nil
}

func (s *S3Store) EnsureBucket(ctx context.Context) (retErr error) {
	ctx, span := observability.Start(ctx, "s3.EnsureBucket", trace.SpanKindClient, attribute.String("db.system.name", "aws_s3"), attribute.String("aws.s3.bucket", s.bucket))
	defer func() { observability.End(span, retErr) }()
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)})
	if err == nil {
		return nil
	}
	var notFound *types.NotFound
	if !errors.As(err, &notFound) {
		var apiErr interface{ ErrorCode() string }
		if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "NotFound" {
			return fmt.Errorf("s3 head bucket failed: %w", err)
		}
	}
	_, err = s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(s.bucket)})
	if err != nil {
		return fmt.Errorf("s3 create bucket failed: %w", err)
	}
	return nil
}

func (s *S3Store) Put(ctx context.Context, key, contentType string, body io.Reader, size int64) (retErr error) {
	ctx, span := observability.Start(ctx, "s3.Put", trace.SpanKindClient, attribute.String("db.system.name", "aws_s3"), attribute.String("aws.s3.bucket", s.bucket))
	defer func() { observability.End(span, retErr) }()
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          body,
		ContentType:   aws.String(contentType),
		ContentLength: aws.Int64(size),
	})
	if err != nil {
		return errors.Join(models.ErrStorage, err)
	}
	return nil
}

func (s *S3Store) Get(ctx context.Context, key string) (reader io.ReadCloser, contentTypeResult string, retErr error) {
	ctx, span := observability.Start(ctx, "s3.Get", trace.SpanKindClient, attribute.String("db.system.name", "aws_s3"), attribute.String("aws.s3.bucket", s.bucket))
	defer func() { observability.End(span, retErr) }()
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, "", models.ErrNotFound
		}
		return nil, "", errors.Join(models.ErrStorage, err)
	}

	contentType := ""
	if out.ContentType != nil {
		contentType = *out.ContentType
	}
	return out.Body, contentType, nil
}

func (s *S3Store) Delete(ctx context.Context, key string) (retErr error) {
	ctx, span := observability.Start(ctx, "s3.Delete", trace.SpanKindClient, attribute.String("db.system.name", "aws_s3"), attribute.String("aws.s3.bucket", s.bucket))
	defer func() { observability.End(span, retErr) }()
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil && !isNotFound(err) {
		return errors.Join(models.ErrStorage, err)
	}
	return nil
}

func (s *S3Store) Health(ctx context.Context) (retErr error) {
	ctx, span := observability.Start(ctx, "s3.Health", trace.SpanKindClient, attribute.String("db.system.name", "aws_s3"), attribute.String("aws.s3.bucket", s.bucket))
	defer func() { observability.End(span, retErr) }()
	if _, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)}); err != nil {
		return errors.Join(models.ErrStorage, err)
	}
	return nil
}

func isNotFound(err error) bool {
	var notFound *types.NotFound
	if errors.As(err, &notFound) {
		return true
	}
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "NoSuchBucket":
			return true
		}
	}
	return false
}

// MonitorInventory reports physical bucket usage, independent of process restarts or Kafka retries.
// Run only in the API process to avoid duplicate totals across API and worker.
func (s *S3Store) MonitorInventory(ctx context.Context) {
	collect := func() {
		inventoryCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		pages := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket)})
		var bytes, objects int64
		for pages.HasMorePages() {
			page, err := pages.NextPage(inventoryCtx)
			if err != nil {
				slog.WarnContext(ctx, "collect storage inventory", "error", err)
				return
			}
			for _, object := range page.Contents {
				bytes += aws.ToInt64(object.Size)
				objects++
			}
		}
		observability.StorageBytes.Set(float64(bytes))
		observability.StorageObjects.Set(float64(objects))
		observability.StorageLastSuccess.Set(float64(time.Now().Unix()))
	}
	collect()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			collect()
		}
	}
}
