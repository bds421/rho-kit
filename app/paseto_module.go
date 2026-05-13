package app

import (
	"context"

	"github.com/bds421/rho-kit/crypto/v2/paseto"
)

// pasetoModule wires a [paseto.Provider] into the lifecycle so its
// background key-refresh goroutine starts at Init and stops on
// shutdown.
type pasetoModule struct {
	BaseModule

	provider *paseto.Provider
}

// newPasetoModule constructs a module wrapping the supplied
// Provider. The Provider is constructed by the caller (via
// [paseto.OpenProvider]) so they retain control over the key source
// — the kit deliberately doesn't ship a "default" PASETO source the
// way it does for JWT, because PASETO key rotation is much more
// service-specific.
func newPasetoModule(p *paseto.Provider) *pasetoModule {
	if p == nil {
		panic("app: paseto module requires a non-nil Provider")
	}
	return &pasetoModule{
		BaseModule: NewBaseModule("paseto"),
		provider:   p,
	}
}

func (m *pasetoModule) Init(_ context.Context, mc ModuleContext) error {
	// The Provider's refresh loop is already running (kicked off in
	// paseto.OpenProvider). We just need to ensure Close is called on
	// shutdown.
	mc.Runner.AddFunc("paseto-provider", func(ctx context.Context) error {
		<-ctx.Done()
		return m.provider.Close()
	})
	mc.Logger.Info("paseto provider wired")
	return nil
}

func (m *pasetoModule) Populate(infra *Infrastructure) {
	infra.PASETO = m.provider
}
