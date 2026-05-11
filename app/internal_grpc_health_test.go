package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInternalGRPCHealthRequest_ContentTypeValidation(t *testing.T) {
	tests := []struct {
		name       string
		protoMajor int
		values     []string
		want       bool
	}{
		{name: "grpc", protoMajor: 2, values: []string{"application/grpc"}, want: true},
		{name: "grpc proto", protoMajor: 2, values: []string{"application/grpc+proto"}, want: true},
		{name: "grpc with parameter", protoMajor: 2, values: []string{"Application/Grpc; charset=utf-8"}, want: true},
		{name: "http1", protoMajor: 1, values: []string{"application/grpc"}, want: false},
		{name: "missing", protoMajor: 2, want: false},
		{name: "duplicate", protoMajor: 2, values: []string{"application/grpc", "text/plain"}, want: false},
		{name: "grpc web", protoMajor: 2, values: []string{"application/grpc-web"}, want: false},
		{name: "control", protoMajor: 2, values: []string{"application/grpc\n"}, want: false},
		{name: "invalid utf8", protoMajor: 2, values: []string{string([]byte("application/grpc\xff"))}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			req.ProtoMajor = tt.protoMajor
			for _, value := range tt.values {
				req.Header.Add("Content-Type", value)
			}

			assert.Equal(t, tt.want, internalGRPCHealthRequest(req))
		})
	}
}
