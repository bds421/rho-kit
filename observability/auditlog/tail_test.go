package auditlog

import (
	"context"
	"errors"
	"testing"
)

func TestTailReadsInAppendOrderAndResumes(t *testing.T) {
	ctx := context.Background()
	l := newTestLogger(NewMemoryStore())
	for _, tenant := range []string{"a", "b", "a", "a"} {
		if err := l.LogE(ctx, Event{Actor: "u", Tenant: tenant, Action: "x", Resource: "r", Status: StatusSuccess}); err != nil {
			t.Fatal(err)
		}
	}
	first, pos, err := l.Tail(ctx, "a", 0, 2)
	if err != nil || len(first) != 2 || first[0].Tenant != "a" || first[1].Tenant != "a" {
		t.Fatalf("first batch %v, %v", first, err)
	}
	rest, end, err := l.Tail(ctx, "a", pos, 10)
	if err != nil || len(rest) != 1 || rest[0].ID == first[1].ID {
		t.Fatalf("resumed batch %v, %v", rest, err)
	}
	none, same, err := l.Tail(ctx, "a", end, 10)
	if err != nil || len(none) != 0 || same != end {
		t.Fatalf("after the end: %v at %d, %v", none, same, err)
	}
	all, _, _ := l.Tail(ctx, "", 0, 10)
	if len(all) != 4 {
		t.Fatalf("all tenants: %d events", len(all))
	}
}

func TestTailRejectsBadArguments(t *testing.T) {
	l := newTestLogger(NewMemoryStore())
	for _, tc := range []struct {
		after Position
		limit int
		want  error
	}{{0, -1, ErrLimitNegative}, {0, MaxPageLimit + 1, ErrLimitTooLarge}, {-1, 1, ErrInvalidCursor}} {
		if _, _, err := l.Tail(context.Background(), "", tc.after, tc.limit); !errors.Is(err, tc.want) {
			t.Errorf("after=%d limit=%d: %v, want %v", tc.after, tc.limit, err, tc.want)
		}
	}
}
