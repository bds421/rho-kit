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

	_, err = envelope.SealJSON[card](ctx, nil, card{})
	assert.ErrorIs(t, err, envelope.ErrNilBox)
	assert.Panics(t, func() { envelope.NewBox(kek, "") })
	assert.Panics(t, func() { envelope.NewBox(kek, "\x00p") })
}

func TestABoxKeepsEachBindingElementApart(t *testing.T) {
	ctx := context.Background()
	box := envelope.NewBox(newKEK(t, "box-1"), "p")
	sealed, err := box.Seal(ctx, []byte("plain"), "a/b")
	require.NoError(t, err)

	_, err = box.Open(ctx, sealed, "a", "b")
	assert.ErrorIs(t, err, envelope.ErrAuthFailed)
	other, err := box.Seal(ctx, []byte("plain"), "a")
	require.NoError(t, err)
	_, err = box.Open(ctx, other, "a", "")
	assert.ErrorIs(t, err, envelope.ErrAuthFailed)
	plain, err := box.Open(ctx, sealed, "a/b")
	require.NoError(t, err)
	assert.Equal(t, "plain", string(plain))
}

func TestABoxRefusesValuesSealedWithTheJoinedBinding(t *testing.T) {
	ctx := context.Background()
	kek := newKEK(t, "box-1")
	joined, err := envelope.NewEncryptor(kek).Encrypt(ctx, []byte("plain"), []byte("billing/card/v1/t1/c1"))
	require.NoError(t, err)

	_, err = envelope.NewBox(kek, "billing/card/v1").Open(ctx, joined, "t1", "c1")
	assert.ErrorIs(t, err, envelope.ErrAuthFailed)
}

func TestABoxValueDoesNotOpenUnderTheJoinedBinding(t *testing.T) {
	ctx := context.Background()
	kek := newKEK(t, "box-1")
	sealed, err := envelope.NewBox(kek, "billing/card/v1").Seal(ctx, []byte("plain"), "t1", "c1")
	require.NoError(t, err)

	_, err = envelope.NewEncryptor(kek).Decrypt(ctx, sealed, []byte("billing/card/v1/t1/c1"))
	assert.ErrorIs(t, err, envelope.ErrAuthFailed)

	documented := []byte("\x00envelope.Box/v2\x00\x0fbilling/card/v1\x02t1\x02c1")
	plain, err := envelope.NewEncryptor(kek).Decrypt(ctx, sealed, documented)
	require.NoError(t, err)
	assert.Equal(t, "plain", string(plain))
}
