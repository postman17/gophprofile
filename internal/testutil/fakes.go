package testutil

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"testing"
	"time"

	"github.com/postman17/gophprofile/internal/models"
)

func PNG(t *testing.T, width, height int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := 0; x < width; x++ {
		for y := 0; y < height; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 160, A: 255})
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func Avatar(id, userID string) *models.Avatar {
	return &models.Avatar{
		ID:               id,
		UserID:           userID,
		FileName:         "avatar.png",
		MimeType:         "image/png",
		SizeBytes:        128,
		S3Key:            "avatars/" + userID + "/" + id + "/original.png",
		Thumbnails:       map[string]models.Thumbnail{},
		Width:            64,
		Height:           64,
		ETag:             "etag-" + id,
		UploadStatus:     models.UploadStatusUploaded,
		ProcessingStatus: models.ProcessingStatusPending,
		CreatedAt:        time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:        time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

type Repo struct {
	Avatars         map[string]*models.Avatar
	Events          map[string]bool
	Statuses        []models.ProcessingStatus
	ProcessedThumbs map[string]models.Thumbnail
	ProcessedWidth  int
	ProcessedHeight int

	CreateErr error
	ListErr   error
	DeleteErr error
	StatusErr error
	EventErr  error
}

func NewRepo(avatars ...*models.Avatar) *Repo {
	repo := &Repo{
		Avatars: map[string]*models.Avatar{},
		Events:  map[string]bool{},
	}
	for _, avatar := range avatars {
		repo.Avatars[avatar.ID] = avatar
	}
	return repo
}

func (r *Repo) Create(_ context.Context, avatar *models.Avatar) error {
	if r.CreateErr != nil {
		return r.CreateErr
	}
	r.Avatars[avatar.ID] = avatar
	return nil
}

func (r *Repo) GetByID(_ context.Context, id string) (*models.Avatar, error) {
	avatar, ok := r.Avatars[id]
	if !ok {
		return nil, models.ErrNotFound
	}
	return avatar, nil
}

func (r *Repo) GetActiveByUser(_ context.Context, userID string) (*models.Avatar, error) {
	var found *models.Avatar
	for _, avatar := range r.Avatars {
		if avatar.UserID != userID || avatar.Deleted() {
			continue
		}
		if found == nil || avatar.CreatedAt.After(found.CreatedAt) {
			found = avatar
		}
	}
	if found == nil {
		return nil, models.ErrNotFound
	}
	return found, nil
}

func (r *Repo) ListByUser(_ context.Context, userID string) ([]*models.Avatar, error) {
	if r.ListErr != nil {
		return nil, r.ListErr
	}
	list := make([]*models.Avatar, 0, len(r.Avatars))
	for _, avatar := range r.Avatars {
		if avatar.UserID == userID && !avatar.Deleted() {
			list = append(list, avatar)
		}
	}
	return list, nil
}

func (r *Repo) SoftDelete(_ context.Context, id, userID string) error {
	if r.DeleteErr != nil {
		return r.DeleteErr
	}
	avatar, ok := r.Avatars[id]
	if !ok || avatar.UserID != userID || avatar.Deleted() {
		return models.ErrNotFound
	}
	now := time.Now().UTC()
	avatar.DeletedAt = &now
	avatar.UploadStatus = models.UploadStatusDeleted
	return nil
}

func (r *Repo) SetProcessingStatus(_ context.Context, id string, status models.ProcessingStatus) error {
	if r.StatusErr != nil {
		return r.StatusErr
	}
	avatar, ok := r.Avatars[id]
	if !ok {
		return models.ErrNotFound
	}
	avatar.ProcessingStatus = status
	r.Statuses = append(r.Statuses, status)
	return nil
}

func (r *Repo) SetProcessed(_ context.Context, id string, thumbnails map[string]models.Thumbnail, width, height int) error {
	avatar, ok := r.Avatars[id]
	if !ok {
		return models.ErrNotFound
	}
	avatar.Thumbnails = thumbnails
	avatar.Width = width
	avatar.Height = height
	avatar.ProcessingStatus = models.ProcessingStatusCompleted
	r.ProcessedThumbs = thumbnails
	r.ProcessedWidth = width
	r.ProcessedHeight = height
	return nil
}

func (r *Repo) IsEventProcessed(_ context.Context, eventID string) (bool, error) {
	if r.EventErr != nil {
		return false, r.EventErr
	}
	return r.Events[eventID], nil
}

func (r *Repo) MarkEventProcessed(_ context.Context, eventID, _ string) (bool, error) {
	if r.EventErr != nil {
		return false, r.EventErr
	}
	if r.Events[eventID] {
		return false, nil
	}
	r.Events[eventID] = true
	return true, nil
}

type Storage struct {
	Objects   map[string][]byte
	Types     map[string]string
	Deleted   []string
	PutErr    error
	GetErr    error
	DeleteErr error
}

func NewStorage() *Storage {
	return &Storage{Objects: map[string][]byte{}, Types: map[string]string{}}
}

func (s *Storage) Put(_ context.Context, key, contentType string, body io.Reader, _ int64) error {
	if s.PutErr != nil {
		return s.PutErr
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	s.Objects[key] = data
	s.Types[key] = contentType
	return nil
}

func (s *Storage) Get(_ context.Context, key string) (io.ReadCloser, string, error) {
	if s.GetErr != nil {
		return nil, "", s.GetErr
	}
	data, ok := s.Objects[key]
	if !ok {
		return nil, "", models.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), s.Types[key], nil
}

func (s *Storage) Delete(_ context.Context, key string) error {
	if s.DeleteErr != nil {
		return s.DeleteErr
	}
	s.Deleted = append(s.Deleted, key)
	delete(s.Objects, key)
	return nil
}

type Publisher struct {
	Uploads   []models.AvatarUploadEvent
	Deletes   []models.AvatarDeleteEvent
	UploadErr error
	DeleteErr error
}

func (p *Publisher) PublishUpload(_ context.Context, event models.AvatarUploadEvent) error {
	if p.UploadErr != nil {
		return p.UploadErr
	}
	p.Uploads = append(p.Uploads, event)
	return nil
}

func (p *Publisher) PublishDelete(_ context.Context, event models.AvatarDeleteEvent) error {
	if p.DeleteErr != nil {
		return p.DeleteErr
	}
	p.Deletes = append(p.Deletes, event)
	return nil
}
