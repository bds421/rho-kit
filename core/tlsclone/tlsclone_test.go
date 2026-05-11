package tlsclone

import (
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
