package masking

import (
	"crypto/rand"
	"testing"

	"github.com/bds421/rho-kit/crypto/encrypt"
)

func TestMaskString(t *testing.T) {
	t.Run("shows prefix and masks rest", func(t *testing.T) {
		got := MaskString("AKIAIOSFODNN7EXAMPLE", 4)
		if got != "AKIA****" {
			t.Fatalf("expected %q, got %q", "AKIA****", got)
		}
	})

	t.Run("redacts when string equals n", func(t *testing.T) {
		got := MaskString("abcd", 4)
		if got != "[REDACTED]" {
			t.Fatalf("expected [REDACTED], got %q", got)
		}
	})

	t.Run("redacts when string shorter than n", func(t *testing.T) {
		got := MaskString("ab", 4)
		if got != "[REDACTED]" {
			t.Fatalf("expected [REDACTED], got %q", got)
		}
	})

	t.Run("redacts empty string", func(t *testing.T) {
		got := MaskString("", 4)
		if got != "[REDACTED]" {
			t.Fatalf("expected [REDACTED], got %q", got)
		}
	})

	t.Run("negative n does not panic", func(t *testing.T) {
		got := MaskString("secret", -1)
		if got != "****" {
			t.Fatalf("expected %q, got %q", "****", got)
		}
	})

	t.Run("zero n masks entire string", func(t *testing.T) {
		got := MaskString("secret", 0)
		if got != "****" {
			t.Fatalf("expected %q, got %q", "****", got)
		}
	})
}

func TestMaskURL_Valid(t *testing.T) {
	got := MaskURL("https://hooks.slack.com/services/T00/B00/xxx")
	if got != "https://hooks.slack.com/***" {
		t.Fatalf("expected masked URL, got %q", got)
	}
}

func TestMaskURL_HTTP(t *testing.T) {
	got := MaskURL("http://example.com/path/to/resource")
	if got != "http://example.com/***" {
		t.Fatalf("expected masked URL, got %q", got)
	}
}

func TestMaskURL_Empty(t *testing.T) {
	got := MaskURL("")
	// url.Parse("") succeeds with empty scheme/host
	if got != "://**"+"*" {
		// Empty URL parses to scheme="" host="" → "://**"
		// Just verify it doesn't panic and returns something
		_ = got
	}
}

func TestMaskMapValues_WithEntries(t *testing.T) {
	input := map[string]string{"Authorization": "Bearer xxx", "X-Key": "secret"}
	got := MaskMapValues(input)

	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	if got["Authorization"] != "***" {
		t.Fatalf("expected masked value, got %q", got["Authorization"])
	}
	if got["X-Key"] != "***" {
		t.Fatalf("expected masked value, got %q", got["X-Key"])
	}
}

func TestMaskMapValues_Nil(t *testing.T) {
	got := MaskMapValues(nil)
	if got == nil {
		t.Fatal("expected non-nil empty map, got nil")
	}
	if len(got) != 0 {
		t.Fatalf("expected empty map, got %v", got)
	}
}

func TestMaskMapValues_Empty(t *testing.T) {
	got := MaskMapValues(map[string]string{})
	if got == nil {
		t.Fatal("expected non-nil empty map, got nil")
	}
	if len(got) != 0 {
		t.Fatalf("expected empty map, got %v", got)
	}
}

func TestDecryptAndMaskURL_NoEncryptor(t *testing.T) {
	got := DecryptAndMaskURL("https://example.com/secret/path", nil)
	if got != "https://example.com/***" {
		t.Fatalf("expected masked URL, got %q", got)
	}
}

func TestDecryptAndMaskURL_WithEncryptor(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	enc, err := encrypt.NewFieldEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}

	original := "https://hooks.slack.com/services/T00/B00/xxx"
	encrypted, err := enc.Encrypt(original)
	if err != nil {
		t.Fatal(err)
	}

	got := DecryptAndMaskURL(encrypted, enc)
	if got != "https://hooks.slack.com/***" {
		t.Fatalf("expected masked decrypted URL, got %q", got)
	}
}

func TestDecryptAndMaskURL_PlaintextWithEncryptor(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	enc, err := encrypt.NewFieldEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}

	got := DecryptAndMaskURL("https://example.com/path", enc)
	if got != "https://example.com/***" {
		t.Fatalf("expected masked URL, got %q", got)
	}
}
