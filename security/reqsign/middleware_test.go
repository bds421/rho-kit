package reqsign

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bds421/rho-kit/crypto/signing"
)

func TestMiddleware(t *testing.T) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))

	signTime := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	verifyTime := signTime.Add(10 * time.Minute)
	signSigner := signing.NewSigner(signing.WithClock(fixedClock(signTime)))
	verifySigner := signing.NewSigner(signing.WithClock(fixedClock(verifyTime)))

	tests := []struct {
		name       string
		method     string
		path       string
		body       []byte
		opts       []VerifyOption
		setupReq   func(t *testing.T, req *http.Request, body []byte)
		wantStatus int
		checkBody  bool // if true, verify downstream body matches
	}{
		{
			name:   "valid POST signature",
			method: http.MethodPost,
			path:   "/api/test",
			body:   []byte(`{"action":"test"}`),
			opts:   []VerifyOption{WithVerifySigner(signer)},
			setupReq: func(t *testing.T, req *http.Request, body []byte) {
				t.Helper()
				if err := SignRequest(req, body, store, WithSigner(signer)); err != nil {
					t.Fatalf("SignRequest failed: %v", err)
				}
				req.Body = io.NopCloser(bytes.NewReader(body))
			},
			wantStatus: http.StatusOK,
			checkBody:  true,
		},
		{
			name:       "missing signature",
			method:     http.MethodGet,
			path:       "/api/test",
			setupReq:   func(_ *testing.T, _ *http.Request, _ []byte) {},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:   "invalid signature",
			method: http.MethodPost,
			path:   "/api/test",
			body:   []byte(`{"action":"test"}`),
			opts:   []VerifyOption{WithVerifySigner(signer)},
			setupReq: func(_ *testing.T, req *http.Request, _ []byte) {
				req.Header.Set(HeaderSignature, "sha256=invalid")
				req.Header.Set(HeaderTimestamp, "1718452800")
				req.Header.Set(HeaderKeyID, "primary")
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:   "expired signature",
			method: http.MethodPost,
			path:   "/api/test",
			body:   []byte(`{"action":"test"}`),
			opts:   []VerifyOption{WithVerifySigner(verifySigner)},
			setupReq: func(t *testing.T, req *http.Request, body []byte) {
				t.Helper()
				if err := SignRequest(req, body, store, WithSigner(signSigner)); err != nil {
					t.Fatalf("SignRequest failed: %v", err)
				}
				req.Body = io.NopCloser(bytes.NewReader(body))
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:   "body too large",
			method: http.MethodPost,
			path:   "/api/test",
			body:   make([]byte, MaxBodySize+1),
			setupReq: func(_ *testing.T, req *http.Request, _ []byte) {
				req.Header.Set(HeaderSignature, "sha256=placeholder")
				req.Header.Set(HeaderTimestamp, "1718452800")
				req.Header.Set(HeaderKeyID, "primary")
			},
			wantStatus: http.StatusRequestEntityTooLarge,
		},
		{
			name:   "valid GET request",
			method: http.MethodGet,
			path:   "/api/status",
			opts:   []VerifyOption{WithVerifySigner(signer)},
			setupReq: func(t *testing.T, req *http.Request, _ []byte) {
				t.Helper()
				if err := SignRequest(req, nil, store, WithSigner(signer)); err != nil {
					t.Fatalf("SignRequest failed: %v", err)
				}
			},
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var downstreamBody []byte
			handler := RequireSignedRequest(store, tt.opts...)(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					downstreamBody, _ = io.ReadAll(r.Body)
					w.WriteHeader(http.StatusOK)
				}),
			)

			var bodyReader io.Reader
			if tt.body != nil {
				bodyReader = bytes.NewReader(tt.body)
			}
			req := httptest.NewRequest(tt.method, tt.path, bodyReader)
			tt.setupReq(t, req, tt.body)

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d; body = %s", rr.Code, tt.wantStatus, rr.Body.String())
			}

			if tt.checkBody && !bytes.Equal(downstreamBody, tt.body) {
				t.Errorf("downstream body = %q, want %q", downstreamBody, tt.body)
			}
		})
	}
}
