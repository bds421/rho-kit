package sqldb

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// JSONB is a generic type for PostgreSQL JSONB columns.
// It handles JSON marshaling/unmarshaling transparently through the
// standard sql.Scanner and driver.Valuer interfaces, eliminating
// per-type boilerplate.
//
// Usage:
//
//	type User struct {
//	    ID       uint
//	    Settings JSONB[map[string]any]
//	    Tags     JSONB[[]string]
//	}
type JSONB[T any] struct {
	Data  T
	Valid bool // false if database value is NULL
}

// NewJSONB creates a JSONB value from data.
func NewJSONB[T any](data T) JSONB[T] {
	return JSONB[T]{Data: data, Valid: true}
}

// Scan implements sql.Scanner for reading from the database.
func (j *JSONB[T]) Scan(src any) error {
	if src == nil {
		j.Valid = false
		return nil
	}
	var data []byte
	switch v := src.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		return fmt.Errorf("sqldb.JSONB: unsupported scan source type %T", src)
	}
	if err := json.Unmarshal(data, &j.Data); err != nil {
		j.Valid = false
		return err
	}
	j.Valid = true
	return nil
}

// Value implements driver.Valuer for writing to the database.
func (j JSONB[T]) Value() (driver.Value, error) {
	if !j.Valid {
		return nil, nil
	}
	return json.Marshal(j.Data)
}

// GormDataType returns "jsonb" so callers that still use this type with
// GORM (outside the kit's own modules) get the correct column type.
// The kit itself does not import GORM.
func (j JSONB[T]) GormDataType() string {
	return "jsonb"
}
