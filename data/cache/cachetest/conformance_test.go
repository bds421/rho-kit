package cachetest_test

import (
	"testing"

	"github.com/bds421/rho-kit/data/v2/cache"
	"github.com/bds421/rho-kit/data/v2/cache/cachetest"
)

// TestMemoryCache_Conformance dogfoods the conformance suite
// against MemoryCache.
func TestMemoryCache_Conformance(t *testing.T) {
	cachetest.Run(t, func(t *testing.T) cache.Cache {
		mc, err := cache.NewMemoryCache()
		if err != nil {
			t.Fatalf("NewMemoryCache: %v", err)
		}
		return mc
	})
}
