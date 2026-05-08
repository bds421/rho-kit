# Database — MySQL & PostgreSQL

Packages: `infra/sqldb`, `infra/sqldb/gormdb`

## When to Use

Every service that needs a relational database uses the `infra/sqldb` package for config loading and `infra/sqldb/gormdb` for GORM setup. The `app.Builder` handles this automatically via `WithMySQL` or `WithPostgres`. Migrations are NOT auto-run by the Builder — call `gormdb.AutoMigrate` (dev) or wire `WithMigrations(dir)` (prod) to execute schema changes.

## Decision: MySQL vs PostgreSQL

| Factor | MySQL | PostgreSQL |
|---|---|---|
| Default port | 3306 | 5432 |
| SSL mode config | Via TLS env vars | `DB_SSL_MODE` env var |
| JSON support | Basic | Native JSONB |
| Full-text search | Built-in | Built-in + advanced |
| Use when | Existing MySQL infra | New projects (preferred) |

**Rule: `WithMySQL` and `WithPostgres` are mutually exclusive.** Calling both panics at validation.

## Quick Start

```go
type Config struct {
    app.BaseConfig
    sqldb.Fields // shared MySQL/Postgres field set
}

func LoadConfig() (Config, error) {
    base, err := app.LoadBaseConfig(8080)
    if err != nil { return Config{}, err }

    db, err := sqldb.LoadFields("MYAPP", "postgres", 10, 100) // prefix, driver, maxIdle, maxOpen
    if err != nil { return Config{}, err }

    cfg := Config{BaseConfig: base, Fields: db}
    if err := cfg.ValidateBase(); err != nil { return Config{}, err }
    if err := cfg.Validate("postgres", cfg.Environment); err != nil { return Config{}, err }
    return cfg, nil
}

// In main:
app.New(...).
    WithPostgres(cfg.Database, cfg.DatabasePool).
    WithDBMetrics(). // optional: Prometheus pool metrics every 15s
    Router(func(infra app.Infrastructure) http.Handler {
        // infra.DB is *gorm.DB, ready to use
        if err := gormdb.AutoMigrate(infra.DB, &User{}, &Order{}); err != nil { panic(err) }
    })
```

## Model Definition

```go
type User struct {
    ID        string    `gorm:"type:char(36);primaryKey"`
    Email     string    `gorm:"type:varchar(255);uniqueIndex;not null"`
    Name      string    `gorm:"type:varchar(100);not null"`
    CreatedAt time.Time `gorm:"autoCreateTime"`
    UpdatedAt time.Time `gorm:"autoUpdateTime"`
}
```

Auto-migration is no longer wired into `WithPostgres`/`WithMySQL`. Call `gormdb.AutoMigrate(infra.DB, &User{}, &Order{})` explicitly inside `Router` for dev, or use `WithMigrations(dir)` to run goose-style SQL migrations on startup.

## Repository Pattern

```go
type UserRepository struct {
    db *gorm.DB
}

func NewUserRepository(db *gorm.DB) *UserRepository {
    return &UserRepository{db: db}
}

func (r *UserRepository) FindByID(ctx context.Context, id string) (*User, error) {
    var user User
    if err := r.db.WithContext(ctx).First(&user, "id = ?", id).Error; err != nil {
        if errors.Is(err, gorm.ErrRecordNotFound) {
            return nil, apperror.NewNotFound("user", id)
        }
        return nil, err
    }
    return &user, nil
}

func (r *UserRepository) Create(ctx context.Context, user *User) error {
    return r.db.WithContext(ctx).Create(user).Error
}

func (r *UserRepository) Update(ctx context.Context, id string, updates map[string]any) error {
    result := r.db.WithContext(ctx).Model(&User{}).Where("id = ?", id).Updates(updates)
    if result.RowsAffected == 0 {
        return apperror.NewNotFound("user", id)
    }
    return result.Error
}
```

## Environment Variables

### PostgreSQL
| Variable | Default | Required | Notes |
|---|---|---|---|
| `DB_HOST` | `localhost` | Yes | |
| `DB_PORT` | `5432` | No | |
| `{PREFIX}_DB_USER` | — | Yes | |
| `{PREFIX}_DB_PASSWORD` | — | Yes | Min 12 chars in prod |
| `{PREFIX}_DB_NAME` | — | Yes | |
| `DB_SSL_MODE` | disabled | No | `disable`, `require`, `verify-ca`, `verify-full` |
| `DB_LOG_LEVEL` | `warn` | No | `info` = verbose SQL |

### MariaDB
| Variable | Default | Required | Notes |
|---|---|---|---|
| `DB_HOST` | `localhost` | Yes | |
| `DB_PORT` | `3306` | No | |
| `{PREFIX}_DB_USER` | — | Yes | |
| `{PREFIX}_DB_PASSWORD` | — | Yes | Min 12 chars in prod |
| `{PREFIX}_DB_NAME` | — | Yes | |
| `DB_LOG_LEVEL` | `warn` | No | |

### Connection Pool (shared)
| Variable | Default | Notes |
|---|---|---|
| `DB_POOL_MAX_IDLE_CONNS` | service-specific | |
| `DB_POOL_MAX_OPEN_CONNS` | service-specific | |
| `DB_POOL_CONN_MAX_LIFETIME_MIN` | `60` | Minutes |
| `DB_POOL_CONN_MAX_IDLE_TIME_MIN` | `5` | Minutes |

## Password Validation

In non-development environments:
- Minimum 12 characters
- Must not contain "changeme"
- Must not contain special chars that break DSN parsing (`@`, `/`, `'`, `"`, `\`)

## Seeding

```go
app.New(...).
    WithPostgres(cfg.Database, cfg.DatabasePool).
    WithSeed(func(db *gorm.DB, path string, log *slog.Logger) error {
        var users []User
        if err := app.LoadSeedJSON(path, &users); err != nil { return err }
        return db.Create(&users).Error
    }).
    Run()
// Run: ./service --seed ./seeds/data.json
```

## Anti-Patterns

- **Never** call `WithMySQL` and `WithPostgres` together.
- **Never** hardcode DB credentials — always use env vars with `{PREFIX}_` naming.
- **Never** use `DB_LOG_LEVEL=info` in production — it logs every SQL query.
- **Never** skip `WithContext(ctx)` on GORM queries — breaks tracing and cancellation.
- **Never** use `db.Exec` with string concatenation — use parameterized queries.

## Testing

```go
//go:build integration

func TestUserRepository(t *testing.T) {
    cfg := dbtest.StartPostgres(t, "testdb") // testcontainers, auto-cleanup
    db, err := gormdb.NewPostgres(cfg, sqldb.PoolConfig{MaxIdleConns: 2, MaxOpenConns: 5})
    require.NoError(t, err)

    gormdb.AutoMigrate(db, &User{})
    repo := NewUserRepository(db)
    // ... test CRUD operations
}
```
