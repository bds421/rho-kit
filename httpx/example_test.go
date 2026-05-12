package httpx_test

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/httpx/v2"
)

func ExampleNewServer() {
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("pong"))
	})

	srv := httpx.NewServer(":0", mux)
	fmt.Println(srv.ReadHeaderTimeout)
	fmt.Println(srv.IdleTimeout)
	// Output:
	// 5s
	// 1m0s
}

func ExampleWriteServiceError() {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/users/u-1", nil)

	err := apperror.NewNotFound("user", "u-1")
	httpx.WriteServiceError(rec, req, logger, err)

	fmt.Println(rec.Code)
	fmt.Println(rec.Body.String())
	// Output:
	// 404
	// {"error":"resource not found","code":"NOT_FOUND"}
}
