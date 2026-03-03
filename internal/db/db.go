package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaFS embed.FS

func Open(ctx context.Context, dbPath string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	database, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if _, err := database.ExecContext(ctx, "PRAGMA journal_mode = WAL;"); err != nil {
		database.Close()
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	if _, err := database.ExecContext(ctx, "PRAGMA busy_timeout = 5000;"); err != nil {
		database.Close()
		return nil, fmt.Errorf("set busy timeout: %w", err)
	}
	if _, err := database.ExecContext(ctx, "PRAGMA foreign_keys = ON;"); err != nil {
		database.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}
	if err := database.PingContext(ctx); err != nil {
		database.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	schema, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		database.Close()
		return nil, fmt.Errorf("read schema: %w", err)
	}
	if _, err := database.ExecContext(ctx, string(schema)); err != nil {
		database.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}

	return database, nil
}
