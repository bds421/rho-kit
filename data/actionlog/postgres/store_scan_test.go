package postgres

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/v2/actionlog"
)

// fakeRow implements scannable by copying a fixed set of column values
// into the destination pointers that scanEntry passes to Scan, mimicking
// what pgx writes back for a single action_log_entries row. The metadata
// column carries the exact JSON bytes that insertEntry would have stored
// (json.Marshal of the original Metadata map), so a round-trip through
// scanEntry must reproduce byte-identical canonical JSON for the signature
// to keep verifying.
type fakeRow struct {
	id, tenantID, actor, action, resource, outcome, reason string
	metaRaw                                                []byte
	occurredAt                                             time.Time
	signatureKeyID, prevHash, signature                    string
	seq                                                    int64
}

func (r fakeRow) Scan(dest ...any) error {
	vals := []any{
		r.id, r.tenantID, r.actor, r.action, r.resource, r.outcome, r.reason,
		r.metaRaw, r.occurredAt, r.signatureKeyID, r.seq, r.prevHash, r.signature,
	}
	for i, d := range dest {
		switch p := d.(type) {
		case *string:
			*p = vals[i].(string)
		case *[]byte:
			*p = vals[i].([]byte)
		case *time.Time:
			*p = vals[i].(time.Time)
		case *int64:
			*p = vals[i].(int64)
		default:
			// scanEntry passes only the pointer kinds handled above.
		}
	}
	return nil
}

// TestScanEntry_PreservesLargeIntegerMetadata pins the JSONB number
// round-trip: validMetadata accepts int64/uint64 of any magnitude and the
// signature is computed over their exact decimal form, so on read the
// metadata must re-marshal to the same bytes that were stored. Plain
// json.Unmarshal decodes every number as float64, which silently rounds
// integers above 2^53 (Snowflake IDs, unix-nano timestamps) and breaks
// signature verification permanently for that row.
func TestScanEntry_PreservesLargeIntegerMetadata(t *testing.T) {
	cases := []struct {
		name string
		meta map[string]any
	}{
		{
			name: "snowflake id int64 above 2^53",
			meta: map[string]any{"snowflake": int64(1234567890123456789)},
		},
		{
			name: "unix nano timestamp int64",
			meta: map[string]any{"unix_nano": int64(1718000000000000000)},
		},
		{
			name: "uint64 above 2^53",
			meta: map[string]any{"big": uint64(18446744073709551615)},
		},
		{
			name: "nested array of large ids",
			meta: map[string]any{"ids": []any{int64(9007199254740993), int64(9007199254740995)}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// stored is the metadata JSON exactly as insertEntry persists it.
			stored, err := json.Marshal(tc.meta)
			require.NoError(t, err)

			row := fakeRow{
				id:             "11111111-1111-1111-1111-111111111111",
				tenantID:       "tenant",
				actor:          "actor",
				action:         "action",
				resource:       "resource",
				outcome:        string(actionlog.OutcomeSuccess),
				reason:         "",
				metaRaw:        stored,
				occurredAt:     time.Unix(0, 0).UTC(),
				signatureKeyID: "key-1",
				seq:            1,
				prevHash:       "0000000000000000000000000000000000000000000000000000000000000000",
				signature:      "deadbeef",
			}

			entry, err := scanEntry(row)
			require.NoError(t, err)

			// Re-marshalling the scanned metadata must reproduce the stored
			// bytes; any divergence here is exactly the divergence that makes
			// the recomputed canonical form mismatch the signed form.
			got, err := json.Marshal(entry.Metadata)
			require.NoError(t, err)
			assert.Equal(t, string(stored), string(got),
				"scanned metadata must re-marshal to the stored JSON byte-for-byte")
		})
	}
}
