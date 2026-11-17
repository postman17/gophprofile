package observability

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	HTTPRequests       = promauto.NewCounterVec(prometheus.CounterOpts{Name: "http_requests_total", Help: "HTTP requests by route template and status."}, []string{"method", "route", "status"})
	HTTPDuration       = promauto.NewHistogramVec(prometheus.HistogramOpts{Name: "http_request_duration_seconds", Help: "HTTP request latency.", Buckets: prometheus.DefBuckets}, []string{"method", "route"})
	Uploads            = promauto.NewCounterVec(prometheus.CounterOpts{Name: "avatars_uploads_total", Help: "Upload attempts reaching the service."}, []string{"status"})
	UploadDuration     = promauto.NewHistogramVec(prometheus.HistogramOpts{Name: "avatars_upload_duration_seconds", Help: "Upload latency.", Buckets: []float64{.01, .05, .1, .25, .5, 1, 2.5, 5, 10, 30}}, []string{"status"})
	Events             = promauto.NewCounterVec(prometheus.CounterOpts{Name: "avatar_events_total", Help: "Kafka produce and consume outcomes."}, []string{"operation", "topic", "status"})
	EventDuration      = promauto.NewHistogramVec(prometheus.HistogramOpts{Name: "avatar_event_duration_seconds", Help: "Kafka operation duration.", Buckets: []float64{.01, .1, .5, 1, 5, 10, 30, 60}}, []string{"operation", "topic"})
	StorageBytes       = promauto.NewGauge(prometheus.GaugeOpts{Name: "avatars_storage_bytes", Help: "Actual S3 bucket bytes including originals and thumbnails; refreshed every minute."})
	StorageObjects     = promauto.NewGauge(prometheus.GaugeOpts{Name: "avatars_storage_objects", Help: "Actual object count in S3 bucket."})
	StorageLastSuccess = promauto.NewGauge(prometheus.GaugeOpts{Name: "avatars_storage_last_success_timestamp_seconds", Help: "Last successful S3 inventory timestamp."})
)

func Status(err error) string {
	if err != nil {
		return "error"
	}
	return "success"
}

// ServeMetrics binds synchronously so address conflicts fail startup, and shuts down on cancellation.
func ServeMetrics(ctx context.Context, addr string) (func(), error) {
	listenConfig := net.ListenConfig{}
	listener, err := listenConfig.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("metrics server failed", "error", err)
		}
	}()
	done := make(chan struct{})
	stopped := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
		case <-done:
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("metrics shutdown", "error", err)
		}
	}()
	return func() { once.Do(func() { close(done) }); <-stopped }, nil
}
