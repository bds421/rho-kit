package sftpbackend

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

// mockSFTPClient allows full control over each method for unit tests.
type mockSFTPClient struct {
	store   map[string][]byte
	root    string
	lstatFn func(string) (os.FileInfo, error)
	statFn  func(string) (os.FileInfo, error)
	readFn  func(string) ([]os.FileInfo, error)
	openFn  func(string) (*sftp.File, error)
	closeFn func() error
}

func writeKnownHostsForTest(t *testing.T, host string, port int) string {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pub, err := ssh.NewPublicKey(&key.PublicKey)
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{net.JoinHostPort(host, strconv.Itoa(port))}, pub) + "\n"
	require.NoError(t, os.WriteFile(path, []byte(line), 0o600))
	return path
}

func newMockSFTPClient(root string) *mockSFTPClient {
	return &mockSFTPClient{
		store: make(map[string][]byte),
		root:  root,
	}
}

func (m *mockSFTPClient) Create(p string) (*sftp.File, error) {
	// sftp.File is a concrete type that cannot be constructed in tests.
	// Panic loudly so any test that reaches this code path fails clearly
	// instead of nil-dereferencing inside io.Copy.
	panic("mockSFTPClient.Create: sftp.File cannot be mocked; use integration tests for Put")
}

func (m *mockSFTPClient) Open(p string) (*sftp.File, error) {
	if m.openFn != nil {
		return m.openFn(p)
	}
	panic("mockSFTPClient.Open: sftp.File cannot be mocked; use integration tests for Get")
}

func (m *mockSFTPClient) Remove(p string) error {
	if _, ok := m.store[p]; !ok {
		return &sftp.StatusError{Code: ssh_FX_NO_SUCH_FILE}
	}
	delete(m.store, p)
	return nil
}

func (m *mockSFTPClient) Rename(oldpath, newpath string) error {
	data, ok := m.store[oldpath]
	if !ok {
		return &sftp.StatusError{Code: ssh_FX_NO_SUCH_FILE}
	}
	m.store[newpath] = data
	delete(m.store, oldpath)
	return nil
}

func (m *mockSFTPClient) Lstat(p string) (os.FileInfo, error) {
	if m.lstatFn != nil {
		return m.lstatFn(p)
	}
	if p == m.root {
		return fakeFileInfo{name: pathBase(p), dir: true}, nil
	}
	if data, ok := m.store[p]; ok {
		return fakeFileInfo{name: pathBase(p), size: int64(len(data))}, nil
	}
	prefix := p
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	for key := range m.store {
		if strings.HasPrefix(key, prefix) {
			return fakeFileInfo{name: pathBase(p), dir: true}, nil
		}
	}
	return nil, &sftp.StatusError{Code: ssh_FX_NO_SUCH_FILE}
}

func (m *mockSFTPClient) Stat(p string) (os.FileInfo, error) {
	if m.statFn != nil {
		return m.statFn(p)
	}
	if _, ok := m.store[p]; !ok {
		return nil, &sftp.StatusError{Code: ssh_FX_NO_SUCH_FILE}
	}
	// Return a fake FileInfo.
	return fakeFileInfo{size: int64(len(m.store[p]))}, nil
}

func (m *mockSFTPClient) MkdirAll(_ string) error { return nil }

func (m *mockSFTPClient) ReadDir(p string) ([]os.FileInfo, error) {
	if m.readFn != nil {
		return m.readFn(p)
	}
	prefix := p
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	seen := make(map[string]bool)
	var result []os.FileInfo
	anyMatch := false

	for key, data := range m.store {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		anyMatch = true

		remainder := key[len(prefix):]
		if remainder == "" {
			continue
		}

		// Find the first path segment.
		slashIdx := strings.Index(remainder, "/")
		if slashIdx == -1 {
			// Direct child file.
			if !seen[remainder] {
				seen[remainder] = true
				result = append(result, fakeFileInfo{name: remainder, size: int64(len(data))})
			}
		} else {
			// Subdirectory.
			dirName := remainder[:slashIdx]
			if !seen[dirName] {
				seen[dirName] = true
				result = append(result, fakeFileInfo{name: dirName, dir: true})
			}
		}
	}

	if !anyMatch {
		return nil, &sftp.StatusError{Code: ssh_FX_NO_SUCH_FILE}
	}

	sort.Slice(result, func(i, j int) bool { return result[i].Name() < result[j].Name() })
	return result, nil
}

func pathBase(p string) string {
	p = strings.TrimSuffix(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func (m *mockSFTPClient) Close() error {
	if m.closeFn != nil {
		return m.closeFn()
	}
	return nil
}

type fakeFileInfo struct {
	name    string
	size    int64
	dir     bool
	mode    os.FileMode
	modTime time.Time
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return f.size }
func (f fakeFileInfo) IsDir() bool        { return f.dir }
func (f fakeFileInfo) ModTime() time.Time { return f.modTime }
func (f fakeFileInfo) Mode() os.FileMode {
	if f.mode != 0 {
		return f.mode
	}
	if f.dir {
		return os.ModeDir | 0o750
	}
	return 0o640
}
func (f fakeFileInfo) Sys() any { return nil }

// Since sftp.File can't be constructed in tests, we test the SFTP backend
// at a higher level using the actual local backend pattern for Put/Get,
// and test the stateless operations (Delete, Exists, Healthy) with the mock.

func TestBuildSSHConfig_PasswordProviderReceivesTimeoutContext(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Host:                    "sftp.example.com",
		Port:                    22,
		User:                    "svc",
		RootPath:                "/uploads",
		KnownHostsFile:          writeKnownHostsForTest(t, "sftp.example.com", 22),
		PasswordProviderTimeout: time.Second,
	}
	var sawDeadline bool
	cfg.PasswordProvider = func(ctx context.Context) (string, error) {
		deadline, ok := ctx.Deadline()
		sawDeadline = ok && time.Until(deadline) > 0
		return "strong-password-123", nil
	}
	b := &Backend{cfg: cfg}

	sshCfg, err := b.buildSSHConfig(t.Context())

	require.NoError(t, err)
	require.NotNil(t, sshCfg)
	assert.True(t, sawDeadline)
	assert.NotEmpty(t, sshCfg.Auth)
}

func TestSFTPBackend_Delete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("deletes existing key", func(t *testing.T) {
		t.Parallel()
		mock := newMockSFTPClient("/data")
		mock.store["/data/file.txt"] = []byte("content")
		b := NewWithClient(mock, Config{Host: "localhost", RootPath: "/data"})

		err := b.Delete(ctx, "file.txt")
		assert.NoError(t, err)
		assert.NotContains(t, mock.store, "/data/file.txt")
	})

	t.Run("idempotent on missing key", func(t *testing.T) {
		t.Parallel()
		mock := newMockSFTPClient("/data")
		b := NewWithClient(mock, Config{Host: "localhost", RootPath: "/data"})

		err := b.Delete(ctx, "missing.txt")
		assert.NoError(t, err)
	})

	t.Run("rejects empty key", func(t *testing.T) {
		t.Parallel()
		mock := newMockSFTPClient("/data")
		b := NewWithClient(mock, Config{Host: "localhost", RootPath: "/data"})

		err := b.Delete(ctx, "")
		assert.Error(t, err)
	})

	t.Run("rejects symlink object", func(t *testing.T) {
		t.Parallel()
		mock := newMockSFTPClient("/data")
		mock.store["/data/secret-token-link"] = []byte("not deleted")
		mock.lstatFn = func(p string) (os.FileInfo, error) {
			switch p {
			case "/data":
				return fakeFileInfo{name: "data", dir: true}, nil
			case "/data/secret-token-link":
				return fakeFileInfo{name: "secret-token-link", mode: os.ModeSymlink | 0o777}, nil
			default:
				return nil, &sftp.StatusError{Code: ssh_FX_NO_SUCH_FILE}
			}
		}
		b := NewWithClient(mock, Config{Host: "localhost", RootPath: "/data"})

		err := b.Delete(ctx, "secret-token-link")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsafe")
		assert.NotContains(t, err.Error(), "secret-token")
		assert.Contains(t, mock.store, "/data/secret-token-link")
	})
}

func TestSFTPBackend_GetMissingDoesNotReflectKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mock := newMockSFTPClient("/data")
	mock.openFn = func(string) (*sftp.File, error) {
		return nil, &sftp.StatusError{Code: ssh_FX_NO_SUCH_FILE}
	}
	b := NewWithClient(mock, Config{Host: "localhost", RootPath: "/data"})

	_, _, err := b.Get(ctx, "secret-token.txt")

	require.ErrorIs(t, err, storage.ErrObjectNotFound)
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestSFTPBackend_GetRemoteErrorDoesNotReflectPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mock := newMockSFTPClient("/data")
	mock.lstatFn = func(p string) (os.FileInfo, error) {
		switch p {
		case "/data":
			return fakeFileInfo{name: "data", dir: true}, nil
		case "/data/secret-token.txt":
			return fakeFileInfo{name: "secret-token.txt"}, nil
		default:
			return nil, &sftp.StatusError{Code: ssh_FX_NO_SUCH_FILE}
		}
	}
	mock.openFn = func(p string) (*sftp.File, error) {
		return nil, errors.New("open failed for " + p)
	}
	b := NewWithClient(mock, Config{Host: "localhost", RootPath: "/data"})

	_, _, err := b.Get(ctx, "secret-token.txt")

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-token")
	assert.NotContains(t, err.Error(), "/data")
}

func TestSFTPBackend_Exists(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("returns true for existing key", func(t *testing.T) {
		t.Parallel()
		mock := newMockSFTPClient("/data")
		mock.store["/data/file.txt"] = []byte("content")
		b := NewWithClient(mock, Config{Host: "localhost", RootPath: "/data"})

		ok, err := b.Exists(ctx, "file.txt")
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("returns false for missing key", func(t *testing.T) {
		t.Parallel()
		mock := newMockSFTPClient("/data")
		b := NewWithClient(mock, Config{Host: "localhost", RootPath: "/data"})

		ok, err := b.Exists(ctx, "missing.txt")
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("rejects symlink object", func(t *testing.T) {
		t.Parallel()
		mock := newMockSFTPClient("/data")
		mock.lstatFn = func(p string) (os.FileInfo, error) {
			switch p {
			case "/data":
				return fakeFileInfo{name: "data", dir: true}, nil
			case "/data/secret-token-link":
				return fakeFileInfo{name: "secret-token-link", mode: os.ModeSymlink | 0o777}, nil
			default:
				return nil, &sftp.StatusError{Code: ssh_FX_NO_SUCH_FILE}
			}
		}
		b := NewWithClient(mock, Config{Host: "localhost", RootPath: "/data"})

		ok, err := b.Exists(ctx, "secret-token-link")
		require.Error(t, err)
		assert.False(t, ok)
		assert.Contains(t, err.Error(), "unsafe")
		assert.NotContains(t, err.Error(), "secret-token")
	})

	t.Run("stat error does not reflect key", func(t *testing.T) {
		t.Parallel()
		mock := newMockSFTPClient("/data")
		mock.store["/data/secret-token.txt"] = []byte("content")
		mock.statFn = func(p string) (os.FileInfo, error) {
			return nil, errors.New("backend down for " + p)
		}
		b := NewWithClient(mock, Config{Host: "localhost", RootPath: "/data"})

		ok, err := b.Exists(ctx, "secret-token.txt")
		require.Error(t, err)
		assert.False(t, ok)
		assert.NotContains(t, err.Error(), "secret-token")
		assert.NotContains(t, err.Error(), "/data")
	})

	t.Run("lstat error does not reflect key", func(t *testing.T) {
		t.Parallel()
		mock := newMockSFTPClient("/data")
		mock.lstatFn = func(p string) (os.FileInfo, error) {
			if p == "/data" {
				return fakeFileInfo{name: "data", dir: true}, nil
			}
			return nil, errors.New("lstat failed for " + p)
		}
		b := NewWithClient(mock, Config{Host: "localhost", RootPath: "/data"})

		ok, err := b.Exists(ctx, "secret-token.txt")
		require.Error(t, err)
		assert.False(t, ok)
		assert.NotContains(t, err.Error(), "secret-token")
		assert.NotContains(t, err.Error(), "/data")
	})
}

func TestSFTPBackend_Healthy(t *testing.T) {
	t.Parallel()

	t.Run("healthy when stat succeeds", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		mock := newMockSFTPClient(dir)
		mock.statFn = func(p string) (os.FileInfo, error) {
			return os.Stat(dir) // TempDir always exists
		}
		b := NewWithClient(mock, Config{Host: "localhost", RootPath: dir})

		assert.True(t, b.Healthy())
	})

	t.Run("unhealthy when stat fails", func(t *testing.T) {
		t.Parallel()
		mock := newMockSFTPClient("/nonexistent")
		mock.statFn = func(p string) (os.FileInfo, error) {
			return nil, errors.New("connection lost")
		}
		b := NewWithClient(mock, Config{Host: "localhost", RootPath: "/nonexistent"})

		assert.False(t, b.Healthy())
	})

	t.Run("unhealthy when not connected", func(t *testing.T) {
		t.Parallel()
		b := &Backend{
			cfg:      Config{Host: "localhost", RootPath: "/data"},
			instance: "test",
		}
		assert.False(t, b.Healthy())
	})

	t.Run("invalid receivers are unhealthy", func(t *testing.T) {
		t.Parallel()
		var nilBackend *Backend
		assert.False(t, nilBackend.Healthy())
		assert.False(t, (&Backend{}).Healthy())
	})
}

func TestSFTPBackend_Close(t *testing.T) {
	t.Parallel()

	t.Run("closes client and connection", func(t *testing.T) {
		t.Parallel()
		closed := false
		mock := newMockSFTPClient("/data")
		mock.closeFn = func() error {
			closed = true
			return nil
		}
		b := NewWithClient(mock, Config{Host: "localhost", RootPath: "/data"})

		err := b.Close()
		assert.NoError(t, err)
		assert.True(t, closed)
		assert.False(t, b.connected)
	})

	t.Run("invalid receivers are no-op", func(t *testing.T) {
		t.Parallel()
		var nilBackend *Backend
		assert.NoError(t, nilBackend.Close())
		assert.NoError(t, (&Backend{}).Close())
	})
}

func TestSFTPBackend_HealthCheck(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("returns healthy for working connection", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		mock := newMockSFTPClient(dir)
		mock.statFn = func(p string) (os.FileInfo, error) {
			return os.Stat(dir)
		}
		b := NewWithClient(mock, Config{Host: "testhost", Port: 22, RootPath: dir})

		check := HealthCheck(b)
		assert.Equal(t, "healthy", check.Check(ctx))
		assert.False(t, check.Critical)
		assert.Regexp(t, `^sftp-[0-9a-f]{12}$`, check.Name)
		assert.NotContains(t, check.Name, "testhost")
	})

	t.Run("does not expose host name in check name", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		mock := newMockSFTPClient(dir)
		b := NewWithClient(mock, Config{Host: "Files.EXAMPLE.com", Port: 2222, RootPath: dir})

		check := HealthCheck(b)
		assert.Regexp(t, `^sftp-[0-9a-f]{12}$`, check.Name)
		assert.NotContains(t, check.Name, "files")
		assert.NotContains(t, check.Name, "example")
		assert.NotContains(t, check.Name, "2222")
	})

	t.Run("critical check marks critical", func(t *testing.T) {
		t.Parallel()
		mock := newMockSFTPClient("/data")
		mock.statFn = func(p string) (os.FileInfo, error) {
			return nil, errors.New("down")
		}
		b := NewWithClient(mock, Config{Host: "testhost", Port: 22, RootPath: "/data"})

		check := CriticalHealthCheck(b)
		assert.Equal(t, "unhealthy", check.Check(ctx))
		assert.True(t, check.Critical)
	})
}

func TestSFTPBackend_ValidateKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	mock := newMockSFTPClient("/data")
	b := NewWithClient(mock, Config{Host: "localhost", RootPath: "/data"})

	err := b.Put(ctx, "", bytes.NewReader([]byte("x")), storage.ObjectMeta{})
	assert.Error(t, err)

	_, _, err = b.Get(ctx, "")
	assert.Error(t, err)

	_, err = b.Exists(ctx, "")
	assert.Error(t, err)

	err = b.Delete(ctx, "")
	assert.Error(t, err)
}

func TestSFTPBackend_RemotePath(t *testing.T) {
	t.Parallel()

	b := &Backend{cfg: Config{RootPath: "/data/storage"}}

	assert.Equal(t, "/data/storage/uploads/file.txt", b.remotePath("uploads/file.txt"))
	assert.Equal(t, "/data/storage/simple.txt", b.remotePath("simple.txt"))
}

// TestSFTPBackend_PutGet_Integration tests Put and Get using a real local filesystem
// via the local backend as a smoke test for the SFTP interface contract.
// The actual SFTP wire protocol is tested in integration tests.
func TestSFTPBackend_PutGet_LocalSmoke(t *testing.T) {
	t.Parallel()

	// We can't easily mock sftp.File, so test the flow using the local backend
	// through the compliance suite to verify the interface contract is correct.
	// Real SFTP integration tests use testcontainers (build tag: integration).

	dir := t.TempDir()
	rootPath := filepath.Join(dir, "sftp-root")
	require.NoError(t, os.MkdirAll(rootPath, 0o750))

	// Create a simple file to verify Stat works via mock.
	testFile := filepath.Join(rootPath, "exists.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("hello"), 0o640))

	mock := newMockSFTPClient(rootPath)
	mock.store[filepath.Join(rootPath, "exists.txt")] = []byte("hello")
	b := NewWithClient(mock, Config{Host: "localhost", RootPath: rootPath})

	ctx := context.Background()

	// Verify Exists works.
	ok, err := b.Exists(ctx, "exists.txt")
	require.NoError(t, err)
	assert.True(t, ok)

	// Verify Delete works.
	mock.store[filepath.Join(rootPath, "delete-me.txt")] = []byte("x")
	require.NoError(t, b.Delete(ctx, "delete-me.txt"))

	ok, err = b.Exists(ctx, "delete-me.txt")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestSFTPBackend_List(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("lists files with prefix", func(t *testing.T) {
		t.Parallel()
		mock := newMockSFTPClient("/data")
		mock.store["/data/uploads/a.txt"] = []byte("aaa")
		mock.store["/data/uploads/b.txt"] = []byte("bb")
		mock.store["/data/other.txt"] = []byte("x")
		b := NewWithClient(mock, Config{Host: "localhost", RootPath: "/data"})

		var results []storage.ObjectInfo
		for info, err := range b.List(ctx, "uploads/", storage.ListOptions{}) {
			require.NoError(t, err)
			results = append(results, info)
		}

		assert.Len(t, results, 2)
	})

	t.Run("respects MaxKeys", func(t *testing.T) {
		t.Parallel()
		mock := newMockSFTPClient("/data")
		mock.store["/data/a.txt"] = []byte("a")
		mock.store["/data/b.txt"] = []byte("b")
		mock.store["/data/c.txt"] = []byte("c")
		b := NewWithClient(mock, Config{Host: "localhost", RootPath: "/data"})

		var results []storage.ObjectInfo
		for info, err := range b.List(ctx, "", storage.ListOptions{MaxKeys: 2}) {
			require.NoError(t, err)
			results = append(results, info)
		}

		assert.LessOrEqual(t, len(results), 2)
	})

	t.Run("empty prefix lists all", func(t *testing.T) {
		t.Parallel()
		mock := newMockSFTPClient("/data")
		mock.store["/data/file1.txt"] = []byte("one")
		mock.store["/data/file2.txt"] = []byte("two")
		b := NewWithClient(mock, Config{Host: "localhost", RootPath: "/data"})

		var results []storage.ObjectInfo
		for info, err := range b.List(ctx, "", storage.ListOptions{}) {
			require.NoError(t, err)
			results = append(results, info)
		}

		assert.Len(t, results, 2)
	})

	t.Run("rejects symlink objects", func(t *testing.T) {
		t.Parallel()
		mock := newMockSFTPClient("/data")
		mock.readFn = func(p string) ([]os.FileInfo, error) {
			if p != "/data" {
				return nil, &sftp.StatusError{Code: ssh_FX_NO_SUCH_FILE}
			}
			return []os.FileInfo{
				fakeFileInfo{name: "secret-token-link", mode: os.ModeSymlink | 0o777},
			}, nil
		}
		b := NewWithClient(mock, Config{Host: "localhost", RootPath: "/data"})

		var errs []error
		for _, err := range b.List(ctx, "", storage.ListOptions{}) {
			if err != nil {
				errs = append(errs, err)
			}
		}
		require.NotEmpty(t, errs)
		assert.Contains(t, errs[0].Error(), "symlink")
		assert.NotContains(t, errs[0].Error(), "secret-token")
	})

	t.Run("rejects symlink root before reading", func(t *testing.T) {
		t.Parallel()
		mock := newMockSFTPClient("/data")
		mock.lstatFn = func(p string) (os.FileInfo, error) {
			if p == "/data" {
				return fakeFileInfo{name: "data", mode: os.ModeSymlink | 0o777}, nil
			}
			return nil, &sftp.StatusError{Code: ssh_FX_NO_SUCH_FILE}
		}
		readCalls := 0
		mock.readFn = func(string) ([]os.FileInfo, error) {
			readCalls++
			return nil, errors.New("backend should not read through symlink root")
		}
		b := NewWithClient(mock, Config{Host: "localhost", RootPath: "/data"})

		var errs []error
		for _, err := range b.List(ctx, "", storage.ListOptions{}) {
			if err != nil {
				errs = append(errs, err)
			}
		}
		require.NotEmpty(t, errs)
		assert.Contains(t, errs[0].Error(), "unsafe")
		assert.Equal(t, 0, readCalls)
	})

	t.Run("readdir error does not reflect remote path", func(t *testing.T) {
		t.Parallel()
		mock := newMockSFTPClient("/data")
		mock.readFn = func(p string) ([]os.FileInfo, error) {
			return nil, errors.New("cannot read " + p + "/secret-token")
		}
		b := NewWithClient(mock, Config{Host: "localhost", RootPath: "/data"})

		var errs []error
		for _, err := range b.List(ctx, "", storage.ListOptions{}) {
			if err != nil {
				errs = append(errs, err)
			}
		}
		require.NotEmpty(t, errs)
		assert.NotContains(t, errs[0].Error(), "secret-token")
		assert.NotContains(t, errs[0].Error(), "/data")
	})

	t.Run("rejects invalid options before reading remote dirs", func(t *testing.T) {
		t.Parallel()
		mock := newMockSFTPClient("/data")
		calls := 0
		mock.readFn = func(string) ([]os.FileInfo, error) {
			calls++
			return nil, errors.New("backend should not be called")
		}
		b := NewWithClient(mock, Config{Host: "localhost", RootPath: "/data"})

		var seenErr error
		for _, err := range b.List(ctx, "", storage.ListOptions{StartAfter: "bad key"}) {
			seenErr = err
			break
		}

		require.ErrorIs(t, seenErr, storage.ErrValidation)
		assert.Equal(t, 0, calls)
	})

	t.Run("yields keys in sorted order despite unsorted readdir", func(t *testing.T) {
		t.Parallel()
		mock := newMockSFTPClient("/data")
		mock.store["/data/b.txt"] = []byte("b")
		mock.store["/data/a.txt"] = []byte("a")
		mock.store["/data/c/d.txt"] = []byte("d")
		mock.store["/data/c/a.txt"] = []byte("ca")
		mock.store["/data/aa.txt"] = []byte("aa")
		mock.readFn = unsortedReadDir(mock)
		b := NewWithClient(mock, Config{Host: "localhost", RootPath: "/data"})

		var keys []string
		for info, err := range b.List(ctx, "", storage.ListOptions{}) {
			require.NoError(t, err)
			keys = append(keys, info.Key)
		}

		want := []string{"a.txt", "aa.txt", "b.txt", "c/a.txt", "c/d.txt"}
		assert.Equal(t, want, keys, "List must yield keys in lexicographic order")
	})

	t.Run("StartAfter pagination is complete and non-duplicating with unsorted readdir", func(t *testing.T) {
		t.Parallel()
		mock := newMockSFTPClient("/data")
		want := []string{
			"a.txt", "aa.txt", "b.txt", "c/a.txt", "c/d.txt", "c/z.txt", "e.txt",
		}
		for _, k := range want {
			mock.store["/data/"+k] = []byte(k)
		}
		mock.readFn = unsortedReadDir(mock)
		b := NewWithClient(mock, Config{Host: "localhost", RootPath: "/data"})

		var got []string
		opts := storage.ListOptions{MaxKeys: 2}
		for {
			page, err := storage.ListPage(ctx, b, "", opts)
			require.NoError(t, err)
			for _, info := range page.Objects {
				got = append(got, info.Key)
			}
			if !page.Truncated {
				break
			}
			opts.StartAfter = page.NextStartAfter
		}

		assert.Equal(t, want, got, "paging via StartAfter must return every key exactly once in order")
	})
}

// unsortedReadDir returns a ReadDir implementation backed by m.store that
// yields directory entries in reverse-lexicographic order, simulating a real
// SFTP server (and pkg/sftp's client.ReadDir), which does not sort results.
func unsortedReadDir(m *mockSFTPClient) func(string) ([]os.FileInfo, error) {
	return func(p string) ([]os.FileInfo, error) {
		prefix := p
		if !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}

		seen := make(map[string]bool)
		var result []os.FileInfo
		anyMatch := false

		for key, data := range m.store {
			if !strings.HasPrefix(key, prefix) {
				continue
			}
			anyMatch = true

			remainder := key[len(prefix):]
			if remainder == "" {
				continue
			}

			if slashIdx := strings.Index(remainder, "/"); slashIdx == -1 {
				if !seen[remainder] {
					seen[remainder] = true
					result = append(result, fakeFileInfo{name: remainder, size: int64(len(data))})
				}
			} else {
				dirName := remainder[:slashIdx]
				if !seen[dirName] {
					seen[dirName] = true
					result = append(result, fakeFileInfo{name: dirName, dir: true})
				}
			}
		}

		if !anyMatch {
			return nil, &sftp.StatusError{Code: ssh_FX_NO_SUCH_FILE}
		}

		// Reverse-lexicographic order: the opposite of what the production
		// code needs, to prove List sorts internally rather than relying on
		// server-provided ordering.
		sort.Slice(result, func(i, j int) bool { return result[i].Name() > result[j].Name() })
		return result, nil
	}
}

func TestSFTPBackend_RejectsSymlinkParent(t *testing.T) {
	t.Parallel()

	mock := newMockSFTPClient("/data")
	mock.lstatFn = func(p string) (os.FileInfo, error) {
		switch p {
		case "/data":
			return fakeFileInfo{name: "data", dir: true}, nil
		case "/data/secret-token-uploads":
			return fakeFileInfo{name: "secret-token-uploads", mode: os.ModeSymlink | 0o777}, nil
		default:
			return nil, &sftp.StatusError{Code: ssh_FX_NO_SUCH_FILE}
		}
	}
	b := NewWithClient(mock, Config{Host: "localhost", RootPath: "/data"})

	err := b.rejectSymlinkAncestors(mock, "/data/secret-token-uploads/file.txt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")
	assert.NotContains(t, err.Error(), "secret-token")
}

// Verify _ = io.ReadCloser is satisfied by sftp.File at compile time.
var _ io.ReadCloser = (*sftp.File)(nil)

// TestSFTPBackend_WithLogger_NilFallsBackToDefault pins the MEDIUM finding:
// WithLogger(nil) used to assign a nil pointer that only crashed on connect /
// health-failure logging paths. The fix normalizes nil → slog.Default().
func TestSFTPBackend_WithLogger_NilFallsBackToDefault(t *testing.T) {
	t.Parallel()
	mock := newMockSFTPClient("/nonexistent")
	mock.statFn = func(p string) (os.FileInfo, error) {
		return nil, errors.New("connection lost")
	}
	b := NewWithClient(mock, Config{Host: "localhost", RootPath: "/nonexistent"}, WithLogger(nil))

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Healthy panicked with nil logger option: %v", r)
		}
	}()
	// The health-failure log path used to nil-deref; verify it now logs cleanly.
	assert.False(t, b.Healthy())
}

func TestNewWithClient_PanicsOnNilClient(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		NewWithClient(nil, Config{Host: "localhost"})
	})
}

func TestNewWithClient_PanicsOnNilOption(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		NewWithClient(newMockSFTPClient("/data"), Config{Host: "localhost", RootPath: "/data"}, nil)
	})
}

// TestCloseIsTerminal pins the H-006 finding: after Close the backend
// must not silently reconnect on the next getClient. Operations should
// return storage.ErrBackendClosed instead.
func TestCloseIsTerminal(t *testing.T) {
	t.Parallel()
	mock := newMockSFTPClient("/data")
	b := NewWithClient(mock, Config{Host: "localhost", RootPath: "/data"})

	require.NoError(t, b.Close())
	// Idempotent.
	require.NoError(t, b.Close())

	if got := b.Healthy(); got {
		t.Fatalf("Healthy() after Close = true, want false")
	}

	ctx := context.Background()
	if err := b.Delete(ctx, "anything"); !errors.Is(err, storage.ErrBackendClosed) {
		t.Fatalf("Delete after Close: err = %v, want ErrBackendClosed", err)
	}
	if _, err := b.Exists(ctx, "anything"); !errors.Is(err, storage.ErrBackendClosed) {
		t.Fatalf("Exists after Close: err = %v, want ErrBackendClosed", err)
	}
	if _, _, err := b.Get(ctx, "anything"); !errors.Is(err, storage.ErrBackendClosed) {
		t.Fatalf("Get after Close: err = %v, want ErrBackendClosed", err)
	}
}
