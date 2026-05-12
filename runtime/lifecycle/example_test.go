package lifecycle_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/bds421/rho-kit/runtime/v2/lifecycle"
)

func ExampleRunner() {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner := lifecycle.NewRunner(logger, lifecycle.WithStopTimeout(time.Second))

	runner.Add("ticker", lifecycle.NewFuncComponent(func(ctx context.Context) error {
		fmt.Println("started")
		<-ctx.Done()
		fmt.Println("stopped")
		return nil
	}))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	_ = runner.Run(ctx)
	// Output:
	// started
	// stopped
}
