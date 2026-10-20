package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/postman17/gophprofile/internal/imaging"
	"github.com/postman17/gophprofile/internal/models"
)

type Repository interface {
	Create(ctx context.Context, avatar *models.Avatar) error
	GetByID(ctx context.Context, id string) (*models.Avatar, error)
	GetActiveByUser(ctx context.Context, userID string) (*models.Avatar, error)
	ListByUser(ctx context.Context, userID string) ([]*models.Avatar, error)
	SoftDelete(ctx context.Context, id, userID string) error
	SetProcessingStatus(ctx context.Context, id string, status models.ProcessingStatus) error
}

type Storage interface {
	Put(ctx context.Context, key, contentType string, r io.Reader, size int64) error
	Get(ctx context.Context, key string) (io.ReadCloser, string, error)
	Delete(ctx context.Context, key string) error
}

type Publisher interface {
	PublishUpload(ctx context.Context, event models.AvatarUploadEvent) error
	PublishDelete(ctx context.Context, event models.AvatarDeleteEvent) error
}

type Options struct {
	MaxFileSize int64
	Now         func() time.Time
	NewID       func() string
	Logger      *slog.Logger
}

type Image struct {
	Reader   io.ReadCloser
	MimeType string
	ETag     string
	Size     int64
}

type AvatarService struct {
	repo        Repository
	storage     Storage
	publisher   Publisher
	maxFileSize int64
	now         func() time.Time
	newID       func() string
	logger      *slog.Logger
}

func NewAvatarService(repo Repository, storage Storage, publisher Publisher, opts Options) *AvatarService {
	if opts.MaxFileSize <= 0 {
		opts.MaxFileSize = 10 * 1024 * 1024
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	if opts.NewID == nil {
		opts.NewID = func() string { return uuid.NewString() }
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &AvatarService{
		repo:        repo,
		storage:     storage,
		publisher:   publisher,
		maxFileSize: opts.MaxFileSize,
		now:         opts.Now,
		newID:       opts.NewID,
		logger:      opts.Logger,
	}
}

func (s *AvatarService) Upload(ctx context.Context, userID, fileName string, r io.Reader) (*models.Avatar, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, models.ErrMissingUserID
	}

	data, err := io.ReadAll(io.LimitReader(r, s.maxFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("read upload body: %w", err)
	}
	if int64(len(data)) > s.maxFileSize {
		return nil, models.ErrFileTooLarge
	}
	if len(data) == 0 {
		return nil, models.ErrInvalidFormat
	}

	img, mimeType, err := imaging.Decode(data)
	if err != nil || !imaging.SupportedMime(mimeType) {
		return nil, models.ErrInvalidFormat
	}
	width, height := imaging.Dimensions(img)

	now := s.now()
	avatar := &models.Avatar{
		ID:               s.newID(),
		UserID:           userID,
		FileName:         models.NormalizeFileName(fileName),
		MimeType:         mimeType,
		SizeBytes:        int64(len(data)),
		Thumbnails:       map[string]models.Thumbnail{},
		Width:            width,
		Height:           height,
		ETag:             hashBytes(data),
		UploadStatus:     models.UploadStatusUploaded,
		ProcessingStatus: models.ProcessingStatusPending,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	avatar.S3Key = ObjectKey(userID, avatar.ID, mimeType)

	if err := s.storage.Put(ctx, avatar.S3Key, avatar.MimeType, bytes.NewReader(data), avatar.SizeBytes); err != nil {
		return nil, fmt.Errorf("store original: %w", err)
	}
	if err := s.repo.Create(ctx, avatar); err != nil {
		s.cleanup(ctx, avatar.S3Key)
		return nil, fmt.Errorf("create avatar: %w", err)
	}

	event := models.AvatarUploadEvent{
		EventID:  s.newID(),
		AvatarID: avatar.ID,
		UserID:   avatar.UserID,
		S3Key:    avatar.S3Key,
	}
	if err := s.publisher.PublishUpload(ctx, event); err != nil {
		if statusErr := s.repo.SetProcessingStatus(ctx, avatar.ID, models.ProcessingStatusFailed); statusErr != nil {
			s.logger.Error("failed to mark avatar as failed", "avatar_id", avatar.ID, "error", statusErr)
		}
		return nil, fmt.Errorf("publish upload event: %w", err)
	}

	return avatar, nil
}

func (s *AvatarService) Get(ctx context.Context, id string) (*models.Avatar, error) {
	avatar, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if avatar.Deleted() {
		return nil, models.ErrNotFound
	}
	return avatar, nil
}

func (s *AvatarService) GetByUser(ctx context.Context, userID string) (*models.Avatar, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, models.ErrMissingUserID
	}
	return s.repo.GetActiveByUser(ctx, userID)
}

func (s *AvatarService) List(ctx context.Context, userID string) ([]*models.Avatar, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, models.ErrMissingUserID
	}
	return s.repo.ListByUser(ctx, userID)
}

func (s *AvatarService) Delete(ctx context.Context, avatarID, userID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return models.ErrMissingUserID
	}

	avatar, err := s.Get(ctx, strings.TrimSpace(avatarID))
	if err != nil {
		return err
	}
	if avatar.UserID != userID {
		return models.ErrForbidden
	}
	if err := s.repo.SoftDelete(ctx, avatar.ID, userID); err != nil {
		return err
	}
	return s.publishDelete(ctx, avatar)
}

func (s *AvatarService) DeleteByUser(ctx context.Context, userID, requesterID string) error {
	userID = strings.TrimSpace(userID)
	requesterID = strings.TrimSpace(requesterID)
	if userID == "" || requesterID == "" {
		return models.ErrMissingUserID
	}
	if userID != requesterID {
		return models.ErrForbidden
	}

	avatar, err := s.GetByUser(ctx, userID)
	if err != nil {
		return err
	}
	return s.Delete(ctx, avatar.ID, requesterID)
}

func (s *AvatarService) Open(ctx context.Context, id, size, format string) (*Image, error) {
	avatar, err := s.Get(ctx, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	return s.OpenAvatar(ctx, avatar, size, format)
}

func (s *AvatarService) OpenByUser(ctx context.Context, userID, size, format string) (*Image, error) {
	avatar, err := s.GetByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.OpenAvatar(ctx, avatar, size, format)
}

func (s *AvatarService) OpenAvatar(ctx context.Context, avatar *models.Avatar, size, format string) (*Image, error) {
	key, mimeType, etag, err := resolveSource(avatar, size)
	if err != nil {
		return nil, err
	}

	reader, storedMime, err := s.storage.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if storedMime != "" {
		mimeType = storedMime
	}

	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" || formatMime(format) == mimeType {
		return &Image{Reader: reader, MimeType: mimeType, ETag: etag}, nil
	}
	defer func() { _ = reader.Close() }()

	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read object: %w", err)
	}
	img, _, err := imaging.Decode(data)
	if err != nil {
		return nil, fmt.Errorf("decode object: %w", err)
	}
	encoded, encodedMime, err := imaging.Encode(img, format)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", models.ErrUnsupportedFormat, format)
	}
	sum := sha256.Sum256(encoded)
	return &Image{
		Reader:   io.NopCloser(bytes.NewReader(encoded)),
		MimeType: encodedMime,
		ETag:     hex.EncodeToString(sum[:]),
		Size:     int64(len(encoded)),
	}, nil
}

func (s *AvatarService) publishDelete(ctx context.Context, avatar *models.Avatar) error {
	event := models.AvatarDeleteEvent{
		EventID:  s.newID(),
		AvatarID: avatar.ID,
		S3Keys:   avatar.S3Keys(),
	}
	if err := s.publisher.PublishDelete(ctx, event); err != nil {
		s.logger.Error("failed to publish delete event", "avatar_id", avatar.ID, "error", err)
		return fmt.Errorf("publish delete event: %w", err)
	}
	return nil
}

func (s *AvatarService) cleanup(ctx context.Context, key string) {
	if err := s.storage.Delete(ctx, key); err != nil {
		s.logger.Error("failed to cleanup object", "key", key, "error", err)
	}
}

func ObjectKey(userID, avatarID, mimeType string) string {
	return fmt.Sprintf("avatars/%s/%s/original%s",
		models.SafePathSegment(userID), avatarID, imaging.Extension(mimeType))
}

func ThumbnailKey(avatarID, size string) string {
	return fmt.Sprintf("thumbnails/%s/%s.jpg", avatarID, models.SafePathSegment(size))
}

func resolveSource(avatar *models.Avatar, size string) (string, string, string, error) {
	switch normalized := strings.TrimSpace(size); normalized {
	case "", "original":
		return avatar.S3Key, avatar.MimeType, avatar.ETag, nil
	case models.ThumbnailSize100, models.ThumbnailSize300:
		thumb, ok := avatar.Thumbnails[normalized]
		if !ok {
			if !avatar.Processed() {
				return "", "", "", models.ErrNotReady
			}
			return "", "", "", models.ErrNotFound
		}
		return thumb.S3Key, thumb.MimeType, avatar.ETag + "-" + thumb.Size, nil
	default:
		return "", "", "", models.ErrInvalidSize
	}
}

func formatMime(format string) string {
	switch format {
	case "jpeg", "jpg":
		return imaging.MimeJPEG
	case "png":
		return imaging.MimePNG
	default:
		return ""
	}
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
