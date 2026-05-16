//go:build integration

package kittest_test

import (
	"testing"

	"github.com/bds421/rho-kit/testing/kittest/v2/amqp"
	"github.com/bds421/rho-kit/testing/kittest/v2/db"
	"github.com/bds421/rho-kit/testing/kittest/v2/redis"
	"github.com/bds421/rho-kit/testing/kittest/v2/storage"
)

// TestIntegrationReExportsCompile is a smoke test under `-tags integration`
// that references each integration-tagged re-export to prove the umbrella
// resolves to live symbols. It does NOT start any containers — calling the
// helpers is left to the underlying packages' own integration tests.
func TestIntegrationReExportsCompile(t *testing.T) {
	if db.StartPostgres == nil {
		t.Fatalf("db.StartPostgres should be a live function reference")
	}
	if redis.Start == nil {
		t.Fatalf("redis.Start should be a live function reference")
	}
	if redis.FlushDB == nil {
		t.Fatalf("redis.FlushDB should be a live function reference")
	}
	if storage.StartS3 == nil {
		t.Fatalf("storage.StartS3 should be a live function reference")
	}
	if storage.StartSFTP == nil {
		t.Fatalf("storage.StartSFTP should be a live function reference")
	}
	if amqp.Start == nil {
		t.Fatalf("amqp.Start should be a live function reference")
	}
}
