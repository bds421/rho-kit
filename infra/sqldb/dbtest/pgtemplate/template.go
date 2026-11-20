package pgtemplate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Template is a migrated database that test databases are copied from.
type Template struct {
	// Prefix starts the name of every template of this kind, such as
	// "tmpl_"; the cluster keeps the one of Hash and drops the other
	// templates under it. No other database may start with it: a database
	// of the template's name that is no template yet is rebuilt.
	Prefix string
	// Hash names the state Build leaves, such as [Hash] of the migrations.
	Hash string
	// Owner owns the template and each copy; empty leaves the admin
	// connection's user. It is not part of Hash; a caller that changes it
	// passes it in Hash's extra.
	Owner string
	// Build migrates the new, empty database name. It closes every
	// connection it opens to it before it returns, since PostgreSQL copies
	// only a database no session is connected to.
	Build func(ctx context.Context, name string) error
}

// Name is the template database of Hash.
func (tp Template) Name() string { return tp.Prefix + tp.Hash }

// Clone creates the database name as a copy of the template, building the
// template first when the cluster has none of Hash. CREATE DATABASE …
// TEMPLATE copies the template's objects but not its privileges on the
// database itself (pg_database.datacl), such as a migration's REVOKE
// TEMPORARY, so Clone copies those too, writing the catalog directly: admin
// connects as a superuser, and the copy's grants record no dependency
// (pg_shdepend), which a test cluster can do without. A copy that fails
// half-way is dropped.
func (tp Template) Clone(ctx context.Context, admin *pgxpool.Pool, name string) error {
	if tp.Prefix == "" || tp.Hash == "" || tp.Build == nil {
		return errors.New("pgtemplate: a template needs a Prefix, a Hash and a Build")
	}
	if strings.HasPrefix(name, tp.Prefix) {
		return fmt.Errorf("pgtemplate: the copy %s starts with the templates' prefix %s", name, tp.Prefix)
	}
	conn, err := admin.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("pgtemplate: acquire: %w", err)
	}
	defer conn.Release()
	copied := false
	err = tp.holdLock(ctx, conn, "pg_advisory_lock_shared", "pg_advisory_unlock_shared", func() error {
		ready, err := isTemplate(ctx, conn, tp.Name())
		if err != nil || !ready {
			return err
		}
		copied = true
		return tp.copyInto(ctx, conn, name)
	})
	if err != nil || copied {
		return err
	}
	return tp.holdLock(ctx, conn, "pg_advisory_lock", "pg_advisory_unlock", func() error {
		ready, err := isTemplate(ctx, conn, tp.Name())
		if err != nil {
			return err
		}
		if !ready {
			if err := tp.build(ctx, conn); err != nil {
				return err
			}
		}
		return tp.copyInto(ctx, conn, name)
	})
}

// holdLock runs do while holding the prefix's advisory lock in the mode
// lock and unlock name.
func (tp Template) holdLock(ctx context.Context, conn *pgxpool.Conn, lock, unlock string, do func() error) error {
	if _, err := conn.Exec(ctx, "SELECT "+lock+"(hashtext('pgtemplate'), hashtext($1))", tp.Prefix); err != nil {
		return fmt.Errorf("pgtemplate: lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT "+unlock+"(hashtext('pgtemplate'), hashtext($1))", tp.Prefix)
	}()
	return do()
}

func isTemplate(ctx context.Context, conn *pgxpool.Conn, name string) (bool, error) {
	var ready bool
	err := conn.QueryRow(ctx, "SELECT coalesce((SELECT datistemplate FROM pg_database WHERE datname = $1), false)", name).Scan(&ready)
	if err != nil {
		return false, fmt.Errorf("pgtemplate: read template: %w", err)
	}
	return ready, nil
}

func (tp Template) copyInto(ctx context.Context, conn *pgxpool.Conn, name string) error {
	ident := pgx.Identifier{name}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+ident+" TEMPLATE "+pgx.Identifier{tp.Name()}.Sanitize()+tp.ownerClause()); err != nil {
		return fmt.Errorf("pgtemplate: copy: %w", err)
	}
	if _, err := conn.Exec(ctx, "UPDATE pg_database SET datacl = (SELECT datacl FROM pg_database WHERE datname = $1) WHERE datname = $2", tp.Name(), name); err != nil {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)")
		return fmt.Errorf("pgtemplate: copy privileges: %w", err)
	}
	return nil
}

func (tp Template) ownerClause() string {
	if tp.Owner == "" {
		return ""
	}
	return " OWNER " + pgx.Identifier{tp.Owner}.Sanitize()
}

// build migrates the template from empty; its caller holds the lock
// exclusively. A database of the name that is no template yet is a build
// that died half-way and is built again.
func (tp Template) build(ctx context.Context, conn *pgxpool.Conn) error {
	if err := tp.dropStale(ctx, conn); err != nil {
		return err
	}
	ident := pgx.Identifier{tp.Name()}.Sanitize()
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)"); err != nil {
		return fmt.Errorf("pgtemplate: drop unfinished template: %w", err)
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+ident+tp.ownerClause()); err != nil {
		return fmt.Errorf("pgtemplate: create template: %w", err)
	}
	if err := tp.Build(ctx, tp.Name()); err != nil {
		return fmt.Errorf("pgtemplate: build %s: %w", tp.Name(), err)
	}
	// The flag is set last, so a template is complete whenever it is one.
	if _, err := conn.Exec(ctx, "ALTER DATABASE "+ident+" IS_TEMPLATE true"); err != nil {
		return fmt.Errorf("pgtemplate: mark template: %w", err)
	}
	return nil
}

// dropStale drops the templates of other hashes under the prefix; its
// caller holds the lock exclusively, so none of them is being copied. An
// unfinished build of another hash is no template and stays.
func (tp Template) dropStale(ctx context.Context, conn *pgxpool.Conn) error {
	rows, err := conn.Query(ctx, "SELECT datname FROM pg_database WHERE datistemplate AND starts_with(datname, $1) AND datname <> $2", tp.Prefix, tp.Name())
	if err != nil {
		return fmt.Errorf("pgtemplate: list templates: %w", err)
	}
	stale, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("pgtemplate: list templates: %w", err)
	}
	for _, name := range stale {
		ident := pgx.Identifier{name}.Sanitize()
		if _, err := conn.Exec(ctx, "ALTER DATABASE "+ident+" IS_TEMPLATE false"); err != nil {
			return fmt.Errorf("pgtemplate: unmark %s: %w", name, err)
		}
		if _, err := conn.Exec(ctx, "DROP DATABASE "+ident+" WITH (FORCE)"); err != nil {
			return fmt.Errorf("pgtemplate: drop %s: %w", name, err)
		}
	}
	return nil
}
