// Package problemdetails implements RFC 7807 Problem Details for HTTP APIs.
//
// Use this alongside (not instead of) httpx.WriteError. WriteError emits the
// kit's compact `{error, code}` envelope which is fine for first-party
// consumers; problem-details is the right choice when:
//
//   - The API is consumed by third parties or generated SDKs that expect
//     `application/problem+json`.
//   - You need to attach extension fields (validation errors, retry hints,
//     correlation IDs) without redefining the envelope schema.
//   - You are publishing public error-type URIs that link to documentation.
package problemdetails

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bds421/rho-kit/core/v2/apperror"
	coreconfig "github.com/bds421/rho-kit/core/v2/config"
)

// ContentType is the RFC 7807 media type.
const ContentType = "application/problem+json"

// Problem is the RFC 7807 envelope. Extensions are marshaled inline.
type Problem struct {
	Type     string `json:"type,omitempty"`
	Title    string `json:"title,omitempty"`
	Status   int    `json:"status,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`

	// Extensions hold caller-supplied additional members. Marshalled
	// inline at the top level of the JSON object via the custom
	// MarshalJSON below. Keys must not collide with the RFC 7807
	// reserved members ("type", "title", "status", "detail",
	// "instance") — Write panics if they do.
	Extensions map[string]any `json:"-"`
}

// MarshalJSON serialises the Problem envelope, inlining Extensions at
// the top level of the JSON object.
func (p Problem) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, 5+len(p.Extensions))
	if p.Type != "" {
		out["type"] = p.Type
	}
	if p.Title != "" {
		out["title"] = p.Title
	}
	if p.Status != 0 {
		out["status"] = p.Status
	}
	if p.Detail != "" {
		out["detail"] = p.Detail
	}
	if p.Instance != "" {
		out["instance"] = p.Instance
	}
	for k, v := range p.Extensions {
		switch k {
		case "type", "title", "status", "detail", "instance":
			return nil, errors.New("problemdetails: extension key collides with reserved RFC 7807 member")
		}
		out[k] = v
	}
	return json.Marshal(out)
}

// Write serialises p to w with the application/problem+json media type
// and the status code from p.Status (defaulting to 500 if unset).
//
// If err is non-nil, Write surfaces it via the response writer's body
// only when w supports a flusher and the headers are not yet written —
// in normal use Write returns no error. This signature mirrors
// http.Error: callers shouldn't have to handle a write failure.
func Write(w http.ResponseWriter, p Problem) {
	status := p.Status
	if status == 0 {
		status = http.StatusInternalServerError
		p.Status = status
	}
	body, err := json.Marshal(p)
	if err != nil {
		writeInternalMarshalError(w)
		return
	}
	w.Header().Set("Content-Type", ContentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeInternalMarshalError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", ContentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write([]byte(`{"title":"Internal Server Error","status":500,"detail":"internal error"}`))
}

// Option configures [FromError].
type Option func(*config)

type config struct {
	baseURL  string
	instance string
}

// WithBaseURL sets the prefix for the auto-generated `type` URI when
// FromError converts a kit apperror to a Problem. The default is
// "about:blank" per RFC 7807. Pass a documentation root such as
// "https://errors.example.com/" to point clients at human-readable
// descriptions.
func WithBaseURL(s string) Option {
	return func(c *config) {
		base, err := validateBaseURL(s)
		if err != nil {
			panic("problemdetails: base URL is invalid")
		}
		c.baseURL = base
	}
}

// WithInstance sets the `instance` URI on the generated Problem.
// Prefer path-only request-derived values such as r.URL.EscapedPath(); query
// strings often carry secrets and should not be reflected into error bodies.
//
// Invalid instances are dropped silently rather than panicking — every
// HTTP error path routes r.URL.EscapedPath() through this option, and
// some legitimate-but-unusual URL shapes (OPTIONS *, CONNECT, mounts
// stripped to "", router rewrites) would otherwise crash the host
// service mid-response. The Problem just omits the instance field on
// invalid input; FromError continues to produce a valid Problem.
func WithInstance(instance string) Option {
	if err := validateInstance(instance); err != nil {
		return func(*config) {}
	}
	return func(c *config) { c.instance = instance }
}

// FromError maps a core/apperror.AppError (or a generic error) to a
// Problem. Status comes from the kit's existing apperror→HTTP mapping
// (mirroring httpx.HTTPStatus). If err is a *apperror.RateLimitError or
// *apperror.UnavailableError with a RetryAfter, it is added as a
// "retry_after_seconds" extension.
//
// Validation errors (apperror.FieldErrors) become an "errors" extension
// with per-field messages.
func FromError(err error, opts ...Option) Problem {
	if err == nil {
		err = errors.New("internal error")
	}
	cfg := config{baseURL: ""}
	for _, o := range opts {
		if o == nil {
			panic("problemdetails: option must not be nil")
		}
		o(&cfg)
	}

	status := mapStatus(err)
	title := http.StatusText(status)

	p := Problem{
		Type:     resolveType(cfg.baseURL, err, status),
		Title:    title,
		Status:   status,
		Detail:   SafeDetail(err),
		Instance: cfg.instance,
	}

	if rl, ok := apperror.AsRateLimit(err); ok && rl.RetryAfter > 0 {
		ensureExt(&p)["retry_after_seconds"] = int(math.Ceil(rl.RetryAfter.Seconds()))
	}
	if ue, ok := apperror.AsUnavailable(err); ok && ue.RetryAfter > 0 {
		ensureExt(&p)["retry_after_seconds"] = int(math.Ceil(ue.RetryAfter.Seconds()))
	}

	var ve *apperror.ValidationError
	if errors.As(err, &ve) && len(ve.Fields) > 0 {
		ensureExt(&p)["errors"] = fieldErrorsExtension(ve.Fields)
	}

	return p
}

// SafeDetail returns the client-facing RFC 7807 detail for err.
//
// It deliberately mirrors httpx.WriteServiceError's response policy: validation
// details preserve the original message, while operation/dependency failures and
// generic errors collapse to stable generic strings so internal hostnames,
// ports, SQL text, or wrapped causes do not leak through problem+json responses.
func SafeDetail(err error) string {
	if err == nil {
		return "internal error"
	}
	if ve, ok := apperror.AsValidation(err); ok {
		if ve.Error() != "" {
			return ve.Error()
		}
		return "validation failed"
	}
	if apperror.IsRateLimit(err) {
		return "rate limit exceeded"
	}
	if apperror.IsNotFound(err) {
		return "resource not found"
	}
	if apperror.IsConflict(err) {
		return "resource conflict"
	}
	if apperror.IsPermanent(err) {
		return "operation cannot be completed"
	}
	if apperror.IsAuthRequired(err) {
		return "authentication required"
	}
	if apperror.IsForbidden(err) {
		return "forbidden"
	}
	if apperror.IsUnavailable(err) {
		return "service unavailable"
	}
	if apperror.IsOperationFailed(err) {
		return "internal error"
	}
	return "internal error"
}

func validateBaseURL(raw string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if base == "" {
		return "", nil
	}
	if base != strings.TrimRight(raw, "/") {
		return "", errors.New("problemdetails: base URL must not contain surrounding whitespace")
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", errors.New("problemdetails: invalid base URL")
	}
	if !u.IsAbs() || u.Host == "" {
		return "", errors.New("problemdetails: base URL must be absolute with a host")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", errors.New("problemdetails: base URL scheme must be http or https")
	}
	if u.User != nil {
		return "", errors.New("problemdetails: base URL must not contain credentials")
	}
	if err := coreconfig.ValidateURLHost("problemdetails: base URL", u); err != nil {
		return "", err
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("problemdetails: base URL must not contain query or fragment components")
	}
	return base, nil
}

func validateInstance(instance string) error {
	if instance == "" {
		return nil
	}
	if strings.TrimSpace(instance) != instance {
		return errors.New("problemdetails: instance must not contain surrounding whitespace")
	}
	if !utf8.ValidString(instance) {
		return errors.New("problemdetails: instance must be valid UTF-8")
	}
	if !strings.HasPrefix(instance, "/") || strings.HasPrefix(instance, "//") {
		return errors.New("problemdetails: instance must be a path-only URI beginning with /")
	}
	if strings.ContainsAny(instance, `\?#`) {
		return errors.New("problemdetails: instance must not contain query, fragment, or backslash components")
	}
	for _, r := range instance {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return errors.New("problemdetails: instance must not contain whitespace or control characters")
		}
	}
	if _, err := url.PathUnescape(instance); err != nil {
		return errors.New("problemdetails: invalid instance path escape")
	}
	return nil
}

func ensureExt(p *Problem) map[string]any {
	if p.Extensions == nil {
		p.Extensions = make(map[string]any)
	}
	return p.Extensions
}

// mapStatus mirrors httpx.HTTPStatus without taking a circular import.
func mapStatus(err error) int {
	if ue, ok := apperror.AsUnavailable(err); ok && ue.Dependency == "" {
		return http.StatusServiceUnavailable
	}
	var ae apperror.AppError
	if !errors.As(err, &ae) {
		return http.StatusInternalServerError
	}
	switch ae.ErrorCode() {
	case apperror.CodeNotFound:
		return http.StatusNotFound
	case apperror.CodeValidation:
		return http.StatusBadRequest
	case apperror.CodeConflict:
		return http.StatusConflict
	case apperror.CodePermanent:
		return http.StatusUnprocessableEntity
	case apperror.CodeAuthRequired:
		return http.StatusUnauthorized
	case apperror.CodeRateLimit:
		return http.StatusTooManyRequests
	case apperror.CodeForbidden:
		return http.StatusForbidden
	case apperror.CodeUnavailable:
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}

func resolveType(base string, err error, status int) string {
	var ae apperror.AppError
	if errors.As(err, &ae) {
		code := string(ae.ErrorCode())
		if base == "" {
			return "about:blank"
		}
		return base + "/" + code
	}
	if base == "" {
		return "about:blank"
	}
	return base + "/" + strconv.Itoa(status)
}

// fieldErrorsExtension converts a slice of apperror.FieldError into the
// JSON-friendly shape for the RFC 7807 "errors" extension convention.
func fieldErrorsExtension(fields []apperror.FieldError) []map[string]string {
	out := make([]map[string]string, 0, len(fields))
	for _, e := range fields {
		out = append(out, map[string]string{
			"field":   e.Field,
			"message": e.Message,
		})
	}
	return out
}
