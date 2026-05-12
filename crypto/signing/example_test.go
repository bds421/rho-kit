package signing_test

import (
	"fmt"
	"time"

	"github.com/bds421/rho-kit/crypto/v2/signing"
)

func ExampleSign() {
	fixed := time.Unix(1700000000, 0)
	signer := signing.NewSigner(signing.WithClock(func() time.Time { return fixed }))

	secret := signing.Secret("0123456789abcdef0123456789abcdef")
	body := []byte(`{"event":"ping"}`)

	sig, ts, err := signer.Sign(secret, body)
	if err != nil {
		fmt.Println("err:", err)
		return
	}
	fmt.Println(ts)
	fmt.Println(sig)
	// Output:
	// 1700000000
	// sha256=d87c3110177ea3d0b3d41701f4a66541fcd9cb4a8f0360c5428c6edebbba15f3
}
