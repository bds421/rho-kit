package oauth2_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/auth/oauth2/v2"
	"github.com/bds421/rho-kit/core/v2/secret"
	"github.com/bds421/rho-kit/observability/v2/health"
)

func TestClientCredentialsCachesToken(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, "POST", r.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"opaque-token","token_type":"Bearer","expires_in":3600}`))
	}))
	defer srv.Close()

	source, err := oauth2.NewClientCredentials(oauth2.ClientCredentialsConfig{TokenURL: srv.URL, ClientID: "svc", ClientSecret: secret.NewFromString("secret")})
	require.NoError(t, err)
	first, err := source.Token(context.Background())
	require.NoError(t, err)
	second, err := source.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "opaque-token", first.AccessToken)
	assert.Equal(t, first.AccessToken, second.AccessToken)
	assert.Equal(t, int32(1), calls.Load())
}

func TestClientCredentialsSharesRefreshAcrossConcurrentCallers(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"opaque-token","token_type":"Bearer","expires_in":3600}`))
	}))
	defer srv.Close()

	source, err := oauth2.NewClientCredentials(oauth2.ClientCredentialsConfig{TokenURL: srv.URL, ClientID: "svc", ClientSecret: secret.NewFromString("secret")})
	require.NoError(t, err)

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := source.Token(context.Background())
			errs <- err
		}()
	}
	<-started
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, int32(1), calls.Load())
}

func TestClientCredentialsWaitingCallerHonorsItsDeadline(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"opaque-token","token_type":"Bearer","expires_in":3600}`))
	}))
	defer srv.Close()

	source, err := oauth2.NewClientCredentials(oauth2.ClientCredentialsConfig{TokenURL: srv.URL, ClientID: "svc", ClientSecret: secret.NewFromString("secret")})
	require.NoError(t, err)
	leaderDone := make(chan error, 1)
	go func() { _, err := source.Token(context.Background()); leaderDone <- err }()
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = source.Token(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	close(release)
	require.NoError(t, <-leaderDone)
}

func TestClientCredentialsHealthAndMetrics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"opaque-token","token_type":"Bearer","expires_in":3600}`))
	}))
	defer srv.Close()
	reg := prometheus.NewRegistry()
	metrics := oauth2.NewClientCredentialsMetrics(oauth2.WithClientCredentialsRegisterer(reg))
	source, err := oauth2.NewClientCredentials(oauth2.ClientCredentialsConfig{TokenURL: srv.URL, ClientID: "svc", ClientSecret: secret.NewFromString("secret")}, oauth2.WithClientCredentialsMetrics(metrics))
	require.NoError(t, err)

	check := source.HealthCheck()
	assert.Equal(t, health.StatusHealthy, check.Check(context.Background()))
	_, err = source.Token(context.Background())
	require.NoError(t, err)
	families, err := reg.Gather()
	require.NoError(t, err)
	assert.Len(t, families, 4)
}

func TestClientCredentialsLifecycleStartStopsWithContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"opaque-token","token_type":"Bearer","expires_in":3600}`))
	}))
	defer srv.Close()
	source, err := oauth2.NewClientCredentials(oauth2.ClientCredentialsConfig{TokenURL: srv.URL, ClientID: "svc", ClientSecret: secret.NewFromString("secret")})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- source.Start(ctx) }()
	require.Eventually(t, func() bool {
		return source.HealthCheck().Check(context.Background()) == health.StatusHealthy
	}, time.Second, 10*time.Millisecond)
	cancel()
	require.NoError(t, <-done)
	require.NoError(t, source.Stop(context.Background()))
}
