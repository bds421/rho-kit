package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// orderRecorder middleware appends name to order when the request enters
// (outermost runs first).
func orderRecorder(name string, order *[]string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*order = append(*order, name)
			next.ServeHTTP(w, r)
		})
	}
}

type fakeMWModule struct {
	BaseModule
	mws []PhasedMiddleware
}

func (m *fakeMWModule) PublicMiddleware() []PhasedMiddleware { return m.mws }

func TestApplyPhasedMiddleware_EmptyPassthrough(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	h := applyPhasedMiddleware(inner, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusTeapot, rec.Code)
}

func TestApplyPhasedMiddleware_SkipsNilFunc(t *testing.T) {
	var order []string
	mods := []Module{
		&fakeMWModule{
			BaseModule: NewBaseModule("nil-func"),
			mws: []PhasedMiddleware{
				{Phase: PhaseAuth, Func: nil},
				{Phase: PhaseAuth, Func: orderRecorder("auth", &order)},
			},
		},
	}
	h := applyPhasedMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), mods)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, []string{"auth"}, order)
}

func TestApplyPhasedMiddleware_PhaseOrderAndRegistrationTieBreak(t *testing.T) {
	// Expected traversal (outermost first): highest phase first; within a
	// phase last-registered wraps first-registered so it is outermost.
	// Sort ascending by phase then registration index, apply inside-out:
	//   wrap order: budget → tenant → auth → signed → rateA → rateB
	// Traversal: rateB → rateA → signed → auth → tenant → budget → handler
	var order []string
	mods := []Module{
		&fakeMWModule{BaseModule: NewBaseModule("budget"), mws: []PhasedMiddleware{{Phase: PhaseBudget, Func: orderRecorder("budget", &order)}}},
		&fakeMWModule{BaseModule: NewBaseModule("tenant"), mws: []PhasedMiddleware{{Phase: PhaseTenant, Func: orderRecorder("tenant", &order)}}},
		&fakeMWModule{BaseModule: NewBaseModule("auth"), mws: []PhasedMiddleware{{Phase: PhaseAuth, Func: orderRecorder("auth", &order)}}},
		&fakeMWModule{BaseModule: NewBaseModule("rate-a"), mws: []PhasedMiddleware{{Phase: PhaseRateLimit, Func: orderRecorder("rateA", &order)}}},
		&fakeMWModule{BaseModule: NewBaseModule("rate-b"), mws: []PhasedMiddleware{{Phase: PhaseRateLimit, Func: orderRecorder("rateB", &order)}}},
		&fakeMWModule{BaseModule: NewBaseModule("signed"), mws: []PhasedMiddleware{{Phase: PhaseSignedRequest, Func: orderRecorder("signed", &order)}}},
	}

	innerCalled := false
	h := applyPhasedMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		innerCalled = true
		order = append(order, "handler")
	}), mods)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	require.True(t, innerCalled)
	assert.Equal(t, []string{"rateB", "rateA", "signed", "auth", "tenant", "budget", "handler"}, order)
}

func TestApplyPhasedMiddleware_IgnoresNonInstaller(t *testing.T) {
	var order []string
	mods := []Module{
		NewBaseModule("plain"),
		&fakeMWModule{BaseModule: NewBaseModule("auth"), mws: []PhasedMiddleware{{Phase: PhaseAuth, Func: orderRecorder("auth", &order)}}},
	}
	h := applyPhasedMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), mods)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, []string{"auth"}, order)
}
