package auditlog

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchText_CoversFieldsAndMetadataValuesNotKeys(t *testing.T) {
	text := SearchText(Event{
		Actor: "user:alice", Action: "member.added", Resource: "org:1/membership:2",
		Metadata: json.RawMessage(`{"details":{"role":"Owner","count":3,"tags":["a","b"],"on":true,"none":null,"empty":""}}`),
	})
	assert.Equal(t, "user:alice\nmember.added\norg:1/membership:2\n3\nOwner\na\nb", text)
	assert.NotContains(t, text, "details")
}

func TestSearchText_DropsNULAndInvalidMetadata(t *testing.T) {
	assert.Equal(t, "a\nb\nc\nxy", SearchText(Event{Actor: "a", Action: "b", Resource: "c", Metadata: json.RawMessage(`{"k":"x\u0000y"}`)}))
	assert.Equal(t, "a\nb\nc", SearchText(Event{Actor: "a", Action: "b", Resource: "c", Metadata: json.RawMessage(`{not json`)}))
}

func TestMatchesSearch_CaseInsensitiveSubstring(t *testing.T) {
	e := Event{Actor: "user:Alice", Action: "role.granted", Resource: "r", Metadata: json.RawMessage(`{"role":"Billing Admin"}`)}
	assert.True(t, MatchesSearch(e, ""))
	assert.True(t, MatchesSearch(e, "alice"))
	assert.True(t, MatchesSearch(e, "ROLE.GR"))
	assert.True(t, MatchesSearch(e, "billing ad"))
	assert.False(t, MatchesSearch(e, "role\":"))
	assert.False(t, MatchesSearch(e, "bob"))
}

func TestLogger_List_SearchPagesWithCursor(t *testing.T) {
	ctx := context.Background()
	l := newTestLogger(NewMemoryStore())
	base := time.Now().Add(-time.Hour)
	for i := range 7 {
		actor := "user:bob"
		if i%2 == 0 {
			actor = "user:alice"
		}
		require.NoError(t, l.LogE(ctx, Event{
			Timestamp: base.Add(time.Duration(i) * time.Second), Actor: actor, Action: "x", Resource: "r", Status: StatusSuccess,
		}))
	}

	var got []Event
	cursor := ""
	for {
		page, next, err := l.List(ctx, Filter{Search: "ALICE"}, cursor, 3)
		require.NoError(t, err)
		got = append(got, page...)
		if next == "" {
			break
		}
		cursor = next
	}
	require.Len(t, got, 4)
	for i, e := range got {
		assert.Equal(t, "user:alice", e.Actor)
		if i > 0 {
			assert.True(t, e.Timestamp.Before(got[i-1].Timestamp))
		}
	}
}

func TestLogger_List_RejectsLongSearch(t *testing.T) {
	l := newTestLogger(NewMemoryStore())
	_, _, err := l.List(context.Background(), Filter{Search: strings.Repeat("a", MaxSearchBytes+1)}, "", 10)
	assert.ErrorIs(t, err, ErrSearchTooLong)
}

func TestSearchTextConsistent(t *testing.T) {
	e := Event{Actor: "user:a", Action: "role.granted", Resource: "org:1",
		Metadata: json.RawMessage(`{"details":{"role":"Auditor"},"n":42.5}`)}

	assert.True(t, SearchTextConsistent(e, SearchText(e)))
	assert.True(t, SearchTextConsistent(e, "user:a\nrole.granted\norg:1\n42.50\nAuditor"),
		"order and number spelling may differ")
	assert.False(t, SearchTextConsistent(e, ""))
	assert.False(t, SearchTextConsistent(e, "user:a\nrole.granted\norg:1\n42.5"))
	assert.False(t, SearchTextConsistent(e, SearchText(e)+"\nextra"))
	assert.False(t, SearchTextConsistent(e, "user:b\nrole.granted\norg:1\n42.5\nAuditor"))
}
