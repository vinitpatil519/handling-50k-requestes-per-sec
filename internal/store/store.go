// Package store is the PostgreSQL persistence layer.
package store

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// Item is the main API resource.
type Item struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

// Rollup is a per-minute event counter delta.
type Rollup struct {
	Bucket time.Time
	Type   string
	Count  int64
}

// Store is implemented by Postgres and by in-memory fakes in tests.
type Store interface {
	GetItem(ctx context.Context, id int64) (Item, error)
	ListItems(ctx context.Context, limit int) ([]Item, error)
	CreateItem(ctx context.Context, name string, payload json.RawMessage) (Item, error)
	CountItems(ctx context.Context) (int64, error)
	AddRollups(ctx context.Context, rollups []Rollup) error
	Ping(ctx context.Context) error
	Close()
}

var (
	tracer        = otel.Tracer("github.com/titanedge/titanedge/internal/store")
	queryDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "titan_db_query_duration_seconds",
		Help:    "PostgreSQL query latency by operation.",
		Buckets: []float64{.0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1},
	}, []string{"op", "result"})
)

// Postgres implements Store with a pgx connection pool.
type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres opens a tuned connection pool.
func NewPostgres(ctx context.Context, url string, maxConns int32) (*Postgres, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	cfg.MaxConns = maxConns
	cfg.MinConns = min(2, maxConns)
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second
	// Statement cache keeps hot queries prepared per connection.
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeCacheStatement

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &Postgres{pool: pool}, nil
}

func observe(ctx context.Context, op string) (context.Context, func(error)) {
	ctx, span := tracer.Start(ctx, "db."+op)
	span.SetAttributes(attribute.String("db.system", "postgresql"), attribute.String("db.operation", op))
	start := time.Now()
	return ctx, func(err error) {
		result := "ok"
		if err != nil && !errors.Is(err, ErrNotFound) {
			result = "error"
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		queryDuration.WithLabelValues(op, result).Observe(time.Since(start).Seconds())
		span.End()
	}
}

func (p *Postgres) GetItem(ctx context.Context, id int64) (it Item, err error) {
	ctx, done := observe(ctx, "get_item")
	defer func() { done(err) }()
	err = p.pool.QueryRow(ctx,
		`SELECT id, name, payload, created_at FROM items WHERE id = $1`, id,
	).Scan(&it.ID, &it.Name, &it.Payload, &it.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return it, ErrNotFound
	}
	return it, err
}

func (p *Postgres) ListItems(ctx context.Context, limit int) (items []Item, err error) {
	ctx, done := observe(ctx, "list_items")
	defer func() { done(err) }()
	rows, err := p.pool.Query(ctx,
		`SELECT id, name, payload, created_at FROM items ORDER BY created_at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items = make([]Item, 0, limit)
	for rows.Next() {
		var it Item
		if err = rows.Scan(&it.ID, &it.Name, &it.Payload, &it.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

func (p *Postgres) CreateItem(ctx context.Context, name string, payload json.RawMessage) (it Item, err error) {
	ctx, done := observe(ctx, "create_item")
	defer func() { done(err) }()
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	err = p.pool.QueryRow(ctx,
		`INSERT INTO items (name, payload) VALUES ($1, $2) RETURNING id, name, payload, created_at`,
		name, payload,
	).Scan(&it.ID, &it.Name, &it.Payload, &it.CreatedAt)
	return it, err
}

func (p *Postgres) CountItems(ctx context.Context) (n int64, err error) {
	ctx, done := observe(ctx, "count_items")
	defer func() { done(err) }()
	// Planner estimate: O(1) instead of a full scan on a hot table.
	err = p.pool.QueryRow(ctx,
		`SELECT GREATEST(reltuples::bigint, 0) FROM pg_class WHERE relname = 'items'`).Scan(&n)
	if err == nil && n == 0 {
		err = p.pool.QueryRow(ctx, `SELECT count(*) FROM items`).Scan(&n)
	}
	return n, err
}

// AddRollups upserts counter deltas in a single round trip.
func (p *Postgres) AddRollups(ctx context.Context, rollups []Rollup) (err error) {
	if len(rollups) == 0 {
		return nil
	}
	ctx, done := observe(ctx, "add_rollups")
	defer func() { done(err) }()
	buckets := make([]time.Time, len(rollups))
	types := make([]string, len(rollups))
	counts := make([]int64, len(rollups))
	for i, r := range rollups {
		buckets[i], types[i], counts[i] = r.Bucket, r.Type, r.Count
	}
	_, err = p.pool.Exec(ctx, `
		INSERT INTO event_rollups (bucket, event_type, count)
		SELECT * FROM unnest($1::timestamptz[], $2::text[], $3::bigint[])
		ON CONFLICT (bucket, event_type)
		DO UPDATE SET count = event_rollups.count + EXCLUDED.count`,
		buckets, types, counts)
	return err
}

func (p *Postgres) Ping(ctx context.Context) error { return p.pool.Ping(ctx) }

func (p *Postgres) Close() { p.pool.Close() }

// migrationLockID is an arbitrary constant used with pg_advisory_lock so that
// only one replica (or the Helm hook Job) runs migrations at a time.
const migrationLockID = 727_310_001

// Migrate applies embedded SQL migrations in lexical order exactly once.
func (p *Postgres) Migrate(ctx context.Context, log *slog.Logger) error {
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migrationLockID)
	}()

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return err
	}

	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		version := strings.TrimSuffix(strings.TrimPrefix(name, "migrations/"), ".sql")
		var exists bool
		if err := conn.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		sql, err := migrationFS.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		// Simple protocol allows multi-statement migration files.
		if _, err := tx.Exec(ctx, string(sql), pgx.QueryExecModeSimpleProtocol); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", version, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		log.Info("migration applied", "version", version)
	}
	return nil
}
