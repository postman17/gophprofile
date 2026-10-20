package models

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var (
	ErrNotFound          = errors.New("avatar not found")
	ErrForbidden         = errors.New("forbidden")
	ErrMissingUserID     = errors.New("missing user id")
	ErrInvalidFormat     = errors.New("invalid file format")
	ErrInvalidSize       = errors.New("invalid size")
	ErrFileTooLarge      = errors.New("file too large")
	ErrNotReady          = errors.New("avatar is still processing")
	ErrUnsupportedFormat = errors.New("unsupported format")
	ErrStorage           = errors.New("storage error")
)

const (
	EventTypeUploaded = "avatar.uploaded"
	EventTypeDeleted  = "avatar.deleted"
)

const (
	ThumbnailSize100 = "100x100"
	ThumbnailSize300 = "300x300"
)

type UploadStatus string

const (
	UploadStatusUploaded UploadStatus = "uploaded"
	UploadStatusDeleted  UploadStatus = "deleted"
)

type ProcessingStatus string

const (
	ProcessingStatusPending    ProcessingStatus = "pending"
	ProcessingStatusProcessing ProcessingStatus = "processing"
	ProcessingStatusCompleted  ProcessingStatus = "completed"
	ProcessingStatusFailed     ProcessingStatus = "failed"
)

type Thumbnail struct {
	Size     string `json:"size"`
	S3Key    string `json:"s3_key"`
	MimeType string `json:"mime_type"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

type Avatar struct {
	ID               string
	UserID           string
	FileName         string
	MimeType         string
	SizeBytes        int64
	S3Key            string
	Thumbnails       map[string]Thumbnail
	Width            int
	Height           int
	ETag             string
	UploadStatus     UploadStatus
	ProcessingStatus ProcessingStatus
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeletedAt        *time.Time
}

func (a *Avatar) Deleted() bool {
	return a.DeletedAt != nil
}

func (a *Avatar) Processed() bool {
	return a.ProcessingStatus == ProcessingStatusCompleted
}

func (a *Avatar) S3Keys() []string {
	keys := make([]string, 0, len(a.Thumbnails)+1)
	if a.S3Key != "" {
		keys = append(keys, a.S3Key)
	}
	for _, thumb := range a.Thumbnails {
		if thumb.S3Key != "" {
			keys = append(keys, thumb.S3Key)
		}
	}
	sort.Strings(keys)
	return keys
}

func (a *Avatar) ThumbnailList() []Thumbnail {
	list := make([]Thumbnail, 0, len(a.Thumbnails))
	for _, thumb := range a.Thumbnails {
		list = append(list, thumb)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Width != list[j].Width {
			return list[i].Width < list[j].Width
		}
		return list[i].Size < list[j].Size
	})
	return list
}

func (a *Avatar) Thumbnail(size string) (Thumbnail, bool) {
	thumb, ok := a.Thumbnails[size]
	return thumb, ok
}

type AvatarUploadEvent struct {
	EventID  string `json:"event_id"`
	AvatarID string `json:"avatar_id"`
	UserID   string `json:"user_id"`
	S3Key    string `json:"s3_key"`
}

type AvatarDeleteEvent struct {
	EventID  string   `json:"event_id"`
	AvatarID string   `json:"avatar_id"`
	S3Keys   []string `json:"s3_keys"`
}

func NormalizeFileName(name string) string {
	name = strings.TrimSpace(name)
	if idx := strings.LastIndexAny(name, `/\`); idx >= 0 {
		name = name[idx+1:]
	}
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "avatar"
	}
	if len(name) > 255 {
		name = name[:255]
	}
	return name
}

func SafePathSegment(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	return b.String()
}

func (a Avatar) String() string {
	return fmt.Sprintf("avatar(id=%s user=%s status=%s/%s)", a.ID, a.UserID, a.UploadStatus, a.ProcessingStatus)
}
