// Package tlsclone provides defensive cloning helpers for tls.Config.
package tlsclone

import (
	"crypto/tls"
	"errors"
)

// ErrMaxVersionBelowFloor reports a TLS configuration whose MaxVersion makes
// the requested minimum impossible.
var ErrMaxVersionBelowFloor = errors.New("tlsclone: TLS MaxVersion is below minimum")

// ConfigWithFloor returns a detached clone of cfg with MinVersion raised to
// minVersion. A nil input returns nil.
func ConfigWithFloor(cfg *tls.Config, minVersion uint16) (*tls.Config, error) {
	if cfg == nil {
		return nil, nil
	}
	cloned := cfg.Clone()
	detachTLSConfig(cloned)
	if cloned.MaxVersion != 0 && cloned.MaxVersion < minVersion {
		return nil, ErrMaxVersionBelowFloor
	}
	if cloned.MinVersion < minVersion {
		cloned.MinVersion = minVersion
	}
	return cloned, nil
}

// ConfigOrEmptyWithFloor is like ConfigWithFloor, but nil input produces a new
// empty tls.Config with MinVersion raised to minVersion.
func ConfigOrEmptyWithFloor(cfg *tls.Config, minVersion uint16) (*tls.Config, error) {
	if cfg == nil {
		cfg = &tls.Config{}
	}
	return ConfigWithFloor(cfg, minVersion)
}

func detachTLSConfig(cfg *tls.Config) {
	cfg.Certificates = cloneCertificates(cfg.Certificates)
	//nolint:staticcheck // Legacy callers may still populate this deprecated field; clone it if present.
	cfg.NameToCertificate = cloneNameToCertificate(cfg.NameToCertificate)
	if cfg.RootCAs != nil {
		cfg.RootCAs = cfg.RootCAs.Clone()
	}
	cfg.NextProtos = append([]string(nil), cfg.NextProtos...)
	if cfg.ClientCAs != nil {
		cfg.ClientCAs = cfg.ClientCAs.Clone()
	}
	cfg.CipherSuites = append([]uint16(nil), cfg.CipherSuites...)
	cfg.CurvePreferences = append([]tls.CurveID(nil), cfg.CurvePreferences...)
	cfg.EncryptedClientHelloConfigList = append([]byte(nil), cfg.EncryptedClientHelloConfigList...)
	cfg.EncryptedClientHelloKeys = cloneEncryptedClientHelloKeys(cfg.EncryptedClientHelloKeys)
}

func cloneCertificates(certs []tls.Certificate) []tls.Certificate {
	if certs == nil {
		return nil
	}
	cloned := make([]tls.Certificate, len(certs))
	for i, cert := range certs {
		cloned[i] = cloneCertificate(cert)
	}
	return cloned
}

func cloneNameToCertificate(certs map[string]*tls.Certificate) map[string]*tls.Certificate {
	if certs == nil {
		return nil
	}
	cloned := make(map[string]*tls.Certificate, len(certs))
	for name, cert := range certs {
		if cert == nil {
			continue
		}
		copied := cloneCertificate(*cert)
		cloned[name] = &copied
	}
	return cloned
}

func cloneCertificate(cert tls.Certificate) tls.Certificate {
	cert.Certificate = cloneByteSlices(cert.Certificate)
	cert.SupportedSignatureAlgorithms = append([]tls.SignatureScheme(nil), cert.SupportedSignatureAlgorithms...)
	cert.OCSPStaple = append([]byte(nil), cert.OCSPStaple...)
	cert.SignedCertificateTimestamps = cloneByteSlices(cert.SignedCertificateTimestamps)
	if cert.Leaf != nil {
		leaf := *cert.Leaf
		cert.Leaf = &leaf
	}
	return cert
}

func cloneEncryptedClientHelloKeys(keys []tls.EncryptedClientHelloKey) []tls.EncryptedClientHelloKey {
	if keys == nil {
		return nil
	}
	cloned := make([]tls.EncryptedClientHelloKey, len(keys))
	for i, key := range keys {
		cloned[i] = key
		cloned[i].Config = append([]byte(nil), key.Config...)
		cloned[i].PrivateKey = append([]byte(nil), key.PrivateKey...)
	}
	return cloned
}

func cloneByteSlices(values [][]byte) [][]byte {
	if values == nil {
		return nil
	}
	cloned := make([][]byte, len(values))
	for i, value := range values {
		cloned[i] = append([]byte(nil), value...)
	}
	return cloned
}
