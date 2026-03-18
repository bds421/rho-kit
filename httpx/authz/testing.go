package authz

import "context"

// AllowAll returns a Policy that permits every request. Use in tests.
func AllowAll() Policy {
	return policyFunc(func(context.Context, string, string, string) (bool, error) {
		return true, nil
	})
}

// DenyAll returns a Policy that denies every request. Use in tests.
func DenyAll() Policy {
	return policyFunc(func(context.Context, string, string, string) (bool, error) {
		return false, nil
	})
}

// AllowOnly returns a Policy that permits only the specified (subject, action, resource)
// triple and denies everything else. Use in tests.
func AllowOnly(subject, action, resource string) Policy {
	return policyFunc(func(_ context.Context, s, a, r string) (bool, error) {
		return s == subject && a == action && r == resource, nil
	})
}

// policyFunc adapts a function to the Policy interface.
type policyFunc func(ctx context.Context, subject, action, resource string) (bool, error)

func (f policyFunc) Allowed(ctx context.Context, subject, action, resource string) (bool, error) {
	return f(ctx, subject, action, resource)
}
