package problemdetails

import (
	"errors"
	"maps"
	"math"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/bds421/rho-kit/core/v2/contextutil"
)

// Coded is a problem an API declares once and answers many times: a stable
// code the client translates, with parameters for its text. Its body is
// the RFC 9457 problem with the extensions code, params (when any) and
// requestId (when the request has one).
type Coded struct {
	Status int
	Code   string
	Params map[string]string
	// RetryAfter, when set, is sent as the Retry-After header in seconds.
	RetryAfter time.Duration
}

var (
	declaredMu sync.Mutex
	declared   = map[string]struct{}{}
)

// Declare registers a problem the API can answer. Declare problems as
// package variables, so [Declared] lists every code once the declaring
// package is linked, and a test can prove each code has its texts.
func Declare(status int, code string) Coded {
	declaredMu.Lock()
	declared[code] = struct{}{}
	declaredMu.Unlock()
	return Coded{Status: status, Code: code}
}

// Declared lists the codes of every declared problem, sorted.
func Declared() []string {
	declaredMu.Lock()
	defer declaredMu.Unlock()
	return slices.Sorted(maps.Keys(declared))
}

// With returns the problem carrying these parameters for the client's text.
func (p Coded) With(params map[string]string) Coded {
	p.Params = params
	return p
}

func (p Coded) Error() string { return p.Code }

// Is reports whether err is the declared problem target, whatever its
// parameters.
func Is(err error, target Coded) bool {
	var p Coded
	return errors.As(err, &p) && p.Code == target.Code
}

// WriteCoded answers with the problem, detail as its text for a reader.
func WriteCoded(w http.ResponseWriter, r *http.Request, p Coded, detail string) {
	ext := map[string]any{"code": p.Code}
	if id := contextutil.RequestID(r.Context()); id != "" {
		ext["requestId"] = id
	}
	if len(p.Params) > 0 {
		ext["params"] = p.Params
	}
	if p.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(p.RetryAfter.Seconds()))))
	}
	Write(w, Problem{Status: p.Status, Detail: detail, Extensions: ext})
}
