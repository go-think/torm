// Package faketb provides an in-memory fake database/sql driver so the torm
// test suites can exercise the connection layer without a real MySQL server.
//
// The package deliberately does NOT import torm: in-package test files of the
// torm root package may only import packages that never (transitively) import
// torm, and the external torm_test package shares the same fake through the
// globally registered "fake" driver name.
package faketb

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

// Result is one stubbed outcome for a SQL statement.
type Result struct {
	Columns  []string
	Rows     [][]driver.Value
	Affected int64
	LastID   int64
	Err      error
}

// Call records one executed statement.
type Call struct {
	Kind string // query / exec
	Sql  string
	Args []driver.Value
}

// Store holds the stubbed results and the call log of one fake connection.
type Store struct {
	mu      sync.Mutex
	name    string
	results map[string][]*Result
	actions []string
	calls   []Call
}

var (
	storesMu sync.Mutex
	stores   = map[string]*Store{}
)

// New creates a store bound to a fresh fake database name.
func New(t *testing.T) *Store {
	t.Helper()
	storesMu.Lock()
	defer storesMu.Unlock()
	name := "probe" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_"))
	st := &Store{name: name, results: map[string][]*Result{}}
	stores[name] = st
	return st
}

// NewNamed returns (creating when absent) the store bound to a fixed
// database name, for tests that need several distinct fake databases.
func NewNamed(name string) *Store {
	storesMu.Lock()
	defer storesMu.Unlock()
	st, ok := stores[name]
	if !ok {
		st = &Store{name: name, results: map[string][]*Result{}}
		stores[name] = st
	}
	return st
}

// Name returns the DSN that opens a connection wired to this store.
func (s *Store) Name() string { return s.name }

// On stubs a result for an exact SQL match.
func (s *Store) On(sql string, r *Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results[sql] = append(s.results[sql], r)
}

// Record logs one executed statement.
func (s *Store) Record(kind, sql string, args []driver.Value) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, Call{Kind: kind, Sql: sql, Args: args})
}

// LastCall asserts that sql was executed at least once and returns the most
// recent matching call.
func (s *Store) LastCall(t *testing.T, sql string) Call {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.calls) - 1; i >= 0; i-- {
		if s.calls[i].Sql == sql {
			return s.calls[i]
		}
	}
	var got []string
	for _, call := range s.calls {
		got = append(got, call.Sql)
	}
	t.Fatalf("no call for %q; got %v", sql, got)
	return Call{}
}

// Calls returns a snapshot of the executed-statement log.
func (s *Store) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.calls...)
}

// Find pops the next stubbed result for sql. A single stub is reusable
// (kept in place); multiple stubs are consumed in insertion order.
func (s *Store) Find(sql string) *Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.actions = append(s.actions, sql)
	queue := s.results[sql]
	if len(queue) == 0 {
		return nil
	}
	if len(queue) == 1 {
		return queue[0]
	}
	r := queue[0]
	s.results[sql] = queue[1:]
	return r
}

// ---- fake driver plumbing ----

type fakeDriver struct{}

func init() { sql.Register("fake", &fakeDriver{}) }

func (d *fakeDriver) Open(name string) (driver.Conn, error) {
	storesMu.Lock()
	defer storesMu.Unlock()
	st, ok := stores[name]
	if !ok {
		st = &Store{name: name, results: map[string][]*Result{}}
		stores[name] = st
	}
	return &fakeConn{st: st}, nil
}

type fakeConn struct{ st *Store }

func (c *fakeConn) Prepare(query string) (driver.Stmt, error) {
	return &fakeStmt{st: c.st, q: query}, nil
}
func (c *fakeConn) Close() error { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) {
	c.st.Record("exec", "BEGIN", nil)
	return &fakeTx{st: c.st}, nil
}

type fakeTx struct{ st *Store }

func (t *fakeTx) Commit() error {
	t.st.Record("exec", "COMMIT", nil)
	return nil
}
func (t *fakeTx) Rollback() error {
	t.st.Record("exec", "ROLLBACK", nil)
	return nil
}

type fakeStmt struct {
	st *Store
	q  string
}

func (s *fakeStmt) Close() error  { return nil }
func (s *fakeStmt) NumInput() int { return -1 }

func (s *fakeStmt) Exec(args []driver.Value) (driver.Result, error) {
	s.st.Record("exec", s.q, args)
	r := s.st.Find(s.q)
	if r == nil {
		return nil, errNotFound
	}
	if r.Err != nil {
		return nil, r.Err
	}
	return fakeDriverResult{r}, nil
}

func (s *fakeStmt) Query(args []driver.Value) (driver.Rows, error) {
	s.st.Record("query", s.q, args)
	r := s.st.Find(s.q)
	if r == nil {
		return nil, errNotFound
	}
	if r.Err != nil {
		return nil, r.Err
	}
	return &fakeRows{r: r, i: -1}, nil
}

var errNotFound = errFake{"no stubbed result for query"}

type errFake struct{ msg string }

func (e errFake) Error() string { return "fake: " + e.msg }

type fakeDriverResult struct{ r *Result }

func (f fakeDriverResult) LastInsertId() (int64, error) { return f.r.LastID, nil }
func (f fakeDriverResult) RowsAffected() (int64, error) { return f.r.Affected, nil }

type fakeRows struct {
	r  *Result
	i  int
	mu sync.Mutex
}

func (f *fakeRows) Columns() []string { return f.r.Columns }
func (f *fakeRows) Close() error      { return nil }
func (f *fakeRows) Next(dest []driver.Value) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.i++
	if f.i >= len(f.r.Rows) {
		return io.EOF
	}
	copy(dest, f.r.Rows[f.i])
	return nil
}

var _ = fmt.Sprintf
