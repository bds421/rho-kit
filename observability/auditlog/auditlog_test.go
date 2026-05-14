package auditlog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/secret"
)

// testChainKey and testCursorKey are fixed 32-byte test keys. Real
// deployments source these from KMS / secret config; using deterministic
// bytes here keeps the tests' chain HMACs reproducible across runs.
var (
	testChainKey  = bytes.Repeat([]byte("c"), MinChainKeyLen)
	testCursorKey = bytes.Repeat([]byte("u"), MinCursorKeyLen)
)

// newTestLogger constructs a Logger with deterministic test keys.
// Tests that don't care about which keys are used should call this; tests
// that exercise key-required panics or key-mismatch paths build their own
// logger explicitly.
func newTestLogger(store Store, opts ...Option) *Logger {
	opts = append([]Option{
		WithChainKey(testChainKey),
		WithCursorKey(testCursorKey),
	}, opts...)
	return New(store, opts...)
}

func TestLogger_Log_AutoPopulates(t *testing.T) {
	store := NewMemoryStore()
	l := newTestLogger(store)

	l.Log(context.Background(), Event{
		Actor:    "user-1",
		Action:   "create",
		Resource: "orders/123",
		Status:   "success",
	})

	events := store.Events()
	require.Len(t, events, 1)
	assert.NotEmpty(t, events[0].ID, "ID should be auto-generated")
	assert.False(t, events[0].Timestamp.IsZero(), "Timestamp should be auto-set")
	assert.Equal(t, "user-1", events[0].Actor)
	assert.Equal(t, "create", events[0].Action)
	assert.Equal(t, "orders/123", events[0].Resource)
	assert.Equal(t, "success", events[0].Status)
}

func TestLogger_LogAction(t *testing.T) {
	store := NewMemoryStore()
	l := newTestLogger(store)

	l.LogAction(context.Background(), "admin", "delete", "users/456", "success")

	events := store.Events()
	require.Len(t, events, 1)
	assert.Equal(t, "admin", events[0].Actor)
	assert.Equal(t, "delete", events[0].Action)
	assert.Equal(t, "users/456", events[0].Resource)
}

func TestLogger_Log_PreservesExplicitFields(t *testing.T) {
	store := NewMemoryStore()
	l := newTestLogger(store)

	now := time.Now()
	l.Log(context.Background(), Event{
		ID:        "custom-id",
		Timestamp: now,
		Actor:     "svc",
		Action:    "sync",
		Resource:  "data",
		Status:    "success",
		TraceID:   "0123456789abcdef0123456789abcdef",
	})

	events := store.Events()
	require.Len(t, events, 1)
	assert.Equal(t, "custom-id", events[0].ID)
	assert.Equal(t, now, events[0].Timestamp)
	assert.Equal(t, "0123456789abcdef0123456789abcdef", events[0].TraceID)
}

func TestQuery_FilterByActor(t *testing.T) {
	store := NewMemoryStore()
	l := newTestLogger(store)

	l.LogAction(context.Background(), "alice", "create", "r/1", "success")
	l.LogAction(context.Background(), "bob", "create", "r/2", "success")
	l.LogAction(context.Background(), "alice", "update", "r/1", "success")

	events, _, err := l.List(context.Background(), Filter{Actor: "alice"}, "", 10)
	require.NoError(t, err)
	assert.Len(t, events, 2)
}

func TestQuery_FilterByAction(t *testing.T) {
	store := NewMemoryStore()
	l := newTestLogger(store)

	l.LogAction(context.Background(), "a", "create", "r/1", "success")
	l.LogAction(context.Background(), "a", "delete", "r/2", "success")

	events, _, err := l.List(context.Background(), Filter{Action: "delete"}, "", 10)
	require.NoError(t, err)
	assert.Len(t, events, 1)
	assert.Equal(t, "delete", events[0].Action)
}

func TestQuery_FilterByResource(t *testing.T) {
	store := NewMemoryStore()
	l := newTestLogger(store)

	l.LogAction(context.Background(), "a", "x", "orders/1", "success")
	l.LogAction(context.Background(), "a", "x", "orders/2", "success")
	l.LogAction(context.Background(), "a", "x", "users/1", "success")

	events, _, err := l.List(context.Background(), Filter{Resource: "orders"}, "", 10)
	require.NoError(t, err)
	assert.Len(t, events, 2)
}

func TestQuery_FilterByTime(t *testing.T) {
	store := NewMemoryStore()
	l := newTestLogger(store)

	past := time.Now().Add(-2 * time.Hour)
	recent := time.Now()

	l.Log(context.Background(), Event{Actor: "a", Action: "x", Resource: "r", Status: "success", Timestamp: past})
	l.Log(context.Background(), Event{Actor: "a", Action: "x", Resource: "r", Status: "success", Timestamp: recent})

	events, _, err := l.List(context.Background(), Filter{Since: time.Now().Add(-1 * time.Hour)}, "", 10)
	require.NoError(t, err)
	assert.Len(t, events, 1)
}

func TestQuery_Pagination(t *testing.T) {
	store := NewMemoryStore()
	l := newTestLogger(store)

	for i := range 5 {
		l.LogAction(context.Background(), "a", "x", "r/"+string(rune('a'+i)), "success")
	}

	// Page 1: 2 events.
	page1, cursor1, err := l.List(context.Background(), Filter{}, "", 2)
	require.NoError(t, err)
	assert.Len(t, page1, 2)
	assert.NotEmpty(t, cursor1)

	// Page 2: 2 events.
	page2, cursor2, err := l.List(context.Background(), Filter{}, cursor1, 2)
	require.NoError(t, err)
	assert.Len(t, page2, 2)
	assert.NotEmpty(t, cursor2)

	// Page 3: 1 event (last page).
	page3, cursor3, err := l.List(context.Background(), Filter{}, cursor2, 2)
	require.NoError(t, err)
	assert.Len(t, page3, 1)
	assert.Empty(t, cursor3)
}

func TestMemoryStore_Reset(t *testing.T) {
	store := NewMemoryStore()
	l := newTestLogger(store)

	l.LogAction(context.Background(), "a", "x", "r", "success")
	assert.Len(t, store.Events(), 1)

	store.Reset()
	assert.Empty(t, store.Events())
}

func TestNew_PanicsOnNilStore(t *testing.T) {
	assert.Panics(t, func() {
		New(nil, WithChainKey(testChainKey), WithCursorKey(testCursorKey))
	})
}

func TestNew_PanicsOnNilOption(t *testing.T) {
	store := NewMemoryStore()
	assert.Panics(t, func() {
		New(store, WithChainKey(testChainKey), WithCursorKey(testCursorKey), nil)
	})
}

// TestAppend_RequiresChainKey verifies that New fails fast when no chain
// key is configured. Without this check the audit log would silently ship
// without tamper-evidence — the very property §5.4 of the threat model
// claims is provided.
func TestAppend_RequiresChainKey(t *testing.T) {
	store := NewMemoryStore()
	assert.Panics(t, func() {
		// Cursor key present but chain key missing.
		New(store, WithCursorKey(testCursorKey))
	})
	// A too-short chain key must also panic so operators cannot silently
	// downgrade chain security by mis-sizing the secret.
	assert.Panics(t, func() {
		New(store, WithChainKey([]byte("short")), WithCursorKey(testCursorKey))
	})
}

// TestQuery_RequiresCursorKey verifies that New fails fast when no cursor
// key is configured. Without it, Query would either expose raw cursors
// (forgeable) or return broken pagination — both compliance hazards.
func TestQuery_RequiresCursorKey(t *testing.T) {
	store := NewMemoryStore()
	assert.Panics(t, func() {
		// Chain key present but cursor key missing.
		New(store, WithChainKey(testChainKey))
	})
	assert.Panics(t, func() {
		New(store, WithChainKey(testChainKey), WithCursorKey([]byte("short")))
	})
}

func TestValidateEventRejectsInvalidFields(t *testing.T) {
	valid := Event{
		ID:        "1",
		Timestamp: time.Now(),
		Actor:     "alice",
		Action:    "login",
		Resource:  "sessions",
		Status:    "success",
		IPAddress: "10.0.0.1",
		TraceID:   "0123456789abcdef0123456789abcdef",
		Metadata:  json.RawMessage(`{"ok":true}`),
	}
	require.NoError(t, ValidateEvent(valid))

	for name, mutate := range map[string]func(*Event){
		"zero timestamp": func(e *Event) { e.Timestamp = time.Time{} },
		"empty id":       func(e *Event) { e.ID = "" },
		"id too long":    func(e *Event) { e.ID = strings.Repeat("i", MaxEventIDBytes+1) },
		"actor newline":  func(e *Event) { e.Actor = "alice\nbob" },
		"empty action":   func(e *Event) { e.Action = "" },
		"resource space": func(e *Event) { e.Resource = "orders 1" },
		"bad status":     func(e *Event) { e.Status = "ok" },
		"bad ip":         func(e *Event) { e.IPAddress = "not an ip" },
		"bad trace":      func(e *Event) { e.TraceID = strings.Repeat("A", MaxTraceIDBytes) },
		"bad metadata":   func(e *Event) { e.Metadata = json.RawMessage(`{"broken"`) },
		"large metadata": func(e *Event) { e.Metadata = json.RawMessage(`"` + strings.Repeat("x", MaxMetadataBytes) + `"`) },
	} {
		t.Run(name, func(t *testing.T) {
			event := valid
			mutate(&event)
			err := ValidateEvent(event)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidEvent)
			switch name {
			case "id too long":
				assert.NotContains(t, err.Error(), "36")
				assert.NotContains(t, err.Error(), "37")
			case "large metadata":
				assert.NotContains(t, err.Error(), "65536")
				assert.NotContains(t, err.Error(), "65537")
			}
		})
	}
}

func TestLogger_LogERejectsInvalidEventBeforeStore(t *testing.T) {
	store := &captureStore{}
	l := newTestLogger(store)

	err := l.LogE(context.Background(), Event{
		Actor:    "alice smith",
		Action:   "login",
		Resource: "sessions",
		Status:   "success",
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidEvent)
	assert.Empty(t, store.event.ID)
	assert.Equal(t, uint64(1), l.DroppedCount())
}

// failingStore returns a fixed error from Append and Query. LastHMAC always
// returns nil (i.e. "empty chain") so the test can independently exercise
// the Append-error path without the prev-HMAC read also failing.
type failingStore struct {
	err error
}

func (s failingStore) Append(context.Context, Event) error {
	return s.err
}

func (s failingStore) AppendChained(_ context.Context, build func(prev []byte) (Event, error)) error {
	// Allow build to run (so the Logger exercises its HMAC path) and
	// then surface the store error as if the persist step failed.
	if _, err := build(nil); err != nil {
		return err
	}
	return s.err
}

func (s failingStore) Query(context.Context, Filter, string, int) ([]Event, string, error) {
	return nil, "", s.err
}

func (s failingStore) RangeChain(context.Context, func(Event) error) error {
	return s.err
}

func (s failingStore) LastHMAC(context.Context) ([]byte, error) {
	return nil, nil
}

// hmacFailingStore returns an error from AppendChained without invoking
// build. Used to exercise the branch where the Logger cannot read the
// chain tail and must drop.
type hmacFailingStore struct {
	failingStore
	hmacErr error
}

func (s hmacFailingStore) AppendChained(context.Context, func(prev []byte) (Event, error)) error {
	return s.hmacErr
}

func (s hmacFailingStore) LastHMAC(context.Context) ([]byte, error) {
	return nil, s.hmacErr
}

func TestLogger_LogEReturnsIDGenerationError(t *testing.T) {
	idErr := errors.New("rng unavailable")
	prev := newAuditID
	newAuditID = func() (uuid.UUID, error) { return uuid.Nil, idErr }
	t.Cleanup(func() { newAuditID = prev })

	l := newTestLogger(NewMemoryStore())
	err := l.LogE(context.Background(), Event{
		Actor:    "a",
		Action:   "x",
		Resource: "r",
		Status:   "failure",
	})

	require.ErrorIs(t, err, idErr)
	assert.Equal(t, uint64(1), l.DroppedCount())
}

func TestLogger_LogE_PrevHMACReadFailureCountsAsDrop(t *testing.T) {
	hmacErr := errors.New("tail read failed")
	l := newTestLogger(hmacFailingStore{hmacErr: hmacErr})

	err := l.LogE(context.Background(), Event{
		Actor:    "alice",
		Action:   "login",
		Resource: "sessions",
		Status:   "success",
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, hmacErr)
	assert.Equal(t, uint64(1), l.DroppedCount())
}

// TestLogger_LogE_ConcurrentAppendDoesNotForkChain pins the invariant
// that drove the AppendChained refactor: the Store, not the Logger,
// holds the lock across the read-tail / compute-HMAC / persist
// sequence. Two concurrent LogE calls must still observe distinct
// PrevHMACs and produce a strictly-monotonic chain.
func TestLogger_LogE_ConcurrentAppendDoesNotForkChain(t *testing.T) {
	store := NewMemoryStore()
	l := newTestLogger(store)
	ctx := context.Background()

	const writers = 8
	const perWriter = 20
	var wg sync.WaitGroup
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				_ = l.LogE(ctx, Event{
					Actor:    fmt.Sprintf("worker-%d", id),
					Action:   "ping",
					Resource: "r",
					Status:   StatusSuccess,
				})
			}
		}(w)
	}
	wg.Wait()

	events := store.Events()
	require.Len(t, events, writers*perWriter)
	if err := VerifyChain(events, testChainKey); err != nil {
		t.Fatalf("chain verification failed under concurrent append: %v", err)
	}
}

type captureStore struct {
	mu     sync.Mutex
	event  Event
	events []Event
}

func (s *captureStore) Append(_ context.Context, event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.event = event
	s.events = append(s.events, cloneEvent(event))
	return nil
}

func (s *captureStore) AppendChained(_ context.Context, build func(prev []byte) (Event, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var prev []byte
	if len(s.events) > 0 {
		tail := s.events[len(s.events)-1].HMAC
		if len(tail) > 0 {
			prev = append([]byte(nil), tail...)
		}
	}
	event, err := build(prev)
	if err != nil {
		return err
	}
	s.event = event
	s.events = append(s.events, cloneEvent(event))
	return nil
}

func (s *captureStore) Query(_ context.Context, _ Filter, _ string, _ int) ([]Event, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) == 0 {
		// Preserve legacy single-event behaviour for tests that set
		// .event directly without using Append.
		return []Event{s.event}, "", nil
	}
	out := make([]Event, len(s.events))
	for i := range s.events {
		out[i] = cloneEvent(s.events[len(s.events)-1-i])
	}
	return out, "", nil
}

func (s *captureStore) LastHMAC(context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) == 0 {
		return nil, nil
	}
	tail := s.events[len(s.events)-1].HMAC
	if len(tail) == 0 {
		return nil, nil
	}
	return append([]byte(nil), tail...), nil
}

func (s *captureStore) RangeChain(_ context.Context, fn func(Event) error) error {
	s.mu.Lock()
	snap := make([]Event, len(s.events))
	for i, e := range s.events {
		snap[i] = cloneEvent(e)
	}
	s.mu.Unlock()
	for _, e := range snap {
		if err := fn(e); err != nil {
			return err
		}
	}
	return nil
}

func TestLogger_LogEClonesMetadataBeforeStore(t *testing.T) {
	store := &captureStore{}
	l := newTestLogger(store)
	metadata := json.RawMessage(`{"token":"original"}`)

	require.NoError(t, l.LogE(context.Background(), Event{
		Actor:    "alice",
		Action:   "login",
		Resource: "sessions",
		Status:   "success",
		Metadata: metadata,
	}))
	metadata[10] = 'X'

	assert.JSONEq(t, `{"token":"original"}`, string(store.event.Metadata))
}

func TestLogger_QueryClonesMetadataFromStore(t *testing.T) {
	store := &captureStore{
		event: Event{
			ID:       "1",
			Metadata: json.RawMessage(`{"token":"original"}`),
		},
	}
	l := newTestLogger(store)

	events, _, err := l.List(context.Background(), Filter{}, "", 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	events[0].Metadata[10] = 'X'

	again, _, err := l.List(context.Background(), Filter{}, "", 10)
	require.NoError(t, err)
	require.Len(t, again, 1)
	assert.JSONEq(t, `{"token":"original"}`, string(again[0].Metadata))
}

func TestLogger_LogE_OnDropPanicDoesNotPanic(t *testing.T) {
	storeErr := errors.New("store unavailable")
	l := newTestLogger(failingStore{err: storeErr}, WithOnDrop(func(context.Context, Event, error) {
		panic("drop hook exploded")
	}))

	var err error
	assert.NotPanics(t, func() {
		err = l.LogE(context.Background(), Event{Actor: "a", Action: "x", Resource: "r", Status: "failure"})
	})
	require.ErrorIs(t, err, storeErr)
	assert.Equal(t, uint64(1), l.DroppedCount())
}

func TestLogger_LogEAppendFailureLogRedactsEventAndError(t *testing.T) {
	storeErr := errors.New("append failed token=tenant-secret")
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	l := newTestLogger(failingStore{err: storeErr}, WithLogger(logger))

	err := l.LogE(context.Background(), Event{
		ID:       "event-secret",
		Actor:    "alice",
		Action:   "download-token",
		Resource: "sessions",
		Status:   "failure",
	})

	require.ErrorIs(t, err, storeErr)
	got := logs.String()
	assert.Contains(t, got, "auditlog: failed to append event")
	assert.Contains(t, got, "<redacted error")
	assert.NotContains(t, got, "tenant-secret")
	assert.NotContains(t, got, "event-secret")
	assert.NotContains(t, got, "download-token")
}

func TestWithLogger_NilNormalizesToDefault(t *testing.T) {
	store := NewMemoryStore()
	l := newTestLogger(store, WithLogger(nil))
	require.NotNil(t, l.logger)
}

func TestMemoryStore_QueryFiltersByIPAddress(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	require.NoError(t, store.Append(ctx, Event{
		ID: "1", Actor: "alice", Action: "login", Resource: "sessions", Status: "success",
		IPAddress: "10.0.0.1", Timestamp: time.Now(),
	}))
	require.NoError(t, store.Append(ctx, Event{
		ID: "2", Actor: "alice", Action: "login", Resource: "sessions", Status: "success",
		IPAddress: "10.0.0.2", Timestamp: time.Now(),
	}))

	got, _, err := store.Query(ctx, Filter{IPAddress: "10.0.0.1"}, "", 100)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "1", got[0].ID)
}

func TestMemoryStore_CopiesMetadataOnPublicBoundaries(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	metadata := json.RawMessage(`{"token":"original"}`)

	require.NoError(t, store.Append(ctx, Event{
		ID:        "1",
		Timestamp: time.Now(),
		Actor:     "alice",
		Action:    "login",
		Resource:  "sessions",
		Status:    "success",
		Metadata:  metadata,
	}))
	metadata[10] = 'X'

	events := store.Events()
	require.Len(t, events, 1)
	assert.JSONEq(t, `{"token":"original"}`, string(events[0].Metadata))

	events[0].Metadata[10] = 'X'
	events = store.Events()
	require.Len(t, events, 1)
	assert.JSONEq(t, `{"token":"original"}`, string(events[0].Metadata))

	queried, _, err := store.Query(ctx, Filter{}, "", 10)
	require.NoError(t, err)
	require.Len(t, queried, 1)
	queried[0].Metadata[10] = 'X'

	queried, _, err = store.Query(ctx, Filter{}, "", 10)
	require.NoError(t, err)
	require.Len(t, queried, 1)
	assert.JSONEq(t, `{"token":"original"}`, string(queried[0].Metadata))
}

func TestMemoryStore_AppendRejectsInvalidEvent(t *testing.T) {
	store := NewMemoryStore()

	err := store.Append(context.Background(), Event{
		ID:        "1",
		Timestamp: time.Now(),
		Actor:     "alice",
		Action:    "login",
		Resource:  "sessions",
		Status:    "not-a-status",
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidEvent)
	assert.Empty(t, store.Events())
}

func TestMemoryStore_LastHMAC_EmptyReturnsNil(t *testing.T) {
	store := NewMemoryStore()
	got, err := store.LastHMAC(context.Background())
	require.NoError(t, err)
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// HMAC chain: build, tamper-detect, verify
// ---------------------------------------------------------------------------

// TestAppend_BuildsHMACChain verifies the core §5.4 claim: each appended
// event's PrevHMAC equals the previous event's HMAC, and each HMAC is a
// recomputable function of the event's content + chain key. Without this
// linkage, deletion of a middle record would leave the chain intact, so we
// also check that the recomputed HMAC matches the stored value.
func TestAppend_BuildsHMACChain(t *testing.T) {
	store := NewMemoryStore()
	l := newTestLogger(store)
	ctx := context.Background()

	for i := range 4 {
		l.LogAction(ctx, "alice", "create", "orders/"+string(rune('a'+i)), "success")
	}

	events := store.Events()
	require.Len(t, events, 4)

	// First event: PrevHMAC must be nil/empty (no predecessor).
	assert.Empty(t, events[0].PrevHMAC, "first event's PrevHMAC must be empty")
	assert.Len(t, events[0].HMAC, hmacSize, "HMAC must be HMAC-SHA256 sized")

	// Each subsequent event must chain to its predecessor.
	for i := 1; i < len(events); i++ {
		assert.Equal(t,
			events[i-1].HMAC, events[i].PrevHMAC,
			"event[%d].PrevHMAC must equal event[%d].HMAC", i, i-1,
		)
		// HMAC must be recomputable from content.
		expected := computeHMAC(testChainKey, events[i].PrevHMAC, eventWithoutHMAC(events[i]))
		assert.Equal(t, expected, events[i].HMAC,
			"event[%d].HMAC must be recomputable", i)
	}

	// And the canonical entry point validates the whole chain.
	require.NoError(t, VerifyChain(events, testChainKey))
	require.NoError(t, l.VerifyChain(ctx))
}

// TestAppend_TamperedRecordDetected makes the threat-model claim concrete:
// mutating a stored record's Resource invalidates its HMAC, and
// VerifyChain reports it.
func TestAppend_TamperedRecordDetected(t *testing.T) {
	store := NewMemoryStore()
	l := newTestLogger(store)
	ctx := context.Background()

	for i := range 3 {
		l.LogAction(ctx, "alice", "create", "orders/"+string(rune('a'+i)), "success")
	}

	// Simulate an attacker who flips a stored field directly in the
	// store's underlying slice. We reach in via the test helper.
	tampered := store.events
	tampered[1].Resource = "orders/forged"

	err := VerifyChain(store.Events(), testChainKey)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrChainBroken)
	assert.Contains(t, err.Error(), "event[1]")

	// Logger.VerifyChain reports the same.
	err = l.VerifyChain(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrChainBroken)
}

// TestVerifyChain_RejectsPrependedRecord verifies that an attacker who
// invents a record and inserts it at the head — keeping every later
// PrevHMAC pointing into their forged record — is still detected: the
// forged record's own HMAC cannot match its content under the legitimate
// chain key.
func TestVerifyChain_RejectsPrependedRecord(t *testing.T) {
	store := NewMemoryStore()
	l := newTestLogger(store)
	ctx := context.Background()

	for i := range 3 {
		l.LogAction(ctx, "alice", "create", "orders/"+string(rune('a'+i)), "success")
	}
	original := store.Events()
	require.Len(t, original, 3)

	forged := Event{
		ID:        "forged-0",
		Timestamp: original[0].Timestamp.Add(-time.Hour),
		Actor:     "mallory",
		Action:    "create",
		Resource:  "orders/forged",
		Status:    "success",
	}
	// Attacker even computes a "plausible" HMAC with their own key — but
	// it cannot match the legitimate chain key.
	attackerKey := bytes.Repeat([]byte("z"), MinChainKeyLen)
	forged.HMAC = computeHMAC(attackerKey, nil, forged)

	prepended := append([]Event{forged}, original...)
	err := VerifyChain(prepended, testChainKey)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrChainBroken)
}

// TestVerifyChain_RejectsTruncatedChain shows that deletion of any non-tail
// record changes a successor's recomputed HMAC because its PrevHMAC no
// longer matches the (now-missing) predecessor's HMAC.
func TestVerifyChain_RejectsTruncatedChain(t *testing.T) {
	store := NewMemoryStore()
	l := newTestLogger(store)
	ctx := context.Background()

	for i := range 4 {
		l.LogAction(ctx, "alice", "create", "orders/"+string(rune('a'+i)), "success")
	}

	events := store.Events()
	require.Len(t, events, 4)

	// Drop event[1]. event[2].PrevHMAC still points at the original
	// event[1].HMAC, which the new event[1] (= original event[2]) does
	// not produce.
	truncated := []Event{events[0], events[2], events[3]}
	err := VerifyChain(truncated, testChainKey)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrChainBroken)
	// The first survivor's PrevHMAC no longer matches its (now-) predecessor.
	assert.Contains(t, err.Error(), "event[1]")
}

// TestVerifyChain_DifferentKeyFails ensures the chain key is actually
// load-bearing: a correctly-formed chain cannot be validated under a
// different key.
func TestVerifyChain_DifferentKeyFails(t *testing.T) {
	store := NewMemoryStore()
	l := newTestLogger(store)
	ctx := context.Background()
	l.LogAction(ctx, "alice", "create", "orders/1", "success")

	otherKey := bytes.Repeat([]byte("w"), MinChainKeyLen)
	err := VerifyChain(store.Events(), otherKey)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrChainBroken)
}

// TestVerifyChain_EmptyIsValid documents the degenerate case so callers
// can rely on VerifyChain not erroring at boot before any events have
// been recorded.
func TestVerifyChain_EmptyIsValid(t *testing.T) {
	assert.NoError(t, VerifyChain(nil, testChainKey))
	assert.NoError(t, VerifyChain([]Event{}, testChainKey))

	store := NewMemoryStore()
	l := newTestLogger(store)
	require.NoError(t, l.VerifyChain(context.Background()))
}

// TestLogger_VerifyChain_AppendsOrderIndependentOfTimestamp pins the
// R2-001 fix: chain verification scans via Store.RangeChain (append
// order), not Store.Query (timestamp order). A service that backfills
// a historical event with an older timestamp than the previous append
// must still produce a verifiable chain.
func TestLogger_VerifyChain_AppendsOrderIndependentOfTimestamp(t *testing.T) {
	store := NewMemoryStore()
	l := newTestLogger(store)
	ctx := context.Background()

	// First append carries "now". Second append is a backfill with a
	// deliberately older timestamp — the kind of thing a replay or
	// import flow would write. Query() will return them
	// timestamp-descending (first, then second), which is the wrong
	// order for chain link validation. RangeChain() must yield them in
	// append order (second event sees the first event's HMAC as its
	// PrevHMAC) regardless.
	now := time.Now()
	require.NoError(t, l.LogE(ctx, Event{
		Actor:     "alice",
		Action:    "login",
		Resource:  "sessions",
		Status:    StatusSuccess,
		Timestamp: now,
	}))
	require.NoError(t, l.LogE(ctx, Event{
		Actor:     "system",
		Action:    "backfill",
		Resource:  "audit",
		Status:    StatusSuccess,
		Timestamp: now.Add(-24 * time.Hour),
	}))

	// The chain is intact in append order, so VerifyChain succeeds.
	require.NoError(t, l.VerifyChain(ctx))

	// Sanity: Query order is timestamp-descending. The newer-timestamp
	// event comes first even though it was appended first; this is the
	// expected user-facing list order and exactly the case that would
	// break a timestamp-order chain verifier.
	page, _, err := store.Query(ctx, Filter{}, "", 10)
	require.NoError(t, err)
	require.Len(t, page, 2)
	assert.Equal(t, "alice", page[0].Actor, "Query is timestamp-descending so the newer-timestamped (first-appended) event leads")
	assert.Equal(t, "system", page[1].Actor)
}

// TestLogger_Append_ConcurrentDoesNotReuseHMAC is the regression test for
// the append-mutex: without it, two concurrent appenders could both read
// the same PrevHMAC from the store and write events whose PrevHMAC was
// the same — i.e. forking the chain. The test fires N goroutines and
// asserts every appended event has a unique HMAC AND the chain still
// validates.
func TestLogger_Append_ConcurrentDoesNotReuseHMAC(t *testing.T) {
	store := NewMemoryStore()
	l := newTestLogger(store)
	ctx := context.Background()

	const writers = 16
	const perWriter = 8

	var wg sync.WaitGroup
	wg.Add(writers)
	start := make(chan struct{})
	for w := range writers {
		go func(w int) {
			defer wg.Done()
			<-start
			for i := range perWriter {
				_ = l.LogE(ctx, Event{
					Actor:    "w" + string(rune('a'+w)),
					Action:   "create",
					Resource: "r/" + string(rune('a'+i)),
					Status:   "success",
				})
			}
		}(w)
	}
	close(start)
	wg.Wait()

	events := store.Events()
	require.Len(t, events, writers*perWriter)

	// No HMAC may repeat — that would mean two events shared a PrevHMAC
	// or were byte-identical.
	seen := make(map[string]struct{}, len(events))
	for i, e := range events {
		key := string(e.HMAC)
		_, dup := seen[key]
		require.Falsef(t, dup, "event[%d].HMAC duplicated", i)
		seen[key] = struct{}{}
	}

	// And the whole chain must still validate.
	require.NoError(t, VerifyChain(events, testChainKey))
}

// TestLogger_LogE_DiscardsCallerSuppliedHMAC ensures a caller cannot
// "preset" their own PrevHMAC / HMAC to splice into the chain at an
// arbitrary point. The Logger is the sole authority on those fields.
func TestLogger_LogE_DiscardsCallerSuppliedHMAC(t *testing.T) {
	store := NewMemoryStore()
	l := newTestLogger(store)
	ctx := context.Background()

	fake := []byte("forged-prev-hmac---------------!")
	require.NoError(t, l.LogE(ctx, Event{
		Actor:    "alice",
		Action:   "create",
		Resource: "orders/1",
		Status:   "success",
		PrevHMAC: fake,
		HMAC:     fake,
	}))

	events := store.Events()
	require.Len(t, events, 1)
	assert.NotEqual(t, fake, events[0].PrevHMAC)
	assert.NotEqual(t, fake, events[0].HMAC)
	assert.Empty(t, events[0].PrevHMAC, "first event in chain should have empty PrevHMAC")
}

// ---------------------------------------------------------------------------
// Signed cursors
// ---------------------------------------------------------------------------

// TestQuery_RejectsForgedCursor verifies the §5.4 cursor claim. A
// hand-crafted "next page" string that was not produced by the Logger's
// signer must be rejected before reaching the Store.
func TestQuery_RejectsForgedCursor(t *testing.T) {
	store := NewMemoryStore()
	l := newTestLogger(store)
	ctx := context.Background()
	for i := range 5 {
		l.LogAction(ctx, "alice", "create", "orders/"+string(rune('a'+i)), "success")
	}

	for name, forged := range map[string]string{
		"no separator":            "not-a-real-cursor",
		"bad base64 in payload":   "!@#$.YWJj",
		"bad base64 in signature": "YWJj.!@#$",
		"good payload, bad mac":   "YWJj.YWJj", // payload "abc", signature "abc" (random)
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := l.List(ctx, Filter{}, forged, 2)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidCursor)
		})
	}
}

// TestQuery_RejectsCursorSignedWithDifferentKey is the more interesting
// adversarial case: the cursor is *well-formed* — base64 payload, base64
// signature — and was signed by *some* HMAC key. Only the verification
// step (which uses our cursorKey, not the attacker's) catches it.
func TestQuery_RejectsCursorSignedWithDifferentKey(t *testing.T) {
	store := NewMemoryStore()
	l := newTestLogger(store)
	ctx := context.Background()
	for i := range 3 {
		l.LogAction(ctx, "alice", "create", "orders/"+string(rune('a'+i)), "success")
	}

	// Attacker signs "page-2" with a different key.
	attackerKey := bytes.Repeat([]byte("z"), MinCursorKeyLen)
	attackerSigner := signedCursor{key: secret.New(attackerKey), keyLen: len(attackerKey)}
	forged := attackerSigner.encodeCursor("some-real-event-id")
	require.NotEmpty(t, forged, "attacker should be able to produce a well-formed cursor")

	_, _, err := l.List(ctx, Filter{}, forged, 2)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidCursor)
}

// TestQuery_RoundTripsSignedCursors verifies that the cursors actually
// work end-to-end: page 1 returns a signed cursor, page 2 accepts it,
// and so on through to the end of the result set.
func TestQuery_RoundTripsSignedCursors(t *testing.T) {
	store := NewMemoryStore()
	l := newTestLogger(store)
	ctx := context.Background()
	for i := range 5 {
		l.LogAction(ctx, "alice", "create", "orders/"+string(rune('a'+i)), "success")
	}

	collected := make([]string, 0, 5)
	cursor := ""
	for range 5 {
		page, next, err := l.List(ctx, Filter{}, cursor, 2)
		require.NoError(t, err)
		for _, e := range page {
			collected = append(collected, e.Resource)
		}
		// Page cursors must contain a period (i.e. be a signed envelope)
		// or be empty (last page).
		if next != "" {
			assert.Contains(t, next, ".", "next cursor must be signed-envelope shaped")
		}
		cursor = next
		if cursor == "" {
			break
		}
	}
	assert.Len(t, collected, 5)
}

func TestRetentionJobPanicsOnInvalidConfig(t *testing.T) {
	assert.Panics(t, func() {
		RetentionJob(nil, time.Hour, nil)
	})
	assert.Panics(t, func() {
		RetentionJob(&retentionCaptureStore{}, 0, nil)
	})
	assert.Panics(t, func() {
		RetentionJob(&retentionCaptureStore{}, -time.Hour, nil)
	})
}

func TestRetentionJobDeletesBeforeCutoff(t *testing.T) {
	store := &retentionCaptureStore{}
	retention := time.Hour
	job := RetentionJob(store, retention, nil)

	beforeRun := time.Now().Add(-retention)
	require.NoError(t, job(context.Background()))
	afterRun := time.Now().Add(-retention)

	assert.Equal(t, 1, store.deleteCalls)
	assert.False(t, store.before.Before(beforeRun.Add(-time.Second)))
	assert.False(t, store.before.After(afterRun.Add(time.Second)))
}

func TestRetentionJobLogRedactsCleanupError(t *testing.T) {
	storeErr := errors.New("delete failed token=tenant-secret")
	store := &retentionCaptureStore{deleteErr: storeErr}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	job := RetentionJob(store, time.Hour, logger)

	err := job(context.Background())

	require.ErrorIs(t, err, storeErr)
	got := logs.String()
	assert.Contains(t, got, "audit retention cleanup failed")
	assert.Contains(t, got, "<redacted error")
	assert.NotContains(t, got, "tenant-secret")
}

type retentionCaptureStore struct {
	before      time.Time
	deleteCalls int
	deleteErr   error
}

func (s *retentionCaptureStore) Append(context.Context, Event) error {
	return nil
}

func (s *retentionCaptureStore) AppendChained(_ context.Context, build func(prev []byte) (Event, error)) error {
	_, err := build(nil)
	return err
}

func (s *retentionCaptureStore) Query(context.Context, Filter, string, int) ([]Event, string, error) {
	return nil, "", nil
}

func (s *retentionCaptureStore) LastHMAC(context.Context) ([]byte, error) {
	return nil, nil
}

func (s *retentionCaptureStore) RangeChain(context.Context, func(Event) error) error {
	return nil
}

func (s *retentionCaptureStore) DeleteBefore(_ context.Context, before time.Time) (int64, error) {
	s.before = before
	s.deleteCalls++
	if s.deleteErr != nil {
		return 0, s.deleteErr
	}
	return 0, nil
}

func TestClose_ZeroesKeysAndRejectsSubsequentUse(t *testing.T) {
	store := NewMemoryStore()
	l := newTestLogger(store)
	ctx := context.Background()

	// Append a record so a chain HMAC is computed under the live key.
	require.NoError(t, l.LogE(ctx, Event{Actor: "alice", Action: "create", Resource: "x", Status: StatusSuccess}))

	// Close zeroes the chainKey and cursors.key.
	require.NoError(t, l.Close())

	// Verify the wrapped secrets were zeroed.
	assert.True(t, l.chainKey.IsEmpty(), "chainKey must be zeroed after Close")
	assert.True(t, l.cursors.key.IsEmpty(), "cursor key must be zeroed after Close")

	// Subsequent LogE / Query / VerifyChain all return ErrLoggerClosed.
	err := l.LogE(ctx, Event{Actor: "alice", Action: "create", Resource: "y", Status: StatusSuccess})
	assert.ErrorIs(t, err, ErrLoggerClosed)

	_, _, err = l.List(ctx, Filter{}, "", 10)
	assert.ErrorIs(t, err, ErrLoggerClosed)

	err = l.VerifyChain(ctx)
	assert.ErrorIs(t, err, ErrLoggerClosed)

	// Idempotent.
	require.NoError(t, l.Close())
}

func TestClose_NilReceiverIsSafe(t *testing.T) {
	var l *Logger
	assert.NoError(t, l.Close())
}

// gatedStore lets the test pause inside AppendChained between
// claiming the store lock and calling the build callback, so the
// test can interleave a Logger.Close call deterministically.
type gatedStore struct {
	mu      sync.Mutex
	events  []Event
	enter   chan struct{} // store signals "about to call build" here
	release chan struct{} // test signals "you may now call build" here
}

func newGatedStore() *gatedStore {
	return &gatedStore{
		enter:   make(chan struct{}, 1),
		release: make(chan struct{}, 1),
	}
}

func (s *gatedStore) Append(_ context.Context, event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, cloneEvent(event))
	return nil
}

func (s *gatedStore) AppendChained(_ context.Context, build func(prev []byte) (Event, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var prev []byte
	if len(s.events) > 0 {
		prev = append([]byte(nil), s.events[len(s.events)-1].HMAC...)
	}
	s.enter <- struct{}{}
	<-s.release
	event, err := build(prev)
	if err != nil {
		return err
	}
	s.events = append(s.events, cloneEvent(event))
	return nil
}

func (s *gatedStore) Query(_ context.Context, _ Filter, _ string, _ int) ([]Event, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.events...), "", nil
}

func (s *gatedStore) RangeChain(_ context.Context, fn func(Event) error) error {
	s.mu.Lock()
	snap := append([]Event(nil), s.events...)
	s.mu.Unlock()
	for _, e := range snap {
		if err := fn(e); err != nil {
			return err
		}
	}
	return nil
}

func (s *gatedStore) LastHMAC(context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) == 0 {
		return nil, nil
	}
	return append([]byte(nil), s.events[len(s.events)-1].HMAC...), nil
}

func (s *gatedStore) DeleteOlderThan(context.Context, time.Time) (int64, error) {
	return 0, nil
}

// TestLogE_ReturnsClosedWhenZeroRacesBuildCallback exercises H2-003:
// if Close zeroes the chainKey between LogE's outer closed check and
// the inner build callback's chainKey.Use snapshot, LogE must abort
// with ErrLoggerClosed and never persist a record HMACed over zero
// key material.
func TestLogE_ReturnsClosedWhenZeroRacesBuildCallback(t *testing.T) {
	store := newGatedStore()
	l := newTestLogger(store)

	logErrCh := make(chan error, 1)
	go func() {
		logErrCh <- l.LogE(context.Background(), Event{
			Actor:    "alice",
			Action:   "create",
			Resource: "r",
			Status:   StatusSuccess,
		})
	}()

	// Wait until the store has captured the tail and is about to call build.
	<-store.enter
	// Close the logger — this sets closed=true and zeroes chainKey.
	require.NoError(t, l.Close())
	// Now let the store call build(prev). The inner re-check (closed)
	// OR the empty-key guard inside chainKey.Use must fire.
	store.release <- struct{}{}

	err := <-logErrCh
	require.ErrorIs(t, err, ErrLoggerClosed)
	require.Empty(t, store.events, "no event must be persisted when Close races the build callback")
}

// Reference secret to keep the unused-import linter quiet — secret.New is
// used in the cursor-forgery test above.
var _ = secret.New
