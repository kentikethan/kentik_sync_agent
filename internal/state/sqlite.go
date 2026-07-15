package state

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kentikethan/kentik_sync_agent/internal/core"
	_ "modernc.org/sqlite" // pure-Go driver, keeps the agent a static binary
)

const schema = `
CREATE TABLE IF NOT EXISTS mappings (
	source_name  TEXT NOT NULL,
	object_type  TEXT NOT NULL,
	external_id  TEXT NOT NULL,
	kentik_id    TEXT NOT NULL,
	content_hash TEXT NOT NULL,
	updated_at   TEXT NOT NULL,
	PRIMARY KEY (source_name, object_type, external_id)
);

CREATE TABLE IF NOT EXISTS cursors (
	source_name TEXT NOT NULL,
	object_type TEXT NOT NULL,
	cursor      TEXT NOT NULL,
	updated_at  TEXT NOT NULL,
	PRIMARY KEY (source_name, object_type)
);
`

// SQLiteStore is the default Store implementation, backed by a local
// database file via the pure-Go modernc.org/sqlite driver.
type SQLiteStore struct {
	db *sql.DB
}

// OpenSQLite opens (creating if necessary, including parent directories)
// the SQLite database at path and ensures its schema exists.
func OpenSQLite(path string) (*SQLiteStore, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("state: creating directory %s: %w", dir, err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("state: opening %s: %w", path, err)
	}
	// SQLite only supports one writer at a time; a single connection avoids
	// SQLITE_BUSY errors under the agent's own concurrent source jobs.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("state: creating schema: %w", err)
	}
	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Close() error { return s.db.Close() }

func (s *SQLiteStore) GetCursor(ctx context.Context, sourceName string, obj core.ObjectType) (string, bool, error) {
	var cursor string
	err := s.db.QueryRowContext(ctx,
		`SELECT cursor FROM cursors WHERE source_name = ? AND object_type = ?`,
		sourceName, string(obj),
	).Scan(&cursor)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("state: get cursor: %w", err)
	}
	return cursor, true, nil
}

func (s *SQLiteStore) SetCursor(ctx context.Context, sourceName string, obj core.ObjectType, cursor string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO cursors (source_name, object_type, cursor, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (source_name, object_type) DO UPDATE SET cursor = excluded.cursor, updated_at = excluded.updated_at
	`, sourceName, string(obj), cursor, nowRFC3339())
	if err != nil {
		return fmt.Errorf("state: set cursor: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetMapping(ctx context.Context, sourceName string, obj core.ObjectType, externalID string) (Mapping, bool, error) {
	var m Mapping
	var updatedAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT source_name, object_type, external_id, kentik_id, content_hash, updated_at
		FROM mappings WHERE source_name = ? AND object_type = ? AND external_id = ?
	`, sourceName, string(obj), externalID).Scan(&m.SourceName, &m.ObjectType, &m.ExternalID, &m.KentikID, &m.ContentHash, &updatedAt)
	if err == sql.ErrNoRows {
		return Mapping{}, false, nil
	}
	if err != nil {
		return Mapping{}, false, fmt.Errorf("state: get mapping: %w", err)
	}
	m.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return m, true, nil
}

func (s *SQLiteStore) UpsertMapping(ctx context.Context, m Mapping) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO mappings (source_name, object_type, external_id, kentik_id, content_hash, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (source_name, object_type, external_id) DO UPDATE SET
			kentik_id = excluded.kentik_id,
			content_hash = excluded.content_hash,
			updated_at = excluded.updated_at
	`, m.SourceName, string(m.ObjectType), m.ExternalID, m.KentikID, m.ContentHash, nowRFC3339())
	if err != nil {
		return fmt.Errorf("state: upsert mapping: %w", err)
	}
	return nil
}

func (s *SQLiteStore) DeleteMapping(ctx context.Context, sourceName string, obj core.ObjectType, externalID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM mappings WHERE source_name = ? AND object_type = ? AND external_id = ?`,
		sourceName, string(obj), externalID,
	)
	if err != nil {
		return fmt.Errorf("state: delete mapping: %w", err)
	}
	return nil
}

func (s *SQLiteStore) ListMappings(ctx context.Context, sourceName string, obj core.ObjectType) ([]Mapping, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT source_name, object_type, external_id, kentik_id, content_hash, updated_at
		FROM mappings WHERE source_name = ? AND object_type = ?
	`, sourceName, string(obj))
	if err != nil {
		return nil, fmt.Errorf("state: list mappings: %w", err)
	}
	defer rows.Close()

	var out []Mapping
	for rows.Next() {
		var m Mapping
		var updatedAt string
		if err := rows.Scan(&m.SourceName, &m.ObjectType, &m.ExternalID, &m.KentikID, &m.ContentHash, &updatedAt); err != nil {
			return nil, fmt.Errorf("state: scan mapping: %w", err)
		}
		m.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		out = append(out, m)
	}
	return out, rows.Err()
}

// nowRFC3339 is a package-level var so tests can override it; production
// code must not call time.Now() directly per repo convention elsewhere, but
// the state package is the one place wall-clock time is actually needed.
var nowRFC3339 = func() string { return time.Now().UTC().Format(time.RFC3339) }
