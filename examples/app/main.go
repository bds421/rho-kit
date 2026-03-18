//go:build ignore

package main

import (
	"log/slog"
	"net/http"

	"github.com/bds421/rho-kit/app"
	"github.com/bds421/rho-kit/infra/sqldb"
	"github.com/bds421/rho-kit/core/config"
	"github.com/bds421/rho-kit/httpx/middleware/csrf"
	"github.com/bds421/rho-kit/httpx/middleware/stack"
	"github.com/bds421/rho-kit/observability/tracing"
)

type Config struct {
	app.BaseConfig
	sqldb.PostgresFields
	TraceEndpoint string
}

func LoadConfig() (Config, error) {
	base, err := app.LoadBaseConfig(8080)
	if err != nil {
		return Config{}, err
	}
	db, err := sqldb.LoadPostgresFields("EXAMPLE", 10, 100)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		BaseConfig:     base,
		PostgresFields: db,
		TraceEndpoint:  config.Get("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
	}
	if err := cfg.ValidateBase(); err != nil {
		return Config{}, err
	}
	if err := cfg.ValidatePostgres("EXAMPLE", cfg.Environment); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func main() {
	app.Main("example-service", "dev", func(logger *slog.Logger) error {
		cfg, err := LoadConfig()
		if err != nil {
			return err
		}

		return app.New("example-service", "dev", cfg.BaseConfig).
			WithPostgres(cfg.Database, cfg.DatabasePool).
			WithTracing(tracing.Config{
				ServiceName:    "example-service",
				ServiceVersion: "dev",
				Environment:    cfg.Environment,
				Endpoint:       cfg.TraceEndpoint,
			}).
			Router(func(infra app.Infrastructure) http.Handler {
				mux := http.NewServeMux()
				mux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusOK)
				})

				return stack.Default(mux, logger,
					stack.WithOuter(csrf.RequireJSONContentType, csrf.RequireCSRF),
				)
			}).
			Run()
	})
}
