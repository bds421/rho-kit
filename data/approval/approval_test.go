package approval

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestState_IsTerminal(t *testing.T) {
	cases := map[State]bool{
		StatePending:  false,
		StateApproved: false,
		StateRejected: false,
		StateExecuted: true,
		StateExpired:  true,
	}
	for s, want := range cases {
		assert.Equal(t, want, s.IsTerminal(), "state %s", s)
	}
}

func TestValidate(t *testing.T) {
	now := time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	cases := []struct {
		name string
		r    Request
		ok   bool
	}{
		{"happy", Request{ID: "i", TenantID: "t", Actor: "a", Action: "x", ExpiresAt: future}, true},
		{"happy with explicit pending", Request{ID: "i", TenantID: "t", Actor: "a", Action: "x", State: StatePending, ExpiresAt: future}, true},
		{"missing id", Request{TenantID: "t", Actor: "a", Action: "x", ExpiresAt: future}, false},
		{"missing tenant", Request{ID: "i", Actor: "a", Action: "x", ExpiresAt: future}, false},
		{"missing actor", Request{ID: "i", TenantID: "t", Action: "x", ExpiresAt: future}, false},
		{"missing action", Request{ID: "i", TenantID: "t", Actor: "a", ExpiresAt: future}, false},
		{"already approved", Request{ID: "i", TenantID: "t", Actor: "a", Action: "x", State: StateApproved, ExpiresAt: future}, false},
		{"missing expiry", Request{ID: "i", TenantID: "t", Actor: "a", Action: "x"}, false},
		{"past expiry", Request{ID: "i", TenantID: "t", Actor: "a", Action: "x", ExpiresAt: past}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validate(c.r, now)
			if c.ok {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, ErrInvalidRequest)
			}
		})
	}
}
