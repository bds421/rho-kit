package sqldb

import (
	"errors"
	"testing"
)

func TestIsDuplicateKeyError_Nil(t *testing.T) {
	if IsDuplicateKeyError(nil) {
		t.Fatal("nil error should not be duplicate key")
	}
}

func TestIsDuplicateKeyError_PostgresString(t *testing.T) {
	err := errors.New("ERROR: duplicate key value violates unique constraint")
	if !IsDuplicateKeyError(err) {
		t.Fatal("error containing 'duplicate key' should match")
	}
}

func TestIsDuplicateKeyError_MySQLString(t *testing.T) {
	err := errors.New("Error 1062: Duplicate entry 'foo' for key")
	if !IsDuplicateKeyError(err) {
		t.Fatal("error containing 'Duplicate entry' should match")
	}
}

func TestIsDuplicateKeyError_UnrelatedError(t *testing.T) {
	err := errors.New("connection refused")
	if IsDuplicateKeyError(err) {
		t.Fatal("unrelated error should not be duplicate key")
	}
}

func TestIsForeignKeyError_String(t *testing.T) {
	err := errors.New("violates foreign key constraint")
	if !IsForeignKeyError(err) {
		t.Fatal("error containing 'foreign key constraint' should match")
	}
}

func TestIsForeignKeyError_Nil(t *testing.T) {
	if IsForeignKeyError(nil) {
		t.Fatal("nil should not be foreign key error")
	}
}

func TestIsNotNullError_String(t *testing.T) {
	err := errors.New("violates not-null constraint")
	if !IsNotNullError(err) {
		t.Fatal("error containing 'not-null constraint' should match")
	}
}

func TestIsSerializationError_PostgresString(t *testing.T) {
	err := errors.New("could not serialize access due to concurrent update: serialization failure")
	if !IsSerializationError(err) {
		t.Fatal("error containing 'serialization failure' should match")
	}
}

func TestIsSerializationError_MySQLString(t *testing.T) {
	err := errors.New("Deadlock found when trying to get lock")
	if !IsSerializationError(err) {
		t.Fatal("error containing 'Deadlock found' should match")
	}
}

func TestIsNotFound(t *testing.T) {
	if IsNotFound(nil) {
		t.Fatal("nil should not be not-found")
	}
	if IsNotFound(errors.New("other")) {
		t.Fatal("other error should not be not-found")
	}
}

func TestValidateColumn_Safe(t *testing.T) {
	safe := []string{"name", "user_id", "table.column", "a1", "_private"}
	for _, col := range safe {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("ValidateColumn(%q) panicked: %v", col, r)
				}
			}()
			ValidateColumn(col)
		}()
	}
}

func TestValidateColumn_Unsafe(t *testing.T) {
	unsafe := []string{"1name", "name; DROP TABLE", "col name", "a-b", ""}
	for _, col := range unsafe {
		func() {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("ValidateColumn(%q) should have panicked", col)
				}
			}()
			ValidateColumn(col)
		}()
	}
}

func TestEscapeLike_NoSpecialChars(t *testing.T) {
	if got := EscapeLike("hello"); got != "hello" {
		t.Fatalf("expected 'hello', got %q", got)
	}
}

func TestEscapeLike_Percent(t *testing.T) {
	if got := EscapeLike("100%"); got != `100\%` {
		t.Fatalf("expected '100\\%%', got %q", got)
	}
}

func TestEscapeLike_Underscore(t *testing.T) {
	if got := EscapeLike("user_name"); got != `user\_name` {
		t.Fatalf("expected 'user\\_name', got %q", got)
	}
}

func TestEscapeLike_Backslash(t *testing.T) {
	if got := EscapeLike(`path\to`); got != `path\\to` {
		t.Fatalf("expected 'path\\\\to', got %q", got)
	}
}

func TestEscapeLike_AllSpecial(t *testing.T) {
	if got := EscapeLike(`%_\`); got != `\%\_\\` {
		t.Fatalf("expected '\\%%\\_\\\\', got %q", got)
	}
}

func TestEscapeLike_Empty(t *testing.T) {
	if got := EscapeLike(""); got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}
