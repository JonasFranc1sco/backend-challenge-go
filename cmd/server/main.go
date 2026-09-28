package main

import (
	"github.com/JonasFranc1sco/backend-challenge-go/internal/config"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/delivery/http"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/auth"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/postgres"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/postgres/repository"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/infrastructure/sqs"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/observability"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/usecase"
	"github.com/JonasFranc1sco/backend-challenge-go/internal/worker"
	"go.uber.org/fx"
)

var AppModules = fx.Options(
	config.Module,
	observability.Module,
	postgres.Module,
	repository.Module,
	sqs.Module,
	auth.Module,
	usecase.Module,
	worker.Module,
	http.Module,
)

func NewApp(opts ...fx.Option) *fx.App {
	return fx.New(
		AppModules,
		fx.Options(opts...),
	)
}

func main() {
	app := NewApp()
	app.Run()
}
