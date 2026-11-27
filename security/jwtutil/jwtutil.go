// Package jwtutil provides JWT verification backed by lestrrat-go/jwx/v4.
//
// It verifies JWTs signed with asymmetric keys published through a JWKS
// endpoint and caches keys with periodic background refresh.
package jwtutil
