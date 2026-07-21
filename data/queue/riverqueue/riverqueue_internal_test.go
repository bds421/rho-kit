package riverqueue

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/riverqueue/river"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/apperror"
	kitqueue "github.com/bds421/rho-kit/data/v2/queue"
)

// TestEnvelopeArgs_DedupeKeyedByIDOnly guards FR-059: with ByArgs set,
// River scopes the uniqueness hash to the fields tagged `river:"unique"`.
// Only the ID (the kit's idempotency token) may carry that tag. If Type
// or Payload were also hashed, a second Enqueue with the same ID but a
// different payload would produce a distinct unique key and execute
// twice — defeating the idempotency-token semantics and diverging from
// the redisqueue sibling, which keys strictly on the message ID.
func TestEnvelopeArgs_DedupeKeyedByIDOnly(t *testing.T) {
	typ := reflect.TypeOf(envelopeArgs{})

	cases := []struct {
		field      string
		wantUnique bool
	}{
		{"ID", true},
		{"Type", false},
		{"Payload", false},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			f, ok := typ.FieldByName(tc.field)
			require.True(t, ok, "field %s must exist", tc.field)
			hasUnique := f.Tag.Get("river") == "unique"
			assert.Equal(t, tc.wantUnique, hasUnique, "field %s river tag", tc.field)
		})
	}
}

func TestEnvelopeWorker_WorkDispatchesValidatedClone(t *testing.T) {
	payload := []byte(`{"id":42}`)
	w := NewEnvelopeWorker(func(_ context.Context, msg kitqueue.Message) error {
		assert.Equal(t, "msg-1", msg.ID)
		assert.Equal(t, "user.created", msg.Type)
		assert.JSONEq(t, `{"id":42}`, string(msg.Payload))
		msg.Payload[1] = 'X'
		return nil
	})

	err := w.Work(context.Background(), &river.Job[envelopeArgs]{
		Args: envelopeArgs{
			ID:      "msg-1",
			Type:    "user.created",
			Payload: payload,
		},
	})

	require.NoError(t, err)
	assert.JSONEq(t, `{"id":42}`, string(payload))
}

func TestEnvelopeWorker_WorkRejectsInvalidEnvelopeBeforeHandler(t *testing.T) {
	tests := []struct {
		name    string
		worker  *EnvelopeWorker
		args    envelopeArgs
		wantErr error
	}{
		{
			name:    "invalid type",
			worker:  NewEnvelopeWorker(func(context.Context, kitqueue.Message) error { return nil }),
			args:    envelopeArgs{ID: "msg-1", Type: "bad type", Payload: []byte(`{}`)},
			wantErr: kitqueue.ErrInvalidMessage,
		},
		{
			name:    "invalid id",
			worker:  NewEnvelopeWorker(func(context.Context, kitqueue.Message) error { return nil }),
			args:    envelopeArgs{ID: "bad id", Type: "user.created", Payload: []byte(`{}`)},
			wantErr: kitqueue.ErrInvalidMessage,
		},
		{
			name:    "oversize payload",
			worker:  NewEnvelopeWorker(func(context.Context, kitqueue.Message) error { return nil }, WithWorkerMaxPayloadBytes(4)),
			args:    envelopeArgs{ID: "msg-1", Type: "user.created", Payload: []byte(strings.Repeat("x", 5))},
			wantErr: kitqueue.ErrMessageTooLarge,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			tt.worker.handler = func(context.Context, kitqueue.Message) error {
				called = true
				return nil
			}

			err := tt.worker.Work(context.Background(), &river.Job[envelopeArgs]{Args: tt.args})

			assert.True(t, errors.Is(err, tt.wantErr), "err=%v", err)
			var cancel *river.JobCancelError
			assert.ErrorAs(t, err, &cancel, "invalid envelopes must JobCancel (no retry)")
			assert.False(t, called)
		})
	}
}

func TestEnvelopeWorker_WorkPermanentErrorJobCancels(t *testing.T) {
	permanent := apperror.NewPermanent("handler will never succeed")
	w := NewEnvelopeWorker(func(context.Context, kitqueue.Message) error {
		return permanent
	})

	err := w.Work(context.Background(), &river.Job[envelopeArgs]{
		Args: envelopeArgs{ID: "msg-1", Type: "user.created", Payload: []byte(`{}`)},
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, permanent)
	var cancel *river.JobCancelError
	assert.ErrorAs(t, err, &cancel, "permanent errors must JobCancel so River discards without retry")
}

func TestEnvelopeWorker_WorkTransientErrorRemainsRetryable(t *testing.T) {
	transient := errors.New("downstream 503")
	w := NewEnvelopeWorker(func(context.Context, kitqueue.Message) error {
		return transient
	})

	err := w.Work(context.Background(), &river.Job[envelopeArgs]{
		Args: envelopeArgs{ID: "msg-1", Type: "user.created", Payload: []byte(`{}`)},
	})

	require.ErrorIs(t, err, transient)
	var cancel *river.JobCancelError
	assert.False(t, errors.As(err, &cancel), "transient errors must remain retryable")
}
