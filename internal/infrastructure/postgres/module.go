package postgres

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
)

func NewPoolWithLifecycle(lc fx.Lifecycle, cfg *config.Config, logger *slog.Logger) (*pgxpool.Pool, error) {
	pgCfg := Config{
		Host:     cfg.DBHost,
		Port:     cfg.DBPort,
		User:     cfg.DBUser,
		Password: cfg.DBPassword,
		Database: cfg.DBName,
		SSLMode:  cfg.DBSSLMode,
		MaxConns: cfg.DBMaxConns,
		MinConns: cfg.DBMinConns,
	}

	pool, err := NewPool(context.Background(), pgCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize postgres pool: %w", err)
	}

	lc.Append(fx.Hook{
		OnStop: func(ctx context.Context) error {
			logger.Info("Closing PostgreSQL connection pool...")
			pool.Close()
			return nil
		},
	})

	return pool, nil
}

func ProvideMigrator(pool *pgxpool.Pool) *Migrator {
	return NewMigrator(pool, "migrations")
}

func RunMigrations(lc fx.Lifecycle, migrator *Migrator, logger *slog.Logger) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			logger.Info("Executing pending PostgreSQL database migrations...")
			if err := migrator.Up(ctx); err != nil {
				return fmt.Errorf("database migration failed: %w", err)
			}
			logger.Info("Database migrations applied successfully!")
			return nil
		},
	})
}

var Module = fx.Module("postgres",
	fx.Provide(NewPoolWithLifecycle),
	fx.Provide(NewTransactor),
	fx.Provide(ProvideMigrator),
	fx.Invoke(RunMigrations),
)
