package kekstatic_test

import (
	"bytes"
	"testing"

	"github.com/bds421/rho-kit/crypto/v2/envelope/kekstatic"
	"github.com/bds421/rho-kit/crypto/v2/envelope/kektest"
)

func TestTheStaticKEKConforms(t *testing.T) {
	k, err := kekstatic.NewKEK("k1", bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	kektest.Conform(t, k)
}
