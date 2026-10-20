package service_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/postman17/gophprofile/internal/imaging"
	"github.com/postman17/gophprofile/internal/models"
	"github.com/postman17/gophprofile/internal/service"
	"github.com/postman17/gophprofile/internal/testutil"
)

func newService(repo *testutil.Repo, storage *testutil.Storage, publisher *testutil.Publisher, maxSize int64) *service.AvatarService {
	if maxSize == 0 {
		maxSize = 10 * 1024 * 1024
	}
	return service.NewAvatarService(repo, storage, publisher, service.Options{MaxFileSize: maxSize})
}

func TestUploadStoresOriginalAndPublishesEvent(t *testing.T) {
	repo := testutil.NewRepo()
	storage := testutil.NewStorage()
	publisher := &testutil.Publisher{}
	svc := newService(repo, storage, publisher, 0)

	data := testutil.PNG(t, 32, 16)
	avatar, err := svc.Upload(context.Background(), "user-1", "../../avatar.png", bytes.NewReader(data))
	require.NoError(t, err)

	require.NotEmpty(t, avatar.ID)
	require.Equal(t, "user-1", avatar.UserID)
	require.Equal(t, "avatar.png", avatar.FileName)
	require.Equal(t, imaging.MimePNG, avatar.MimeType)
	require.Equal(t, int64(len(data)), avatar.SizeBytes)
	require.Equal(t, 32, avatar.Width)
	require.Equal(t, 16, avatar.Height)
	require.Equal(t, models.ProcessingStatusPending, avatar.ProcessingStatus)
	require.Equal(t, models.UploadStatusUploaded, avatar.UploadStatus)
	require.NotEmpty(t, avatar.ETag)

	require.Contains(t, storage.Objects, avatar.S3Key)
	require.Equal(t, imaging.MimePNG, storage.Types[avatar.S3Key])

	require.Len(t, publisher.Uploads, 1)
	require.Equal(t, avatar.ID, publisher.Uploads[0].AvatarID)
	require.Equal(t, avatar.S3Key, publisher.Uploads[0].S3Key)
	require.NotEmpty(t, publisher.Uploads[0].EventID)

	require.Contains(t, repo.Avatars, avatar.ID)
}

func TestUploadValidation(t *testing.T) {
	ctx := context.Background()

	t.Run("missing user id", func(t *testing.T) {
		svc := newService(testutil.NewRepo(), testutil.NewStorage(), &testutil.Publisher{}, 0)
		_, err := svc.Upload(ctx, "  ", "avatar.png", bytes.NewReader(testutil.PNG(t, 4, 4)))
		require.ErrorIs(t, err, models.ErrMissingUserID)
	})

	t.Run("too large", func(t *testing.T) {
		data := testutil.PNG(t, 16, 16)
		svc := newService(testutil.NewRepo(), testutil.NewStorage(), &testutil.Publisher{}, int64(len(data)-1))
		_, err := svc.Upload(ctx, "user-1", "avatar.png", bytes.NewReader(data))
		require.ErrorIs(t, err, models.ErrFileTooLarge)
	})

	t.Run("invalid format", func(t *testing.T) {
		svc := newService(testutil.NewRepo(), testutil.NewStorage(), &testutil.Publisher{}, 0)
		_, err := svc.Upload(ctx, "user-1", "avatar.png", bytes.NewReader([]byte("not an image")))
		require.ErrorIs(t, err, models.ErrInvalidFormat)
	})

	t.Run("empty body", func(t *testing.T) {
		svc := newService(testutil.NewRepo(), testutil.NewStorage(), &testutil.Publisher{}, 0)
		_, err := svc.Upload(ctx, "user-1", "avatar.png", bytes.NewReader(nil))
		require.ErrorIs(t, err, models.ErrInvalidFormat)
	})
}

func TestUploadFailures(t *testing.T) {
	ctx := context.Background()
	data := testutil.PNG(t, 8, 8)

	t.Run("storage failure", func(t *testing.T) {
		storage := testutil.NewStorage()
		storage.PutErr = errors.New("s3 is down")
		svc := newService(testutil.NewRepo(), storage, &testutil.Publisher{}, 0)

		_, err := svc.Upload(ctx, "user-1", "avatar.png", bytes.NewReader(data))
		require.Error(t, err)
		require.Contains(t, err.Error(), "store original")
	})

	t.Run("repository failure removes uploaded object", func(t *testing.T) {
		repo := testutil.NewRepo()
		repo.CreateErr = errors.New("db is down")
		storage := testutil.NewStorage()
		svc := newService(repo, storage, &testutil.Publisher{}, 0)

		_, err := svc.Upload(ctx, "user-1", "avatar.png", bytes.NewReader(data))
		require.Error(t, err)
		require.Len(t, storage.Deleted, 1)
	})

	t.Run("publish failure marks avatar as failed", func(t *testing.T) {
		repo := testutil.NewRepo()
		publisher := &testutil.Publisher{UploadErr: errors.New("kafka is down")}
		svc := newService(repo, testutil.NewStorage(), publisher, 0)

		_, err := svc.Upload(ctx, "user-1", "avatar.png", bytes.NewReader(data))
		require.Error(t, err)
		require.Equal(t, []models.ProcessingStatus{models.ProcessingStatusFailed}, repo.Statuses)
	})
}

func TestGetAndList(t *testing.T) {
	ctx := context.Background()
	avatar := testutil.Avatar("avatar-1", "user-1")
	repo := testutil.NewRepo(avatar)
	svc := newService(repo, testutil.NewStorage(), &testutil.Publisher{}, 0)

	found, err := svc.Get(ctx, "avatar-1")
	require.NoError(t, err)
	require.Equal(t, "avatar-1", found.ID)

	_, err = svc.Get(ctx, "missing")
	require.ErrorIs(t, err, models.ErrNotFound)

	deletedAt := avatar.CreatedAt
	avatar.DeletedAt = &deletedAt
	_, err = svc.Get(ctx, "avatar-1")
	require.ErrorIs(t, err, models.ErrNotFound)
	avatar.DeletedAt = nil

	byUser, err := svc.GetByUser(ctx, "user-1")
	require.NoError(t, err)
	require.Equal(t, "avatar-1", byUser.ID)

	_, err = svc.GetByUser(ctx, " ")
	require.ErrorIs(t, err, models.ErrMissingUserID)

	list, err := svc.List(ctx, "user-1")
	require.NoError(t, err)
	require.Len(t, list, 1)

	_, err = svc.List(ctx, "")
	require.ErrorIs(t, err, models.ErrMissingUserID)
}

func TestDelete(t *testing.T) {
	ctx := context.Background()

	t.Run("forbidden for another user", func(t *testing.T) {
		svc := newService(testutil.NewRepo(testutil.Avatar("avatar-1", "user-1")), testutil.NewStorage(), &testutil.Publisher{}, 0)
		err := svc.Delete(ctx, "avatar-1", "user-2")
		require.ErrorIs(t, err, models.ErrForbidden)
	})

	t.Run("soft delete publishes cleanup event", func(t *testing.T) {
		avatar := testutil.Avatar("avatar-1", "user-1")
		avatar.Thumbnails = map[string]models.Thumbnail{
			models.ThumbnailSize100: {Size: models.ThumbnailSize100, S3Key: "thumb-100.jpg"},
		}
		repo := testutil.NewRepo(avatar)
		publisher := &testutil.Publisher{}
		svc := newService(repo, testutil.NewStorage(), publisher, 0)

		require.NoError(t, svc.Delete(ctx, "avatar-1", "user-1"))
		require.True(t, avatar.Deleted())
		require.Len(t, publisher.Deletes, 1)
		require.ElementsMatch(t, []string{avatar.S3Key, "thumb-100.jpg"}, publisher.Deletes[0].S3Keys)
	})

	t.Run("missing user id", func(t *testing.T) {
		svc := newService(testutil.NewRepo(testutil.Avatar("avatar-1", "user-1")), testutil.NewStorage(), &testutil.Publisher{}, 0)
		require.ErrorIs(t, svc.Delete(ctx, "avatar-1", ""), models.ErrMissingUserID)
	})

	t.Run("not found", func(t *testing.T) {
		svc := newService(testutil.NewRepo(), testutil.NewStorage(), &testutil.Publisher{}, 0)
		require.ErrorIs(t, svc.Delete(ctx, "missing", "user-1"), models.ErrNotFound)
	})
}

func TestDeleteByUser(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewRepo(testutil.Avatar("avatar-1", "user-1"))
	svc := newService(repo, testutil.NewStorage(), &testutil.Publisher{}, 0)

	require.ErrorIs(t, svc.DeleteByUser(ctx, "user-1", "user-2"), models.ErrForbidden)
	require.NoError(t, svc.DeleteByUser(ctx, "user-1", "user-1"))
}

func TestOpen(t *testing.T) {
	ctx := context.Background()
	pngData := testutil.PNG(t, 64, 64)

	newServiceWithAvatar := func(avatar *models.Avatar) (*service.AvatarService, *testutil.Storage) {
		storage := testutil.NewStorage()
		storage.Objects[avatar.S3Key] = pngData
		storage.Types[avatar.S3Key] = imaging.MimePNG
		for _, thumb := range avatar.Thumbnails {
			storage.Objects[thumb.S3Key] = pngData
			storage.Types[thumb.S3Key] = imaging.MimeJPEG
		}
		return newService(testutil.NewRepo(avatar), storage, &testutil.Publisher{}, 0), storage
	}

	t.Run("original", func(t *testing.T) {
		avatar := testutil.Avatar("avatar-1", "user-1")
		svc, _ := newServiceWithAvatar(avatar)

		img, err := svc.Open(ctx, "avatar-1", "", "")
		require.NoError(t, err)
		defer func() { _ = img.Reader.Close() }()

		body, err := io.ReadAll(img.Reader)
		require.NoError(t, err)
		require.Equal(t, pngData, body)
		require.Equal(t, imaging.MimePNG, img.MimeType)
		require.Equal(t, avatar.ETag, img.ETag)
	})

	t.Run("thumbnail", func(t *testing.T) {
		avatar := testutil.Avatar("avatar-1", "user-1")
		avatar.ProcessingStatus = models.ProcessingStatusCompleted
		avatar.Thumbnails = map[string]models.Thumbnail{
			models.ThumbnailSize100: {
				Size: models.ThumbnailSize100, S3Key: service.ThumbnailKey(avatar.ID, models.ThumbnailSize100),
				MimeType: imaging.MimeJPEG, Width: 100, Height: 100,
			},
		}
		svc, _ := newServiceWithAvatar(avatar)

		img, err := svc.Open(ctx, "avatar-1", models.ThumbnailSize100, "")
		require.NoError(t, err)
		defer func() { _ = img.Reader.Close() }()
		require.Equal(t, imaging.MimeJPEG, img.MimeType)
		require.Contains(t, img.ETag, models.ThumbnailSize100)
	})

	t.Run("thumbnail not ready", func(t *testing.T) {
		avatar := testutil.Avatar("avatar-1", "user-1")
		svc, _ := newServiceWithAvatar(avatar)

		_, err := svc.Open(ctx, "avatar-1", models.ThumbnailSize100, "")
		require.ErrorIs(t, err, models.ErrNotReady)
	})

	t.Run("thumbnail missing after processing", func(t *testing.T) {
		avatar := testutil.Avatar("avatar-1", "user-1")
		avatar.ProcessingStatus = models.ProcessingStatusCompleted
		svc, _ := newServiceWithAvatar(avatar)

		_, err := svc.Open(ctx, "avatar-1", models.ThumbnailSize300, "")
		require.ErrorIs(t, err, models.ErrNotFound)
	})

	t.Run("invalid size", func(t *testing.T) {
		svc, _ := newServiceWithAvatar(testutil.Avatar("avatar-1", "user-1"))
		_, err := svc.Open(ctx, "avatar-1", "512x512", "")
		require.ErrorIs(t, err, models.ErrInvalidSize)
	})

	t.Run("format conversion", func(t *testing.T) {
		svc, _ := newServiceWithAvatar(testutil.Avatar("avatar-1", "user-1"))
		img, err := svc.Open(ctx, "avatar-1", "original", "jpeg")
		require.NoError(t, err)
		defer func() { _ = img.Reader.Close() }()

		require.Equal(t, imaging.MimeJPEG, img.MimeType)
		require.NotNil(t, img.Size)
		require.Positive(t, img.Size)
		require.NotEqual(t, "etag-avatar-1", img.ETag)
	})

	t.Run("unsupported output format", func(t *testing.T) {
		svc, _ := newServiceWithAvatar(testutil.Avatar("avatar-1", "user-1"))
		_, err := svc.Open(ctx, "avatar-1", "original", "webp")
		require.ErrorIs(t, err, models.ErrUnsupportedFormat)
	})

	t.Run("same format streams original", func(t *testing.T) {
		svc, _ := newServiceWithAvatar(testutil.Avatar("avatar-1", "user-1"))
		img, err := svc.Open(ctx, "avatar-1", "original", "png")
		require.NoError(t, err)
		defer func() { _ = img.Reader.Close() }()
		require.Equal(t, imaging.MimePNG, img.MimeType)
	})

	t.Run("open by user", func(t *testing.T) {
		svc, _ := newServiceWithAvatar(testutil.Avatar("avatar-1", "user-1"))
		img, err := svc.OpenByUser(ctx, "user-1", "", "")
		require.NoError(t, err)
		defer func() { _ = img.Reader.Close() }()
		require.Equal(t, imaging.MimePNG, img.MimeType)
	})
}

func TestObjectKeyAndThumbnailKey(t *testing.T) {
	require.Equal(t, "avatars/user-1/avatar-1/original.png", service.ObjectKey("user-1", "avatar-1", imaging.MimePNG))
	require.Equal(t, "avatars/___user_1/avatar-1/original.jpg", service.ObjectKey("../user/1", "avatar-1", imaging.MimeJPEG))
	require.Equal(t, "thumbnails/avatar-1/100x100.jpg", service.ThumbnailKey("avatar-1", models.ThumbnailSize100))
}
