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
	"github.com/postman17/gophprofile/internal/broker"
	"github.com/postman17/gophprofile/internal/config"
	"github.com/postman17/gophprofile/internal/handlers"
	"github.com/postman17/gophprofile/internal/middleware"
	"github.com/postman17/gophprofile/internal/observability"
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
	var (
		shutdownTelemetry, stopMetrics func()
		postgres                       *repo.PostgresStorage
		kafka                          *broker.Kafka
	)
	// Keep cancellation and resource shutdown in one explicit sequence, including
	// partial initialization failures. Telemetry stays alive until cleanup ends.
	defer func() {
		stop()
		if kafka != nil {
			if err := kafka.Close(); err != nil {
				slog.Error("close kafka", "error", err)
			}
		}
		if postgres != nil {
			postgres.Close()
		}
		if stopMetrics != nil {
			stopMetrics()
		}
		if shutdownTelemetry != nil {
			shutdownTelemetry()
		}
	}()
	shutdownTelemetry, err = observability.Init(ctx, "gophprofile-server")
	if err != nil {
		return err
	}
	stopMetrics, err = observability.ServeMetrics(context.WithoutCancel(ctx), cfg.MetricsAddr)
	if err != nil {
		return fmt.Errorf("metrics listener: %w", err)
	}

	postgres, err = repo.NewPostgresStorage(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("initialize postgres: %w", err)
	}
	postgres.RegisterMetrics()

	storage, err := s3.NewS3Client(ctx, cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3Bucket)
	if err != nil {
		return fmt.Errorf("initialize s3: %w", err)
	}
	if err := app.EnsureBucket(ctx, storage); err != nil {
		return err
	}

	go storage.MonitorInventory(ctx)

	kafka, err = app.ConnectKafka(ctx, cfg, "gophprofile-server")
	if err != nil {
		return err
	}

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
	e.Use(observability.HTTP, echomw.Recover())

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
		// Keep Echo's logger during startup and graceful shutdown while wrapping
		// the complete HTTP exchange, including its centralized error handler.
		BeforeServeFunc: func(server *http.Server) error {
			server.Handler = observability.HTTPHandler(server.Handler)
			return nil
		},
	}
	if err := startConfig.Start(ctx, e); err != nil {
		return fmt.Errorf("http server: %w", err)
	}
	slog.Info("http server stopped")
	return nil
}
