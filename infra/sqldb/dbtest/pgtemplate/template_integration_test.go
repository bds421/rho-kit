//go:build integration

package pgtemplate_test

import (
	"context"
	"fmt"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bds421/rho-kit/infra/sqldb/dbtest/pgtemplate/v2"
	"github.com/bds421/rho-kit/infra/sqldb/dbtest/v2"
)

type cluster struct {
	admin *pgxpool.Pool
	url   url.URL
}

func startCluster(t *testing.T) cluster {
	t.Helper()
	cfg := dbtest.StartPostgres(t, "postgres")
	u := url.URL{Scheme: "postgres", User: url.UserPassword(cfg.User, cfg.Password), Host: fmt.Sprintf("%s:%d", cfg.Host, cfg.Port), Path: "/postgres", RawQuery: "sslmode=disable"}
	admin, err := pgxpool.New(t.Context(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	return cluster{admin: admin, url: u}
}

func (c cluster) connect(t *testing.T, name string) *pgx.Conn {
	t.Helper()
	u := c.url
	u.Path = "/" + name
	conn, err := pgx.Connect(t.Context(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// migrated is a template whose build creates a table and revokes
// TEMPORARY on its database, and counts its builds.
func (c cluster) migrated(hash string, builds *atomic.Int32) pgtemplate.Template {
	return pgtemplate.Template{Prefix: "tmpl_", Hash: hash, Build: func(ctx context.Context, name string) error {
		builds.Add(1)
		u := c.url
		u.Path = "/" + name
		conn, err := pgx.Connect(ctx, u.String())
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close(ctx) }()
		_, err = conn.Exec(ctx, "CREATE TABLE items (id int); REVOKE TEMPORARY ON DATABASE "+pgx.Identifier{name}.Sanitize()+" FROM PUBLIC")
		return err
	}}
}

func (c cluster) databases(t *testing.T, prefix string) []string {
	t.Helper()
	rows, err := c.admin.Query(t.Context(), "SELECT datname FROM pg_database WHERE starts_with(datname, $1) ORDER BY 1", prefix)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return names
}

// Copies made at once build the template once, and each holds the
// template's tables and its privileges on the database.
func TestConcurrentClonesBuildOnceAndCopyTheTemplate(t *testing.T) {
	c := startCluster(t)
	var builds atomic.Int32
	tp := c.migrated("aaaa", &builds)
	var wg sync.WaitGroup
	for i := range 6 {
		wg.Go(func() {
			if err := tp.Clone(t.Context(), c.admin, fmt.Sprintf("copy_%d", i)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if n := builds.Load(); n != 1 {
		t.Fatalf("built %d times, want once", n)
	}
	for i := range 6 {
		conn := c.connect(t, fmt.Sprintf("copy_%d", i))
		var items int
		if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM items").Scan(&items); err != nil {
			t.Fatalf("copy_%d lacks the template's table: %v", i, err)
		}
		var temporary bool
		if err := conn.QueryRow(t.Context(), "SELECT has_database_privilege('public', current_database(), 'TEMPORARY')").Scan(&temporary); err != nil {
			t.Fatal(err)
		}
		if temporary {
			t.Errorf("copy_%d grants TEMPORARY to PUBLIC, which the template revoked", i)
		}
	}
}

// A template of another hash goes when a new one is built, an unfinished
// build of another hash stays, and a database of the template's name that
// is no template yet is built again.
func TestANewHashDropsTheOldTemplateAndRebuildsAnUnfinishedOne(t *testing.T) {
	c := startCluster(t)
	var builds atomic.Int32
	if err := c.migrated("old", &builds).Clone(t.Context(), c.admin, "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.admin.Exec(t.Context(), "CREATE DATABASE tmpl_new"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.admin.Exec(t.Context(), "CREATE DATABASE tmpl_other"); err != nil {
		t.Fatal(err)
	}
	if err := c.migrated("new", &builds).Clone(t.Context(), c.admin, "second"); err != nil {
		t.Fatal(err)
	}
	if got := c.databases(t, "tmpl_"); len(got) != 2 || got[0] != "tmpl_new" || got[1] != "tmpl_other" {
		t.Fatalf("databases %v, want tmpl_new and the unfinished tmpl_other", got)
	}
	if n := builds.Load(); n != 2 {
		t.Fatalf("built %d times, want twice", n)
	}
	var items int
	if err := c.connect(t, "second").QueryRow(t.Context(), "SELECT count(*) FROM items").Scan(&items); err != nil {
		t.Fatalf("the rebuilt template lacks its table: %v", err)
	}
}

// Owner owns the template and every copy.
func TestOwnerOwnsTheTemplateAndItsCopies(t *testing.T) {
	c := startCluster(t)
	if _, err := c.admin.Exec(t.Context(), "CREATE ROLE migrator NOLOGIN"); err != nil {
		t.Fatal(err)
	}
	var builds atomic.Int32
	tp := c.migrated("owned", &builds)
	tp.Owner = "migrator"
	if err := tp.Clone(t.Context(), c.admin, "copy"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tmpl_owned", "copy"} {
		var owner string
		if err := c.admin.QueryRow(t.Context(), "SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname = $1", name).Scan(&owner); err != nil {
			t.Fatal(err)
		}
		if owner != "migrator" {
			t.Errorf("%s is owned by %s", name, owner)
		}
	}
}

// A build that fails leaves no template, and the next copy builds again.
func TestAFailedBuildLeavesNoTemplate(t *testing.T) {
	c := startCluster(t)
	tp := pgtemplate.Template{Prefix: "tmpl_", Hash: "broken", Build: func(context.Context, string) error { return fmt.Errorf("migration failed") }}
	if err := tp.Clone(t.Context(), c.admin, "copy"); err == nil {
		t.Fatal("cloned a template whose build failed")
	}
	var builds atomic.Int32
	if err := c.migrated("broken", &builds).Clone(t.Context(), c.admin, "copy"); err != nil {
		t.Fatal(err)
	}
	if n := builds.Load(); n != 1 {
		t.Fatalf("built %d times after the failure, want once", n)
	}
}
