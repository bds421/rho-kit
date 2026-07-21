package tlsclone

import (
	"bytes"
	"crypto/tls"
	"errors"
	"testing"
)

func TestConfigWithFloor_ClonesMutableFieldsAndEnforcesFloor(t *testing.T) {
	cfg := &tls.Config{
		MinVersion:                     tls.VersionTLS10,
		NextProtos:                     []string{"h2"},
		CipherSuites:                   []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256},
		CurvePreferences:               []tls.CurveID{tls.CurveP256},
		EncryptedClientHelloConfigList: []byte{1, 2, 3},
		EncryptedClientHelloKeys: []tls.EncryptedClientHelloKey{{
			Config:     []byte{4, 5},
			PrivateKey: []byte{6, 7},
		}},
		Certificates: []tls.Certificate{{
			Certificate:                  [][]byte{{1, 2, 3}},
			SupportedSignatureAlgorithms: []tls.SignatureScheme{tls.PKCS1WithSHA256},
			OCSPStaple:                   []byte{4, 5, 6},
			SignedCertificateTimestamps:  [][]byte{{7, 8, 9}},
		}},
	}

	cloned, err := ConfigWithFloor(cfg, tls.VersionTLS12)
	if err != nil {
		t.Fatalf("ConfigWithFloor: %v", err)
	}
	if cloned == cfg {
		t.Fatal("expected detached config")
	}

	cfg.NextProtos[0] = "http/1.1"
	cfg.CipherSuites[0] = tls.TLS_RSA_WITH_AES_128_CBC_SHA
	cfg.CurvePreferences[0] = tls.CurveP384
	cfg.EncryptedClientHelloConfigList[0] = 9
	cfg.EncryptedClientHelloKeys[0].Config[0] = 9
	cfg.EncryptedClientHelloKeys[0].PrivateKey[0] = 9
	cfg.Certificates[0].Certificate[0][0] = 9
	cfg.Certificates[0].SupportedSignatureAlgorithms[0] = tls.PSSWithSHA256
	cfg.Certificates[0].OCSPStaple[0] = 9
	cfg.Certificates[0].SignedCertificateTimestamps[0][0] = 9

	if cloned.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %x, want TLS 1.2", cloned.MinVersion)
	}
	if cloned.NextProtos[0] != "h2" {
		t.Fatalf("NextProtos aliased caller config: %v", cloned.NextProtos)
	}
	if cloned.CipherSuites[0] != tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256 {
		t.Fatalf("CipherSuites aliased caller config: %v", cloned.CipherSuites)
	}
	if cloned.CurvePreferences[0] != tls.CurveP256 {
		t.Fatalf("CurvePreferences aliased caller config: %v", cloned.CurvePreferences)
	}
	if cloned.EncryptedClientHelloConfigList[0] != 1 {
		t.Fatalf("ECH config list aliased caller config: %v", cloned.EncryptedClientHelloConfigList)
	}
	if cloned.EncryptedClientHelloKeys[0].Config[0] != 4 || cloned.EncryptedClientHelloKeys[0].PrivateKey[0] != 6 {
		t.Fatalf("ECH keys aliased caller config: %+v", cloned.EncryptedClientHelloKeys)
	}
	if cloned.Certificates[0].Certificate[0][0] != 1 {
		t.Fatalf("certificate chain aliased caller config: %+v", cloned.Certificates[0].Certificate)
	}
	if cloned.Certificates[0].SupportedSignatureAlgorithms[0] != tls.PKCS1WithSHA256 {
		t.Fatalf("signature algorithms aliased caller config: %+v", cloned.Certificates[0].SupportedSignatureAlgorithms)
	}
	if cloned.Certificates[0].OCSPStaple[0] != 4 {
		t.Fatalf("OCSP staple aliased caller config: %+v", cloned.Certificates[0].OCSPStaple)
	}
	if cloned.Certificates[0].SignedCertificateTimestamps[0][0] != 7 {
		t.Fatalf("SCTs aliased caller config: %+v", cloned.Certificates[0].SignedCertificateTimestamps)
	}
}

func TestConfigWithFloor_RejectsMaxVersionBelowFloor(t *testing.T) {
	_, err := ConfigWithFloor(&tls.Config{MaxVersion: tls.VersionTLS11}, tls.VersionTLS12)
	if !errors.Is(err, ErrMaxVersionBelowFloor) {
		t.Fatalf("err = %v, want ErrMaxVersionBelowFloor", err)
	}
}

func TestConfigOrEmptyWithFloor_NilCreatesConfig(t *testing.T) {
	cfg, err := ConfigOrEmptyWithFloor(nil, tls.VersionTLS12)
	if err != nil {
		t.Fatalf("ConfigOrEmptyWithFloor: %v", err)
	}
	if cfg == nil || cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("cfg = %+v, want TLS 1.2 floor", cfg)
	}
}

func TestConfigWithFloor_ForcesRenegotiateNever(t *testing.T) {
	// Each enum below is a renegotiation policy that pre-Go-1.13 code
	// or unaware adapters might leave on a caller-provided tls.Config.
	// The clone must reset all of them to RenegotiateNever — the kit
	// never renegotiates, and the legacy "OnceAsClient" / "FreelyAsClient"
	// modes are the credential-confusion / MITM vectors from CVE-2009-3555
	// et al.
	for _, mode := range []tls.RenegotiationSupport{
		tls.RenegotiateOnceAsClient,
		tls.RenegotiateFreelyAsClient,
	} {
		cfg := &tls.Config{Renegotiation: mode}
		cloned, err := ConfigWithFloor(cfg, tls.VersionTLS12)
		if err != nil {
			t.Fatalf("ConfigWithFloor(mode=%d): %v", mode, err)
		}
		if cloned.Renegotiation != tls.RenegotiateNever {
			t.Fatalf("Renegotiation = %d, want RenegotiateNever — kit must not inherit %d", cloned.Renegotiation, mode)
		}
	}
}

func TestConfigWithFloor_DropsDeprecatedNameToCertificate(t *testing.T) {
	// NameToCertificate is deprecated since Go 1.14 and is shadowed by
	// GetCertificate on a modern config. Carrying a stale SNI map into
	// a kit-owned config would silently override certificate selection
	// after the kit applied its TLS floor — drop it so the kit config
	// has a single, unambiguous resolution path.
	cfg := &tls.Config{
		//nolint:staticcheck // Intentionally populate the deprecated field to assert it is dropped.
		NameToCertificate: map[string]*tls.Certificate{
			"example.com": {Certificate: [][]byte{{1, 2, 3}}},
		},
	}
	cloned, err := ConfigWithFloor(cfg, tls.VersionTLS12)
	if err != nil {
		t.Fatalf("ConfigWithFloor: %v", err)
	}
	//nolint:staticcheck // Reading the deprecated field for the assertion.
	//lint:ignore SA1019 reading the deprecated field for the assertion.
	if cloned.NameToCertificate != nil {
		//lint:ignore SA1019 message formatting reads the deprecated field for the assertion.
		t.Fatalf("NameToCertificate = %+v, want nil — kit must not inherit the deprecated SNI map", cloned.NameToCertificate)
	}
}

func TestConfigWithFloor_RejectsInsecureSkipVerifyByDefault(t *testing.T) {
	_, err := ConfigWithFloor(&tls.Config{InsecureSkipVerify: true}, tls.VersionTLS12)
	if !errors.Is(err, ErrInsecureSkipVerifyNotPermitted) {
		t.Fatalf("err = %v, want ErrInsecureSkipVerifyNotPermitted — silent InsecureSkipVerify inheritance disables every certificate check", err)
	}
}

func TestConfigWithFloor_AllowInsecureSkipVerifyOptIn(t *testing.T) {
	cfg := &tls.Config{InsecureSkipVerify: true}
	cloned, err := ConfigWithFloor(cfg, tls.VersionTLS12, WithAllowInsecureSkipVerify())
	if err != nil {
		t.Fatalf("ConfigWithFloor with AllowInsecureSkipVerify: %v", err)
	}
	if !cloned.InsecureSkipVerify {
		t.Fatal("InsecureSkipVerify=false on clone, want true — explicit opt-in must preserve the caller's choice")
	}
}

func TestConfigOrEmptyWithFloor_RejectsInsecureSkipVerifyByDefault(t *testing.T) {
	_, err := ConfigOrEmptyWithFloor(&tls.Config{InsecureSkipVerify: true}, tls.VersionTLS12)
	if !errors.Is(err, ErrInsecureSkipVerifyNotPermitted) {
		t.Fatalf("err = %v, want ErrInsecureSkipVerifyNotPermitted", err)
	}
}

func TestConfigWithFloor_ClearsKeyLogWriterByDefault(t *testing.T) {
	var buf bytes.Buffer
	cfg := &tls.Config{KeyLogWriter: &buf}
	cloned, err := ConfigWithFloor(cfg, tls.VersionTLS12)
	if err != nil {
		t.Fatalf("ConfigWithFloor: %v", err)
	}
	if cloned.KeyLogWriter != nil {
		t.Fatal("KeyLogWriter must be cleared by default (session-secret leak)")
	}
	if cfg.KeyLogWriter == nil {
		t.Fatal("source config KeyLogWriter must not be mutated")
	}
}

func TestConfigWithFloor_WithAllowKeyLogWriterKeepsWriter(t *testing.T) {
	var buf bytes.Buffer
	cfg := &tls.Config{KeyLogWriter: &buf}
	cloned, err := ConfigWithFloor(cfg, tls.VersionTLS12, WithAllowKeyLogWriter())
	if err != nil {
		t.Fatalf("ConfigWithFloor: %v", err)
	}
	if cloned.KeyLogWriter != &buf {
		t.Fatal("WithAllowKeyLogWriter must retain KeyLogWriter")
	}
}
