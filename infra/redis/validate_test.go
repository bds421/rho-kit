package redis

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		kind    string
		wantErr string
	}{
		{
			name:  "valid name",
			input: "my-stream",
			kind:  "stream",
		},
		{
			name:  "valid name with colons",
			input: "app:events:created",
			kind:  "queue",
		},
		{
			name:    "empty name",
			input:   "",
			kind:    "stream",
			wantErr: "stream name must not be empty",
		},
		{
			name:    "null byte",
			input:   "bad\x00name",
			kind:    "stream",
			wantErr: "invalid characters",
		},
		{
			name:    "newline",
			input:   "bad\nname",
			kind:    "queue",
			wantErr: "invalid characters",
		},
		{
			name:    "carriage return",
			input:   "bad\rname",
			kind:    "cache",
			wantErr: "invalid characters",
		},
		{
			name:    "too long",
			input:   strings.Repeat("x", maxNameLen+1),
			kind:    "stream",
			wantErr: "exceeds maximum length",
		},
		{
			name:  "exactly at max length",
			input: strings.Repeat("x", maxNameLen),
			kind:  "stream",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateName(tt.input, tt.kind)
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}
