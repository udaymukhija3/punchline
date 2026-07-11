package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type migrationDB interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

func main() {
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		if truthy(os.Getenv("PUNCHLINE_REQUIRE_DATABASE")) {
			log.Fatal("DATABASE_URL is required when PUNCHLINE_REQUIRE_DATABASE is enabled")
		}
		log.Println("DATABASE_URL is not set; skipping migrations")
		return
	}

	dir, err := migrationsDir()
	if err != nil {
		log.Fatal(err)
	}
	files, err := migrationFiles(dir)
	if err != nil {
		log.Fatal(err)
	}
	if len(files) == 0 {
		log.Fatalf("no migration files found in %s", dir)
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		log.Fatalf("open postgres: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("connect postgres: %v", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		log.Fatalf("get migration connection: %v", err)
	}
	defer conn.Close()
	if err := lockMigrations(ctx, conn); err != nil {
		log.Fatal(err)
	}
	defer unlockMigrations(conn)
	if err := ensureMigrationTable(ctx, conn); err != nil {
		log.Fatal(err)
	}
	for _, path := range files {
		version := filepath.Base(path)
		checksum, err := migrationChecksum(path)
		if err != nil {
			log.Fatal(err)
		}
		applied, err := migrationApplied(ctx, conn, version, checksum)
		if err != nil {
			log.Fatal(err)
		}
		if applied {
			log.Printf("migration %s already applied", version)
			continue
		}
		if err := applyMigration(ctx, conn, version, path, checksum); err != nil {
			log.Fatal(err)
		}
		log.Printf("migration %s applied", version)
	}
}

func migrationsDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("PUNCHLINE_MIGRATIONS_DIR")); dir != "" {
		return dir, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(wd, "migrations")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate, nil
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			break
		}
		wd = parent
	}
	return "", errors.New("migrations directory not found; set PUNCHLINE_MIGRATIONS_DIR")
}

func migrationFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		files = append(files, filepath.Join(dir, entry.Name()))
	}
	sort.Strings(files)
	return files, nil
}

func lockMigrations(ctx context.Context, db migrationDB) error {
	if _, err := db.ExecContext(ctx, `SELECT pg_advisory_lock(hashtext('punchline_schema_migrations'))`); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	return nil
}

func unlockMigrations(db migrationDB) {
	_, _ = db.ExecContext(context.Background(), `SELECT pg_advisory_unlock(hashtext('punchline_schema_migrations'))`)
}

func ensureMigrationTable(ctx context.Context, db migrationDB) error {
	_, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
	version TEXT PRIMARY KEY,
	checksum TEXT,
	applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`)
	if err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS checksum TEXT`); err != nil {
		return fmt.Errorf("add schema_migrations checksum: %w", err)
	}
	return nil
}

func migrationApplied(ctx context.Context, db migrationDB, version string, checksum string) (bool, error) {
	var stored sql.NullString
	err := db.QueryRowContext(ctx, `SELECT checksum FROM schema_migrations WHERE version = $1`, version).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check migration %s: %w", version, err)
	}
	if !stored.Valid || stored.String == "" {
		if _, err := db.ExecContext(ctx, `UPDATE schema_migrations SET checksum = $2 WHERE version = $1`, version, checksum); err != nil {
			return false, fmt.Errorf("record baseline checksum for %s: %w", version, err)
		}
		return true, nil
	}
	if stored.String != checksum {
		return false, fmt.Errorf("migration %s has changed after it was applied", version)
	}
	return true, nil
}

func migrationChecksum(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read migration %s: %w", filepath.Base(path), err)
	}
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:]), nil
}

func applyMigration(ctx context.Context, db migrationDB, version string, path string, checksum string) error {
	sqlBytes, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read migration %s: %w", version, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", version, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, string(sqlBytes)); err != nil {
		return fmt.Errorf("apply migration %s: %w", version, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, checksum) VALUES ($1, $2)`, version, checksum); err != nil {
		return fmt.Errorf("record migration %s: %w", version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", version, err)
	}
	return nil
}

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
