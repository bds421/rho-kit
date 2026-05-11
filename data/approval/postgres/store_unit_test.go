package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"

	"github.com/bds421/rho-kit/data/v2/approval"
)

func TestNew_PanicsOnNilPool(t *testing.T) {
	assert.Panics(t, func() { New(nil) })
}

func TestNew_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() { New(&pgxpool.Pool{}, nil) })
}

func TestStore_InvalidReceiverReturnsError(t *testing.T) {
	ctx := context.Background()

	for name, store := range map[string]*Store{
		"nil":  nil,
		"zero": {},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := store.Create(ctx, approval.Request{})
			assert.ErrorIs(t, err, approval.ErrInvalidStore)

			_, err = store.Get(ctx, "r")
			assert.ErrorIs(t, err, approval.ErrInvalidStore)

			_, err = store.List(ctx, approval.Query{TenantID: "tenant"})
			assert.ErrorIs(t, err, approval.ErrInvalidStore)

			_, err = store.Decide(ctx, "r", "approver", "ok", true)
			assert.ErrorIs(t, err, approval.ErrInvalidStore)

			_, err = store.MarkExecuted(ctx, "r")
			assert.ErrorIs(t, err, approval.ErrInvalidStore)
		})
	}
}

func TestList_ValidatesQueryScopeBeforeDBUse(t *testing.T) {
	store := &Store{pool: &pgxpool.Pool{}, clock: time.Now}

	_, err := store.List(context.Background(), approval.Query{TenantID: "tenant", AllTenants: true})
	assert.ErrorIs(t, err, approval.ErrQueryScopeConflict)
}

func TestCreate_UsesSharedValidationBeforeDBUse(t *testing.T) {
	store := &Store{pool: &pgxpool.Pool{}, clock: time.Now}

	r := approval.Request{
		ID:        strings.Repeat("a", approval.MaxIDLen+1),
		TenantID:  "tenant",
		Actor:     "agent",
		Action:    "user.delete",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	_, err := store.Create(context.Background(), r)
	assert.ErrorIs(t, err, approval.ErrInvalidRequest)

	r.ID = "payload-too-large"
	r.Payload = make([]byte, approval.MaxPayloadSize+1)
	_, err = store.Create(context.Background(), r)
	assert.ErrorIs(t, err, approval.ErrInvalidRequest)

	r.Payload = nil
	r.Actor = strings.Repeat("a", approval.MaxActorLen+1)
	_, err = store.Create(context.Background(), r)
	assert.ErrorIs(t, err, approval.ErrInvalidRequest)
}

func TestDecide_UsesSharedValidationBeforeDBUse(t *testing.T) {
	store := &Store{pool: &pgxpool.Pool{}, clock: time.Now}

	_, err := store.Decide(context.Background(), "r1", strings.Repeat("a", approval.MaxActorLen+1), "ok", true)
	assert.ErrorIs(t, err, approval.ErrInvalidApprover)
}
