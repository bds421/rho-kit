//go:build integration

// Postgres roundtrip test: proves the ciphertext format produced by
// FieldEncryptor stores cleanly in a TEXT column and decrypts after
// SELECT. Catches the bug class where defence-in-depth bytes (such
// as the leading "\x00" the v1 format used) silently break the
// only datatype most users actually pick.
//
// Run with:
//
//	go test -tags=integration ./crypto/encrypt/integrationtest/...

package encrypt_test

import (
	"context"
	"net"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/crypto/v2/encrypt"
	"github.com/bds421/rho-kit/infra/sqldb/dbtest/v2"
)

func startPostgres(t *testing.T) string {
	t.Helper()
	cfg := dbtest.StartPostgres(t, "encrypt")
	q := url.Values{}
	for k, v := range cfg.Options {
		q.Set(k, v)
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(cfg.User, cfg.Password),
		Host:     net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Path:     cfg.Name,
		RawQuery: q.Encode(),
	}
	return u.String()
}

func TestIntegration_FieldEncryptor_RoundtripsThroughPostgresTEXT(t *testing.T) {
	dsn := startPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	_, err = pool.Exec(ctx, "CREATE TABLE secrets (id INT PRIMARY KEY, val TEXT NOT NULL)")
	require.NoError(t, err)

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	enc, err := encrypt.NewFieldEncryptor(key)
	require.NoError(t, err)

	// Cover plaintexts that previously triggered the null-byte bug
	// from different angles: short, multi-line, multibyte UTF-8, and a
	// payload containing literal NULs (the encryptor accepts any byte
	// sequence as plaintext — only the ciphertext output needs to be
	// TEXT-safe).
	cases := []struct {
		id   int
		want string
	}{
		{1, "alice@example.com"},
		{2, "line one\nline two"},
		{3, "multibyte 🔒 emoji"},
		{4, string([]byte{0, 0, 0, 0, 0, 'X'})},
	}

	for _, c := range cases {
		ct, err := enc.Encrypt(c.want)
		require.NoError(t, err, "encrypt id=%d", c.id)

		_, err = pool.Exec(ctx,
			"INSERT INTO secrets (id, val) VALUES ($1, $2)",
			c.id, ct,
		)
		require.NoErrorf(t, err, "insert id=%d (this is the bug v1 hit — TEXT columns reject \\x00)", c.id)

		var stored string
		require.NoError(t, pool.QueryRow(ctx, "SELECT val FROM secrets WHERE id = $1", c.id).Scan(&stored))
		assert.Equal(t, ct, stored, "stored ciphertext must round-trip byte-for-byte")

		got, err := enc.Decrypt(stored)
		require.NoErrorf(t, err, "decrypt id=%d", c.id)
		assert.Equal(t, c.want, got, "decrypted plaintext mismatch id=%d", c.id)
	}
}
