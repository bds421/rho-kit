package tlsclone

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"reflect"
)

// ErrServerNameRequired is returned by a [PeerVerifier] callback when the
// handshake has no server name: with stdlib verification switched off, an
// empty ServerName would let the peer's chain pass without hostname
// binding. Set [tls.Config.ServerName] before dialing.
var ErrServerNameRequired = errors.New("tlsclone: peer verification requires tls.Config.ServerName to be set before dialing")

// PeerVerifier returns a [tls.Config.VerifyConnection] callback that
// verifies the peer's chain and server name against the pool cas returns
// on every handshake. With InsecureSkipVerify=true it replaces stdlib
// verification by an equivalent one that sees a rotated CA pool without
// a restart; security/netutil.ReloadingClientTLS builds on it.
//
// It is the one VerifyConnection [WithAllowPeerVerifier] and [VerifiesPeer]
// accept. Panics on a nil cas.
func PeerVerifier(cas func() (*x509.CertPool, error)) func(tls.ConnectionState) error {
	if cas == nil {
		panic("tlsclone: PeerVerifier requires a non-nil CA pool source")
	}
	return (&peerVerifier{cas: cas}).verify
}

// VerifiesPeer reports whether cfg's VerifyConnection is a [PeerVerifier]
// callback. The answer survives [tls.Config.Clone]; a VerifyConnection
// written by the caller, even one wrapping a PeerVerifier, reports false.
func VerifiesPeer(cfg *tls.Config) bool {
	return cfg != nil && cfg.VerifyConnection != nil &&
		reflect.ValueOf(cfg.VerifyConnection).Pointer() == peerVerifierCode
}

// peerVerifierCode identifies the method value PeerVerifier returns: every
// method value of peerVerifier.verify shares one code pointer, and no other
// function has it.
var peerVerifierCode = reflect.ValueOf((&peerVerifier{}).verify).Pointer()

type peerVerifier struct {
	cas func() (*x509.CertPool, error)
}

func (v *peerVerifier) verify(state tls.ConnectionState) error {
	if state.ServerName == "" {
		return ErrServerNameRequired
	}
	pool, err := v.cas()
	if err != nil {
		return fmt.Errorf("tlsclone: verify peer: %w", err)
	}
	if len(state.PeerCertificates) == 0 {
		return errors.New("tlsclone: verify peer: server presented no certificates")
	}
	intermediates := x509.NewCertPool()
	for _, c := range state.PeerCertificates[1:] {
		intermediates.AddCert(c)
	}
	opts := x509.VerifyOptions{
		Roots:         pool,
		Intermediates: intermediates,
		DNSName:       state.ServerName,
	}
	if _, err := state.PeerCertificates[0].Verify(opts); err != nil {
		return fmt.Errorf("tlsclone: verify peer: %w", err)
	}
	return nil
}
