//go:build integration

package dbtest

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/bds421/rho-kit/infra/v2/sqldb"
)

// Cluster is one PostgreSQL container shared by the tests of a package. Each
// test takes an empty database of its own from it ([Cluster.Database]);
// roles and other cluster-wide objects are shared, so a test that creates
// one names it uniquely. The container starts with the first database and
// goes when [Cluster.Run] returns:
//
//	var cluster = dbtest.NewCluster("store_test")
//
//	func TestMain(m *testing.M) { os.Exit(cluster.Run(m)) }
type Cluster struct {
	prefix    string
	start     sync.Once
	startErr  error
	admin     sqldb.Config
	container *tcpostgres.PostgresContainer
	next      atomic.Int64
}

// NewCluster returns a cluster whose databases are named prefix_1, prefix_2, …
func NewCluster(prefix string) *Cluster {
	return &Cluster{prefix: prefix}
}

// Run runs the package's tests and then removes the container, if one
// started. It returns m.Run's exit code.
func (c *Cluster) Run(m *testing.M) int {
	code := m.Run()
	if c.container != nil {
		if err := testcontainers.TerminateContainer(c.container); err != nil {
			fmt.Fprintf(os.Stderr, "terminate postgres container: %v\n", err)
		}
	}
	return code
}

// Database creates an empty database for t and drops it when t ends.
func (c *Cluster) Database(t *testing.T) sqldb.Config {
	t.Helper()
	ctx := context.Background()
	c.start.Do(func() { c.startErr = c.startContainer(ctx) })
	if c.startErr != nil {
		t.Fatal(c.startErr)
	}
	name := pgx.Identifier{fmt.Sprintf("%s_%d", c.prefix, c.next.Add(1))}
	c.exec(t, ctx, "CREATE DATABASE "+name.Sanitize())
	t.Cleanup(func() {
		c.exec(t, context.Background(), "DROP DATABASE "+name.Sanitize()+" WITH (FORCE)")
	})
	cfg := c.admin
	cfg.Name = name[0]
	return cfg
}

func (c *Cluster) startContainer(ctx context.Context) error {
	container, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	c.container = container
	if err != nil {
		return fmt.Errorf("start postgres container: %w", err)
	}
	c.admin, err = configOf(ctx, container, "postgres")
	return err
}

func (c *Cluster) exec(t *testing.T, ctx context.Context, statement string) {
	t.Helper()
	conn, err := pgx.Connect(ctx, dsnOf(c.admin))
	if err != nil {
		t.Fatalf("connect to the postgres cluster: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, statement); err != nil {
		t.Fatalf("%s: %v", strings.Fields(statement)[0], err)
	}
}

func dsnOf(cfg sqldb.Config) string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(cfg.User, cfg.Password),
		Host:     net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Path:     cfg.Name,
		RawQuery: "sslmode=disable",
	}
	return u.String()
}
