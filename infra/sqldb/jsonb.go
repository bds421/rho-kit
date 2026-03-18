package sqldb

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// JSONB is a generic type for PostgreSQL JSONB columns.
// It handles JSON marshaling/unmarshaling transparently via GORM's
// Scan/Value interface, eliminating per-type boilerplate.
//
// Usage:
//
//	type User struct {
//	    ID       uint
//	    Settings JSONB[map[string]any] `gorm:"type:jsonb"`
//	    Tags     JSONB[[]string]       `gorm:"type:jsonb"`
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
	j.Valid = true
	return json.Unmarshal(data, &j.Data)
}

// Value implements driver.Valuer for writing to the database.
func (j JSONB[T]) Value() (driver.Value, error) {
	if !j.Valid {
		return nil, nil
	}
	return json.Marshal(j.Data)
}

// GormDataType returns the GORM data type hint.
func (j JSONB[T]) GormDataType() string {
	return "jsonb"
}
