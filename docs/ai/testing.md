# Testing — Testcontainers & Compliance Suites

Packages: `infra/sqldb/dbtest`, `infra/redis/redistest`, `messaging/amqpbackend/rabbitmqtest`, `testutil/storagetest`

## When to Use

Use integration test helpers for **integration tests** that need real infrastructure (database, Redis, RabbitMQ). All use Testcontainers (requires Docker). Each helper lives in its respective module:
- `infra/sqldb/dbtest` — PostgreSQL and MariaDB containers
- `infra/redis/redistest` — Redis container
- `messaging/amqpbackend/rabbitmqtest` — RabbitMQ container
- `testutil/storagetest` — Storage backend compliance suites

Unit tests should use in-memory alternatives (`membackend`, `cache.MemoryCache`, `testutil/memdb`, `messaging/membroker`) — no Docker needed.

## Build Tag

All integration tests require the `integration` build tag:

```go
//go:build integration

package mypackage_test
```

Run with: `go test -tags integration ./...`

## Database (PostgreSQL / MariaDB)

```go
//go:build integration

func TestUserRepository(t *testing.T) {
    cfg := dbtest.StartPostgres(t, "mydb")
    // cfg is sqldb.PostgresConfig, ready to use
    // Container auto-terminates via t.Cleanup

    db, err := gormdb.NewPostgres(cfg, sqldb.PoolConfig{
        MaxIdleConns: 2,
        MaxOpenConns: 5,
    })
    require.NoError(t, err)

    gormdb.AutoMigrate(db, &User{})
    repo := NewUserRepository(db)

    // Test CRUD:
    err = repo.Create(ctx, &User{ID: "abc", Email: "test@example.com"})
    require.NoError(t, err)

    user, err := repo.FindByID(ctx, "abc")
    require.NoError(t, err)
    assert.Equal(t, "test@example.com", user.Email)
}
```

MariaDB equivalent:
```go
cfg := dbtest.StartMariaDB(t, "mydb")
db, err := gormdb.New(cfg, poolCfg) // gormdb.New for MariaDB
```

Each call starts a **fresh container** — tests are fully isolated.

## Redis

```go
//go:build integration

func TestRedisCache(t *testing.T) {
    url := redistest.Start(t) // returns connection URL
    // Shared container per process (sync.Once), auto-cleaned by Ryuk

    opts, _ := redis.ParseURL(url)
    conn, err := redis.Connect(opts, redis.WithLogger(slog.Default()))
    require.NoError(t, err)
    defer conn.Close()

    c, _ := cache.NewRedisCache(conn.Client(), "test-"+t.Name())
    // ... test operations
}
```

**Shared container**: starts once, reused across all tests in the process. Use unique key prefixes (e.g., `t.Name()`) to avoid collisions.

## RabbitMQ

```go
//go:build integration

func TestMessaging(t *testing.T) {
    url := rabbitmqtest.Start(t)
    // Shared container per process

    conn, err := amqpbackend.Dial(url, slog.Default())
    require.NoError(t, err)
    defer conn.Close()

    // IMPORTANT: Use unique names per test — broker state leaks between tests
    exchange := "test-" + strings.ReplaceAll(t.Name(), "/", "-")
    queue := exchange + ".queue"

    binding, _ := amqpbackend.DeclareTopology(conn, messaging.BindingSpec{
        Exchange: exchange, ExchangeType: messaging.ExchangeDirect,
        Queue: queue, RoutingKey: "test.event",
    })
    // ... test publish/consume
}
```

Import path: `messaging/amqpbackend/rabbitmqtest` for the test helper, `messaging/amqpbackend` for `Dial`/`DeclareTopology`.

## Storage Compliance Suites

Verify any `storage.Storage` implementation against the contract:

```go
func TestMyBackendCompliance(t *testing.T) {
    backend := mypackage.New(config)
    storagetest.BackendSuite(t, backend)
    // Tests: PutAndGet, PutOverwrites, GetNotFound, ExistsTrue, ExistsFalse,
    //        DeleteExisting, DeleteIdempotent, EmptyKeyRejected, NestedKeys, LargeContent
}

func TestMyBackendListerCompliance(t *testing.T) {
    backend := mypackage.New(config)
    storagetest.ListerSuite(t, backend, backend) // backend must implement Lister
    // Tests: ListAll, ListWithPrefix, ListMaxKeys, ListEmptyPrefix, ListPopulatesSize
}
```

Each suite generates unique random prefixes per run to prevent collisions on shared backends.

### Quick Local Backend for Tests

```go
func TestUploadHandler(t *testing.T) {
    backend := storagetest.NewLocalBackend(t) // auto-cleaned via t.TempDir()
    handler := NewUploadHandler(backend)
    // ... test HTTP handlers with real storage
}
```

## Integration Test Pattern

```go
//go:build integration

func TestFullOrderFlow(t *testing.T) {
    // 1. Start infrastructure
    pgCfg := dbtest.StartPostgres(t, "orders")
    redisURL := redistest.Start(t)
    amqpURL := rabbitmqtest.Start(t)

    // 2. Setup
    db, _ := gormdb.NewPostgres(pgCfg, poolCfg)
    gormdb.AutoMigrate(db, &Order{})

    redisOpts, _ := redis.ParseURL(redisURL)
    redisConn, _ := redis.Connect(redisOpts)
    defer redisConn.Close()

    mqConn, _ := amqpbackend.Dial(amqpURL, slog.Default())
    defer mqConn.Close()

    // 3. Use t.Name() for unique resource names
    exchange := "test-" + strings.ReplaceAll(t.Name(), "/", "-")

    // 4. Test end-to-end flow
    // ...
}
```

## In-Memory Database (Unit Tests)

SQLite-backed `*gorm.DB` for fast unit tests without Docker:

```go
func TestUserRepository(t *testing.T) {
    db := memdb.New(t, &User{}, &Order{})  // auto-migrates models
    // Each call returns an isolated database
    // Auto-cleaned via t.Cleanup

    repo := NewUserRepository(db)
    err := repo.Create(ctx, &User{Name: "Alice"})
    require.NoError(t, err)
}
```

## In-Memory Broker (Unit Tests)

In-memory message broker implementing `messaging.MessagePublisher` (package `messaging/membroker`):

```go
func TestOrderService(t *testing.T) {
    broker := membroker.New()

    // Subscribe to events:
    var received []messaging.Delivery
    broker.Subscribe("orders", "order.created", func(_ context.Context, d messaging.Delivery) error {
        received = append(received, d)
        return nil
    })

    // Service publishes via broker:
    svc := NewOrderService(broker)
    svc.CreateOrder(ctx, order)

    // Process all pending messages synchronously:
    err := broker.Drain(context.Background())
    require.NoError(t, err)

    assert.Len(t, received, 1)

    // Inspect published messages:
    published := broker.Published()

    // Reset between test cases:
    broker.Reset()
}
```

Use `"*"` for exchange/routing key to match all messages.

## Anti-Patterns

- **Never** forget `//go:build integration` — tests will fail in CI without Docker.
- **Never** hardcode exchange/queue names in RabbitMQ tests — use `t.Name()` for uniqueness.
- **Never** assume Redis state is clean between tests — use unique key prefixes.
- **Never** skip `defer conn.Close()` — leaks connections and may exhaust container limits.
- **Never** use `storagetest.NewLocalBackend` outside of tests — it uses `t.TempDir()`.
