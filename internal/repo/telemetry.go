package repo

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/postman17/gophprofile/internal/observability"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Query arguments and SQL text are deliberately excluded: they may contain personal data.
type queryTracer struct{}

func (queryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	operation := "query"
	if fields := strings.Fields(data.SQL); len(fields) > 0 {
		operation = strings.ToUpper(fields[0])
	}
	ctx, _ = observability.Start(ctx, "postgresql "+operation, trace.SpanKindClient, attribute.String("db.system.name", "postgresql"), attribute.String("db.operation.name", operation))
	return ctx
}
func (queryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	observability.End(trace.SpanFromContext(ctx), data.Err)
}

func (s *PostgresStorage) RegisterMetrics() {
	for _, metric := range []struct {
		name, help string
		value      func() float64
	}{
		{"db_pool_connections", "Total PostgreSQL pool connections.", func() float64 { return float64(s.pool.Stat().TotalConns()) }},
		{"db_pool_acquired_connections", "Acquired PostgreSQL connections.", func() float64 { return float64(s.pool.Stat().AcquiredConns()) }},
		{"db_pool_idle_connections", "Idle PostgreSQL connections.", func() float64 { return float64(s.pool.Stat().IdleConns()) }},
		{"db_pool_max_connections", "Maximum PostgreSQL pool connections.", func() float64 { return float64(s.pool.Stat().MaxConns()) }},
	} {
		prometheus.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: metric.name, Help: metric.help}, metric.value))
	}
}
