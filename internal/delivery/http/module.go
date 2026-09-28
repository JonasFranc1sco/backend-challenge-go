package http

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/config"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/auth"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/observability"
	"go.uber.org/fx"
)

func ProvideRouter(
	walletHandler *WalletHandler,
	txHandler *TransactionHandler,
	healthHandler *HealthHandler,
	jwtValidator auth.JWTValidator,
	metrics *observability.Metrics,
) http.Handler {
	return NewRouter(RouterConfig{
		WalletHandler:      walletHandler,
		TransactionHandler: txHandler,
		HealthHandler:      healthHandler,
		JWTValidator:       jwtValidator,
		Metrics:            metrics,
	})
}

func StartHTTPServer(lc fx.Lifecycle, handler http.Handler, cfg *config.Config, logger *slog.Logger) {
	srv := &http.Server{
		Addr:         ":" + cfg.HTTPPort,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			logger.Info("Starting HTTP server", slog.String("port", cfg.HTTPPort))
			ln, err := net.Listen("tcp", srv.Addr)
			if err != nil {
				return fmt.Errorf("failed to bind HTTP server to %s: %w", srv.Addr, err)
			}
			go func() {
				if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					logger.Error("HTTP server error", slog.String("error", err.Error()))
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			logger.Info("Shutting down HTTP server gracefully...")
			return srv.Shutdown(ctx)
		},
	})
}

var Module = fx.Module("http",
	fx.Provide(NewHealthHandler),
	fx.Provide(NewWalletHandler),
	fx.Provide(NewTransactionHandler),
	fx.Provide(ProvideRouter),
	fx.Invoke(StartHTTPServer),
)
