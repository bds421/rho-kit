package auditlog

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
)

// MaxSearchBytes caps [Filter.Search]. Longer terms are rejected by
// [Logger.List] with [ErrSearchTooLong].
const MaxSearchBytes = 256

// ErrSearchTooLong is returned by [Logger.List] when [Filter.Search]
// exceeds [MaxSearchBytes].
var ErrSearchTooLong = errors.New("auditlog: search exceeds MaxSearchBytes")

// SearchText is the text [Filter.Search] matches for an event: actor,
// action, resource and every string and number value in the metadata
// (not its keys), one per line. Stores that persist events compute it at
// append time so the match is served by an index; the bundled
// [MemoryStore] computes it per query.
func SearchText(e Event) string {
	parts := []string{e.Actor, e.Action, e.Resource}
	parts = appendMetadataValues(parts, e.Metadata)
	text := strings.Join(parts, "\n")
	// PostgreSQL text cannot hold NUL; it never helps a search either.
	return strings.ReplaceAll(strings.ToValidUTF8(text, "�"), "\x00", "")
}

// MatchesSearch reports whether the event matches a [Filter.Search] term:
// a case-insensitive substring of [SearchText]. An empty term matches
// every event.
func MatchesSearch(e Event, search string) bool {
	if search == "" {
		return true
	}
	return strings.Contains(strings.ToLower(SearchText(e)), strings.ToLower(search))
}

// SearchTextConsistent reports whether stored, a store's persisted search
// text, holds the same lines as [SearchText] for the event. Order and
// number spelling may differ (a SQL backfill walks JSONB in its own key
// order and prints numbers its own way); a missing, added or altered value
// does not. Stores that persist search text check it while streaming the
// chain, so a row edited to hide from search fails [Logger.VerifyChain].
func SearchTextConsistent(e Event, stored string) bool {
	want := searchLines(SearchText(e))
	got := searchLines(stored)
	if len(want) != len(got) {
		return false
	}
	for line, n := range want {
		if got[line] != n {
			return false
		}
	}
	return true
}

func searchLines(text string) map[string]int {
	lines := map[string]int{}
	for line := range strings.SplitSeq(text, "\n") {
		if line == "" {
			continue
		}
		if f, err := strconv.ParseFloat(line, 64); err == nil {
			line = strconv.FormatFloat(f, 'g', -1, 64)
		}
		lines[line]++
	}
	return lines
}

func appendMetadataValues(parts []string, metadata json.RawMessage) []string {
	if len(metadata) == 0 {
		return parts
	}
	dec := json.NewDecoder(bytes.NewReader(metadata))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return parts
	}
	return appendLeaves(parts, doc)
}

func appendLeaves(parts []string, v any) []string {
	switch t := v.(type) {
	case string:
		if t != "" {
			parts = append(parts, t)
		}
	case json.Number:
		parts = append(parts, t.String())
	case []any:
		for _, item := range t {
			parts = appendLeaves(parts, item)
		}
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			parts = appendLeaves(parts, t[k])
		}
	}
	return parts
}
