package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/labstack/echo/v5"
	echomw "github.com/labstack/echo/v5/middleware"

	"github.com/postman17/gophprofile/internal/app"
	"github.com/postman17/gophprofile/internal/config"
	"github.com/postman17/gophprofile/internal/handlers"
	"github.com/postman17/gophprofile/internal/middleware"
	"github.com/postman17/gophprofile/internal/repo"
	"github.com/postman17/gophprofile/internal/s3"
	"github.com/postman17/gophprofile/internal/service"
	"github.com/postman17/gophprofile/web"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if err := run(); err != nil {
		slog.Error("server stopped with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	postgres := repo.NewPostgresStorage(ctx, cfg.DatabaseURL)
	defer postgres.Close()

	storage := s3.NewS3Client(ctx, cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3Bucket)
	if err := app.EnsureBucket(ctx, storage); err != nil {
		return err
	}

	kafka, err := app.ConnectKafka(ctx, cfg, "gophprofile-server")
	if err != nil {
		return err
	}
	defer func() { _ = kafka.Close() }()

	avatarService := service.NewAvatarService(postgres, storage, kafka, service.Options{
		MaxFileSize: cfg.MaxFileSize,
		Logger:      slog.Default(),
	})

	h, err := handlers.NewHandlers(avatarService, cfg.MaxFileSize, web.Templates, map[string]handlers.Checker{
		"database": postgres.Ping,
		"storage":  storage.Health,
		"broker":   kafka.Health,
	})
	if err != nil {
		return err
	}

	e := echo.New()
	e.Logger = slog.Default()

	uploadMiddleware := []echo.MiddlewareFunc{
		middleware.PayloadTooLarge(cfg.MaxFileSize),
		echomw.BodyLimit(cfg.MaxFileSize),
	}

	v1 := e.Group("/api/v1")
	v1.POST("/avatars", h.Avatars.Upload, uploadMiddleware...)
	v1.GET("/avatars/:avatar_id", h.Avatars.Get)
	v1.GET("/avatars/:avatar_id/metadata", h.Avatars.Metadata)
	v1.DELETE("/avatars/:avatar_id", h.Avatars.Delete)
	v1.GET("/users/:user_id/avatar", h.Avatars.GetByUser)
	v1.DELETE("/users/:user_id/avatar", h.Avatars.DeleteByUser)
	v1.GET("/users/:user_id/avatars", h.Avatars.ListByUser)

	e.GET("/health", h.Health.Health)

	webRoutes := e.Group("/web")
	webRoutes.GET("/upload", h.Web.UploadForm)
	webRoutes.POST("/upload", h.Web.Upload, echomw.BodyLimit(cfg.MaxFileSize))
	webRoutes.GET("/gallery/:user_id", h.Web.Gallery)

	e.GET("/", func(c *echo.Context) error {
		return c.Redirect(http.StatusFound, "/web/upload")
	})

	slog.Info("http server starting", "address", cfg.HTTPAddr)
	startConfig := echo.StartConfig{
		Address:         cfg.HTTPAddr,
		GracefulTimeout: config.DefaultGracefulTimeout,
	}
	if err := startConfig.Start(ctx, e); err != nil {
		return fmt.Errorf("http server: %w", err)
	}
	slog.Info("http server stopped")
	return nil
}
