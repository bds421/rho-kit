package approval

import (
	"strings"
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
		{"happy action with method and path", Request{ID: "i", TenantID: "t", Actor: "a", Action: "DELETE /v1/users/42", ExpiresAt: future}, true},
		{"missing id", Request{TenantID: "t", Actor: "a", Action: "x", ExpiresAt: future}, false},
		{"missing tenant", Request{ID: "i", Actor: "a", Action: "x", ExpiresAt: future}, false},
		{"missing actor", Request{ID: "i", TenantID: "t", Action: "x", ExpiresAt: future}, false},
		{"missing action", Request{ID: "i", TenantID: "t", Actor: "a", ExpiresAt: future}, false},
		{"already approved", Request{ID: "i", TenantID: "t", Actor: "a", Action: "x", State: StateApproved, ExpiresAt: future}, false},
		{"id too long", Request{ID: strings.Repeat("a", MaxIDLen+1), TenantID: "t", Actor: "a", Action: "x", ExpiresAt: future}, false},
		{"id invalid chars", Request{ID: "bad/id", TenantID: "t", Actor: "a", Action: "x", ExpiresAt: future}, false},
		{"tenant too long", Request{ID: "i", TenantID: strings.Repeat("t", MaxTenantIDLen+1), Actor: "a", Action: "x", ExpiresAt: future}, false},
		{"tenant invalid token", Request{ID: "i", TenantID: "tenant/1", Actor: "a", Action: "x", ExpiresAt: future}, false},
		{"actor too long", Request{ID: "i", TenantID: "t", Actor: strings.Repeat("a", MaxActorLen+1), Action: "x", ExpiresAt: future}, false},
		{"actor invalid utf8", Request{ID: "i", TenantID: "t", Actor: string([]byte{0xff}), Action: "x", ExpiresAt: future}, false},
		{"actor contains nul", Request{ID: "i", TenantID: "t", Actor: "a\x00b", Action: "x", ExpiresAt: future}, false},
		{"actor contains newline", Request{ID: "i", TenantID: "t", Actor: "a\nb", Action: "x", ExpiresAt: future}, false},
		{"actor contains space", Request{ID: "i", TenantID: "t", Actor: "a b", Action: "x", ExpiresAt: future}, false},
		{"actor contains tab", Request{ID: "i", TenantID: "t", Actor: "a\tb", Action: "x", ExpiresAt: future}, false},
		{"action too long", Request{ID: "i", TenantID: "t", Actor: "a", Action: strings.Repeat("x", MaxActionLen+1), ExpiresAt: future}, false},
		{"action contains carriage return", Request{ID: "i", TenantID: "t", Actor: "a", Action: "x\ry", ExpiresAt: future}, false},
		{"action contains tab", Request{ID: "i", TenantID: "t", Actor: "a", Action: "x\ty", ExpiresAt: future}, false},
		{"resource too long", Request{ID: "i", TenantID: "t", Actor: "a", Action: "x", Resource: strings.Repeat("r", MaxResourceLen+1), ExpiresAt: future}, false},
		{"resource contains newline", Request{ID: "i", TenantID: "t", Actor: "a", Action: "x", Resource: "orders\n1", ExpiresAt: future}, false},
		{"resource contains space", Request{ID: "i", TenantID: "t", Actor: "a", Action: "x", Resource: "orders 1", ExpiresAt: future}, false},
		{"reason too long", Request{ID: "i", TenantID: "t", Actor: "a", Action: "x", Reason: strings.Repeat("r", MaxReasonLen+1), ExpiresAt: future}, false},
		{"reason invalid utf8", Request{ID: "i", TenantID: "t", Actor: "a", Action: "x", Reason: string([]byte{0xff}), ExpiresAt: future}, false},
		{"reason contains newline", Request{ID: "i", TenantID: "t", Actor: "a", Action: "x", Reason: "line\nbreak", ExpiresAt: future}, false},
		{"payload too large", Request{ID: "i", TenantID: "t", Actor: "a", Action: "x", Payload: make([]byte, MaxPayloadSize+1), ExpiresAt: future}, false},
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

func TestValidateDecision(t *testing.T) {
	tests := []struct {
		name      string
		decidedBy string
		ok        bool
	}{
		{name: "happy", decidedBy: "approver-1", ok: true},
		{name: "empty"},
		{name: "too long", decidedBy: strings.Repeat("a", MaxActorLen+1)},
		{name: "invalid utf8", decidedBy: string([]byte{0xff})},
		{name: "contains nul", decidedBy: "approver\x001"},
		{name: "contains newline", decidedBy: "approver\n1"},
		{name: "contains space", decidedBy: "approver 1"},
		{name: "contains tab", decidedBy: "approver\t1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDecision(tt.decidedBy)
			if tt.ok {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, ErrInvalidApprover)
			}
		})
	}
}

func TestValidateReason(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		ok     bool
	}{
		{name: "empty", ok: true},
		{name: "sentence", reason: "looks legit", ok: true},
		{name: "max", reason: strings.Repeat("r", MaxReasonLen), ok: true},
		{name: "too long", reason: strings.Repeat("r", MaxReasonLen+1)},
		{name: "invalid utf8", reason: string([]byte{0xff})},
		{name: "contains nul", reason: "bad\x00reason"},
		{name: "contains newline", reason: "bad\nreason"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateReason(tt.reason)
			if tt.ok {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, ErrInvalidReason)
			}
		})
	}
}
