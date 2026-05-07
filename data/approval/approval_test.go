package approval

import (
	"testing"

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
	cases := []struct {
		name string
		r    Request
		ok   bool
	}{
		{"happy", Request{ID: "i", TenantID: "t", Actor: "a", Action: "x"}, true},
		{"happy with explicit pending", Request{ID: "i", TenantID: "t", Actor: "a", Action: "x", State: StatePending}, true},
		{"missing id", Request{TenantID: "t", Actor: "a", Action: "x"}, false},
		{"missing tenant", Request{ID: "i", Actor: "a", Action: "x"}, false},
		{"missing actor", Request{ID: "i", TenantID: "t", Action: "x"}, false},
		{"missing action", Request{ID: "i", TenantID: "t", Actor: "a"}, false},
		{"already approved", Request{ID: "i", TenantID: "t", Actor: "a", Action: "x", State: StateApproved}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validate(c.r)
			if c.ok {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, ErrInvalidRequest)
			}
		})
	}
}
