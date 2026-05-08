package authz

import (
	"context"
	"errors"

	kitauthz "github.com/bds421/rho-kit/authz/v2"
)

// FromDecider adapts a [kitauthz.Decider] (the kit's vendor-neutral
// authorization seam) into an [httpx/authz.Policy] that
// [RequirePermission] middleware accepts. The bridge is the recommended
// integration path: services configure a single Decider via
// app.Builder.WithAuthz, then per-route middleware uses
// `authz.RequirePermission(authz.FromDecider(infra.Authz), ...)`.
//
// The adapter translates the Decider's error-sentinel idiom
// (kitauthz.ErrDenied) into the Policy's (bool, error) signature:
//
//   - nil error from Decider → (true, nil)
//   - errors.Is(err, kitauthz.ErrDenied) → (false, nil)
//   - any other error → (false, err) — engine failure surfaced for
//     middleware error handling, not silently denied
//
// asvs: V4.1.1, V4.1.5
func FromDecider(d kitauthz.Decider) Policy {
	if d == nil {
		panic("authz: FromDecider requires a non-nil Decider")
	}
	return deciderPolicy{d: d}
}

type deciderPolicy struct {
	d kitauthz.Decider
}

func (p deciderPolicy) Allowed(ctx context.Context, subject, action, resource string) (bool, error) {
	if err := p.d.Allow(ctx, subject, action, resource); err != nil {
		if errors.Is(err, kitauthz.ErrDenied) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
