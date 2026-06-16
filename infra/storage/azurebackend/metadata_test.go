package azurebackend

import (
	"testing"
)

func TestToAzureMetadata_NilAndEmptyReturnNil(t *testing.T) {
	t.Parallel()

	if got := toAzureMetadata(nil); got != nil {
		t.Fatalf("nil input must produce nil output, got %v", got)
	}
	if got := toAzureMetadata(map[string]string{}); got != nil {
		t.Fatalf("empty input must produce nil output (omit x-ms-meta-* headers), got %v", got)
	}
}

func TestToAzureMetadata_CopiesEntriesWithDistinctPointers(t *testing.T) {
	t.Parallel()

	in := map[string]string{"author": "alice", "Tenant-ID": "acme"}
	out := toAzureMetadata(in)

	if len(out) != len(in) {
		t.Fatalf("entry count mismatch: want %d, got %d", len(in), len(out))
	}
	for k, want := range in {
		ptr, ok := out[k]
		if !ok {
			t.Fatalf("missing key %q in converted metadata", k)
		}
		if ptr == nil {
			t.Fatalf("value pointer for key %q must not be nil", k)
		}
		if *ptr != want {
			t.Fatalf("value mismatch for %q: want %q, got %q", k, want, *ptr)
		}
	}

	// Each value must point at a distinct backing string; a naive loop that
	// takes &v of the range variable would alias every entry to the last
	// value. Verify the pointers are independent.
	if out["author"] == out["Tenant-ID"] {
		t.Fatal("metadata value pointers must be distinct, not aliased")
	}
	if *out["author"] != "alice" || *out["Tenant-ID"] != "acme" {
		t.Fatalf("aliased values detected: author=%q tenant=%q", *out["author"], *out["Tenant-ID"])
	}
}

func TestFromAzureMetadata_NilAndEmptyReturnNil(t *testing.T) {
	t.Parallel()

	if got := fromAzureMetadata(nil); got != nil {
		t.Fatalf("nil input must produce nil output, got %v", got)
	}
	if got := fromAzureMetadata(map[string]*string{}); got != nil {
		t.Fatalf("empty input must produce nil output, got %v", got)
	}
}

func TestFromAzureMetadata_DropsNilValuePointers(t *testing.T) {
	t.Parallel()

	present := "v"
	in := map[string]*string{
		"keep": &present,
		"drop": nil, // Azure SDK can yield a key with a nil *string.
	}

	out := fromAzureMetadata(in)

	if _, ok := out["drop"]; ok {
		t.Fatal("nil value pointer must be dropped, not surfaced as empty string")
	}
	if got := out["keep"]; got != "v" {
		t.Fatalf("present value lost: want %q, got %q", "v", got)
	}
}

// TestAzureMetadata_RoundTripPreservesValuesAndKeys documents the converter
// contract: values survive a to/from round trip unchanged, and key casing is
// preserved verbatim (the helpers do not normalize case; case stability across
// a real Azure round trip depends on the service / SDK header canonicalization,
// which these in-process helpers neither emulate nor alter).
func TestAzureMetadata_RoundTripPreservesValuesAndKeys(t *testing.T) {
	t.Parallel()

	in := map[string]string{
		"author":    "alice",
		"Tenant-ID": "acme",
		"x-trace":   "abc123",
	}

	out := fromAzureMetadata(toAzureMetadata(in))

	if len(out) != len(in) {
		t.Fatalf("round-trip entry count mismatch: want %d, got %d", len(in), len(out))
	}
	for k, want := range in {
		got, ok := out[k]
		if !ok {
			t.Fatalf("round trip lost key %q (case preserved verbatim by the helpers)", k)
		}
		if got != want {
			t.Fatalf("round trip altered value for %q: want %q, got %q", k, want, got)
		}
	}
}
