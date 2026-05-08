package sqldb

import (
	"regexp"
	"strings"
)

// safeColumnName matches valid SQL column names: letters, digits, underscores,
// with an optional single dot-separated qualifier (e.g. "table.column").
// Rejects trailing dots, leading dots, double dots, and multi-dot names like "a.b.c".
var safeColumnName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)?$`)

// ValidateColumn panics if name is not a safe SQL column identifier.
// This is a defense-in-depth measure — callers should always pass hardcoded
// column names, never user input.
func ValidateColumn(name string) {
	if !safeColumnName.MatchString(name) {
		panic("database: unsafe column name: " + name)
	}
}

// QuoteColumn wraps a column name in PostgreSQL double quotes. Supports
// dot-qualified names like "table.column" by quoting each part separately.
// Escapes embedded quote characters as defense-in-depth even though
// ValidateColumn would normally reject names containing them.
//
// v2 dropped the Dialect parameter — kit only supports PostgreSQL now.
func QuoteColumn(name string) string {
	ValidateColumn(name)

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
