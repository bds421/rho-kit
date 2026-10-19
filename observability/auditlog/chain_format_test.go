package auditlog

import (
	"bytes"
	"testing"
	"time"
)

func TestVersionedEncodingCannotCollideWithLegacy(t *testing.T) {
	base := Event{ID: "e1", Timestamp: time.Unix(1, 0), Actor: "a", Action: "x", Resource: "r", Status: StatusSuccess}

	legacy := base
	legacy.Tenant = string([]byte{FormatExactMetadata})
	versioned := base
	versioned.Format = FormatExactMetadata

	if bytes.Equal(canonicalEvent(legacy), canonicalEvent(versioned)) {
		t.Fatal("legacy event with a one-byte tenant encodes like a versioned event")
	}
	if !bytes.HasPrefix(canonicalEvent(versioned), formatHeader) {
		t.Fatal("versioned encoding does not start with the format header")
	}
}
