package postgres

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Migrator manages database migrations from SQL files.
type Migrator struct {
	pool          *pgxpool.Pool
	migrationsDir string
}

// NewMigrator constructs a Migrator.
func NewMigrator(pool *pgxpool.Pool, migrationsDir string) *Migrator {
	return &Migrator{
		pool:          pool,
		migrationsDir: migrationsDir,
	}
}

// EnsureSchemaMigrationsTable creates the migrations tracking table if not present.
func (m *Migrator) EnsureSchemaMigrationsTable(ctx context.Context) error {
	query := `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version BIGINT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
	`
	_, err := m.pool.Exec(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to create schema_migrations table: %w", err)
	}
	return nil
}

// Up applies all pending migrations in ascending order.
func (m *Migrator) Up(ctx context.Context) error {
	if err := m.EnsureSchemaMigrationsTable(ctx); err != nil {
		return err
	}

	files, err := os.ReadDir(m.migrationsDir)
	if err != nil {
		return fmt.Errorf("failed to read migrations directory '%s': %w", m.migrationsDir, err)
	}

	// Filter and sort .up.sql files
	var upFiles []string
	for _, f := range files {
		if !f.IsDir() && strings.HasSuffix(f.Name(), ".up.sql") {
			upFiles = append(upFiles, f.Name())
		}
	}
	sort.Strings(upFiles)

	for _, fileName := range upFiles {
		version, err := parseMigrationVersion(fileName)
		if err != nil {
			return err
		}

		// Check if already applied
		var exists bool
		err = m.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)", version).Scan(&exists)
		if err != nil {
			return fmt.Errorf("failed to check migration %d: %w", version, err)
		}
		if exists {
			continue
		}

		// Read and execute migration inside transaction
		content, err := os.ReadFile(filepath.Join(m.migrationsDir, fileName))
		if err != nil {
			return fmt.Errorf("failed to read migration file %s: %w", fileName, err)
		}

		tx, err := m.pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("failed to begin transaction for migration %s: %w", fileName, err)
		}

		if _, err := tx.Exec(ctx, string(content)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("failed to execute migration %s: %w", fileName, err)
		}

		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", version); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("failed to record migration %s: %w", fileName, err)
		}

		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("failed to commit migration %s: %w", fileName, err)
		}
	}

	return nil
}

// Down rolls back the last applied migration.
func (m *Migrator) Down(ctx context.Context) error {
	if err := m.EnsureSchemaMigrationsTable(ctx); err != nil {
		return err
	}

	// Get latest applied version
	var latestVersion int64
	err := m.pool.QueryRow(ctx, "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1").Scan(&latestVersion)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil // No migrations to revert
		}
		return fmt.Errorf("failed to get latest migration version: %w", err)
	}

	// Find corresponding .down.sql
	files, err := os.ReadDir(m.migrationsDir)
	if err != nil {
		return fmt.Errorf("failed to read migrations dir: %w", err)
	}

	var downFile string
	prefix := fmt.Sprintf("%06d", latestVersion)
	for _, f := range files {
		if strings.HasPrefix(f.Name(), prefix) && strings.HasSuffix(f.Name(), ".down.sql") {
			downFile = f.Name()
			break
		}
	}

	if downFile == "" {
		return fmt.Errorf("down migration file not found for version %d", latestVersion)
	}

	content, err := os.ReadFile(filepath.Join(m.migrationsDir, downFile))
	if err != nil {
		return fmt.Errorf("failed to read down file %s: %w", downFile, err)
	}

	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin tx for rollback %s: %w", downFile, err)
	}

	if _, err := tx.Exec(ctx, string(content)); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("failed to execute rollback %s: %w", downFile, err)
	}

	if _, err := tx.Exec(ctx, "DELETE FROM schema_migrations WHERE version = $1", latestVersion); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("failed to remove migration record %d: %w", latestVersion, err)
	}

	return tx.Commit(ctx)
}

func parseMigrationVersion(fileName string) (int64, error) {
	parts := strings.Split(fileName, "_")
	if len(parts) < 2 {
		return 0, fmt.Errorf("invalid migration filename: %s", fileName)
	}
	version, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse migration version from '%s': %w", fileName, err)
	}
	return version, nil
}
