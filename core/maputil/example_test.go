package maputil_test

import (
	"fmt"
	"sort"

	"github.com/bds421/rho-kit/core/v2/maputil"
)

func ExampleSetIfNotNil() {
	name := "alice"
	var role *string
	patch := map[string]any{}

	maputil.SetIfNotNil(patch, "name", &name)
	maputil.SetIfNotNil(patch, "role", role)

	keys := make([]string, 0, len(patch))
	for k := range patch {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("%s=%v\n", k, patch[k])
	}
	// Output:
	// name=alice
}
