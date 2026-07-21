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

func assertErrorDoesNotContainPaths(t *testing.T, err error, paths ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	for _, path := range paths {
		if path != "" && strings.Contains(msg, path) {
			t.Fatalf("error reflected path %q: %q", path, msg)
		}
	}
}

func TestLoad_MissingFile(t *testing.T) {
	got, err := LoadOrZero[testState](filepath.Join(t.TempDir(), "does-not-exist.json"))
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

	_, err := LoadOrZero[testState](path)
	if err == nil {
		t.Fatal("expected error for corrupt file")
	}
}

func TestLoad_ValidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "valid.json")
	if err := os.WriteFile(path, []byte(`{"name":"test","count":42}`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := LoadOrZero[testState](path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "test" || got.Count != 42 {
		t.Fatalf("unexpected value: %+v", got)
	}
}

func TestLoad_RejectsSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte(`{"name":"secret","count":7}`), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "state.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	_, _, err := Load[testState](link)
	if err == nil {
		t.Fatal("expected symlink load to be rejected")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
	assertErrorDoesNotContainPaths(t, err, link, target, dir)
}

func TestLoad_RejectsOversizedFile(t *testing.T) {
	// Cap enforcement must run against the open file descriptor (same
	// inode as the read), not a TOCTOU-prone path-based Stat.
	path := filepath.Join(t.TempDir(), "big.json")
	// Write a file just over MaxLoadBytes without allocating full RAM:
	// create sparse-ish content via truncated write of MaxLoadBytes+1.
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(int64(MaxLoadBytes) + 1); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()

	_, _, err = Load[testState](path)
	if err == nil {
		t.Fatal("expected oversized state file to be rejected")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected size-cap error, got %v", err)
	}
}

func TestLoad_RejectsSymlinkParent(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, "state.json"), []byte(`{"name":"secret","count":7}`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := Load[testState](filepath.Join(link, "state.json"))
	if err == nil {
		t.Fatal("expected symlink parent load to be rejected")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
	assertErrorDoesNotContainPaths(t, err, link, outside, dir)
}

func TestLoad_RejectsSymlinkAncestorWithExistingChild(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "sub", "state.json"), []byte(`{"name":"secret","count":7}`), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	_, _, err := Load[testState](filepath.Join(link, "sub", "state.json"))
	if err == nil {
		t.Fatal("expected symlink ancestor load to be rejected")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
	assertErrorDoesNotContainPaths(t, err, link, outside, dir)
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

	got, err := LoadOrZero[testState](path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "second" || got.Count != 2 {
		t.Fatalf("expected second write, got %+v", got)
	}
}

func TestSave_RejectsSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte(`{"name":"target","count":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "state.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	err := Save(link, testState{Name: "changed", Count: 2})
	if err == nil {
		t.Fatal("expected symlink save to be rejected")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
	assertErrorDoesNotContainPaths(t, err, link, target, dir)

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"name":"target","count":1}` {
		t.Fatalf("target was modified through symlink: %s", data)
	}
}

func TestSave_RejectsSymlinkParent(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	err := Save(filepath.Join(link, "sub", "state.json"), testState{Name: "changed", Count: 2})
	if err == nil {
		t.Fatal("expected symlink parent save to be rejected")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
	assertErrorDoesNotContainPaths(t, err, link, outside, dir)

	_, statErr := os.Stat(filepath.Join(outside, "sub"))
	if !os.IsNotExist(statErr) {
		t.Fatalf("outside directory was modified through symlink: %v", statErr)
	}
}

func TestSave_RejectsSymlinkAncestorWithExistingChild(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	err := Save(filepath.Join(link, "sub", "state.json"), testState{Name: "changed", Count: 2})
	if err == nil {
		t.Fatal("expected symlink ancestor save to be rejected")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
	assertErrorDoesNotContainPaths(t, err, link, outside, dir)

	_, statErr := os.Stat(filepath.Join(outside, "sub", "state.json"))
	if !os.IsNotExist(statErr) {
		t.Fatalf("outside file was modified through symlink: %v", statErr)
	}
}

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roundtrip.json")
	want := testState{Name: "round-trip", Count: 99}

	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}

	got, err := LoadOrZero[testState](path)
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

	_, err := LoadOrZero[testState](path)
	if err == nil {
		t.Fatal("expected error for unreadable file")
	}
	assertErrorDoesNotContainPaths(t, err, path, dir)
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
	assertErrorDoesNotContainPaths(t, err, path, readOnly, dir)
}

func TestLoad_EmptyJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := LoadOrZero[testState](path)
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

	got, err := LoadOrZero[nested](path)
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
	assertErrorDoesNotContainPaths(t, err, path, blocker, dir)
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
	assertErrorDoesNotContainPaths(t, err, target, dir)

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
	// Skipped by default. RLIMIT_FSIZE is process-wide, so while this
	// test is running it also blocks the Go test harness from writing
	// to its own testlog.txt — every concurrently-running test in the
	// same `go test` invocation aborts with "file too large". The
	// test itself is correct (Save() correctly surfaces EFBIG), but
	// it is incompatible with the surrounding harness.
	//
	// Set RUN_RLIMIT_TESTS=1 to opt in. Run it isolated:
	//
	//	RUN_RLIMIT_TESTS=1 go test -run TestSave_WriteErrorLargePayload ./io/atomicfile/...
	//
	// TestSave_ReadOnlyDir already covers the "Save handles write
	// failure" path through a different OS-level mechanism (dir
	// permissions), so coverage of the error branch is not lost.
	if os.Getenv("RUN_RLIMIT_TESTS") == "" {
		t.Skip("set RUN_RLIMIT_TESTS=1 to run; rlimit affects the test harness itself")
	}
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
	assertErrorDoesNotContainPaths(t, err, path, dir)
}

func TestSave_PrimitiveTypes(t *testing.T) {
	dir := t.TempDir()

	strPath := filepath.Join(dir, "string.json")
	if err := Save(strPath, "hello world"); err != nil {
		t.Fatalf("Save string: %v", err)
	}
	got, err := LoadOrZero[string](strPath)
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
	gotInt, err := LoadOrZero[int](intPath)
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
	gotNil, err := LoadOrZero[*testState](nilPath)
	if err != nil {
		t.Fatalf("Load nil: %v", err)
	}
	if gotNil != nil {
		t.Fatalf("expected nil, got %+v", gotNil)
	}
}
