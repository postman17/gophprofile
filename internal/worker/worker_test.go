package worker_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/postman17/gophprofile/internal/broker"
	"github.com/postman17/gophprofile/internal/models"
	"github.com/postman17/gophprofile/internal/testutil"
	"github.com/postman17/gophprofile/internal/worker"
)

func uploadEvent(t *testing.T, avatarID string) broker.Event {
	t.Helper()
	body, err := json.Marshal(models.AvatarUploadEvent{
		EventID:  "3f1b3f9e-0f3a-4a6a-9d1b-6d0c3f3a1111",
		AvatarID: avatarID,
		UserID:   "user-1",
		S3Key:    "avatars/user-1/" + avatarID + "/original.png",
	})
	require.NoError(t, err)
	return broker.Event{ID: "3f1b3f9e-0f3a-4a6a-9d1b-6d0c3f3a1111", Type: models.EventTypeUploaded, Body: body}
}

func deleteEvent(t *testing.T, avatarID string, keys []string) broker.Event {
	t.Helper()
	body, err := json.Marshal(models.AvatarDeleteEvent{
		EventID:  "5c1b3f9e-0f3a-4a6a-9d1b-6d0c3f3a2222",
		AvatarID: avatarID,
		S3Keys:   keys,
	})
	require.NoError(t, err)
	return broker.Event{ID: "5c1b3f9e-0f3a-4a6a-9d1b-6d0c3f3a2222", Type: models.EventTypeDeleted, Body: body}
}

func newWorker(repo *testutil.Repo, storage *testutil.Storage, retries int) *worker.Worker {
	if retries == 0 {
		retries = 3
	}
	return worker.New(repo, storage, worker.Options{Retries: retries, BaseDelay: time.Millisecond})
}

func TestHandleUploadEvent(t *testing.T) {
	avatar := testutil.Avatar("avatar-1", "user-1")
	repo := testutil.NewRepo(avatar)
	storage := testutil.NewStorage()
	storage.Objects[avatar.S3Key] = testutil.PNG(t, 500, 250)

	w := newWorker(repo, storage, 0)
	require.NoError(t, w.HandleWithRetry(context.Background(), uploadEvent(t, avatar.ID)))

	require.Equal(t, models.ProcessingStatusCompleted, avatar.ProcessingStatus)
	require.Equal(t, 500, repo.ProcessedWidth)
	require.Equal(t, 250, repo.ProcessedHeight)
	require.Len(t, repo.ProcessedThumbs, 2)
	require.Contains(t, repo.ProcessedThumbs, models.ThumbnailSize100)
	require.Contains(t, repo.ProcessedThumbs, models.ThumbnailSize300)

	for _, thumb := range repo.ProcessedThumbs {
		require.Contains(t, storage.Objects, thumb.S3Key)
		require.Equal(t, "image/jpeg", thumb.MimeType)
	}
	require.True(t, repo.Events["3f1b3f9e-0f3a-4a6a-9d1b-6d0c3f3a1111"])
}

func TestHandleUploadIsIdempotent(t *testing.T) {
	avatar := testutil.Avatar("avatar-1", "user-1")
	avatar.ProcessingStatus = models.ProcessingStatusCompleted
	repo := testutil.NewRepo(avatar)
	storage := testutil.NewStorage()
	storage.Objects[avatar.S3Key] = testutil.PNG(t, 40, 40)

	w := newWorker(repo, storage, 0)
	require.NoError(t, w.HandleWithRetry(context.Background(), uploadEvent(t, avatar.ID)))
	require.Empty(t, repo.ProcessedThumbs)

	repo.Events["3f1b3f9e-0f3a-4a6a-9d1b-6d0c3f3a1111"] = true
	require.NoError(t, w.HandleEvent(context.Background(), uploadEvent(t, avatar.ID)))
	require.Empty(t, repo.ProcessedThumbs)
}

func TestHandleUploadSkipsMissingAvatar(t *testing.T) {
	repo := testutil.NewRepo()
	w := newWorker(repo, testutil.NewStorage(), 0)

	require.NoError(t, w.HandleEvent(context.Background(), uploadEvent(t, "missing")))
	require.True(t, repo.Events["3f1b3f9e-0f3a-4a6a-9d1b-6d0c3f3a1111"])
}

func TestHandleUploadMarksFailed(t *testing.T) {
	avatar := testutil.Avatar("avatar-1", "user-1")
	repo := testutil.NewRepo(avatar)
	storage := testutil.NewStorage()

	w := newWorker(repo, storage, 1)
	err := w.HandleWithRetry(context.Background(), uploadEvent(t, avatar.ID))
	require.Error(t, err)
	require.Equal(t, models.ProcessingStatusFailed, avatar.ProcessingStatus)
}

func TestHandleDeleteEvent(t *testing.T) {
	avatar := testutil.Avatar("avatar-1", "user-1")
	avatar.Thumbnails = map[string]models.Thumbnail{
		models.ThumbnailSize100: {Size: models.ThumbnailSize100, S3Key: "thumbnails/avatar-1/100x100.jpg"},
	}
	repo := testutil.NewRepo(avatar)
	storage := testutil.NewStorage()
	storage.Objects[avatar.S3Key] = []byte("original")
	storage.Objects["thumbnails/avatar-1/100x100.jpg"] = []byte("thumb")

	w := newWorker(repo, storage, 0)
	require.NoError(t, w.HandleEvent(context.Background(), deleteEvent(t, avatar.ID, nil)))

	require.Empty(t, storage.Objects)
	require.Len(t, storage.Deleted, 2)
	require.True(t, repo.Events["5c1b3f9e-0f3a-4a6a-9d1b-6d0c3f3a2222"])
}

func TestHandleUnknownEventType(t *testing.T) {
	repo := testutil.NewRepo()
	w := newWorker(repo, testutil.NewStorage(), 0)

	event := broker.Event{ID: "7c1b3f9e-0f3a-4a6a-9d1b-6d0c3f3a3333", Type: "avatar.unknown", Body: []byte("{}")}
	require.NoError(t, w.HandleEvent(context.Background(), event))
	require.True(t, repo.Events[event.ID])
}

func TestHandleInvalidPayload(t *testing.T) {
	repo := testutil.NewRepo()
	w := newWorker(repo, testutil.NewStorage(), 3)

	event := broker.Event{ID: "8c1b3f9e-0f3a-4a6a-9d1b-6d0c3f3a4444", Type: models.EventTypeUploaded, Body: []byte("{")}
	err := w.HandleWithRetry(context.Background(), event)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed after 3 attempts")
}

func TestHandleWithRetryStopsOnCancelledContext(t *testing.T) {
	avatar := testutil.Avatar("avatar-1", "user-1")
	repo := testutil.NewRepo(avatar)
	storage := testutil.NewStorage()

	w := worker.New(repo, storage, worker.Options{Retries: 5, BaseDelay: time.Minute})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := w.HandleWithRetry(ctx, uploadEvent(t, avatar.ID))
	require.ErrorIs(t, err, context.Canceled)
}

func TestHandleEventRepositoryError(t *testing.T) {
	avatar := testutil.Avatar("avatar-1", "user-1")
	repo := testutil.NewRepo(avatar)
	repo.EventErr = errors.New("db is down")

	w := newWorker(repo, testutil.NewStorage(), 1)
	require.Error(t, w.HandleEvent(context.Background(), uploadEvent(t, avatar.ID)))
}
