package promutil

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxStaticLabelValueBytes caps developer-defined label values that are
// reused over a process lifetime. Larger values are usually request material,
// identifiers, or payload-derived names rather than stable metric dimensions.
const MaxStaticLabelValueBytes = 256

// ErrInvalidLabelValue is returned when a label value is empty, oversized,
// invalid UTF-8, or contains bytes that corrupt logs and exposition formats.
var ErrInvalidLabelValue = errors.New("promutil: invalid label value")

// ErrInvalidMetricNamePart is returned when a namespace, subsystem, or other
// caller-supplied metric-name fragment cannot be safely used in a Prometheus
// metric name.
var ErrInvalidMetricNamePart = errors.New("promutil: invalid metric name part")

// OtherHTTPMethodLabel is the bounded fallback for non-standard HTTP methods
// in Prometheus labels. Keeping method labels to a small fixed set prevents
// arbitrary request methods from creating unbounded time series.
const OtherHTTPMethodLabel = "OTHER"

var metricNamePartPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateStaticLabelValue checks a bounded, developer-defined Prometheus label
// value before it is stored in long-lived metrics. This does not prove the
// value is low-cardinality; callers still need to use it only for static
// dimensions such as job, worker, event, or route template names.
func ValidateStaticLabelValue(field, value string) error {
	if field == "" {
		field = "value"
	}
	if value == "" {
		return fmt.Errorf("%w: %s is empty", ErrInvalidLabelValue, field)
	}
	if len(value) > MaxStaticLabelValueBytes {
		return fmt.Errorf("%w: %s exceeds maximum length", ErrInvalidLabelValue, field)
	}
	if containsInvalidLabelRune(value) {
		return fmt.Errorf("%w: %s contains invalid data", ErrInvalidLabelValue, field)
	}
	return nil
}

func containsInvalidLabelRune(value string) bool {
	if !utf8.ValidString(value) {
		return true
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

// ValidateMetricNamePart checks a caller-provided Prometheus metric namespace,
// subsystem, or similar name fragment. Empty values are allowed because
// Prometheus namespace/subsystem fields are optional; non-empty values are kept
// to a conservative identifier subset so composed metric names are valid and
// stable.
func ValidateMetricNamePart(field, value string) error {
	if field == "" {
		field = "metric name part"
	}
	if value == "" {
		return nil
	}
	if !utf8.ValidString(value) || !metricNamePartPattern.MatchString(value) {
		return fmt.Errorf("%w: %s must match %s", ErrInvalidMetricNamePart, field, metricNamePartPattern.String())
	}
	return nil
}

// HTTPMethodLabel returns a bounded Prometheus label value for an HTTP method.
// Standard methods are kept as-is; extension or malformed methods are bucketed
// into [OtherHTTPMethodLabel] instead of becoming new time series.
func HTTPMethodLabel(method string) string {
	switch method {
	case http.MethodGet,
		http.MethodHead,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
		http.MethodConnect,
		http.MethodOptions,
		http.MethodTrace:
		return method
	default:
		return OtherHTTPMethodLabel
	}
}

// OpaqueLabelValue returns a bounded Prometheus label value with a visible
// static prefix and, when opaqueParts are supplied, a deterministic hash suffix.
// Use this for labels derived from queue names, stream names, hosts, bucket
// names, or other topology-bearing identifiers that should stay distinct
// without being copied into metrics.
//
// The suffix is deterministic, so it preserves equality and can be brute-forced
// for small candidate sets. Do not pass secrets whose equality or membership in
// a small candidate set must remain hidden.
func OpaqueLabelValue(prefix string, opaqueParts ...string) string {
	visible := normalizeOpaqueLabelPrefix(prefix)
	if len(opaqueParts) == 0 {
		if len(visible) <= MaxStaticLabelValueBytes {
			return visible
		}
		return truncateOpaqueLabelValue(visible, []string{prefix})
	}
	rawParts := append([]string{prefix}, opaqueParts...)
	return truncateOpaqueLabelValue(visible, rawParts)
}

func truncateOpaqueLabelValue(visible string, rawParts []string) string {
	sum := sha256.Sum256([]byte(strings.Join(rawParts, "\x00")))
	suffix := hex.EncodeToString(sum[:6])
	keep := MaxStaticLabelValueBytes - len(suffix) - 1
	if keep < 1 {
		keep = 1
	}
	if len(visible) > keep {
		visible = strings.TrimRight(visible[:keep], "-_")
	}
	if visible == "" {
		visible = "value"
	}
	return visible + "-" + suffix
}

func normalizeOpaqueLabelPrefix(prefix string) string {
	var out []byte
	lastSep := true
	for _, r := range prefix {
		c, ok := opaqueLabelPrefixByte(r)
		if ok {
			out = append(out, c)
			lastSep = false
			continue
		}
		if !lastSep {
			out = append(out, '-')
			lastSep = true
		}
	}
	name := strings.Trim(string(out), "-_")
	if name == "" {
		return "value"
	}
	return name
}

func opaqueLabelPrefixByte(r rune) (byte, bool) {
	switch {
	case r >= 'a' && r <= 'z':
		return byte(r), true
	case r >= 'A' && r <= 'Z':
		return byte(r + ('a' - 'A')), true
	case r >= '0' && r <= '9':
		return byte(r), true
	case r == '-' || r == '_' || r == '.':
		return byte(r), true
	default:
		return 0, false
	}
}
