// Package acceptencoding chooses a content coding from a request's
// Accept-Encoding header (RFC 9110 §12.5.3).
package acceptencoding

import (
	"strconv"
	"strings"
)

// Select returns the coding from offered that the Accept-Encoding value
// accepts with the highest weight, or "" when the client accepts none of them
// (identity). Among codings of equal weight the order of offered decides, so
// the server lists its preferred coding first: browsers send
// "gzip, deflate, br" and still get br. A coding refused with q=0 is never
// chosen, also not through the "*" wildcard.
func Select(acceptEncoding string, offered []string) string {
	weights := make(map[string]float64)
	for _, raw := range strings.Split(acceptEncoding, ",") {
		token, q := parseEntry(raw)
		if token == "" {
			continue
		}
		// Negative q is non-conformant and refuses like q=0.
		weights[token] = max(q, 0)
	}
	best, bestQ := "", 0.0
	for _, coding := range offered {
		q, named := weights[strings.ToLower(coding)]
		if !named {
			q = weights["*"]
		}
		if q > bestQ {
			best, bestQ = coding, q
		}
	}
	return best
}

func parseEntry(raw string) (string, float64) {
	parts := strings.Split(strings.TrimSpace(raw), ";")
	token := strings.ToLower(strings.TrimSpace(parts[0]))
	if token == "" {
		return "", 0
	}
	q := 1.0
	for _, p := range parts[1:] {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, "q=") {
			parsed, err := strconv.ParseFloat(strings.TrimPrefix(p, "q="), 64)
			if err == nil {
				q = parsed
			}
		}
	}
	return token, q
}
