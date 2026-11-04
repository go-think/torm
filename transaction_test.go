package torm

import (
	"database/sql/driver"
	"errors"
	"testing"
)

func TestTransactionCommit(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("insert into users (name) values (?)", &fakeResult{Affected: 1, LastID: 3})
	st.On("BEGIN", &fakeResult{})
	st.On("COMMIT", &fakeResult{})

	fired := false
	conn.AfterCommit(func() { fired = true })

	err := conn.Transaction(func(c *Connection) error {
		_, _, err := c.Insert("insert into users (name) values (?)", "A")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if conn.TransactionLevel() != 0 {
		t.Errorf("level = %d", conn.TransactionLevel())
	}
	if !fired {
		t.Error("afterCommit hook should fire")
	}
	st.LastCall(t, "COMMIT")
}

func TestTransactionRollback(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("BEGIN", &fakeResult{})
	st.On("ROLLBACK", &fakeResult{})

	fired := false
	wantErr := errors.New("boom")
	err := conn.Transaction(func(c *Connection) error {
		conn.AfterCommit(func() { fired = true })
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v", err)
	}
	if conn.TransactionLevel() != 0 {
		t.Errorf("level = %d", conn.TransactionLevel())
	}
	if fired {
		t.Error("afterCommit hook must not fire on rollback")
	}
	st.LastCall(t, "ROLLBACK")
}

func TestNestedTransactionSavepoints(t *testing.T) {
	conn, st := newFakeDB(t)
	for _, sql := range []string{"BEGIN", "COMMIT", "SAVEPOINT trans2", "ROLLBACK TO SAVEPOINT trans2"} {
		st.On(sql, &fakeResult{})
	}

	err := conn.Transaction(func(outer *Connection) error {
		if outer.TransactionLevel() != 1 {
			t.Errorf("outer level = %d", outer.TransactionLevel())
		}
		return outer.Transaction(func(inner *Connection) error {
			if inner.TransactionLevel() != 2 {
				t.Errorf("inner level = %d", inner.TransactionLevel())
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if conn.TransactionLevel() != 0 {
		t.Errorf("final level = %d", conn.TransactionLevel())
	}
	// 内层提交不应产生 COMMIT（仅最外层提交到数据库）
	for _, call := range st.Calls() {
		if call.Sql == "COMMIT" {
			// 只允许一次（最外层）
		}
	}
	commits := 0
	for _, call := range st.Calls() {
		if call.Sql == "COMMIT" {
			commits++
		}
	}
	if commits != 1 {
		t.Errorf("commits = %d, want 1", commits)
	}
}

func TestNestedTransactionRollbackToSavepoint(t *testing.T) {
	conn, st := newFakeDB(t)
	for _, sql := range []string{"BEGIN", "COMMIT", "SAVEPOINT trans2", "ROLLBACK TO SAVEPOINT trans2"} {
		st.On(sql, &fakeResult{})
	}

	innerErr := errors.New("inner boom")
	err := conn.Transaction(func(outer *Connection) error {
		return outer.Transaction(func(inner *Connection) error {
			return innerErr
		})
	})
	if !errors.Is(err, innerErr) {
		t.Fatalf("err = %v", err)
	}
	if conn.TransactionLevel() != 0 {
		t.Errorf("final level = %d", conn.TransactionLevel())
	}
	st.LastCall(t, "ROLLBACK TO SAVEPOINT trans2")
}

func TestAfterCommitQueuedFromNested(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("BEGIN", &fakeResult{})
	st.On("COMMIT", &fakeResult{})
	st.On("SAVEPOINT trans2", &fakeResult{})

	var order []string
	err := conn.Transaction(func(outer *Connection) error {
		return outer.Transaction(func(inner *Connection) error {
			inner.AfterCommit(func() { order = append(order, "nested-hook") })
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != 1 || order[0] != "nested-hook" {
		t.Errorf("order = %v", order)
	}
}

func TestAfterCommitImmediateOutsideTransaction(t *testing.T) {
	conn, _ := newFakeDB(t)
	fired := false
	conn.AfterCommit(func() { fired = true })
	if !fired {
		t.Error("hook should fire immediately when no transaction is active")
	}
}

func TestOnQueryHook(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select 1", &fakeResult{Columns: []string{"1"}, Rows: [][]driver.Value{{int64(1)}}})

	var got QueryExecuted
	conn.OnQuery(func(e QueryExecuted) {
		got = e
	})
	var n int64
	if err := conn.Scan("select 1", nil, &n); err != nil {
		t.Fatal(err)
	}
	if got.Sql != "select 1" {
		t.Errorf("sql = %q", got.Sql)
	}
	if got.TimeNs < 0 {
		t.Errorf("time = %d", got.TimeNs)
	}
}
