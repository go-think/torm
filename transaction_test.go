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

func TestRollbackDiscardsAfterCommitHooks(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("BEGIN", &fakeResult{})
	st.On("ROLLBACK", &fakeResult{})

	discarded := false
	err := conn.Transaction(func(c *Connection) error {
		c.AfterCommit(func() { discarded = true })
		return errors.New("boom")
	})
	if err == nil {
		t.Fatal("want error")
	}
	if discarded {
		t.Error("afterCommit hooks of a rolled-back transaction must be discarded")
	}

	// 后续无关提交：新回调正常触发，被丢弃的回调不得触发
	st.On("BEGIN", &fakeResult{})
	st.On("COMMIT", &fakeResult{})
	fresh := false
	if err := conn.Transaction(func(c *Connection) error {
		c.AfterCommit(func() { fresh = true })
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !fresh {
		t.Error("fresh afterCommit hook should fire")
	}
	if discarded {
		t.Error("discarded hooks must not run on an unrelated commit")
	}
}

func TestAfterRollBackHookFires(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("BEGIN", &fakeResult{})
	st.On("ROLLBACK", &fakeResult{})

	fired := false
	err := conn.Transaction(func(c *Connection) error {
		c.AfterRollBack(func() { fired = true })
		return errors.New("boom")
	})
	if err == nil {
		t.Fatal("want error")
	}
	if !fired {
		t.Error("afterRollBack hook should fire on rollback")
	}
}

func TestNestedTransactionConcurrencyNoRetry(t *testing.T) {
	conn, st := newFakeDB(t)
	// 外层 BEGIN + SAVEPOINT，内层错误 → ROLLBACK TO SAVEPOINT，外层随后 ROLLBACK
	st.On("SAVEPOINT trans2", &fakeResult{})
	st.On("ROLLBACK TO SAVEPOINT trans2", &fakeResult{})

	outerBegan := 0
	innerCalls := 0
	deadlockErr := errors.New("Error 1213 (40001): Deadlock found when trying to get lock")

	err := conn.Transaction(func(c *Connection) error {
		outerBegan++
		return conn.Transaction(func(inner *Connection) error {
			innerCalls++
			return deadlockErr
		})
	})
	if err == nil {
		t.Fatal("want deadlock error to propagate")
	}
	if outerBegan != 1 || innerCalls != 1 {
		t.Errorf("outerBegan=%d innerCalls=%d — nested transactions must not retry",
			outerBegan, innerCalls)
	}
	if conn.TransactionLevel() != 0 {
		t.Errorf("level = %d, want 0", conn.TransactionLevel())
	}
}

func TestOutermostTransactionRetriesOnDeadlock(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("BEGIN", &fakeResult{})
	st.On("ROLLBACK", &fakeResult{})
	st.On("COMMIT", &fakeResult{})

	calls := 0
	deadlockErr := errors.New("SQLSTATE 40001: deadlock detected")
	err := conn.Transaction(func(c *Connection) error {
		calls++
		if calls == 1 {
			return deadlockErr
		}
		return nil
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2 (one retry)", calls)
	}
}

func TestTransactionHooksRunOutsideLock(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("BEGIN", &fakeResult{})
	st.On("COMMIT", &fakeResult{})

	err := conn.Transaction(func(c *Connection) error {
		c.AfterCommit(func() {
			// 回调在锁外执行：这些调用在持锁实现下会自死锁
			if conn.TransactionLevel() != 0 {
				t.Errorf("level = %d inside afterCommit hook, want 0", conn.TransactionLevel())
			}
			c.AfterCommit(func() {})
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
