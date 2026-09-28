package localbackend

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"testing"
)

// TestIsEscapeError_ErrorShapes pins escape detection against the os.Root
// error shapes of different Go releases, independent of the toolchain running
// the tests. Go 1.26.6 started nesting the rejection for an escaping symlink
// component (mkdirat -> statat -> sentinel); matching only the outer PathError
// misclassified a refused escape as a generic filesystem failure.
func TestIsEscapeError_ErrorShapes(t *testing.T) {
	sentinel := errors.New(errPathEscapesText)
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"go<=1.26.5: PathError wrapping sentinel",
			&fs.PathError{Op: "mkdirat", Path: "escape/sub", Err: sentinel}, true},
		{"go>=1.26.6: nested PathError",
			&fs.PathError{Op: "mkdirat", Path: "escape", Err: &fs.PathError{Op: "statat", Path: "escape", Err: sentinel}}, true},
		{"fmt-wrapped nested PathError",
			fmt.Errorf("op: %w", &fs.PathError{Op: "mkdirat", Err: &fs.PathError{Op: "statat", Err: sentinel}}), true},
		{"nil", nil, false},
		{"file exists", &fs.PathError{Op: "mkdirat", Path: "x", Err: os.ErrExist}, false},
		{"permission denied", &fs.PathError{Op: "mkdirat", Path: "x", Err: fs.ErrPermission}, false},
		{"sentinel text only as a substring", errors.New("not a path escapes from parent error"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isEscapeError(tc.err); got != tc.want {
				t.Errorf("isEscapeError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
