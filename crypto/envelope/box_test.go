package envelope_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/crypto/v2/envelope"
)

type card struct {
	Number string `json:"number"`
}

func TestABoxOpensOnlyWithItsPurposeAndBinding(t *testing.T) {
	ctx := context.Background()
	kek := newKEK(t, "box-1")
	box := envelope.NewBox(kek, "billing/card/v1")
	sealed, err := envelope.SealJSON(ctx, box, card{Number: "4111"}, "t1", "c1")
	require.NoError(t, err)

	got, err := envelope.OpenJSON[card](ctx, box, sealed, "t1", "c1")
	require.NoError(t, err)
	assert.Equal(t, "4111", got.Number)

	_, err = envelope.OpenJSON[card](ctx, box, sealed, "t2", "c1")
	assert.Error(t, err, "another tenant")
	_, err = envelope.OpenJSON[card](ctx, envelope.NewBox(kek, "billing/token/v1"), sealed, "t1", "c1")
	assert.Error(t, err, "another purpose")

	// The binding is the caller AAD purpose/parts, so values sealed by an
	// Encryptor with that AAD open in a Box and the other way round.
	raw, err := envelope.NewEncryptor(kek).Encrypt(ctx, []byte("plain"), []byte("billing/card/v1/t1/c1"))
	require.NoError(t, err)
	plain, err := box.Open(ctx, raw, "t1", "c1")
	require.NoError(t, err)
	assert.Equal(t, "plain", string(plain))

	_, err = envelope.SealJSON[card](ctx, nil, card{})
	assert.ErrorIs(t, err, envelope.ErrNilBox)
	assert.Panics(t, func() { envelope.NewBox(kek, "") })
}
