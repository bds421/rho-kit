package concurrency_test

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/bds421/rho-kit/runtime/v2/concurrency"
)

func ExampleFanOut() {
	ctx := context.Background()
	fns := []func(context.Context) (int, error){
		func(context.Context) (int, error) { return 1, nil },
		func(context.Context) (int, error) { return 2, nil },
		func(context.Context) (int, error) { return 3, nil },
	}
	results, err := concurrency.FanOut(ctx, fns)
	fmt.Println(err)
	sort.Ints(results)
	fmt.Println(results)
	// Output:
	// <nil>
	// [1 2 3]
}

func ExampleFanOutSettled() {
	ctx := context.Background()
	fns := []func(context.Context) (string, error){
		func(context.Context) (string, error) { return "ok", nil },
		func(context.Context) (string, error) { return "", errors.New("boom") },
	}
	results := concurrency.FanOutSettled(ctx, fns)
	for _, r := range results {
		if r.Err != nil {
			fmt.Printf("err=%v\n", r.Err)
		} else {
			fmt.Printf("ok=%s\n", r.Value)
		}
	}
	// Output:
	// ok=ok
	// err=boom
}
