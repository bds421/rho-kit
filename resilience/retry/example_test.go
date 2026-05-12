package retry_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bds421/rho-kit/resilience/v2/retry"
)

func ExampleDo() {
	attempts := 0
	err := retry.Do(context.Background(), func(context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("transient")
		}
		return nil
	}, retry.WithMaxRetries(5), retry.WithBaseDelay(time.Millisecond))

	fmt.Println(err)
	fmt.Println("attempts:", attempts)
	// Output:
	// <nil>
	// attempts: 3
}
