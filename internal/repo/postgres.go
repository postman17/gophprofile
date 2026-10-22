package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/postman17/gophprofile/internal/models"
)

const avatarColumns = `id::text, user_id, file_name, mime_type, size_bytes, s3_key,
	COALESCE(thumbnail_s3_keys::text, '{}'), width, height, etag,
	upload_status::text, processing_status::text, created_at, updated_at, deleted_at`

type PostgresStorage struct {
	pool *pgxpool.Pool
}

func NewPostgresStorage(ctx context.Context, dsn string) (*PostgresStorage, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	if err := Migrate(ctx, dsn); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &PostgresStorage{pool: pool}, nil
}

func (s *PostgresStorage) Close() {
	s.pool.Close()
}

func (s *PostgresStorage) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func (s *PostgresStorage) Create(ctx context.Context, avatar *models.Avatar) error {
	thumbnails, err := marshalThumbnails(avatar.Thumbnails)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO avatars (id, user_id, file_name, mime_type, size_bytes, s3_key, thumbnail_s3_keys,
			width, height, etag, upload_status, processing_status, created_at, updated_at)
		VALUES ($1::text::uuid, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $10, $11::text::avatar_upload_status, $12::text::avatar_processing_status, $13, $14)`,
		avatar.ID, avatar.UserID, avatar.FileName, avatar.MimeType, avatar.SizeBytes, avatar.S3Key,
		thumbnails, avatar.Width, avatar.Height, avatar.ETag,
		string(avatar.UploadStatus), string(avatar.ProcessingStatus), avatar.CreatedAt, avatar.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert avatar: %w", err)
	}
	return nil
}

func (s *PostgresStorage) GetByID(ctx context.Context, id string) (*models.Avatar, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+avatarColumns+` FROM avatars WHERE id = $1::text::uuid`, id)
	return scanAvatar(row)
}

func (s *PostgresStorage) GetActiveByUser(ctx context.Context, userID string) (*models.Avatar, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+avatarColumns+`
		FROM avatars
		WHERE user_id = $1 AND deleted_at IS NULL
		ORDER BY created_at DESC
		LIMIT 1`, userID)
	return scanAvatar(row)
}

func (s *PostgresStorage) ListByUser(ctx context.Context, userID string) ([]*models.Avatar, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+avatarColumns+`
		FROM avatars
		WHERE user_id = $1 AND deleted_at IS NULL
		ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("select avatars: %w", err)
	}
	defer rows.Close()

	avatars := make([]*models.Avatar, 0, 8)
	for rows.Next() {
		avatar, err := scanAvatar(rows)
		if err != nil {
			return nil, err
		}
		avatars = append(avatars, avatar)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate avatars: %w", err)
	}
	return avatars, nil
}

func (s *PostgresStorage) SoftDelete(ctx context.Context, id, userID string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE avatars
		SET deleted_at = NOW(), updated_at = NOW(), upload_status = $3::text::avatar_upload_status
		WHERE id = $1::text::uuid AND user_id = $2 AND deleted_at IS NULL`,
		id, userID, string(models.UploadStatusDeleted))
	if err != nil {
		return fmt.Errorf("soft delete avatar: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return nil
}

func (s *PostgresStorage) SetProcessingStatus(ctx context.Context, id string, status models.ProcessingStatus) error {
	tag, err := s.pool.Exec(ctx, `UPDATE avatars
		SET processing_status = $2::text::avatar_processing_status, updated_at = NOW()
		WHERE id = $1::text::uuid`,
		id, string(status))
	if err != nil {
		return fmt.Errorf("update processing status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return nil
}

func (s *PostgresStorage) SetProcessed(ctx context.Context, id string, thumbnails map[string]models.Thumbnail, width, height int) error {
	payload, err := marshalThumbnails(thumbnails)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE avatars
		SET thumbnail_s3_keys = $2::jsonb, width = $3, height = $4, processing_status = $5::text::avatar_processing_status, updated_at = NOW()
		WHERE id = $1::text::uuid`,
		id, payload, width, height, string(models.ProcessingStatusCompleted))
	if err != nil {
		return fmt.Errorf("save processing result: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return nil
}

func (s *PostgresStorage) IsEventProcessed(ctx context.Context, eventID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM processed_events WHERE event_id = $1::text::uuid)`, eventID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check event processed: %w", err)
	}
	return exists, nil
}

func (s *PostgresStorage) MarkEventProcessed(ctx context.Context, eventID, eventType string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `INSERT INTO processed_events (event_id, event_type)
		VALUES ($1::text::uuid, $2)
		ON CONFLICT (event_id) DO NOTHING`, eventID, eventType)
	if err != nil {
		return false, fmt.Errorf("mark event processed: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func scanAvatar(row pgx.Row) (*models.Avatar, error) {
	var (
		avatar     models.Avatar
		thumbnails string
		upload     string
		processing string
	)

	err := row.Scan(
		&avatar.ID, &avatar.UserID, &avatar.FileName, &avatar.MimeType, &avatar.SizeBytes, &avatar.S3Key,
		&thumbnails, &avatar.Width, &avatar.Height, &avatar.ETag, &upload, &processing,
		&avatar.CreatedAt, &avatar.UpdatedAt, &avatar.DeletedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, models.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan avatar: %w", err)
	}

	avatar.UploadStatus = models.UploadStatus(upload)
	avatar.ProcessingStatus = models.ProcessingStatus(processing)
	if thumbnails != "" {
		if err := json.Unmarshal([]byte(thumbnails), &avatar.Thumbnails); err != nil {
			return nil, fmt.Errorf("decode thumbnails: %w", err)
		}
	}
	if avatar.Thumbnails == nil {
		avatar.Thumbnails = map[string]models.Thumbnail{}
	}
	return &avatar, nil
}

func marshalThumbnails(thumbnails map[string]models.Thumbnail) (string, error) {
	if len(thumbnails) == 0 {
		return "{}", nil
	}
	payload, err := json.Marshal(thumbnails)
	if err != nil {
		return "", fmt.Errorf("encode thumbnails: %w", err)
	}
	return string(payload), nil
}
