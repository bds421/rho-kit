// Package pagination provides cursor-based and offset-based pagination
// helpers.
//
// # When to use which
//
// Use cursor pagination ([CursorParams], [BuildResult], [HandleCursorList])
// for hot, high-cardinality lists where the underlying ordering is stable
// and clients consume sequentially. Cursors avoid the O(offset) cost of
// classical offset pagination and are stable under concurrent writes.
//
// Use offset pagination ([ParseOffset], [WriteLinkHeader]) for admin and UI
// tables where the user expects "page 5 of 12" semantics, total counts, and
// jump-to-last. RFC 5988 Link headers (first/prev/next/last) plug straight
// into every front-end paginator and kubectl-style CLI.
package pagination
