package queue_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bds421/rho-kit/data/v2/queue"
)

func TestSentinels(t *testing.T) {
	assert.ErrorIs(t, queue.ErrInvalidQueue, queue.ErrInvalidQueue)
	assert.ErrorIs(t, queue.ErrBatchTooLarge, queue.ErrBatchTooLarge)
}

func TestValidateName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr error
	}{
		{name: "valid", input: "jobs:email-send"},
		{name: "empty", input: "", wantErr: queue.ErrInvalidName},
		{name: "too long", input: strings.Repeat("x", queue.MaxNameBytes+1), wantErr: queue.ErrInvalidName},
		{name: "null byte", input: "jobs\x00email", wantErr: queue.ErrInvalidName},
		{name: "newline", input: "jobs\nemail", wantErr: queue.ErrInvalidName},
		{name: "space", input: "jobs email", wantErr: queue.ErrInvalidName},
		{name: "tab", input: "jobs\temail", wantErr: queue.ErrInvalidName},
		{name: "invalid utf8", input: string([]byte{0xff, 0xfe}), wantErr: queue.ErrInvalidName},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := queue.ValidateName(tt.input, "queue")
			if tt.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			assert.True(t, errors.Is(err, tt.wantErr), "err=%v", err)
			if tt.name == "too long" {
				assert.NotContains(t, err.Error(), "256")
				assert.NotContains(t, err.Error(), "257")
			}
		})
	}
}

func TestValidateMessage(t *testing.T) {
	valid := queue.Message{
		ID:      "msg-1",
		Type:    "email.send",
		Payload: []byte(`{"id":42}`),
	}
	assert.NoError(t, queue.ValidateMessage(valid, queue.DefaultMaxPayloadBytes))

	for name, msg := range map[string]queue.Message{
		"empty type":    {ID: "msg-1", Payload: []byte(`{}`)},
		"newline type":  {ID: "msg-1", Type: "bad\ntype", Payload: []byte(`{}`)},
		"space type":    {ID: "msg-1", Type: "bad type", Payload: []byte(`{}`)},
		"tab type":      {ID: "msg-1", Type: "bad\ttype", Payload: []byte(`{}`)},
		"invalid type":  {ID: "msg-1", Type: string([]byte{0xff, 0xfe}), Payload: []byte(`{}`)},
		"oversize type": {ID: "msg-1", Type: strings.Repeat("x", queue.MaxMessageTypeBytes+1), Payload: []byte(`{}`)},
		"newline id":    {ID: "bad\nid", Type: "email.send", Payload: []byte(`{}`)},
		"space id":      {ID: "bad id", Type: "email.send", Payload: []byte(`{}`)},
		"tab id":        {ID: "bad\tid", Type: "email.send", Payload: []byte(`{}`)},
		"invalid id":    {ID: string([]byte{0xff, 0xfe}), Type: "email.send", Payload: []byte(`{}`)},
		"oversize id":   {ID: strings.Repeat("x", queue.MaxMessageIDBytes+1), Type: "email.send", Payload: []byte(`{}`)},
	} {
		t.Run(name, func(t *testing.T) {
			err := queue.ValidateMessage(msg, queue.DefaultMaxPayloadBytes)
			assert.True(t, errors.Is(err, queue.ErrInvalidMessage), "err=%v", err)
			if strings.Contains(name, "oversize") {
				assert.NotContains(t, err.Error(), "255")
				assert.NotContains(t, err.Error(), "256")
				assert.NotContains(t, err.Error(), "257")
			}
		})
	}

	err := queue.ValidateMessage(queue.Message{
		ID:      "msg-1",
		Type:    "email.send",
		Payload: []byte("too large"),
	}, 4)
	assert.True(t, errors.Is(err, queue.ErrMessageTooLarge), "err=%v", err)
}
