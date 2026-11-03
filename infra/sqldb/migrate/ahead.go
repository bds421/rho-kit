package migrate

import (
	"context"
	"fmt"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/pressly/goose/v3"
)

// ErrAhead reports a database whose version table records a migration newer
// than the newest migration file of the set: one migrated by a newer
// release, or by a migration chain since squashed into a baseline. goose
// would take such a database as up to date and never run a later migration
// whose version is not above the recorded one, so [Up] refuses it.
var ErrAhead = redact.Safe("migrate: database is ahead of the migration files")

func refuseAhead(ctx context.Context, provider *goose.Provider, table string) error {
	recorded, newest, err := provider.GetVersions(ctx)
	if err != nil {
		return redact.WrapError("migrate: read versions", err)
	}
	if recorded <= newest {
		return nil
	}
	sources := provider.ListSources()
	// Table, versions and file name are no secrets; operators need them where
	// logs redact the rest.
	return redact.WrapSafe(fmt.Sprintf("migrate: %s records version %d, the newest migration is %s (version %d)",
		table, recorded, sources[len(sources)-1].Path, newest), ErrAhead)
}
