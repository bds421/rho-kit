# Testing - Integration Helpers and Compliance Suites

Packages: `infra/sqldb/dbtest/v2`, `infra/sqldb/dbtest/pgtemplate/v2`, `infra/redis/redistest/v2`, `infra/messaging/amqpbackend/integrationtest/v2/rabbitmqtest`, `infra/storage/storagetest/v2`

Snippet status: shell blocks are executable from the documented module root.
Go blocks are illustrative fragments unless explicitly introduced as generated
or executable code.

## When to Use

Use integration helpers for tests that need real infrastructure. They use Testcontainers and require Docker. Keep these helpers behind the `integration` build tag and import them only from test files.

- `infra/sqldb/dbtest/v2` starts PostgreSQL containers: `StartPostgres` one per test, `Cluster` one per package with a database per test.
- `infra/sqldb/dbtest/pgtemplate/v2` copies each test database from a template migrated once per state of the migrations, on any PostgreSQL the tests reach (a container or a development cluster): `Template.Clone` builds it on first use and copies it with its database privileges, `Hash` names the state from the migration files and the versions of the modules that migrate.
- `infra/redis/redistest/v2` starts a Redis container.
- `infra/messaging/amqpbackend/integrationtest/v2/rabbitmqtest` starts a RabbitMQ container.
- `infra/storage/storagetest/v2` provides storage compliance suites and local/S3/SFTP helpers.

Unit tests should prefer fakes or in-memory implementations such as `cache.MemoryCache`, `infra/storage/membackend`, and `infra/messaging/membroker`.

## Build Tag

```go
//go:build integration

package mypackage_test
```

Run integration tests with:

```bash
go test -tags integration ./...
```

## Database (PostgreSQL)

```go
//go:build integration

func TestUserRepository(t *testing.T) {
    cfg := dbtest.StartPostgres(t, "users_test")
    dsn := (&url.URL{
        Scheme: "postgres",
        User:   url.UserPassword(cfg.User, cfg.Password),
        Host:   net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
        Path:   "/" + cfg.Name,
        RawQuery: url.Values{
            "sslmode": []string{"disable"},
        }.Encode(),
    }).String()

    pool, err := pgxbackend.Connect(context.Background(), pgxbackend.Config{
        DSN:                            dsn,
        AllowPlaintextLoopbackForTests: true,
    })
    require.NoError(t, err)
    t.Cleanup(func() { _ = pool.Close() })

    sqlDB := stdlib.OpenDBFromPool(pool.Pool())
    t.Cleanup(func() { _ = sqlDB.Close() })
    _, err = migrate.Up(context.Background(), sqlDB, migrate.Config{Dir: migrationsFS})
    require.NoError(t, err)

    repo := NewUserRepository(pool.Pool())
    // ... test CRUD operations
}
```

`StartPostgres` starts a fresh PostgreSQL container per call that terminates via `t.Cleanup`. A package whose tests isolate by database shares one container instead: `Cluster.Database(t)` creates an empty database per test and drops it when the test ends, and the container starts with the first database and goes when `Run` returns. Roles are cluster-wide, so a test that creates one names it uniquely.

```go
var cluster = dbtest.NewCluster("users_test")

func TestMain(m *testing.M) { os.Exit(cluster.Run(m)) }

func TestUserRepository(t *testing.T) {
    cfg := cluster.Database(t)
    // ... connect and migrate as above
}
```

## Redis

```go
//go:build integration

func TestRedisCache(t *testing.T) {
    url := redistest.Start(t)

    opts, err := goredis.ParseURL(url)
    require.NoError(t, err)
    conn, err := redis.Connect(opts, redis.WithLogger(slog.Default()))
    require.NoError(t, err)
    t.Cleanup(func() { _ = conn.Close() })

    c, err := rediscache.NewCache(conn.Client(), "test-"+t.Name())
    require.NoError(t, err)
    // ... test operations
}
```

Redis and RabbitMQ helpers reuse a shared container per process. Use unique key prefixes, exchanges, queues, and stream names based on `t.Name()`.

## RabbitMQ

```go
//go:build integration

func TestMessaging(t *testing.T) {
    url := rabbitmqtest.Start(t)

    conn, err := amqpbackend.Connect(url, slog.Default())
    require.NoError(t, err)
    t.Cleanup(func() { _ = conn.Close() })

    exchange := "test-" + strings.ReplaceAll(t.Name(), "/", "-")
    queue := exchange + ".queue"

    _, err = amqpbackend.DeclareTopology(conn, messaging.BindingSpec{
        Exchange:     exchange,
        ExchangeType: messaging.ExchangeDirect,
        Queue:        queue,
        RoutingKey:   "test.event",
    })
    require.NoError(t, err)
    // ... test publish/consume
}
```

## Storage Compliance Suites

Verify any `storage.Storage` implementation against the shared contract:

```go
func TestMyBackendCompliance(t *testing.T) {
    backend := mypackage.New(config)
    storagetest.BackendSuite(t, backend)
}

func TestMyBackendListerCompliance(t *testing.T) {
    backend := mypackage.New(config)
    storagetest.ListerSuite(t, backend, backend)
}
```

For handler tests that need a real local backend:

```go
func TestUploadHandler(t *testing.T) {
    backend := storagetest.NewLocalBackend(t)
    handler := NewUploadHandler(backend)
    // ... test HTTP handlers with real storage
}
```

Compliance suites generate unique prefixes per run to prevent collisions on shared backends.

## Integration Test Pattern

```go
//go:build integration

func TestFullOrderFlow(t *testing.T) {
    pgCfg := dbtest.StartPostgres(t, "orders")
    redisURL := redistest.Start(t)
    amqpURL := rabbitmqtest.Start(t)

    pgPool := connectTestPostgres(t, pgCfg)
    redisConn := connectTestRedis(t, redisURL)
    mqConn := connectTestRabbitMQ(t, amqpURL)

    _ = pgPool
    _ = redisConn
    _ = mqConn

    exchange := "test-" + strings.ReplaceAll(t.Name(), "/", "-")
    // ... test end-to-end flow with unique resource names
}
```

## In-Memory Broker (Unit Tests)

`infra/messaging/membroker` implements `messaging.Publisher` for unit tests.

```go
func TestOrderService(t *testing.T) {
    broker := membroker.New()

    var received []messaging.Delivery
    broker.Subscribe("orders", "order.created", func(_ context.Context, d messaging.Delivery) error {
        received = append(received, d)
        return nil
    })

    svc := NewOrderService(broker)
    err := svc.CreateOrder(ctx, order)
    require.NoError(t, err)

    err = broker.Drain(context.Background())
    require.NoError(t, err)

    assert.Len(t, received, 1)
    broker.Reset()
}
```
