// Package jwtutil provides JWT verification backed by lestrrat-go/jwx/v3.
//
// It verifies JWTs signed with asymmetric keys published through a JWKS
// endpoint and caches keys with periodic background refresh.
package jwtutil

