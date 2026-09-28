package auth

import (
	"time"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/config"
	"go.uber.org/fx"
)

func ProvideJWKSKeyFetcher(cfg *config.Config) *JWKSKeyFetcher {
	return NewJWKSKeyFetcher(cfg.KeycloakJWKSURL, 1*time.Hour)
}

func ProvideJWTValidator(fetcher *JWKSKeyFetcher, cfg *config.Config) JWTValidator {
	return NewKeycloakJWTValidator(fetcher, cfg.KeycloakIssuer)
}

var Module = fx.Module("auth",
	fx.Provide(ProvideJWKSKeyFetcher),
	fx.Provide(ProvideJWTValidator),
)
