package exactversioncursor

import (
	"encoding/base64"
	"strings"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

// Position is the provider key+generation boundary carried by an opaque page
// cursor. Version may be empty only for providers whose native cursor permits
// a key-only boundary.
type Position struct {
	Key     string
	Version string
}

func Encode(
	kind string,
	prefix string,
	position Position,
	allowEmptyVersion bool,
) (storage.ExactVersionCursor, error) {
	if !valid(prefix, position, allowEmptyVersion) {
		return "", storage.ErrExactVersionUnavailable
	}
	raw := strings.Join(
		[]string{kind, prefix, position.Key, position.Version},
		"\x00",
	)
	return storage.ExactVersionCursor(
		base64.RawURLEncoding.EncodeToString([]byte(raw)),
	), nil
}

func Decode(
	kind string,
	prefix string,
	cursor storage.ExactVersionCursor,
	allowEmptyVersion bool,
) (Position, error) {
	if cursor == "" {
		return Position{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(string(cursor))
	if err != nil {
		return Position{}, storage.ErrExactVersionUnavailable
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) != 4 || parts[0] != kind || parts[1] != prefix {
		return Position{}, storage.ErrExactVersionUnavailable
	}
	position := Position{Key: parts[2], Version: parts[3]}
	if !valid(prefix, position, allowEmptyVersion) {
		return Position{}, storage.ErrExactVersionUnavailable
	}
	return position, nil
}

func valid(
	prefix string,
	position Position,
	allowEmptyVersion bool,
) bool {
	if !strings.HasPrefix(position.Key, prefix) {
		return false
	}
	if position.Version == "" {
		return allowEmptyVersion && storage.ValidateKey(position.Key) == nil
	}
	return (storage.ObjectVersion{
		Key: position.Key, Version: position.Version,
	}).Validate() == nil
}
