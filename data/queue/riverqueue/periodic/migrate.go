package periodic

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/riverqueue/river/riverdriver/riverdatabasesql"
	"github.com/riverqueue/river/rivermigrate"
)

// Migrate creates schema and brings River's tables in it up to date; it
// returns how many of River's migrations it applied.
func Migrate(ctx context.Context, db *sql.DB, schema string) (int, error) {
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS "+pgx.Identifier{schema}.Sanitize()); err != nil {
		return 0, err
	}
	migrator, err := rivermigrate.New(riverdatabasesql.New(db), &rivermigrate.Config{
		Schema: schema, Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		return 0, err
	}
	res, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		return 0, err
	}
	return len(res.Versions), nil
}
