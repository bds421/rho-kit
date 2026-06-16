package pgstore_test

// fakedriver is a tiny in-memory database/sql driver that models just
// enough Postgres behaviour to exercise pgstore's two write paths
// (INSERT … ON CONFLICT DO NOTHING and UPDATE … WHERE updated_at=$old)
// without a live database. It deliberately mirrors the real server's
// updated_at semantics: every write that touches a row stamps a fresh,
// monotonically advancing updated_at. This is what makes a stale
// snapshot from a single Get observable in a unit test.
//
// It registers itself under driverName via init so tests can open it
// with sql.Open(driverName, "").

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
)

const driverName = "pgstore_fake"

func init() {
	sql.Register(driverName, &fakeDriver{store: newFakeStore()})
}

// fakeRow is one saga_instances row, only the columns pgstore reads/writes.
type fakeRow struct {
	id          string
	definition  string
	state       string
	currentStep int64
	compensated string
	input       []byte
	stepResults string
	lastError   string
	createdAt   time.Time
	updatedAt   time.Time
}

type fakeStore struct {
	mu   sync.Mutex
	rows map[string]fakeRow
	// clock advances by 1ms on every now() so successive writes always
	// produce a strictly newer updated_at, matching a busy server where
	// statement_timestamp moves forward between statements.
	clock time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		rows:  make(map[string]fakeRow),
		clock: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func (s *fakeStore) now() time.Time {
	s.clock = s.clock.Add(time.Millisecond)
	return s.clock
}

type fakeDriver struct{ store *fakeStore }

func (d *fakeDriver) Open(string) (driver.Conn, error) {
	return &fakeConn{store: d.store}, nil
}

type fakeConn struct{ store *fakeStore }

func (c *fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not implemented") }
func (c *fakeConn) Close() error                        { return nil }
func (c *fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("not implemented") }

func (c *fakeConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	switch {
	case strings.Contains(query, "INSERT INTO"):
		return c.execInsert(args)
	case strings.Contains(query, "UPDATE"):
		return c.execUpdate(args)
	case strings.Contains(query, "DELETE"):
		return c.execDelete(args)
	}
	return nil, errors.New("fakeConn: unrecognised exec query")
}

// execInsert models: INSERT (...9 cols...) VALUES (...) ON CONFLICT (id) DO NOTHING.
// Args order matches putInsertOnly: id, definition, state, current_step,
// compensated, input, step_results, last_error. updated_at is now().
func (c *fakeConn) execInsert(args []driver.NamedValue) (driver.Result, error) {
	id := args[0].Value.(string)
	if _, exists := c.store.rows[id]; exists {
		return fakeResult{rows: 0}, nil // ON CONFLICT DO NOTHING
	}
	now := c.store.now()
	c.store.rows[id] = fakeRow{
		id:          id,
		definition:  args[1].Value.(string),
		state:       args[2].Value.(string),
		currentStep: args[3].Value.(int64),
		compensated: args[4].Value.(string),
		input:       toBytes(args[5].Value),
		stepResults: args[6].Value.(string),
		lastError:   args[7].Value.(string),
		createdAt:   now,
		updatedAt:   now,
	}
	return fakeResult{rows: 1}, nil
}

// execUpdate models: UPDATE ... SET ..., updated_at=now() WHERE id=$6.
// Args order matches putUpdateOptimistic: state, current_step, compensated,
// step_results, last_error, id. The store stamps a fresh updated_at on
// every write (so a stale caller snapshot is observable), but matches the
// row by ID only — mirroring the corrected query.
func (c *fakeConn) execUpdate(args []driver.NamedValue) (driver.Result, error) {
	id := args[5].Value.(string)
	row, exists := c.store.rows[id]
	if !exists {
		return fakeResult{rows: 0}, nil // WHERE matched no rows
	}
	now := c.store.now()
	row.state = args[0].Value.(string)
	row.currentStep = args[1].Value.(int64)
	row.compensated = args[2].Value.(string)
	row.stepResults = args[3].Value.(string)
	row.lastError = args[4].Value.(string)
	row.updatedAt = now
	c.store.rows[id] = row
	return fakeResult{rows: 1}, nil
}

func (c *fakeConn) execDelete(args []driver.NamedValue) (driver.Result, error) {
	delete(c.store.rows, args[0].Value.(string))
	return fakeResult{rows: 1}, nil
}

func (c *fakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	if !strings.Contains(query, "SELECT") {
		return nil, errors.New("fakeConn: unrecognised query")
	}
	// Get: SELECT ... WHERE id = $1
	if strings.Contains(query, "WHERE id = $1") {
		id := args[0].Value.(string)
		row, ok := c.store.rows[id]
		if !ok {
			return &fakeRows{}, nil
		}
		return &fakeRows{data: [][]driver.Value{rowValues(row)}}, nil
	}
	// ListResumable: return all non-terminal rows (test does not exercise this).
	var data [][]driver.Value
	for _, row := range c.store.rows {
		if row.state == "completed" || row.state == "failed" {
			continue
		}
		data = append(data, rowValues(row))
	}
	return &fakeRows{data: data}, nil
}

func rowValues(row fakeRow) []driver.Value {
	return []driver.Value{
		row.id, row.definition, row.state, row.currentStep,
		[]byte(row.compensated), row.input, []byte(row.stepResults), row.lastError,
		row.createdAt, row.updatedAt,
	}
}

func toBytes(v driver.Value) []byte {
	if v == nil {
		return nil
	}
	if b, ok := v.([]byte); ok {
		return b
	}
	return nil
}

type fakeResult struct{ rows int64 }

func (r fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (r fakeResult) RowsAffected() (int64, error) { return r.rows, nil }

type fakeRows struct {
	data [][]driver.Value
	pos  int
}

func (r *fakeRows) Columns() []string {
	return []string{
		"id", "definition", "state", "current_step",
		"compensated", "input", "step_results", "last_error",
		"created_at", "updated_at",
	}
}

func (r *fakeRows) Close() error { return nil }

func (r *fakeRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.pos])
	r.pos++
	return nil
}

// openFakeDB returns a *sql.DB backed by a fresh fake store so each test
// is isolated.
func openFakeDB(t interface{ Fatalf(string, ...any) }) *sql.DB {
	d := &fakeDriver{store: newFakeStore()}
	name := driverName + "_" + randSuffix()
	sql.Register(name, d)
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("open fake db: %v", err)
	}
	return db
}

var suffixMu sync.Mutex
var suffixSeq int

func randSuffix() string {
	suffixMu.Lock()
	defer suffixMu.Unlock()
	suffixSeq++
	return time.Now().Format("150405.000000000") + "_" + itoa(suffixSeq)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
