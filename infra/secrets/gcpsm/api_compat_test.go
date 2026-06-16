package gcpsm_test

import (
	secretmanager "cloud.google.com/go/secretmanager/apiv1"

	"github.com/bds421/rho-kit/infra/secrets/gcpsm/v2"
)

// Compile-time assertion that the real *secretmanager.Client satisfies
// gcpsm.API. AccessSecretVersion's variadic opts are ...gax.CallOption on
// the real client, so the interface must declare the same type for
// gcpsm.New(realClient) to compile.
var _ gcpsm.API = (*secretmanager.Client)(nil)
