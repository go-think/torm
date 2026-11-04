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
