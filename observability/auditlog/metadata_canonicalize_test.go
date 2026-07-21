package auditlog

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLogE_MetadataCanonicalizedForChainVerify is the regression pin
// for review-23: metadata signed with object key order/whitespace must
// still VerifyChain after JSON re-encoding (Postgres JSONB normalizes
// key order and spacing). LogE canonicalizes before HMAC.
func TestLogE_MetadataCanonicalizedForChainVerify(t *testing.T) {
	// Unordered keys + insignificant whitespace — JSONB would re-order.
	raw := json.RawMessage(`{ "b" : 1, "a" : 2 }`)
	store := NewMemoryStore()
	l := newTestLogger(store)

	require.NoError(t, l.LogE(context.Background(), Event{
		Actor:    "user-1",
		Action:   "write",
		Resource: "obj",
		Status:   "success",
		Metadata: raw,
	}))

	events := store.Events()
	require.Len(t, events, 1)

	// Signed metadata must be compact canonical form (sorted keys).
	assert.JSONEq(t, `{"a":2,"b":1}`, string(events[0].Metadata))
	// Simulate JSONB re-read: re-marshal via map.
	var v any
	require.NoError(t, json.Unmarshal(events[0].Metadata, &v))
	rewritten, err := json.Marshal(v)
	require.NoError(t, err)
	events[0].Metadata = rewritten

	require.NoError(t, VerifyChain(events, testChainKey),
		"chain must verify after metadata JSON re-encode (JSONB round-trip)")
}
