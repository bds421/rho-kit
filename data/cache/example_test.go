package cache_test

import (
	"context"
	"fmt"
	"time"

	"github.com/bds421/rho-kit/data/v2/cache"
)

func ExampleOpenMemoryCache() {
	c, err := cache.OpenMemoryCache()
	if err != nil {
		fmt.Println("err:", err)
		return
	}
	defer func() { _ = c.Close() }()

	ctx := context.Background()
	if err := c.Set(ctx, "greeting", []byte("hello"), time.Minute); err != nil {
		fmt.Println("err:", err)
		return
	}
	c.Sync()

	val, err := c.Get(ctx, "greeting")
	fmt.Println(string(val), err)
	// Output:
	// hello <nil>
}
