package torm

import (
	"errors"
	"testing"
)

func TestErrorClassification(t *testing.T) {
	cases := []struct {
		msg  string
		kind ErrorKind
	}{
		{"Error 1062: Duplicate entry 'foo@x.com' for key 'users.email_unique'", KindUniqueConstraint},
		{"ERROR: duplicate key value violates unique constraint (SQLSTATE 23505)", KindUniqueConstraint},
		{"Error 1213: Deadlock found when trying to get lock", KindDeadlock},
		{"SQLSTATE 40001: serialization failure", KindDeadlock},
		{"Error 2006: MySQL server has gone away", KindLostConnection},
		{"dial tcp 127.0.0.1:3306: connect: connection refused", KindLostConnection},
		{"some other error", KindGeneric},
	}
	for _, tc := range cases {
		if got := classifyError(errors.New(tc.msg)); got != tc.kind {
			t.Errorf("classify(%q) = %v, want %v", tc.msg, got, tc.kind)
		}
	}
}

func TestWrappedQueryError(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("insert into users (email) values (?)", &fakeResult{
		Err: errors.New("Error 1062: Duplicate entry 'a' for key 'email'"),
	})

	_, _, err := conn.Insert("insert into users (email) values (?)", "a")
	if err == nil {
		t.Fatal("expect error")
	}
	var qe *Error
	if !errors.As(err, &qe) {
		t.Fatalf("expect *Error, got %T: %v", err, err)
	}
	if qe.Kind != KindUniqueConstraint {
		t.Errorf("kind = %v", qe.Kind)
	}
	if qe.Sql != "insert into users (email) values (?)" {
		t.Errorf("sql = %q", qe.Sql)
	}
}

func TestErrorRawSqlInterpolation(t *testing.T) {
	err := &Error{
		Kind:     KindGeneric,
		Err:      errors.New("boom"),
		Sql:      "select * from users where name = ? and age > ? and active = ?",
		Bindings: []any{"O'Brien", 18, true},
	}
	want := "select * from users where name = 'O''Brien' and age > 18 and active = 1"
	if got := err.RawSql(); got != want {
		t.Errorf("RawSql = %q, want %q", got, want)
	}
	if err.ConnectionName() != "" {
		t.Errorf("ConnectionName = %q", err.ConnectionName())
	}

	nullErr := &Error{Err: errors.New("x"), Sql: "insert into t values (?, ?)", Bindings: []any{nil, "a"}}
	if got := nullErr.RawSql(); got != "insert into t values (null, 'a')" {
		t.Errorf("RawSql = %q", got)
	}
}

func TestClassifySQLiteAndPGLockErrors(t *testing.T) {
	if got := classifyError(errors.New("database is locked")); got != KindDeadlock {
		t.Errorf("sqlite lock classify = %v", got)
	}
	if got := classifyError(errors.New("SQLSTATE 40P01: deadlock detected")); got != KindDeadlock {
		t.Errorf("pg deadlock classify = %v", got)
	}
	if got := classifyError(errors.New("driver: bad connection")); got != KindLostConnection {
		t.Errorf("lost connection classify = %v", got)
	}
}
