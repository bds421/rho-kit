package gormdb

import (
	"gorm.io/gorm"

	"github.com/bds421/rho-kit/infra/sqldb"
)

// CursorQuery is a mutable query builder for cursor-based pagination.
// Methods modify the receiver in place (builder pattern) and return it
// for chaining. Create a new CursorQuery for each independent query.
//
// It eliminates the repeated pattern of search + cursor + order + limit
// that appears in every List method of every GORM store.
type CursorQuery struct {
	db      *gorm.DB
	table   string           // optional table qualifier for id column (needed for JOINs)
	dialect sqldb.Dialect // quoting style (default: DialectMySQL)
}

// NewCursorQuery starts a cursor query from an existing GORM query.
// The caller typically passes db.WithContext(ctx).Model(&MyModel{}).
// Defaults to MySQL/MariaDB quoting; use WithDialect for PostgreSQL.
func NewCursorQuery(db *gorm.DB) *CursorQuery {
	return &CursorQuery{db: db}
}

// WithTable sets the table name used to qualify the id column in Cursor and
// Desc methods. This is required when the query JOINs another table that also
// has an id column — without it MariaDB returns "Column 'id' is ambiguous".
func (q *CursorQuery) WithTable(table string) *CursorQuery {
	sqldb.ValidateColumn(table)
	q.table = table
	return q
}

// WithDialect sets the SQL dialect for column quoting.
func (q *CursorQuery) WithDialect(d sqldb.Dialect) *CursorQuery {
	q.dialect = d
	return q
}

// SearchLike adds a LIKE search across one or more columns (OR-ed).
// Automatically wraps the term in '%' and escapes SQL LIKE metacharacters.
// No-op if search is empty.
func (q *CursorQuery) SearchLike(search string, columns ...string) *CursorQuery {
	if search == "" || len(columns) == 0 {
		return q
	}
	like := "%" + sqldb.EscapeLike(search) + "%"

	for _, col := range columns {
		sqldb.ValidateColumn(col)
	}
	condition := sqldb.QuoteColumn(columns[0], q.dialect) + " LIKE ?"
	args := []any{like}
	for _, col := range columns[1:] {
		condition += " OR " + sqldb.QuoteColumn(col, q.dialect) + " LIKE ?"
		args = append(args, like)
	}
	q.db = q.db.Where(condition, args...)
	return q
}

// Where adds a simple equality condition. No-op if value is nil.
func (q *CursorQuery) Where(column string, value any) *CursorQuery {
	if value == nil {
		return q
	}
	sqldb.ValidateColumn(column)
	q.db = q.db.Where(sqldb.QuoteColumn(column, q.dialect)+" = ?", value)
	return q
}

// WherePtr adds an equality condition from a typed pointer. No-op if ptr is nil.
func WherePtr[T any](q *CursorQuery, column string, ptr *T) *CursorQuery {
	if ptr == nil {
		return q
	}
	sqldb.ValidateColumn(column)
	q.db = q.db.Where(sqldb.QuoteColumn(column, q.dialect)+" = ?", *ptr)
	return q
}

// maxCursorLen limits cursor values to a reasonable length. UUIDs are 36 chars;
// 256 provides headroom for other ID formats while rejecting garbled input.
const maxCursorLen = 256

// Cursor adds the "id < cursor" condition for keyset pagination.
// No-op if cursor is empty or exceeds maxCursorLen.
//
// Note: The cursor value is passed as a parameterized query argument (SQL-injection
// safe), but no format validation is performed. Callers should validate cursor
// format at the API boundary (e.g., UUID validation in the HTTP handler) to prevent
// malformed cursors from producing silent wrong pagination results.
func (q *CursorQuery) Cursor(cursor string) *CursorQuery {
	if cursor == "" || len(cursor) > maxCursorLen {
		return q
	}
	q.db = q.db.Where(q.idColumn()+" < ?", cursor)
	return q
}

// Desc sets ORDER BY id DESC and LIMIT (limit+1) for hasMore detection.
// Limit is clamped to a minimum of 1 for safety.
func (q *CursorQuery) Desc(limit int) *CursorQuery {
	if limit < 1 {
		limit = 1
	}
	q.db = q.db.Order(q.idColumn() + " DESC").Limit(limit + 1)
	return q
}

// idColumn returns the optionally table-qualified id column name,
// quoted with the configured dialect for consistency with the rest of CursorQuery.
func (q *CursorQuery) idColumn() string {
	if q.table != "" {
		return sqldb.QuoteColumn(q.table+".id", q.dialect)
	}
	return sqldb.QuoteColumn("id", q.dialect)
}

// Find executes the query and scans results into dest.
func (q *CursorQuery) Find(dest any) error {
	return q.db.Find(dest).Error
}
