package models

import (
	"fmt"
	"time"
)

type Dimensions struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type AvatarResponse struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	URL       string    `json:"url"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type ThumbnailResponse struct {
	Size string `json:"size"`
	URL  string `json:"url"`
}

type AvatarMetadataResponse struct {
	ID               string              `json:"id"`
	UserID           string              `json:"user_id"`
	FileName         string              `json:"file_name"`
	MimeType         string              `json:"mime_type"`
	Size             int64               `json:"size"`
	Dimensions       Dimensions          `json:"dimensions"`
	ETag             string              `json:"etag"`
	Thumbnails       []ThumbnailResponse `json:"thumbnails"`
	UploadStatus     string              `json:"upload_status"`
	ProcessingStatus string              `json:"processing_status"`
	CreatedAt        time.Time           `json:"created_at"`
	UpdatedAt        time.Time           `json:"updated_at"`
}

type AvatarListResponse struct {
	UserID  string                   `json:"user_id"`
	Avatars []AvatarMetadataResponse `json:"avatars"`
}

type HealthResponse struct {
	Status     string            `json:"status"`
	Components map[string]string `json:"components"`
}

func AvatarURL(id string) string {
	return fmt.Sprintf("/api/v1/avatars/%s", id)
}

func ThumbnailURL(id, size string) string {
	return fmt.Sprintf("/api/v1/avatars/%s?size=%s", id, size)
}

func NewAvatarResponse(a *Avatar) AvatarResponse {
	return AvatarResponse{
		ID:        a.ID,
		UserID:    a.UserID,
		URL:       AvatarURL(a.ID),
		Status:    string(ProcessingStatusProcessing),
		CreatedAt: a.CreatedAt,
	}
}

func NewAvatarMetadataResponse(a *Avatar) AvatarMetadataResponse {
	thumbs := make([]ThumbnailResponse, 0, len(a.Thumbnails))
	for _, thumb := range a.ThumbnailList() {
		thumbs = append(thumbs, ThumbnailResponse{
			Size: thumb.Size,
			URL:  ThumbnailURL(a.ID, thumb.Size),
		})
	}
	return AvatarMetadataResponse{
		ID:               a.ID,
		UserID:           a.UserID,
		FileName:         a.FileName,
		MimeType:         a.MimeType,
		Size:             a.SizeBytes,
		Dimensions:       Dimensions{Width: a.Width, Height: a.Height},
		ETag:             a.ETag,
		Thumbnails:       thumbs,
		UploadStatus:     string(a.UploadStatus),
		ProcessingStatus: string(a.ProcessingStatus),
		CreatedAt:        a.CreatedAt,
		UpdatedAt:        a.UpdatedAt,
	}
}

func NewAvatarListResponse(userID string, avatars []*Avatar) AvatarListResponse {
	list := make([]AvatarMetadataResponse, 0, len(avatars))
	for _, avatar := range avatars {
		list = append(list, NewAvatarMetadataResponse(avatar))
	}
	return AvatarListResponse{UserID: userID, Avatars: list}
}
