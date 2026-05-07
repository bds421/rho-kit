package pagination

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// WriteLinkHeader writes an RFC 5988 [Link] header on w with first/prev/next/last
// relations for offset-based pagination.
//
// Parameters:
//   - u is the canonical URL of the current page (typically r.URL with the
//     scheme/host filled in by the caller). Its existing limit/offset query
//     params are replaced; other query params are preserved.
//   - total is the total number of items across all pages. When total is
//     unknown (a streaming list), pass a negative value and only "next" is
//     emitted, conditioned on the current page being full.
//   - offset and limit describe the current page.
//
// Skipped relations:
//   - "next" is omitted when the next page would start at or past total.
//   - "prev" is omitted at offset 0.
//   - "first"/"last" are omitted when total is unknown (negative).
//
// The header is set verbatim; callers handing back a streaming response
// should call this before the first WriteHeader to avoid the ResponseWriter
// dropping the header.
//
// [Link]: https://datatracker.ietf.org/doc/html/rfc5988
func WriteLinkHeader(w http.ResponseWriter, u *url.URL, total, offset, limit int) {
	if w == nil || u == nil || limit <= 0 {
		return
	}
	if offset < 0 {
		offset = 0
	}

	parts := make([]string, 0, 4)

	if total < 0 {
		// Unknown total: only emit next, and only when this page is full.
		// The "page is full" heuristic is the standard one — without total
		// we cannot tell that the next page will be empty, but most
		// streaming list APIs document this caveat.
		parts = appendLink(parts, u, "next", offset+limit, limit)
	} else {
		// Known total: first/prev/next/last as appropriate.
		if offset > 0 {
			parts = appendLink(parts, u, "first", 0, limit)
			prev := offset - limit
			if prev < 0 {
				prev = 0
			}
			parts = appendLink(parts, u, "prev", prev, limit)
		}
		if offset+limit < total {
			parts = appendLink(parts, u, "next", offset+limit, limit)
			lastOffset := lastPageOffset(total, limit)
			parts = appendLink(parts, u, "last", lastOffset, limit)
		}
	}

	if len(parts) == 0 {
		return
	}
	w.Header().Set("Link", strings.Join(parts, ", "))
}

func appendLink(parts []string, u *url.URL, rel string, offset, limit int) []string {
	cp := *u
	q := cp.Query()
	q.Set("limit", strconv.Itoa(limit))
	q.Set("offset", strconv.Itoa(offset))
	cp.RawQuery = q.Encode()
	parts = append(parts, fmt.Sprintf(`<%s>; rel=%q`, cp.String(), rel))
	return parts
}

// lastPageOffset returns the offset of the first item on the last page —
// the largest k*limit ≤ total-1 (or 0 when total ≤ limit).
func lastPageOffset(total, limit int) int {
	if total <= limit {
		return 0
	}
	rem := (total - 1) % limit
	return total - 1 - rem
}
