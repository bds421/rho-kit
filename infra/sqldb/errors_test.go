package sqldb

import (
	"errors"
	"strings"
	"testing"

	"github.com/bds421/rho-kit/core/v2/apperror"
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
		if err := ValidateColumn(col); err != nil {
			t.Errorf("ValidateColumn(%q) returned error: %v", col, err)
		}
	}
}

func TestValidateColumn_Unsafe(t *testing.T) {
	unsafe := []string{"1name", "name; DROP TABLE", "col name", "a-b", ""}
	for _, col := range unsafe {
		err := ValidateColumn(col)
		if err == nil {
			t.Errorf("ValidateColumn(%q) should have returned an error", col)
			continue
		}
		var vErr *apperror.ValidationError
		if !errors.As(err, &vErr) {
			t.Errorf("ValidateColumn(%q) error type = %T, want *apperror.ValidationError", col, err)
		}
	}
}

func TestValidateColumn_ErrorDoesNotReflectUnsafeName(t *testing.T) {
	err := ValidateColumn("secret-token; DROP TABLE users")
	if err == nil {
		t.Fatal("expected error")
	}
	if msg := err.Error(); msg != "sqldb: unsafe column name" {
		t.Fatalf("error = %q, want stable unsafe-column message", msg)
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error leaked unsafe column name: %q", err.Error())
	}
}

func TestMustValidateColumn_Safe(t *testing.T) {
	safe := []string{"name", "user_id", "table.column", "a1", "_private"}
	for _, col := range safe {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("MustValidateColumn(%q) panicked: %v", col, r)
				}
			}()
			MustValidateColumn(col)
		}()
	}
}

func TestMustValidateColumn_Unsafe(t *testing.T) {
	unsafe := []string{"1name", "name; DROP TABLE", "col name", "a-b", ""}
	for _, col := range unsafe {
		func() {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("MustValidateColumn(%q) should have panicked", col)
				}
			}()
			MustValidateColumn(col)
		}()
	}
}

func TestMustValidateColumn_PanicDoesNotReflectUnsafeName(t *testing.T) {
	defer func() {
		rec := recover()
		if rec == nil {
			t.Fatal("expected panic")
		}
		msg, ok := rec.(string)
		if !ok {
			t.Fatalf("panic = %T, want string", rec)
		}
		if msg != "sqldb: MustValidateColumn: unsafe column name" {
			t.Fatalf("panic = %q, want stable unsafe-column message", msg)
		}
		if strings.Contains(msg, "secret-token") {
			t.Fatalf("panic leaked unsafe column name: %q", msg)
		}
	}()
	MustValidateColumn("secret-token; DROP TABLE users")
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
