package eventbus_test

import (
	"context"
	"fmt"

	"github.com/bds421/rho-kit/runtime/v2/eventbus"
)

type userCreated struct {
	ID string
}

func (userCreated) EventName() string { return "user.created" }

func ExampleBus_publishSubscribe() {
	bus := eventbus.New()

	eventbus.Subscribe(bus, func(_ context.Context, e userCreated) error {
		fmt.Println("welcome", e.ID)
		return nil
	})

	_ = eventbus.Publish(bus, context.Background(), userCreated{ID: "u-1"})
	// Output:
	// welcome u-1
}
