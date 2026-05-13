package passhash_test

import (
	"fmt"
	"strings"

	"github.com/bds421/rho-kit/crypto/v2/passhash"
)

func ExampleHash() {
	params := passhash.Params{Memory: 64 * 1024, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 16}
	encoded, err := passhash.Hash("correct-horse-battery-staple", params)
	if err != nil {
		fmt.Println("err:", err)
		return
	}
	fmt.Println(strings.HasPrefix(encoded, "$argon2id$v=19$"))
	// Output:
	// true
}

func ExampleVerify() {
	params := passhash.Params{Memory: 64 * 1024, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 16}
	encoded, err := passhash.Hash("correct-horse-battery-staple", params)
	if err != nil {
		fmt.Println("hash err:", err)
		return
	}

	good, err := passhash.Verify("correct-horse-battery-staple", encoded, params)
	if err != nil {
		fmt.Println("verify err:", err)
		return
	}
	bad, err := passhash.Verify("wrong-password", encoded, params)
	if err != nil {
		fmt.Println("verify err:", err)
		return
	}

	fmt.Println("matched:", good.Matched, "needs-rehash:", good.NeedsRehash)
	fmt.Println("bad-matched:", bad.Matched)
	// Output:
	// matched: true needs-rehash: false
	// bad-matched: false
}
