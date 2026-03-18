package gormdb

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// Pinger wraps a GORM DB to provide a simple Ping() method for health checks.
type Pinger struct {
	db *gorm.DB
}

// NewPinger creates a Pinger that checks database connectivity.
func NewPinger(db *gorm.DB) *Pinger {
	return &Pinger{db: db}
}

// Ping checks database connectivity with a 3-second timeout.
func (p *Pinger) Ping() error {
	sqlDB, err := p.db.DB()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return sqlDB.PingContext(ctx)
}
