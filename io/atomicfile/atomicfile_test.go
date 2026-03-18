package atomicfile

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type testState struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func TestLoad_MissingFile(t *testing.T) {
	got, err := Load[testState](filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != (testState{}) {
		t.Fatalf("expected zero value, got %+v", got)
	}
}

func TestLoad_CorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.json")
	if err := os.WriteFile(path, []byte("{invalid"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load[testState](path)
	if err == nil {
		t.Fatal("expected error for corrupt file")
	}
}

func TestLoad_ValidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "valid.json")
	if err := os.WriteFile(path, []byte(`{"name":"test","count":42}`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Load[testState](path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "test" || got.Count != 42 {
		t.Fatalf("unexpected value: %+v", got)
	}
}

func TestSave_CreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "dir")
	path := filepath.Join(dir, "state.json")

	if err := Save(path, testState{Name: "hello", Count: 1}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file should exist: %v", err)
	}
}

func TestSave_Overwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	if err := Save(path, testState{Name: "first", Count: 1}); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, testState{Name: "second", Count: 2}); err != nil {
		t.Fatal(err)
	}

	got, err := Load[testState](path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "second" || got.Count != 2 {
		t.Fatalf("expected second write, got %+v", got)
	}
}

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roundtrip.json")
	want := testState{Name: "round-trip", Count: 99}

	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}

	got, err := Load[testState](path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", got, want)
	}
}

func TestSave_NoTempFileLeftOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	if err := Save(path, testState{Name: "clean", Count: 1}); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestLoad_ReadPermissionError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping: root ignores file permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "noread.json")
	if err := os.WriteFile(path, []byte(`{"name":"test"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(path, 0o644) }()

	_, err := Load[testState](path)
	if err == nil {
		t.Fatal("expected error for unreadable file")
	}
}

func TestSave_MarshalError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	err := Save(path, make(chan int))
	if err == nil {
		t.Fatal("expected marshal error for channel type")
	}
}

func TestSave_ReadOnlyDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping: root ignores directory permissions")
	}
	dir := t.TempDir()
	readOnly := filepath.Join(dir, "readonly")
	if err := os.MkdirAll(readOnly, 0o555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(readOnly, 0o755) }()

	path := filepath.Join(readOnly, "state.json")
	err := Save(path, testState{Name: "fail", Count: 1})
	if err == nil {
		t.Fatal("expected error for read-only directory")
	}
}

func TestLoad_EmptyJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Load[testState](path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "" || got.Count != 0 {
		t.Fatalf("expected zero-value struct from empty JSON, got %+v", got)
	}
}

func TestSave_ComplexStruct(t *testing.T) {
	type nested struct {
		Items []string          `json:"items"`
		Meta  map[string]string `json:"meta"`
	}
	path := filepath.Join(t.TempDir(), "complex.json")

	want := nested{
		Items: []string{"a", "b"},
		Meta:  map[string]string{"key": "value"},
	}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}

	got, err := Load[nested](path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 || got.Items[0] != "a" || got.Meta["key"] != "value" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestSave_MkdirAllFailure(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("blocking file"), 0o644); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(blocker, "sub", "state.json")
	err := Save(path, testState{Name: "fail", Count: 1})
	if err == nil {
		t.Fatal("expected error when MkdirAll fails due to blocking file")
	}
}

func TestSave_RenameFailure(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping: root ignores directory permissions")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "state.json")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}

	err := Save(target, testState{Name: "fail", Count: 1})
	if err == nil {
		t.Fatal("expected error when rename target is a directory")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestSave_WriteErrorLargePayload(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping: root ignores rlimit")
	}

	var orig syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &orig); err != nil {
		t.Skipf("cannot get RLIMIT_FSIZE: %v", err)
	}

	small := syscall.Rlimit{Cur: 1, Max: orig.Max}
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &small); err != nil {
		t.Skipf("cannot set RLIMIT_FSIZE: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Setrlimit(syscall.RLIMIT_FSIZE, &orig)
	})

	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	large := strings.Repeat("x", 4096)
	err := Save(path, large)
	if err == nil {
		t.Fatal("expected write error for payload exceeding RLIMIT_FSIZE")
	}
}

func TestSave_PrimitiveTypes(t *testing.T) {
	dir := t.TempDir()

	strPath := filepath.Join(dir, "string.json")
	if err := Save(strPath, "hello world"); err != nil {
		t.Fatalf("Save string: %v", err)
	}
	got, err := Load[string](strPath)
	if err != nil {
		t.Fatalf("Load string: %v", err)
	}
	if got != "hello world" {
		t.Fatalf("expected 'hello world', got %q", got)
	}

	intPath := filepath.Join(dir, "int.json")
	if err := Save(intPath, 42); err != nil {
		t.Fatalf("Save int: %v", err)
	}
	gotInt, err := Load[int](intPath)
	if err != nil {
		t.Fatalf("Load int: %v", err)
	}
	if gotInt != 42 {
		t.Fatalf("expected 42, got %d", gotInt)
	}

	nilPath := filepath.Join(dir, "nil.json")
	if err := Save(nilPath, (*testState)(nil)); err != nil {
		t.Fatalf("Save nil: %v", err)
	}
	gotNil, err := Load[*testState](nilPath)
	if err != nil {
		t.Fatalf("Load nil: %v", err)
	}
	if gotNil != nil {
		t.Fatalf("expected nil, got %+v", gotNil)
	}
}
