package headerutil

import (
	"net/http"
	"testing"
)

func TestSingletonIdentity(t *testing.T) {
	userID := func(value string) func(http.Header) {
		return func(h http.Header) { h.Set("X-User-Id", value) }
	}
	tests := []struct {
		name string
		set  func(http.Header)
		want string
		ok   bool
	}{
		{name: "single", set: userID("alice"), want: "alice", ok: true},
		{name: "missing", set: func(http.Header) {}},
		{
			name: "duplicate",
			set: func(h http.Header) {
				h.Add("X-User-Id", "alice")
				h.Add("X-User-Id", "bob")
			},
		},
		{name: "blank", set: userID("")},
		{name: "edge whitespace", set: userID(" alice ")},
		{name: "internal whitespace", set: userID("alice bob")},
		{name: "comma combined", set: userID("alice,bob")},
		{name: "invalid field value", set: userID("alice\nbob")},
		{name: "invalid utf8", set: userID(string([]byte{0xff}))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			tt.set(h)
			got, ok := SingletonIdentity(h, "X-User-Id")
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("value = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSingletonTokenDistinguishesMissingFromInvalid(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		value, present, ok := SingletonToken(http.Header{}, "X-Tenant-Id")
		if value != "" || present || ok {
			t.Fatalf("SingletonToken missing = (%q, %v, %v), want empty,false,false", value, present, ok)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		h := http.Header{}
		h.Add("X-Tenant-Id", "tenant-a")
		h.Add("X-Tenant-Id", "tenant-b")

		value, present, ok := SingletonToken(h, "X-Tenant-Id")
		if value != "" || !present || ok {
			t.Fatalf("SingletonToken invalid = (%q, %v, %v), want empty,true,false", value, present, ok)
		}
	})

	t.Run("valid", func(t *testing.T) {
		h := http.Header{}
		h.Set("X-Tenant-Id", "tenant-a")

		value, present, ok := SingletonToken(h, "X-Tenant-Id")
		if value != "tenant-a" || !present || !ok {
			t.Fatalf("SingletonToken valid = (%q, %v, %v), want tenant-a,true,true", value, present, ok)
		}
	})
}
