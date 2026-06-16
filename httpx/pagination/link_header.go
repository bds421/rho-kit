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
//     emitted. Because this function has no item-count input it cannot
//     detect the final page, so "next" is always emitted (except on
//     integer overflow of the next offset); clients must stop following
//     it once a page returns fewer items than limit.
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
		// Unknown total: emit next unconditionally. Without total (and
		// with no item-count input) we cannot tell whether the next page
		// will be empty, so next is always emitted unless the next offset
		// would overflow. Clients must stop following next once a page
		// returns fewer items than limit.
		if next, ok := nextPageOffset(offset, limit); ok {
			parts = appendLink(parts, u, "next", next, limit)
		}
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
		if next, ok := nextPageOffset(offset, limit); ok && next < total {
			parts = appendLink(parts, u, "next", next, limit)
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

func nextPageOffset(offset, limit int) (int, bool) {
	if limit <= 0 {
		return 0, false
	}
	maxInt := int(^uint(0) >> 1)
	if offset > maxInt-limit {
		return 0, false
	}
	return offset + limit, true
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
