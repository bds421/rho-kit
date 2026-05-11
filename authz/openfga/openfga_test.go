package openfga

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openfga/go-sdk/credentials"

	"github.com/bds421/rho-kit/authz/v2"
)

const testStoreID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

var _ slog.LogValuer = Config{}

func TestConfig_LogValue_RedactsCredentialsAndHeaders(t *testing.T) {
	cfg := Config{
		APIURL:    "https://token-user:url-secret@tenant-openfga.internal?token=query-secret#frag",
		StoreID:   testStoreID,
		ModelID:   "tenant-model-secret",
		UserAgent: "orders-service-secret",
		Credentials: &credentials.Credentials{
			Method: credentials.CredentialsMethodApiToken,
			Config: &credentials.Config{ApiToken: "openfga-api-token-secret"},
		},
		DefaultHeaders: map[string]string{
			"Authorization": "Bearer header-secret",
			"X-Api-Key":     "api-key-secret",
		},
	}

	rendered := cfg.LogValue().String()

	for _, secret := range []string{
		"tenant-openfga.internal",
		"token-user",
		"url-secret",
		"query-secret",
		"openfga-api-token-secret",
		"header-secret",
		"api-key-secret",
		testStoreID,
		"tenant-model-secret",
		"orders-service-secret",
		"Authorization",
		"X-Api-Key",
	} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("LogValue leaked %q in %q", secret, rendered)
		}
	}
	for _, expected := range []string{
		"api_url_configured=true",
		"api_url_valid=true",
		"api_host_configured=true",
		"store_id_configured=true",
		"model_id_configured=true",
		"credentials_configured=true",
		"default_headers_configured=true",
		"user_agent_configured=true",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("LogValue %q missing %q", rendered, expected)
		}
	}
}

func TestClientConfiguration_DefaultsHTTPClientTimeout(t *testing.T) {
	cfg := clientConfiguration(Config{
		APIURL:  "https://openfga.test",
		StoreID: testStoreID,
	})

	if cfg.HTTPClient == nil {
		t.Fatal("expected default HTTP client")
	}
	if cfg.HTTPClient.Timeout != defaultHTTPClientTimeout {
		t.Fatalf("HTTP client timeout = %s, want %s", cfg.HTTPClient.Timeout, defaultHTTPClientTimeout)
	}
	tr, ok := cfg.HTTPClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("default transport type = %T, want *http.Transport", cfg.HTTPClient.Transport)
	}
	if tr.MaxIdleConnsPerHost != defaultMaxIdleConnsPerHost {
		t.Fatalf("MaxIdleConnsPerHost = %d, want %d", tr.MaxIdleConnsPerHost, defaultMaxIdleConnsPerHost)
	}
	if tr.MaxResponseHeaderBytes != defaultMaxResponseHeaderBytes {
		t.Fatalf("MaxResponseHeaderBytes = %d, want %d", tr.MaxResponseHeaderBytes, defaultMaxResponseHeaderBytes)
	}
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.MinVersion != minimumHTTPClientTLSVersion {
		t.Fatalf("TLS MinVersion = %v, want %x", tr.TLSClientConfig, minimumHTTPClientTLSVersion)
	}
}

func TestClientConfiguration_DefaultHTTPClientBlocksRedirects(t *testing.T) {
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://openfga-redirect.test", http.StatusFound)
	}))
	defer redirector.Close()

	cfg := clientConfiguration(Config{
		APIURL:  "https://openfga.test",
		StoreID: testStoreID,
	})

	resp, err := cfg.HTTPClient.Get(redirector.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, ErrRedirectBlocked) {
		t.Fatalf("Get redirect error = %v, want ErrRedirectBlocked", err)
	}
}

func TestDefaultHTTPClient_ClonesTLSConfigAndEnforcesFloor(t *testing.T) {
	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })

	base := http.DefaultTransport.(*http.Transport).Clone()
	cfg := &tls.Config{
		MinVersion: minimumHTTPClientTLSVersion - 1,
		NextProtos: []string{"h2"},
		ServerName: "openfga.internal.test",
	}
	base.TLSClientConfig = cfg
	http.DefaultTransport = base

	client := defaultHTTPClient()
	cfg.NextProtos[0] = "mutated"
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if tr.TLSClientConfig == cfg {
		t.Fatal("default client must own a cloned TLS config")
	}
	if cfg.MinVersion != minimumHTTPClientTLSVersion-1 {
		t.Fatalf("caller TLS config was mutated: got MinVersion %x", cfg.MinVersion)
	}
	if tr.TLSClientConfig.MinVersion != minimumHTTPClientTLSVersion {
		t.Fatalf("expected TLS floor %x, got %x", minimumHTTPClientTLSVersion, tr.TLSClientConfig.MinVersion)
	}
	if tr.TLSClientConfig.ServerName != "openfga.internal.test" {
		t.Fatalf("expected ServerName to be preserved, got %q", tr.TLSClientConfig.ServerName)
	}
	if got := tr.TLSClientConfig.NextProtos; len(got) == 0 || got[0] != "h2" {
		t.Fatalf("expected NextProtos to be detached, got %#v", got)
	}
}

func TestDefaultHTTPClient_PanicsOnTLSMaxVersionBelowFloor(t *testing.T) {
	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })

	base := http.DefaultTransport.(*http.Transport).Clone()
	base.TLSClientConfig = &tls.Config{MaxVersion: minimumHTTPClientTLSVersion - 1}
	http.DefaultTransport = base

	defer func() {
		rec := recover()
		if rec == nil {
			t.Fatal("expected panic")
		}
		if rec != "openfga: default HTTP client TLS MaxVersion must allow TLS 1.2 or newer" {
			t.Fatalf("panic = %v", rec)
		}
	}()
	_ = defaultHTTPClient()
}

func TestClientConfiguration_CustomHTTPClientBlocksRedirectsByDefault(t *testing.T) {
	custom := &http.Client{Timeout: 2 * time.Second}

	cfg := clientConfiguration(Config{
		APIURL:     "https://openfga.test",
		StoreID:    testStoreID,
		HTTPClient: custom,
	})

	if cfg.HTTPClient == custom {
		t.Fatal("expected custom HTTP client without redirect policy to be cloned")
	}
	if custom.CheckRedirect != nil {
		t.Fatal("clientConfiguration must not mutate the caller's HTTP client")
	}

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://openfga-redirect.test", http.StatusFound)
	}))
	defer redirector.Close()

	resp, err := cfg.HTTPClient.Get(redirector.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, ErrRedirectBlocked) {
		t.Fatalf("Get redirect error = %v, want ErrRedirectBlocked", err)
	}
}

func TestClientConfiguration_CustomHTTPClientFillsMissingTimeout(t *testing.T) {
	transport := &staticOpenFGATransport{}
	custom := &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	cfg := clientConfiguration(Config{
		APIURL:     "https://openfga.test",
		StoreID:    testStoreID,
		HTTPClient: custom,
	})

	if cfg.HTTPClient == custom {
		t.Fatal("expected custom HTTP client with zero timeout to be cloned")
	}
	if custom.Timeout != 0 {
		t.Fatal("clientConfiguration must not mutate the caller's timeout")
	}
	if cfg.HTTPClient.Timeout != defaultHTTPClientTimeout {
		t.Fatalf("HTTP client timeout = %s, want %s", cfg.HTTPClient.Timeout, defaultHTTPClientTimeout)
	}
	if cfg.HTTPClient.Transport != transport {
		t.Fatal("expected custom transport to be preserved")
	}
	if cfg.HTTPClient.CheckRedirect == nil {
		t.Fatal("expected custom redirect policy to be preserved")
	}
}

func TestClientConfiguration_PreservesCustomHTTPClientWithExplicitRedirectPolicy(t *testing.T) {
	transport := openFGARoundTripper(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})
	custom := &http.Client{
		Timeout:   2 * time.Second,
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	cfg := clientConfiguration(Config{
		APIURL:     "https://openfga.test",
		StoreID:    testStoreID,
		HTTPClient: custom,
	})

	if cfg.HTTPClient != custom {
		t.Fatal("expected explicit redirect policy to preserve custom HTTP client")
	}
}

func TestClientConfiguration_CustomHTTPClientFillsMissingTransport(t *testing.T) {
	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })
	http.DefaultTransport = openFGARoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("global default transport used")
	})

	custom := &http.Client{
		Timeout: 2 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	cfg := clientConfiguration(Config{
		APIURL:     "https://openfga.test",
		StoreID:    testStoreID,
		HTTPClient: custom,
	})

	if cfg.HTTPClient == custom {
		t.Fatal("expected custom client with nil transport to be cloned")
	}
	if custom.Transport != nil {
		t.Fatal("clientConfiguration must not mutate caller transport")
	}
	if _, ok := cfg.HTTPClient.Transport.(*http.Transport); !ok {
		t.Fatalf("transport = %T, want *http.Transport fallback", cfg.HTTPClient.Transport)
	}
}

type openFGARoundTripper func(*http.Request) (*http.Response, error)

func (f openFGARoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type staticOpenFGATransport struct{}

func (*staticOpenFGATransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
}

func TestNew_RejectsInsecureHTTPByDefault(t *testing.T) {
	_, err := New(Config{
		APIURL:  "http://openfga.test",
		StoreID: testStoreID,
	})
	if err == nil {
		t.Fatal("expected insecure HTTP APIURL to be rejected")
	}
	if got := err.Error(); got != "openfga: APIURL must use https unless AllowInsecureHTTP is set" {
		t.Fatalf("error = %q", got)
	}
}

func TestNew_AllowsInsecureHTTPWithExplicitOptIn(t *testing.T) {
	d, err := New(Config{
		APIURL:            "http://openfga.test",
		StoreID:           testStoreID,
		AllowInsecureHTTP: true,
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if d == nil {
		t.Fatal("expected decider")
	}
}

func TestNew_RejectsInvalidAPIURL(t *testing.T) {
	cases := []string{
		"://bad",
		"openfga.test",
		"ftp://openfga.test",
		"https://token@openfga.test",
		"https://openfga.test?token=secret",
		"https://openfga.test#fragment",
	}
	for _, apiURL := range cases {
		t.Run(apiURL, func(t *testing.T) {
			_, err := New(Config{APIURL: apiURL, StoreID: testStoreID})
			if err == nil {
				t.Fatal("expected invalid APIURL to be rejected")
			}
		})
	}
}

func TestNew_InvalidAPIURLParseErrorDoesNotEchoValue(t *testing.T) {
	_, err := New(Config{
		APIURL:  "https://openfga.test/%zz?token=secret-token",
		StoreID: testStoreID,
	})
	if err == nil {
		t.Fatal("expected invalid APIURL to be rejected")
	}
	if !strings.Contains(err.Error(), "APIURL is invalid") {
		t.Fatalf("error %q does not identify invalid APIURL", err.Error())
	}
	if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "token=") || strings.Contains(err.Error(), "%zz") {
		t.Fatalf("error leaked APIURL value: %q", err.Error())
	}
}

func TestNew_UnsupportedAPIURLSchemeDoesNotEchoValue(t *testing.T) {
	_, err := New(Config{
		APIURL:  "secret-token-scheme://openfga.test",
		StoreID: testStoreID,
	})
	if err == nil {
		t.Fatal("expected unsupported APIURL scheme to be rejected")
	}
	if !strings.Contains(err.Error(), "APIURL scheme is not supported") {
		t.Fatalf("error %q does not identify unsupported scheme", err.Error())
	}
	if strings.Contains(err.Error(), "secret-token-scheme") {
		t.Fatalf("error leaked APIURL scheme: %q", err.Error())
	}
}

func TestNew_RejectsMalformedDefaultHeaders(t *testing.T) {
	cases := []map[string]string{
		{"Bad Header secret-token": "value"},
		{"X-Secret-Token": "bad\r\nvalue"},
		{"X-Secret-Token": "bad\x00value"},
	}
	for _, headers := range cases {
		_, err := New(Config{
			APIURL:         "https://openfga.test",
			StoreID:        testStoreID,
			DefaultHeaders: headers,
		})
		if err == nil {
			t.Fatalf("expected headers %v to be rejected", headers)
		}
		if strings.Contains(strings.ToLower(err.Error()), "secret-token") {
			t.Fatalf("error leaked default-header metadata: %q", err.Error())
		}
	}
}

func TestClientConfiguration_ClonesDefaultHeaders(t *testing.T) {
	headers := map[string]string{"X-Trace-ID": "before"}
	cfg := clientConfiguration(Config{
		APIURL:         "https://openfga.test",
		StoreID:        testStoreID,
		DefaultHeaders: headers,
	})
	headers["X-Trace-ID"] = "after"
	headers["X-New"] = "new"

	if cfg.DefaultHeaders["X-Trace-ID"] != "before" {
		t.Fatalf("default header was not cloned: %v", cfg.DefaultHeaders)
	}
	if _, ok := cfg.DefaultHeaders["X-New"]; ok {
		t.Fatalf("new caller header leaked into config: %v", cfg.DefaultHeaders)
	}
}

func TestAllow_NilDeciderReturnsInitializationError(t *testing.T) {
	var d *Decider
	err := d.Allow(context.Background(), "user:alice", "read", "doc:1")
	if err == nil {
		t.Fatal("expected nil decider to return an error")
	}
}

func TestAllow_RejectsEmptyInputsAsDenied(t *testing.T) {
	d, err := New(Config{
		APIURL:  "https://openfga.test",
		StoreID: testStoreID,
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	err = d.Allow(context.Background(), "", "read", "doc:1")
	if !errors.Is(err, authz.ErrDenied) {
		t.Fatalf("expected ErrDenied for empty input, got %v", err)
	}
	if !errors.Is(err, authz.ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest for empty input, got %v", err)
	}
}

func TestAllow_RejectsMalformedInputsAsDenied(t *testing.T) {
	d, err := New(Config{
		APIURL:  "https://openfga.test",
		StoreID: testStoreID,
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	err = d.Allow(context.Background(), "user:alice", "read all", "doc:1")
	if !errors.Is(err, authz.ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest for malformed input, got %v", err)
	}
	if !errors.Is(err, authz.ErrDenied) {
		t.Fatalf("expected ErrDenied for malformed input, got %v", err)
	}
}

func TestAllow_RejectsNilContext(t *testing.T) {
	d, err := New(Config{
		APIURL:  "https://openfga.test",
		StoreID: testStoreID,
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	var ctx context.Context
	err = d.Allow(ctx, "user:alice", "read", "doc:1")
	if err == nil {
		t.Fatal("expected nil context to return an error")
	}
	if got := err.Error(); got != "openfga: context must not be nil" {
		t.Fatalf("error = %q", got)
	}
}
