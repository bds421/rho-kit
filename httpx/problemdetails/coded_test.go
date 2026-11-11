package problemdetails_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/bds421/rho-kit/core/v2/contextutil"
	"github.com/bds421/rho-kit/httpx/v2/problemdetails"
)

var limitReached = problemdetails.Declare(http.StatusConflict, "test.limit_reached")

// A declared problem is listed by its code, found in a wrapped error
// whatever its parameters, and written with code, params, requestId and
// Retry-After.
func TestCodedProblems(t *testing.T) {
	if !slices.Contains(problemdetails.Declared(), "test.limit_reached") {
		t.Errorf("declared: %v", problemdetails.Declared())
	}
	err := fmt.Errorf("saving: %w", limitReached.With(map[string]string{"max": "3"}))
	if !problemdetails.Is(err, limitReached) || problemdetails.Is(errors.New("other"), limitReached) {
		t.Error("Is does not find the declared problem")
	}
	p := limitReached.With(map[string]string{"max": "3"})
	p.RetryAfter = 1500 * time.Millisecond
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r = r.WithContext(contextutil.SetRequestID(r.Context(), "req-1"))
	problemdetails.WriteCoded(rec, r, p, "The limit of 3 is reached.")
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusConflict || rec.Header().Get("Retry-After") != "2" || body["code"] != "test.limit_reached" ||
		body["requestId"] != "req-1" || body["detail"] != "The limit of 3 is reached." || body["params"].(map[string]any)["max"] != "3" {
		t.Errorf("wrote %d %s %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
	}
}
