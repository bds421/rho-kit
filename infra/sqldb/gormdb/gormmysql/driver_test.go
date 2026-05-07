package gormmysql

import (
	"crypto/tls"
	"crypto/x509"
	"log/slog"
	"strings"
	"testing"

	"github.com/bds421/rho-kit/infra/sqldb"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb"
)

func TestMySQLDriver_Name(t *testing.T) {
	d := MySQLDriver{}
	if got := d.Name(); got != "mysql" {
		t.Errorf("Name() = %q, want %q", got, "mysql")
	}
}

func TestMySQLDriver_ImplementsDriver(t *testing.T) {
	var _ gormdb.Driver = MySQLDriver{}
}

func TestBuildMySQLDSN_Defaults(t *testing.T) {
	cfg := sqldb.Config{
		Host:     "localhost",
		Port:     3306,
		User:     "root",
		Password: "secret",
		Name:     "testdb",
	}
	got := buildMySQLDSN(cfg)
	want := "root:secret@tcp(localhost:3306)/testdb?charset=utf8mb4&parseTime=True&loc=UTC&clientFoundRows=true"
	if got != want {
		t.Errorf("buildMySQLDSN() =\n  %q\nwant\n  %q", got, want)
	}
}

func TestBuildMySQLDSN_CustomCharset(t *testing.T) {
	cfg := sqldb.Config{
		Host:     "db.example.com",
		Port:     3307,
		User:     "app",
		Password: "p@ss",
		Name:     "mydb",
		Options:  map[string]string{"charset": "utf8"},
	}
	got := buildMySQLDSN(cfg)
	want := "app:p%40ss@tcp(db.example.com:3307)/mydb?charset=utf8&parseTime=True&loc=UTC&clientFoundRows=true"
	if got != want {
		t.Errorf("buildMySQLDSN() =\n  %q\nwant\n  %q", got, want)
	}
}

func TestRegisterTLSConfigDedup_RefCountReuse(t *testing.T) {
	cfg := &tls.Config{ServerName: "dedup.example.test", RootCAs: x509.NewCertPool()}
	defer ReleaseTLS(cfg)
	defer ReleaseTLS(cfg)

	keyA, err := registerTLSConfigDedup(cfg)
	if err != nil {
		t.Fatalf("first register: %v", err)
	}
	keyB, err := registerTLSConfigDedup(cfg)
	if err != nil {
		t.Fatalf("second register: %v", err)
	}
	if keyA != keyB {
		t.Fatalf("expected dedup to reuse key, got %q vs %q", keyA, keyB)
	}

	fp := tlsFingerprint(cfg)
	tlsRegistryMu.Lock()
	entry, ok := tlsRegistry[fp]
	tlsRegistryMu.Unlock()
	if !ok {
		t.Fatal("expected registry to retain entry after two registers")
	}
	if entry.refCount != 2 {
		t.Errorf("refCount = %d, want 2", entry.refCount)
	}
}

func TestReleaseTLS_DropsEntryAtZero(t *testing.T) {
	cfg := &tls.Config{ServerName: "release.example.test", RootCAs: x509.NewCertPool()}

	if _, err := registerTLSConfigDedup(cfg); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := registerTLSConfigDedup(cfg); err != nil {
		t.Fatalf("register: %v", err)
	}

	fp := tlsFingerprint(cfg)

	ReleaseTLS(cfg)

	tlsRegistryMu.Lock()
	entry, stillThere := tlsRegistry[fp]
	tlsRegistryMu.Unlock()
	if !stillThere {
		t.Fatal("entry dropped after first release; expected still present (refCount > 0)")
	}
	if entry.refCount != 1 {
		t.Errorf("refCount after first Release = %d, want 1", entry.refCount)
	}

	ReleaseTLS(cfg)

	tlsRegistryMu.Lock()
	_, stillThere = tlsRegistry[fp]
	tlsRegistryMu.Unlock()
	if stillThere {
		t.Error("entry still present after refCount hit zero")
	}
}

func TestReleaseTLS_NoOpForUnknownConfig(t *testing.T) {
	cfg := &tls.Config{ServerName: "never-registered.example.test"}
	// Must not panic and must not affect any other entry.
	ReleaseTLS(cfg)
	ReleaseTLS(nil)
}

func TestReleaseTLS_FingerprintEquivalence(t *testing.T) {
	pool := x509.NewCertPool()
	cfgA := &tls.Config{ServerName: "fp.example.test", RootCAs: pool}
	cfgB := &tls.Config{ServerName: "fp.example.test", RootCAs: pool}

	if _, err := registerTLSConfigDedup(cfgA); err != nil {
		t.Fatalf("register: %v", err)
	}

	fp := tlsFingerprint(cfgA)

	// A different *tls.Config pointer with equivalent content must release
	// the same entry — callers shouldn't have to retain the original pointer.
	ReleaseTLS(cfgB)

	tlsRegistryMu.Lock()
	_, stillThere := tlsRegistry[fp]
	tlsRegistryMu.Unlock()
	if stillThere {
		t.Error("entry still present; ReleaseTLS via equivalent cfg did not match by fingerprint")
	}
}

// TestMySQLDriver_Open_TLSTrueWithoutClientConfigErrors pins the high-severity
// finding: Config.Options["tls"]=true used to be treated as enabled by
// IsTLSEnabled while the DSN omitted tls=, so the connection silently
// went plaintext. The fix rejects that combination at Open time when no
// *tls.Config has been supplied.
func TestMySQLDriver_Open_TLSTrueWithoutClientConfigErrors(t *testing.T) {
	cfg := sqldb.Config{
		Host: "localhost", Port: 3306,
		User: "u", Password: "p", Name: "db",
		Options: map[string]string{"tls": "true"},
	}
	_, err := MySQLDriver{}.Open(cfg, sqldb.DefaultPool(), slog.Default(), nil)
	if err == nil {
		t.Fatal("expected error for tls=true without registered TLS config")
	}
	if !strings.Contains(err.Error(), "tls") {
		t.Errorf("error %q should mention TLS", err.Error())
	}
}

// TestMySQLDriver_Open_ReleasesTLSOnFailure pins the medium-severity
// registry-leak finding: an Open that fails after registerTLSConfigDedup
// (host unreachable, ping timeout, etc.) used to leak the TLS registry
// entry forever. With the fix, every error path after registration
// releases the entry.
func TestMySQLDriver_Open_ReleasesTLSOnFailure(t *testing.T) {
	cfg := sqldb.Config{
		// Port 1 reliably refuses on every platform — gorm.Open will
		// fail (or PingContext will), exercising the failure path.
		Host: "127.0.0.1", Port: 1,
		User: "u", Password: "p", Name: "db",
	}
	tlsCfg := &tls.Config{ServerName: "leak.example.test", RootCAs: x509.NewCertPool()}

	fp := tlsFingerprint(tlsCfg)

	_, err := MySQLDriver{}.Open(cfg, sqldb.DefaultPool(), slog.Default(), tlsCfg)
	if err == nil {
		t.Fatal("expected open to fail against unreachable host")
	}

	tlsRegistryMu.Lock()
	_, leaked := tlsRegistry[fp]
	tlsRegistryMu.Unlock()
	if leaked {
		t.Error("TLS registry entry leaked after failed Open")
	}
}

// TestMySQLDriver_Open_PassesThroughTLSBuiltins ensures driver builtins
// like skip-verify and preferred are appended to the DSN verbatim
// without interacting with the registry.
func TestBuildMySQLDSN_TLSBuiltinPassThrough(t *testing.T) {
	cfg := sqldb.Config{
		Host: "h", Port: 3306, User: "u", Password: "p", Name: "db",
		Options: map[string]string{"tls": "skip-verify"},
	}
	// buildMySQLDSN itself does NOT add tls= — that happens in Open.
	got := buildMySQLDSN(cfg)
	if strings.Contains(got, "tls=") {
		t.Errorf("buildMySQLDSN must not emit tls=, got %q", got)
	}
}

func TestBuildMySQLDSN_SpecialCharsInCredentials(t *testing.T) {
	cfg := sqldb.Config{
		Host:     "127.0.0.1",
		Port:     3306,
		User:     "user@host",
		Password: "p@ss:word/test",
		Name:     "db/name",
	}
	got := buildMySQLDSN(cfg)
	want := "user%40host:p%40ss%3Aword%2Ftest@tcp(127.0.0.1:3306)/db%2Fname?charset=utf8mb4&parseTime=True&loc=UTC&clientFoundRows=true"
	if got != want {
		t.Errorf("buildMySQLDSN() =\n  %q\nwant\n  %q", got, want)
	}
}
