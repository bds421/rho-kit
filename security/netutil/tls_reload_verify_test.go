package netutil

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// What the caller sets on the config after ReloadingServerTLS (here
// VerifyConnection) decides every real mTLS handshake, before and after
// the files rotate.
func TestReloadingServerTLS_KeepsTheCallersChecksAcrossReloads(t *testing.T) {
	files, rotate := generateRotatableTLSFixture(t)
	src, err := NewFilesCertificateSource(files)
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })

	var admit atomic.Bool
	cfg := ReloadingServerTLS(src)
	cfg.VerifyConnection = func(tls.ConnectionState) error {
		if !admit.Load() {
			return errors.New("not admitted")
		}
		return nil
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.TLS = cfg
	srv.StartTLS()
	t.Cleanup(srv.Close)

	get := func() error {
		client, err := files.ClientTLS()
		require.NoError(t, err)
		client.ServerName = "rotatable-test"
		client.InsecureSkipVerify = true // the fixture's certificate names no host; the test is about the server's check
		c := &http.Client{Transport: &http.Transport{TLSClientConfig: client, DisableKeepAlives: true}}
		resp, err := c.Get(srv.URL)
		if err == nil {
			_ = resp.Body.Close()
		}
		return err
	}
	require.Error(t, get(), "the caller's check refuses")
	admit.Store(true)
	require.NoError(t, get())

	rotate(t)
	require.NoError(t, src.Reload())
	admit.Store(false)
	require.Error(t, get(), "the check still refuses after a reload")
	admit.Store(true)
	require.NoError(t, get(), "the reloaded certificate and CA serve")
}
