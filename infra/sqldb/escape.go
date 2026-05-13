package sqldb

import (
	"regexp"
	"strings"

	"github.com/bds421/rho-kit/core/v2/apperror"
)

// safeColumnName matches valid SQL column names: letters, digits, underscores,
// with an optional single dot-separated qualifier (e.g. "table.column").
// Rejects trailing dots, leading dots, double dots, and multi-dot names like "a.b.c".
var safeColumnName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)?$`)

// ValidateColumn returns an [apperror.ValidationError] when name is not a
// safe SQL column identifier. Use this when name originates anywhere it
// could plausibly be derived from runtime input (config reload, request
// parameter, generated migration). For hardcoded identifiers in init or
// startup code, prefer [MustValidateColumn] which panics on the same
// inputs and surfaces misconfiguration at boot.
func ValidateColumn(name string) error {
	if !safeColumnName.MatchString(name) {
		return apperror.NewValidation("database: unsafe column name")
	}
	return nil
}

// MustValidateColumn panics if name is not a safe SQL column identifier.
// Reserve for callers that already restrict name to a hardcoded set; any
// caller that accepts runtime input must use [ValidateColumn] and handle
// the returned error.
func MustValidateColumn(name string) {
	if !safeColumnName.MatchString(name) {
		panic("database: unsafe column name")
	}
}

// QuoteColumn wraps a column name in PostgreSQL double quotes. Supports
// dot-qualified names like "table.column" by quoting each part separately.
// Escapes embedded quote characters as defense-in-depth even though
// MustValidateColumn would normally reject names containing them.
//
// QuoteColumn panics on unsafe names. Callers that may pass runtime
// input must first call [ValidateColumn] and return the error rather
// than letting the panic escape.
//
// v2 dropped the Dialect parameter — kit only supports PostgreSQL now.
func QuoteColumn(name string) string {
	MustValidateColumn(name)

	const q = `"`
	const escape = `""`
	name = strings.ReplaceAll(name, q, escape)
	if strings.Contains(name, ".") {
		parts := strings.SplitN(name, ".", 2)
		return q + parts[0] + q + "." + q + parts[1] + q
	}
	return q + name + q
}

// EscapeLike escapes SQL LIKE wildcard characters (%, _, \) in a search term.
func EscapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}
