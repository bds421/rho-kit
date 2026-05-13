package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coretenant "github.com/bds421/rho-kit/core/v2/tenant"
	"github.com/bds421/rho-kit/data/v2/actionlog"
	"github.com/bds421/rho-kit/data/v2/approval"
	httpxbudget "github.com/bds421/rho-kit/httpx/v2/middleware/budget"
	httpxtenant "github.com/bds421/rho-kit/httpx/v2/middleware/tenant"
)

// stubBudget records calls; used to verify the Builder wires the
// budget middleware into the public mux.
type stubBudget struct{ calls int }

func (s *stubBudget) Consume(_ context.Context, _ string, _ int64) (bool, int64, time.Duration, error) {
	s.calls++
	return true, 100, 0, nil
}

func (s *stubBudget) Peek(_ context.Context, _ string) (int64, error) { return 100, nil }

// stubActionLog and stubApproval are minimal interface satisfiers.
type stubActionLog struct{}

func (stubActionLog) Append(_ context.Context, e actionlog.Entry) (actionlog.Entry, error) {
	return e, nil
}
func (stubActionLog) Get(_ context.Context, _ string) (actionlog.Entry, error) {
	return actionlog.Entry{}, nil
}
func (stubActionLog) List(_ context.Context, _ actionlog.Query) ([]actionlog.Entry, error) {
	return nil, nil
}
func (stubActionLog) Sign(_ actionlog.Entry) (string, string, error) {
	return "", "", nil
}
func (stubActionLog) Verify(_ actionlog.Entry) error                { return nil }
func (stubActionLog) VerifyChain(_ context.Context, _ string) error { return nil }

type stubApproval struct{}

func (stubApproval) Create(_ context.Context, r approval.Request) (approval.Request, error) {
	return r, nil
}
func (stubApproval) Get(_ context.Context, _ string) (approval.Request, error) {
	return approval.Request{}, nil
}
func (stubApproval) List(_ context.Context, _ approval.Query) ([]approval.Request, error) {
	return nil, nil
}
func (stubApproval) Approve(_ context.Context, _, _, _ string) (approval.Request, error) {
	return approval.Request{}, nil
}
func (stubApproval) Reject(_ context.Context, _, _, _ string) (approval.Request, error) {
	return approval.Request{}, nil
}
func (stubApproval) MarkExecuted(_ context.Context, _ string) (approval.Request, error) {
	return approval.Request{}, nil
}

func TestWithMultiTenant_RegistersOnBuilder(t *testing.T) {
	ext := httpxtenant.HeaderExtractor("X-Tenant-Id")
	b := New("test", "v1", BaseConfig{}).WithMultiTenant(ext)
	require.NotNil(t, b.tenantSpec)
	assert.True(t, b.tenantSpec.required)
}

func TestTenantMiddleware_PopulatesContext(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).WithMultiTenant(nil)
	mw := b.tenantMiddleware()
	require.NotNil(t, mw)

	captured := ""
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if id, ok := coretenant.FromContext(r.Context()); ok {
			captured = string(id)
		}
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Tenant-Id", "acme")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, "acme", captured)
}

func TestWithTenantBudget_PanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil store")
		}
	}()
	New("test", "v1", BaseConfig{}).WithTenantBudget(nil)
}

func TestWithTenantBudget_BuildsMiddleware(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).WithTenantBudget(&stubBudget{})
	require.NotNil(t, b.budgetMiddleware())
	assert.Same(t, b.budgetSpec.store.(*stubBudget), b.budgetSpecStore())
}

func TestWithTenantBudget_ClonesOptions(t *testing.T) {
	opts := []httpxbudget.Option{httpxbudget.WithScope("tenant")}

	b := New("test", "v1", BaseConfig{}).WithTenantBudget(&stubBudget{}, opts...)
	opts[0] = nil

	require.NotNil(t, b.budgetSpec)
	require.Len(t, b.budgetSpec.opts, 1)
	assert.NotNil(t, b.budgetSpec.opts[0])
}

// R3-H: WithTenantBudget combined with the safe-method bypass would
// send some charged routes to budget enforcement without a tenant key.
// Validate must reject the combination at startup.
func TestWithTenantBudget_RejectsAllowMissingTenantOnSafeMethods(t *testing.T) {
	b := New("test", "v1", validBaseConfig()).
		WithMultiTenant(nil).
		WithAllowMissingTenantOnSafeMethods().
		WithTenantBudget(&stubBudget{})
	err := b.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WithAllowMissingTenantOnSafeMethods")
}

// R3-H: WithTenantBudget combined with required=false on the tenant
// middleware would send some charged routes to budget enforcement
// without a tenant key. Validate must reject this.
func TestWithTenantBudget_RejectsRequiredFalse(t *testing.T) {
	b := New("test", "v1", validBaseConfig()).
		WithMultiTenantOptional(nil).
		WithTenantBudget(&stubBudget{})
	err := b.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required=true")
}

// R3-H: WithTenantBudget + WithMultiTenant(..., required=true) is the
// canonical, accepted shape. End-to-end exercise: GET without
// X-Tenant-Id must receive 400 (the tenant middleware rejects it
// before the budget middleware has to reject a missing key).
func TestWithTenantBudget_RejectsGETWithoutTenant(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).
		WithMultiTenant(nil).
		WithTenantBudget(&stubBudget{})

	tenantMW := b.tenantMiddleware()
	require.NotNil(t, tenantMW)
	budgetMW := b.budgetMiddleware()
	require.NotNil(t, budgetMW)

	handler := tenantMW(budgetMW(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler must not run when tenant is absent")
		w.WriteHeader(http.StatusOK)
	})))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"GET without tenant must be rejected — guarantees the tenant budget cannot be bypassed on safe methods")
}

func TestWithActionLogger_PanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil logger")
		}
	}()
	New("test", "v1", BaseConfig{}).WithActionLogger(nil)
}

func TestWithApprovalStore_PanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil store")
		}
	}()
	New("test", "v1", BaseConfig{}).WithApprovalStore(nil)
}

func TestWithActionLogger_RegistersOnBuilder(t *testing.T) {
	l := stubActionLog{}
	b := New("test", "v1", BaseConfig{}).WithActionLogger(l)
	assert.Equal(t, l, b.actionLogger())
}

func TestWithApprovalStore_RegistersOnBuilder(t *testing.T) {
	s := stubApproval{}
	b := New("test", "v1", BaseConfig{}).WithApprovalStore(s)
	assert.Equal(t, s, b.approvalStore())
}
