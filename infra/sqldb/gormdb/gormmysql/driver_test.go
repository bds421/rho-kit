package gormmysql

import (
	"testing"

	"github.com/bds421/rho-kit/infra/sqldb"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb"
)

func TestMySQLDriver_Name(t *testing.T) {
	d := &MySQLDriver{}
	if got := d.Name(); got != "mysql" {
		t.Errorf("Name() = %q, want %q", got, "mysql")
	}
}

func TestMySQLDriver_ImplementsDriver(t *testing.T) {
	var _ gormdb.Driver = &MySQLDriver{}
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
	want := "root:secret@tcp(localhost:3306)/testdb?charset=utf8mb4&parseTime=True&loc=Local&clientFoundRows=true"
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
	want := "app:p%40ss@tcp(db.example.com:3307)/mydb?charset=utf8&parseTime=True&loc=Local&clientFoundRows=true"
	if got != want {
		t.Errorf("buildMySQLDSN() =\n  %q\nwant\n  %q", got, want)
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
	want := "user%40host:p%40ss%3Aword%2Ftest@tcp(127.0.0.1:3306)/db%2Fname?charset=utf8mb4&parseTime=True&loc=Local&clientFoundRows=true"
	if got != want {
		t.Errorf("buildMySQLDSN() =\n  %q\nwant\n  %q", got, want)
	}
}
