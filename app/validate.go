package app

import "fmt"

// Validate checks for common configuration mistakes before startup.
// Callers may use this directly, but Run calls it automatically.
func (b *Builder) Validate() error {
	if b == nil {
		return fmt.Errorf("builder is nil")
	}

	if b.dbMySQLCfg != nil && b.dbPgCfg != nil {
		return fmt.Errorf("only one database can be configured (MySQL or Postgres)")
	}
	if (b.dbMySQLCfg != nil || b.dbPgCfg != nil) && b.dbPoolCfg == nil {
		return fmt.Errorf("database pool config is required when a database is configured")
	}
	if b.dbMetrics && b.dbMySQLCfg == nil && b.dbPgCfg == nil {
		return fmt.Errorf("database metrics require a configured database")
	}
	if b.seedFn != nil && b.dbMySQLCfg == nil && b.dbPgCfg == nil {
		return fmt.Errorf("seed requires a configured database")
	}
	if b.migrationsDir != nil && b.dbMySQLCfg == nil && b.dbPgCfg == nil {
		return fmt.Errorf("migrations require a configured database (use WithMySQL or WithPostgres)")
	}
	if (b.replicaPgCfg != nil || b.replicaMySQLCfg != nil) && b.dbMySQLCfg == nil && b.dbPgCfg == nil {
		return fmt.Errorf("read replica requires a configured primary database")
	}
	if b.replicaPgCfg != nil && b.dbPgCfg == nil {
		return fmt.Errorf("WithReadReplica requires WithPostgres (driver mismatch)")
	}
	if b.replicaMySQLCfg != nil && b.dbMySQLCfg == nil {
		return fmt.Errorf("WithReadReplicaMySQL requires WithMySQL (driver mismatch)")
	}
	if b.criticalBroker && b.mqURL == "" {
		return fmt.Errorf("critical broker requires a RabbitMQ URL")
	}
	if b.ipRateRequests > 0 && b.ipRateWindow <= 0 {
		return fmt.Errorf("IP rate limit window must be > 0 when rate limiting is enabled")
	}
	if b.ipRateWindow > 0 && b.ipRateRequests < 1 {
		return fmt.Errorf("IP rate limit requests must be > 0 when window is set")
	}
	for _, spec := range b.keyedLimiters {
		if spec.name == "" {
			return fmt.Errorf("keyed rate limiter name is required")
		}
		if spec.requests <= 0 {
			return fmt.Errorf("keyed rate limiter %q must allow at least 1 request", spec.name)
		}
		if spec.window <= 0 {
			return fmt.Errorf("keyed rate limiter %q window must be > 0", spec.name)
		}
	}
	return nil
}
