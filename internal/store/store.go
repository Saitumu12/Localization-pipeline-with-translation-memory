// Package store is the data access layer: SQL in, structs out. Business rules
// live in the pipeline package.
package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed all:migrations
var migrationFiles embed.FS

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Migrate applies any migration files this database has not seen yet.
func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`)
	if err != nil {
		return fmt.Errorf("store: migration table: %w", err)
	}

	entries, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(entries)

	for _, name := range entries {
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, name).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		body, err := migrationFiles.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("store: applying %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

type Project struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	SourceLocale    string    `json:"source_locale"`
	TargetLocale    string    `json:"target_locale"`
	LengthTolerance float32   `json:"length_tolerance"`
	CreatedAt       time.Time `json:"created_at"`
}

func (s *Store) CreateProject(ctx context.Context, p Project) (Project, error) {
	if p.LengthTolerance == 0 {
		p.LengthTolerance = 1
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO projects (name, source_locale, target_locale, length_tolerance)
		VALUES ($1, $2, $3, $4)
		RETURNING id, created_at`,
		p.Name, p.SourceLocale, p.TargetLocale, p.LengthTolerance,
	).Scan(&p.ID, &p.CreatedAt)
	return p, err
}

func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, source_locale, target_locale, length_tolerance, created_at
		FROM projects ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Project{}
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.SourceLocale, &p.TargetLocale, &p.LengthTolerance, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetProject(ctx context.Context, id int64) (Project, error) {
	var p Project
	err := s.pool.QueryRow(ctx, `
		SELECT id, name, source_locale, target_locale, length_tolerance, created_at
		FROM projects WHERE id = $1`, id,
	).Scan(&p.ID, &p.Name, &p.SourceLocale, &p.TargetLocale, &p.LengthTolerance, &p.CreatedAt)
	return p, err
}
