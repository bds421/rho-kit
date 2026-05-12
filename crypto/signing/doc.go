// Package signing provides HMAC signing and verification helpers.
//
// The Sign and Verify entry points take (secret, body) in that order — secret
// first because the key is the longer-lived input, body second because it
// changes per call. In v2.0.0 this argument order was standardized across the
// package; pre-v2 call sites must swap their arguments.
//
// Key material lives in a [KeyStore]. [NewStaticKeyStore] returns
// (*StaticKeyStore, error) and validates key IDs; use [MustNewStaticKeyStore]
// only at package init for known-good static maps.
package signing
