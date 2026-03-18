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

// Dialect controls SQL quoting style for reserved-word escaping.
type Dialect int

const (
	// DialectMySQL uses backticks for quoting (MySQL/MariaDB).
	DialectMySQL Dialect = iota
	// DialectPostgres uses double quotes for quoting (PostgreSQL).
	DialectPostgres
)

// QuoteColumn wraps a column name in the dialect-appropriate quote character.
// Supports dot-qualified names like "table.column" by quoting each part separately.
// Escapes embedded quote characters as defense-in-depth, even though ValidateColumn
// would normally reject names containing them.
func QuoteColumn(name string, dialect Dialect) string {
	// Validate to reject multi-dot names ("a.b.c") that would produce
	// invalid SQL. The safeColumnName regex allows at most one dot.
	ValidateColumn(name)

	q := "`"
	escape := "``"
	if dialect == DialectPostgres {
		q = `"`
		escape = `""`
	}
	// Escape embedded quote characters to prevent SQL injection if
	// ValidateColumn is somehow bypassed.
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
