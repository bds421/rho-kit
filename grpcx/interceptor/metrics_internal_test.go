package interceptor

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGRPCMethodLabel(t *testing.T) {
	tests := []struct {
		name   string
		method string
		want   string
	}{
		{name: "valid", method: "/grpc.health.v1.Health/Check", want: "/grpc.health.v1.Health/Check"},
		{name: "empty", method: "", want: "invalid"},
		{name: "newline", method: "/svc\n/Method", want: "invalid"},
		{name: "invalid utf8", method: string([]byte{0xff}), want: "invalid"},
		{name: "too long", method: strings.Repeat("a", 257), want: "invalid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, grpcMethodLabel(tt.method))
		})
	}
}
