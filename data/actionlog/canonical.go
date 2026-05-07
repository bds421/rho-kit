package actionlog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// canonicalForm produces the exact byte sequence that the HMAC is
// computed over. Two entries with semantically equal field values
// produce byte-identical canonical forms — the property the verify
// step depends on.
//
// Format (length-prefixed, newline-separated):
//
//	<len(id)>:id\n
//	<len(tenant_id)>:tenant_id\n
//	<len(actor)>:actor\n
//	<len(action)>:action\n
//	<len(resource)>:resource\n
//	<len(outcome)>:outcome\n
//	<len(reason)>:reason\n
//	<len(occurred_at)>:occurred_at\n   – RFC3339Nano UTC
//	<len(metadata)>:metadata\n         – canonical JSON (see canonicalJSON)
//
// The length prefix is essential: a field value containing a literal
// newline could otherwise shift the field boundary in the canonical
// bytes, letting two distinct entries (different field assignments,
// same total bytes after a plain newline join) produce identical
// canonical strings and therefore identical signatures. With the
// length prefix the parse is unambiguous — the next field always
// starts at exactly the byte boundary the previous field's length
// dictates.
//
// SignatureKeyID is intentionally not part of the signed form. Including
// it would make it impossible to detect "same content signed by a
// different key" tampering — the verify path resolves the key id
// separately and the failure surfaces as [ErrSignatureInvalid] when the
// MAC mismatches, regardless of which key id the row carries.
//
// The signature itself is also excluded (it's the output, not the
// input).
func canonicalForm(e Entry) ([]byte, error) {
	metaJSON, err := canonicalJSON(e.Metadata)
	if err != nil {
		return nil, fmt.Errorf("actionlog: canonical metadata: %w", err)
	}
	parts := []string{
		e.ID,
		e.TenantID,
		e.Actor,
		e.Action,
		e.Resource,
		string(e.Outcome),
		e.Reason,
		e.OccurredAt.UTC().Format(time.RFC3339Nano),
		string(metaJSON),
	}
	var buf strings.Builder
	buf.Grow(canonicalEstimate(parts))
	for _, p := range parts {
		fmt.Fprintf(&buf, "%d:%s\n", len(p), p)
	}
	return []byte(buf.String()), nil
}

// canonicalEstimate returns a byte-budget estimate for the canonical
// buffer so strings.Builder.Grow can size the backing slice once.
// Conservative: 4 bytes for the length prefix + 1 colon + 1 newline +
// payload bytes.
func canonicalEstimate(parts []string) int {
	n := 0
	for _, p := range parts {
		n += len(p) + 6
	}
	return n
}

// canonicalJSON marshals v with all map keys sorted lexicographically
// at every level. encoding/json already sorts map[string]X keys, so the
// recursion below mainly handles slices-of-maps and acts as belt-and-
// braces against future stdlib changes.
//
// Nil / empty maps canonicalise to "null" so the byte sequence does
// not contain any unstable per-platform JSON quirk.
func canonicalJSON(v map[string]any) ([]byte, error) {
	if len(v) == 0 {
		return []byte("null"), nil
	}
	sorted := sortedAny(v)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// SetEscapeHTML(false) so '<' / '>' / '&' do not get escaped to
	// the < forms — those would still be deterministic, but only
	// the Go encoder produces them, and a future cross-language
	// verifier would diverge.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(sorted); err != nil {
		return nil, err
	}
	// json.Encoder appends a trailing newline; strip for stability.
	out := buf.Bytes()
	if n := len(out); n > 0 && out[n-1] == '\n' {
		out = out[:n-1]
	}
	return out, nil
}

// sortedAny walks v, returning a structurally identical value with
// every map's keys sorted. Non-map / non-slice values are returned
// as-is — JSON serialisation of primitives is already deterministic.
func sortedAny(v any) any {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		// json.Marshal sorts map[string]X keys already, but we lift to
		// a freshly-built map whose iteration order doesn't matter —
		// the stdlib encoder will sort. The sort here makes nested-
		// slice element order deterministic via the recursion.
		out := make(map[string]any, len(x))
		for _, k := range keys {
			out[k] = sortedAny(x[k])
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = sortedAny(x[i])
		}
		return out
	default:
		return v
	}
}
