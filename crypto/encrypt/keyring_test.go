package encrypt_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/bds421/rho-kit/crypto/v2/encrypt"
)

var (
	first  = encrypt.Key{ID: "1", Key: bytes.Repeat([]byte{1}, 32)}
	second = encrypt.Key{ID: "2", Key: bytes.Repeat([]byte{2}, 32)}
)

func TestKeyring_NewestSealsEveryKeyOpens(t *testing.T) {
	old, err := encrypt.NewKeyring(first)
	if err != nil {
		t.Fatal(err)
	}
	sealedByOld, err := encrypt.EncryptBytesAAD(old, []byte("secret"), []byte("aad"))
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := encrypt.NewKeyring(second, first)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Current(sealedByOld) {
		t.Fatal("a value the older key sealed counts as current")
	}
	if plain, err := encrypt.DecryptBytesAAD(rotated, sealedByOld, []byte("aad")); err != nil || string(plain) != "secret" {
		t.Fatalf("the older key's value opened as %q (%v)", plain, err)
	}
	sealed, err := encrypt.EncryptBytesAAD(rotated, []byte("secret"), []byte("aad"))
	if err != nil || !rotated.Current(sealed) || !bytes.HasPrefix(sealed, []byte("2.")) {
		t.Fatalf("sealed %q (%v)", sealed, err)
	}
	if _, err := old.Decrypt(sealed, []byte("aad")); !errors.Is(err, encrypt.ErrUnknownKey) {
		t.Fatalf("a ring without the newer key opened its value: %v", err)
	}
	if _, err := rotated.Decrypt(sealed, []byte("other")); err == nil {
		t.Fatal("a value opened for another binding")
	}
	if _, err := rotated.Decrypt([]byte("no key"), nil); err == nil {
		t.Fatal("a ciphertext without a key ID opened")
	}
}

func TestKeyring_RefusesInvalidRings(t *testing.T) {
	for _, keys := range [][]encrypt.Key{nil, {first, first}, {{ID: "a.b", Key: first.Key}}, {{ID: "3", Key: []byte("short")}}} {
		if _, err := encrypt.NewKeyring(keys...); err == nil {
			t.Fatalf("%v was accepted", keys)
		}
	}
}
