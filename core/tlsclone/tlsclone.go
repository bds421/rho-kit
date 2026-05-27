// Package tlsclone provides defensive cloning helpers for [crypto/tls.Config].
//
// The standard library's [tls.Config.Clone] copies the top-level struct
// but leaves slices, maps, and certificate chains aliased with the
// original. Callers that hand a config to a connection pool (go-redis,
// pgx, amqp091) and then mutate the original would silently change the
// live TLS settings — a classic mTLS regression hiding behind innocent
// "I cloned it" code. This package returns a fully detached config so
// mutation safety is preserved.
//
// Key entry points:
//
//   - [ConfigWithFloor] — clone and raise MinVersion. Returns nil for a
//     nil input. Used by every kit transport that accepts a caller-
//     supplied tls.Config.
//   - [ConfigOrEmptyWithFloor] — same, but a nil input yields a fresh
//     empty config with MinVersion set, suitable for "TLS required but
//     no caller customization" call sites.
//   - [ErrMaxVersionBelowFloor] — sentinel returned when the input's
//     MaxVersion is below the requested floor, which would otherwise
//     leave the cloned config unable to handshake.
//
// The minimum-version floor is the kit's defence against downgrade by
// stale config (a service inheriting a TLS 1.0 config from an old
// adapter). Each call site picks the floor — 1.2 for Redis, 1.3 for
// any new transport.
//
// Cloning also normalises three risky settings that would otherwise be
// silently inherited from the caller:
//
//   - Renegotiation is forced to [tls.RenegotiateNever]. The kit never
//     negotiates renegotiation; legacy renegotiation is a known
//     credential-confusion vector against TLS 1.2 servers and serves no
//     purpose on TLS 1.3, which removes renegotiation entirely.
//   - The deprecated NameToCertificate map is reset to nil. A stale SNI
//     map would otherwise shadow modern certificate selection via
//     [tls.Config.GetCertificate] on the kit-owned config.
//   - InsecureSkipVerify=true on the caller config is refused with
//     [ErrInsecureSkipVerifyNotPermitted] unless the caller passes
//     [AllowInsecureSkipVerify]. kit-verify is the only legitimate
//     caller and supplies the opt-in explicitly.
package tlsclone

import (
	"crypto/tls"
	"errors"
)

// ErrMaxVersionBelowFloor reports a TLS configuration whose MaxVersion makes
// the requested minimum impossible.
var ErrMaxVersionBelowFloor = errors.New("tlsclone: TLS MaxVersion is below minimum")

// ErrInsecureSkipVerifyNotPermitted reports a caller-supplied TLS configuration
// that sets `InsecureSkipVerify=true` without explicit opt-in via
// [AllowInsecureSkipVerify]. Silent inheritance would let every certificate
// (including attacker-presented ones) pass verification, defeating the kit's
// TLS floor.
var ErrInsecureSkipVerifyNotPermitted = errors.New("tlsclone: InsecureSkipVerify=true requires an explicit WithAllowInsecureSkipVerify opt-in")

// Option configures a [ConfigWithFloor] / [ConfigOrEmptyWithFloor] call.
type Option func(*options)

type options struct {
	allowInsecureSkipVerify bool
}

// WithAllowInsecureSkipVerify permits the cloned config to keep
// `InsecureSkipVerify=true` when the caller config has it set. Without this
// opt-in, the clone helpers refuse such configs to make the dangerous
// inheritance explicit at every call site.
//
// kit-verify is the only legitimate user — it probes services with
// dev-issued certificates and accepts the trust trade-off. New callers
// must justify the opt-in in code review.
func WithAllowInsecureSkipVerify() Option {
	return func(o *options) { o.allowInsecureSkipVerify = true }
}

// ConfigWithFloor returns a detached clone of cfg with MinVersion raised to
// minVersion. A nil input returns nil.
//
// The clone additionally has Renegotiation forced to
// [tls.RenegotiateNever] and the deprecated NameToCertificate map
// reset to nil. If cfg.InsecureSkipVerify is true the call returns
// [ErrInsecureSkipVerifyNotPermitted] unless [AllowInsecureSkipVerify]
// is passed.
func ConfigWithFloor(cfg *tls.Config, minVersion uint16, opts ...Option) (*tls.Config, error) {
	if cfg == nil {
		return nil, nil
	}
	var settings options
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		opt(&settings)
	}
	if cfg.InsecureSkipVerify && !settings.allowInsecureSkipVerify {
		return nil, ErrInsecureSkipVerifyNotPermitted
	}
	cloned := cfg.Clone()
	detachTLSConfig(cloned)
	if cloned.MaxVersion != 0 && cloned.MaxVersion < minVersion {
		return nil, ErrMaxVersionBelowFloor
	}
	if cloned.MinVersion < minVersion {
		cloned.MinVersion = minVersion
	}
	// Force the safest renegotiation mode regardless of the caller's
	// choice. TLS 1.3 drops renegotiation entirely; for 1.2 it is a
	// known credential-confusion / MITM vector (CVE-2009-3555 et al.)
	// and the kit never uses renegotiation.
	cloned.Renegotiation = tls.RenegotiateNever
	return cloned, nil
}

// ConfigOrEmptyWithFloor is like ConfigWithFloor, but nil input produces a new
// empty tls.Config with MinVersion raised to minVersion.
func ConfigOrEmptyWithFloor(cfg *tls.Config, minVersion uint16, opts ...Option) (*tls.Config, error) {
	if cfg == nil {
		cfg = &tls.Config{}
	}
	return ConfigWithFloor(cfg, minVersion, opts...)
}

func detachTLSConfig(cfg *tls.Config) {
	cfg.Certificates = cloneCertificates(cfg.Certificates)
	// NameToCertificate has been deprecated since Go 1.14 in favour of
	// GetCertificate. A stale SNI map carried in from the caller would
	// silently shadow modern certificate-selection logic on the
	// kit-owned config — drop it once cloning is complete.
	//nolint:staticcheck // Reset the deprecated map intentionally.
	//lint:ignore SA1019 deliberately zeroing the deprecated field
	cfg.NameToCertificate = nil
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
