package tokenbucket_test

import (
	"context"
	"fmt"

	"github.com/bds421/rho-kit/data/v2/ratelimit/tokenbucket"
)

func ExampleNew() {
	// Capacity 2, refill 1/sec. The first two requests pass; the third is denied.
	lim := tokenbucket.New(2, 1, tokenbucket.WithoutSweeper())
	defer lim.Stop()

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		ok, _, _ := lim.Allow(ctx, "user-1")
		fmt.Println(i, ok)
	}
	// Output:
	// 0 true
	// 1 true
	// 2 false
}
