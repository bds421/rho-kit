package secret

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	plain         = "super-secret-token"
	redactedValue = "<redacted>"
)

func TestNew_CopiesInputBytes(t *testing.T) {
	src := []byte(plain)
	s := New(src)
	src[0] = 'X' // mutate caller's buffer

	assert.Equal(t, plain, s.RevealString(), "internal buffer must not share storage with caller")
}

func TestNewFromString_StoresValue(t *testing.T) {
	s := NewFromString(plain)
	assert.Equal(t, plain, s.RevealString())
}

func TestEmptyConstructors(t *testing.T) {
	assert.True(t, New(nil).IsEmpty())
	assert.True(t, New([]byte{}).IsEmpty())
	assert.True(t, NewFromString("").IsEmpty())
	assert.Empty(t, New(nil).Reveal())
	assert.Equal(t, "", New(nil).RevealString())
}

func TestNilReceiverIsSafe(t *testing.T) {
	var s *String
	assert.True(t, s.IsEmpty())
	assert.Nil(t, s.Reveal())
	assert.Equal(t, "", s.RevealString())
	s.Zero()
}

func TestRevealReturnsCopy(t *testing.T) {
	s := NewFromString(plain)
	a := s.Reveal()
	a[0] = 'X' // mutate the returned slice
	assert.Equal(t, plain, s.RevealString(), "internal buffer must be unaffected by mutations of returned slice")
}

func TestStringForms_AllRedacted(t *testing.T) {
	s := NewFromString(plain)

	cases := []struct {
		name string
		got  string
	}{
		{"String()", s.String()},
		{"GoString()", s.GoString()},
		{"%v", fmt.Sprintf("%v", s)},
		{"%+v", fmt.Sprintf("%+v", s)},
		{"%s", fmt.Sprintf("%s", s)},
		{"%#v", fmt.Sprintf("%#v", s)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, redactedValue, c.got)
			assert.NotContains(t, c.got, plain)
		})
	}

	// %q wraps the redacted literal in quotes.
	assert.Equal(t, `"`+redactedValue+`"`, fmt.Sprintf("%q", s))
}

func TestMarshalJSON_Redacted(t *testing.T) {
	s := NewFromString(plain)
	b, err := json.Marshal(s)
	require.NoError(t, err)
	// Go's json.Marshal HTML-escapes "<" and ">" by default. Decode the
	// emitted JSON to assert on the underlying string value rather than
	// the byte-level encoding.
	var got string
	require.NoError(t, json.Unmarshal(b, &got))
	assert.Equal(t, redactedValue, got)
	assert.NotContains(t, string(b), plain)
}

func TestMarshalJSON_NestedInStruct(t *testing.T) {
	type cfg struct {
		Token *String `json:"token"`
		Open  string  `json:"open"`
	}
	v := cfg{Token: NewFromString(plain), Open: "visible"}
	b, err := json.Marshal(v)
	require.NoError(t, err)

	var decoded struct {
		Token string `json:"token"`
		Open  string `json:"open"`
	}
	require.NoError(t, json.Unmarshal(b, &decoded))
	assert.Equal(t, redactedValue, decoded.Token)
	assert.Equal(t, "visible", decoded.Open)
	assert.NotContains(t, string(b), plain)
}

func TestMarshalText_Redacted(t *testing.T) {
	s := NewFromString(plain)
	b, err := s.MarshalText()
	require.NoError(t, err)
	assert.Equal(t, redactedValue, string(b))
}

func TestMarshalBinary_Redacted(t *testing.T) {
	s := NewFromString(plain)
	b, err := s.MarshalBinary()
	require.NoError(t, err)
	assert.Equal(t, redactedValue, string(b))
	assert.NotContains(t, string(b), plain)
}

func TestSlogLogValue_Redacted(t *testing.T) {
	s := NewFromString(plain)
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	logger.Info("test", "secret", s)
	assert.Contains(t, buf.String(), redactedValue)
	assert.NotContains(t, buf.String(), plain)
}

func TestZero_ZeroesBuffer(t *testing.T) {
	s := NewFromString(plain)
	s.Zero()
	assert.True(t, s.IsEmpty())
	assert.Empty(t, s.Reveal())
	assert.Equal(t, "", s.RevealString())
}

func TestZero_Idempotent(t *testing.T) {
	s := NewFromString(plain)
	s.Zero()
	s.Zero()
	assert.True(t, s.IsEmpty())
}

// TestValueTypedUsage_StillRedacts is the regression test for the H-1
// finding: prior to the value-receiver fix, a by-value copy or a
// dereference of *String would lose the redaction methods from the
// type's method set, and fmt.Printf("%+v", s) printed s.buf as a
// decimal byte slice — i.e. the plaintext, decoded.
func TestValueTypedUsage_StillRedacts(t *testing.T) {
	src := NewFromString(plain)

	// 1. Deref into a value-typed local. Pre-fix, this lost the
	//    redaction methods. Post-fix, value receivers keep them in the
	//    method set.
	v := *src
	assert.Equal(t, redactedValue, fmt.Sprintf("%v", v))
	assert.Equal(t, redactedValue, fmt.Sprintf("%+v", v))
	assert.Equal(t, redactedValue, fmt.Sprintf("%#v", v))
	assert.Equal(t, redactedValue, v.String())
	assert.NotContains(t, fmt.Sprintf("%+v", v), plain)

	// 2. Embed by value in another struct. fmt.Sprintf reaches into
	//    the struct field; if the redaction methods weren't on the
	//    value method set the whole nested struct would print the
	//    backing buf.
	type wrapper struct {
		Token String
		Open  string
	}
	w := wrapper{Token: *src, Open: "visible"}
	out := fmt.Sprintf("%+v", w)
	assert.Contains(t, out, redactedValue)
	assert.Contains(t, out, "visible")
	assert.NotContains(t, out, plain)

	// 3. JSON marshalling of a value-typed field.
	type cfg struct {
		Token String `json:"token"`
	}
	b, err := json.Marshal(cfg{Token: *src})
	require.NoError(t, err)
	var decoded struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(b, &decoded))
	assert.Equal(t, redactedValue, decoded.Token)
	assert.NotContains(t, string(b), plain)

	// 4. Zero-value String (never went through New) — also redacts and
	//    does not panic. This is the variable-declared-in-config-struct
	//    case.
	var zero String
	assert.Equal(t, redactedValue, zero.String())
	assert.Equal(t, redactedValue, fmt.Sprintf("%+v", zero))
}

// TestString_RedactsAcrossAllRenderVerbs is the adversarial render-matrix
// test: it exercises every standard rendering path the package promises to
// redact and asserts the raw plaintext never appears in the output, AND
// that the redaction marker IS present. The matrix is deliberately
// exhaustive — fmt verbs, json.Marshal, slog, encoding.TextMarshaler,
// slice/map/pointer wrappers — because each path dispatches through a
// different interface (fmt.Formatter vs fmt.Stringer vs json.Marshaler vs
// encoding.TextMarshaler vs slog.LogValuer) and an interface that quietly
// disappears from the method set (the H-1 value-receiver regression) would
// silently leak through exactly one of these without breaking the others.
func TestString_RedactsAcrossAllRenderVerbs(t *testing.T) {
	const literal = "hunter2"
	s := NewFromString(literal)

	assertRedacted := func(t *testing.T, name, rendered string) {
		t.Helper()
		assert.NotContainsf(t, rendered, literal,
			"%s rendered the plaintext: %q", name, rendered)
		assert.Containsf(t, rendered, redactedValue,
			"%s missing redaction marker %q in output: %q",
			name, redactedValue, rendered)
	}

	// fmt verbs against *String.
	assertRedacted(t, "%v", fmt.Sprintf("%v", s))
	assertRedacted(t, "%s", fmt.Sprintf("%s", s))
	assertRedacted(t, "%q", fmt.Sprintf("%q", s))
	assertRedacted(t, "%+v", fmt.Sprintf("%+v", s))
	assertRedacted(t, "%#v", fmt.Sprintf("%#v", s))

	// fmt against a pointer-to-value-String — the value redaction
	// methods promote into the pointer method set, so taking the
	// address of a value-typed String must still route through Format
	// rather than reflectively dumping the underlying buffer. (Taking
	// &(*String) yields a **String whose default fmt rendering is the
	// pointer address, which leaks nothing but also is not a sensible
	// API surface to assert on.)
	sValue := *s
	assertRedacted(t, "%v (&sValue)", fmt.Sprintf("%v", &sValue))
	assertRedacted(t, "%+v (&sValue)", fmt.Sprintf("%+v", &sValue))

	// json.Marshal — the literal "<redacted>" gets HTML-escaped to
	// "<redacted>" in the encoded bytes. Decode and assert
	// on the decoded string so escaping cannot hide a leak in either
	// direction (and so a future encoder that disables HTML escaping
	// doesn't silently break this test).
	jb, err := json.Marshal(s)
	require.NoError(t, err)
	assert.NotContains(t, string(jb), literal, "json.Marshal leaked plaintext: %s", jb)
	var decoded string
	require.NoError(t, json.Unmarshal(jb, &decoded))
	assert.Equal(t, redactedValue, decoded,
		"json.Marshal must decode back to the redaction marker; got %q", decoded)

	// slog with a JSON handler — the structured-log path most likely to
	// be deployed in production.
	var buf strings.Builder
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("event", "secret", s)
	logOut := buf.String()
	assert.NotContains(t, logOut, literal, "slog leaked plaintext: %s", logOut)
	// slog's JSON handler emits the redaction marker verbatim ("<redacted>");
	// it does not HTML-escape "<"/">". Assert the marker is present so this
	// arm fails if a future change drops or mangles redaction in the JSON
	// log path. (Previously this ORed two identical literals, so the
	// second arm was dead and the assertion could never distinguish the
	// escaped-vs-unescaped forms.)
	assert.Contains(t, logOut, redactedValue,
		"slog output missing redaction marker: %s", logOut)

	// encoding.TextMarshaler — yaml.v3 / TOML / similar serialisers
	// dispatch through this.
	tb, err := s.MarshalText()
	require.NoError(t, err)
	assertRedacted(t, "MarshalText", string(tb))

	// Slice containing the secret — fmt reaches into the element and
	// must call String.Format / String.String per element rather than
	// printing the buf field.
	slice := []*String{s, s}
	assertRedacted(t, "%v ([]*String)", fmt.Sprintf("%v", slice))

	// Map containing the secret — same reach-through risk as the slice
	// but exercises map-value formatting.
	m := map[string]*String{"k": s}
	assertRedacted(t, "%v (map[string]*String)", fmt.Sprintf("%v", m))

	// Value-typed slice / map exercise the value-receiver method set
	// directly (the H-1 regression surface).
	sliceVal := []String{*s, *s}
	assertRedacted(t, "%v ([]String)", fmt.Sprintf("%v", sliceVal))
	mVal := map[string]String{"k": *s}
	assertRedacted(t, "%v (map[string]String)", fmt.Sprintf("%v", mVal))
}

func TestUse_ProvidesCopyAndZeroesAfter(t *testing.T) {
	s := NewFromString(plain)

	var captured []byte
	s.Use(func(b []byte) {
		assert.Equal(t, plain, string(b))
		// The slice is freshly-allocated copy; storage cannot alias the inner buf.
		captured = b
	})
	// After Use returns, the slice it handed in is zeroed; capturing it
	// outside the closure is a misuse but the post-zero state is observable.
	for _, v := range captured {
		assert.Equal(t, byte(0), v, "Use must zero the temporary slice on return")
	}
	// The wrapped secret itself is untouched.
	assert.Equal(t, plain, s.RevealString())
}

func TestUse_ZeroesEvenOnPanic(t *testing.T) {
	s := NewFromString(plain)

	var captured []byte
	defer func() {
		_ = recover()
		for _, v := range captured {
			assert.Equal(t, byte(0), v, "Use must zero the temporary slice even when fn panics")
		}
	}()
	s.Use(func(b []byte) {
		captured = b
		panic("boom")
	})
}

func TestUse_NilReceiverPassesNilSlice(t *testing.T) {
	var s *String
	called := false
	s.Use(func(b []byte) {
		called = true
		assert.Nil(t, b)
	})
	assert.True(t, called)
}

func TestUse_EmptyStringPassesNilSlice(t *testing.T) {
	s := NewFromString("")
	called := false
	s.Use(func(b []byte) {
		called = true
		assert.Empty(t, b)
	})
	assert.True(t, called)
}

func TestUse_NilCallbackIsNoOp(t *testing.T) {
	s := NewFromString(plain)
	s.Use(nil) // must not panic
	assert.Equal(t, plain, s.RevealString())
}

func TestConcurrentReadsAreSafe(t *testing.T) {
	s := NewFromString(plain)
	done := make(chan struct{})
	for range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			for range 100 {
				_ = s.RevealString()
				_ = s.IsEmpty()
				_ = s.String()
			}
		}()
	}
	for range 8 {
		<-done
	}
}

// TestConstantTimeEqual_RejectsLenDeltaMultipleOf256 locks down the
// wave-66 fix: the prior implementation folded length comparison via
// byte(len(a) ^ len(b)), so a 256-byte longer all-zero suffix would
// collapse to zero and the helper would falsely report equality.
func TestConstantTimeEqual_RejectsLenDeltaMultipleOf256(t *testing.T) {
	a := []byte("secret")
	b := append([]byte("secret"), make([]byte, 256)...)
	if constantTimeEqual(a, b) {
		t.Fatal("length delta of 256 with zero suffix must not compare equal")
	}
	c := []byte("secret")
	d := append([]byte("secret"), make([]byte, 512)...)
	if constantTimeEqual(c, d) {
		t.Fatal("length delta of 512 with zero suffix must not compare equal")
	}
}

func TestConstantTimeEqual_EqualAndDiffer(t *testing.T) {
	if !constantTimeEqual([]byte("aa"), []byte("aa")) {
		t.Fatal("identical inputs must compare equal")
	}
	if constantTimeEqual([]byte("aa"), []byte("ab")) {
		t.Fatal("different inputs of same length must not compare equal")
	}
}

// TestEqual covers the exported String.Equal contract end-to-end: equal and
// differing secrets, self-comparison, the documented nil/uninitialised/zeroed
// "empty" cases, and that two distinct Strings holding the same plaintext are
// compared by value (not identity).
func TestEqual(t *testing.T) {
	t.Run("equal content", func(t *testing.T) {
		a := NewFromString(plain)
		b := NewFromString(plain)
		assert.True(t, a.Equal(b))
		assert.True(t, b.Equal(a), "Equal must be symmetric")
	})

	t.Run("different content", func(t *testing.T) {
		a := NewFromString(plain)
		b := NewFromString("other-secret-token")
		assert.False(t, a.Equal(b))
		assert.False(t, b.Equal(a), "Equal must be symmetric")
	})

	t.Run("self comparison", func(t *testing.T) {
		a := NewFromString(plain)
		assert.True(t, a.Equal(a), "a secret must compare equal to itself")
	})

	t.Run("differing length is unequal", func(t *testing.T) {
		a := NewFromString("abc")
		b := NewFromString("abcd")
		assert.False(t, a.Equal(b))
	})

	t.Run("nil receiver equals nil argument", func(t *testing.T) {
		var a, b *String
		assert.True(t, a.Equal(b), "two nil receivers are both empty and equal")
	})

	t.Run("nil receiver equals empty secret", func(t *testing.T) {
		var a *String
		b := NewFromString("")
		assert.True(t, a.Equal(b), "nil and empty are both treated as empty")
		assert.True(t, b.Equal(a), "empty and nil are both treated as empty")
	})

	t.Run("uninitialised (zero value) equals empty", func(t *testing.T) {
		var a String // never went through New: inner is nil
		b := NewFromString("")
		assert.True(t, a.Equal(b), "an uninitialised String is treated as empty")
	})

	t.Run("nil receiver differs from non-empty", func(t *testing.T) {
		var a *String
		b := NewFromString(plain)
		assert.False(t, a.Equal(b))
		assert.False(t, b.Equal(a))
	})

	t.Run("zeroed secret equals empty", func(t *testing.T) {
		a := NewFromString(plain)
		a.Zero()
		b := NewFromString("")
		assert.True(t, a.Equal(b), "a zeroed secret is empty")
	})

	t.Run("comparison does not mutate operands", func(t *testing.T) {
		a := NewFromString(plain)
		b := NewFromString(plain)
		_ = a.Equal(b)
		// Equal wipes its temporary copies, not the wrapped secrets.
		assert.Equal(t, plain, a.RevealString(), "Equal must not zero the wrapped secret")
		assert.Equal(t, plain, b.RevealString(), "Equal must not zero the wrapped secret")
	})
}
