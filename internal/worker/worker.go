package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/postman17/gophprofile/internal/broker"
	"github.com/postman17/gophprofile/internal/imaging"
	"github.com/postman17/gophprofile/internal/models"
	"github.com/postman17/gophprofile/internal/service"
)

type Repository interface {
	GetByID(ctx context.Context, id string) (*models.Avatar, error)
	SetProcessed(ctx context.Context, id string, thumbnails map[string]models.Thumbnail, width, height int) error
	SetProcessingStatus(ctx context.Context, id string, status models.ProcessingStatus) error
	IsEventProcessed(ctx context.Context, eventID string) (bool, error)
	MarkEventProcessed(ctx context.Context, eventID, eventType string) (bool, error)
}

type Storage interface {
	Put(ctx context.Context, key, contentType string, r io.Reader, size int64) error
	Get(ctx context.Context, key string) (io.ReadCloser, string, error)
	Delete(ctx context.Context, key string) error
}

type Options struct {
	Retries   int
	BaseDelay time.Duration
	Sizes     []imaging.Size
	Logger    *slog.Logger
}

type Worker struct {
	repo      Repository
	storage   Storage
	retries   int
	baseDelay time.Duration
	sizes     []imaging.Size
	logger    *slog.Logger
}

func New(repo Repository, storage Storage, opts Options) *Worker {
	if opts.Retries <= 0 {
		opts.Retries = 3
	}
	if opts.BaseDelay <= 0 {
		opts.BaseDelay = time.Second
	}
	if len(opts.Sizes) == 0 {
		opts.Sizes = imaging.DefaultSizes
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Worker{
		repo:      repo,
		storage:   storage,
		retries:   opts.Retries,
		baseDelay: opts.BaseDelay,
		sizes:     opts.Sizes,
		logger:    opts.Logger,
	}
}

func (w *Worker) HandleWithRetry(ctx context.Context, event broker.Event) error {
	delay := w.baseDelay
	var err error

	for attempt := 1; attempt <= w.retries; attempt++ {
		if err = w.HandleEvent(ctx, event); err == nil {
			return nil
		}

		processed, checkErr := w.isProcessed(ctx, event.ID)
		if checkErr != nil {
			w.logger.Error("failed to check event state", "event_id", event.ID, "error", checkErr)
		} else if processed {
			w.logger.Warn("event already processed by another attempt", "event_id", event.ID)
			return nil
		}

		if attempt == w.retries {
			break
		}
		w.logger.Warn("event handling failed, retrying",
			"event_id", event.ID, "event_type", event.Type, "attempt", attempt,
			"retry_in", delay.String(), "error", err)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}

	return fmt.Errorf("event %s (%s) failed after %d attempts: %w", event.ID, event.Type, w.retries, err)
}

func (w *Worker) HandleEvent(ctx context.Context, event broker.Event) error {
	processed, err := w.isProcessed(ctx, event.ID)
	if err != nil {
		return err
	}
	if processed {
		w.logger.Debug("skip already processed event", "event_id", event.ID, "event_type", event.Type)
		return nil
	}

	switch event.Type {
	case models.EventTypeUploaded:
		return w.handleUpload(ctx, event)
	case models.EventTypeDeleted:
		return w.handleDeleted(ctx, event)
	default:
		w.logger.Warn("skip unknown event type", "event_type", event.Type, "event_id", event.ID)
		return w.markProcessed(ctx, event)
	}
}

func (w *Worker) handleUpload(ctx context.Context, event broker.Event) error {
	var payload models.AvatarUploadEvent
	if err := json.Unmarshal(event.Body, &payload); err != nil {
		return fmt.Errorf("decode upload event: %w", err)
	}

	avatar, err := w.repo.GetByID(ctx, payload.AvatarID)
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			w.logger.Warn("avatar not found, nothing to process", "avatar_id", payload.AvatarID)
			return w.markProcessed(ctx, event)
		}
		return err
	}
	if avatar.Deleted() {
		w.logger.Info("avatar deleted before processing, skip", "avatar_id", avatar.ID)
		return w.markProcessed(ctx, event)
	}
	if avatar.Processed() {
		w.logger.Debug("avatar already processed", "avatar_id", avatar.ID)
		return w.markProcessed(ctx, event)
	}

	if err := w.repo.SetProcessingStatus(ctx, avatar.ID, models.ProcessingStatusProcessing); err != nil {
		return fmt.Errorf("mark avatar as processing: %w", err)
	}

	reader, _, err := w.storage.Get(ctx, avatar.S3Key)
	if err != nil {
		w.markFailed(ctx, avatar.ID, err)
		return fmt.Errorf("load original %s: %w", avatar.S3Key, err)
	}
	data, readErr := io.ReadAll(reader)
	_ = reader.Close()
	if readErr != nil {
		w.markFailed(ctx, avatar.ID, readErr)
		return fmt.Errorf("read original %s: %w", avatar.S3Key, readErr)
	}

	img, _, err := imaging.Decode(data)
	if err != nil {
		w.markFailed(ctx, avatar.ID, err)
		return fmt.Errorf("decode original %s: %w", avatar.S3Key, err)
	}
	width, height := imaging.Dimensions(img)

	thumbnails, err := imaging.Thumbnails(img, w.sizes)
	if err != nil {
		w.markFailed(ctx, avatar.ID, err)
		return fmt.Errorf("build thumbnails: %w", err)
	}

	result := make(map[string]models.Thumbnail, len(thumbnails))
	for _, thumb := range thumbnails {
		key := service.ThumbnailKey(avatar.ID, thumb.Size.Name)
		if err := w.storage.Put(ctx, key, thumb.MimeType, bytes.NewReader(thumb.Data), int64(len(thumb.Data))); err != nil {
			w.markFailed(ctx, avatar.ID, err)
			return fmt.Errorf("store thumbnail %s: %w", key, err)
		}
		result[thumb.Size.Name] = models.Thumbnail{
			Size:     thumb.Size.Name,
			S3Key:    key,
			MimeType: thumb.MimeType,
			Width:    thumb.Size.Width,
			Height:   thumb.Size.Height,
		}
	}

	if err := w.repo.SetProcessed(ctx, avatar.ID, result, width, height); err != nil {
		return fmt.Errorf("save processing result: %w", err)
	}

	w.logger.Info("avatar processed", "avatar_id", avatar.ID, "width", width, "height", height, "thumbnails", len(result))
	return w.markProcessed(ctx, event)
}

func (w *Worker) handleDeleted(ctx context.Context, event broker.Event) error {
	var payload models.AvatarDeleteEvent
	if err := json.Unmarshal(event.Body, &payload); err != nil {
		return fmt.Errorf("decode delete event: %w", err)
	}

	keys := payload.S3Keys
	if len(keys) == 0 && payload.AvatarID != "" {
		if avatar, err := w.repo.GetByID(ctx, payload.AvatarID); err == nil {
			keys = avatar.S3Keys()
		} else if !errors.Is(err, models.ErrNotFound) {
			return fmt.Errorf("load avatar %s: %w", payload.AvatarID, err)
		}
	}

	for _, key := range keys {
		if key == "" {
			continue
		}
		if err := w.storage.Delete(ctx, key); err != nil {
			return fmt.Errorf("delete object %s: %w", key, err)
		}
	}

	w.logger.Info("avatar files deleted", "avatar_id", payload.AvatarID, "objects", len(keys))
	return w.markProcessed(ctx, event)
}

func (w *Worker) isProcessed(ctx context.Context, eventID string) (bool, error) {
	if eventID == "" {
		return false, nil
	}
	return w.repo.IsEventProcessed(ctx, eventID)
}

func (w *Worker) markProcessed(ctx context.Context, event broker.Event) error {
	if event.ID == "" {
		return nil
	}
	inserted, err := w.repo.MarkEventProcessed(ctx, event.ID, event.Type)
	if err != nil {
		return err
	}
	if !inserted {
		w.logger.Debug("event id already recorded", "event_id", event.ID)
	}
	return nil
}

func (w *Worker) markFailed(ctx context.Context, avatarID string, cause error) {
	if err := w.repo.SetProcessingStatus(ctx, avatarID, models.ProcessingStatusFailed); err != nil {
		w.logger.Error("failed to mark avatar as failed", "avatar_id", avatarID, "error", err)
		return
	}
	w.logger.Warn("avatar processing failed", "avatar_id", avatarID, "error", cause)
}
