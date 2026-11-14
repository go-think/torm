package torm

import (
	"database/sql/driver"
	"testing"

	"github.com/go-think/torm/internal/faketb"
)

// ---- 内存假 driver：让 Connection 层测试脱离真实 MySQL ----
// 实现位于 internal/faketb（不依赖 torm 根包，供内部/外部测试包共用）。

type fakeStore = faketb.Store
type fakeCall = faketb.Call
type fakeResult = faketb.Result

func newFakeDB(t *testing.T) (*Connection, *fakeStore) {
	t.Helper()
	st := faketb.New(t)
	conn, err := Open(Config{Driver: "fake", Dsn: st.Name()})
	if err != nil {
		t.Fatal(err)
	}
	return conn, st
}

var _ = driver.Value(nil)

// ---- Connection 层行为测试 ----

type fakeUser struct {
	Id   int64  `torm:"primary_key;column:id"`
	Name string `torm:"column:name"`
}

func TestFakeConnection_SelectOne(t *testing.T) {
	db, st := newFakeDB(t)
	st.On("select * from users where id = ?", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:    [][]driver.Value{{int64(1), "Andrew"}},
	})

	var user User
	err := db.SelectOne("select * from users where id = ?", []interface{}{1}, &user)
	if err != nil {
		t.Fatal(err)
	}
	if user.Id != 1 || user.Name != "Andrew" {
		t.Fatalf("got %+v", user)
	}
}

func TestFakeConnection_SelectSliceOfPointers(t *testing.T) {
	db, st := newFakeDB(t)
	st.On("select * from users", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:    [][]driver.Value{{int64(1), "Andrew"}, {int64(2), "Boston"}},
	})

	var users []*User
	if err := db.Select("select * from users", nil, &users); err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 {
		t.Fatalf("expect 2 users, got %d", len(users))
	}
	// P0-9 证据点：两个指针必须指向不同数据
	if users[0].Name == users[1].Name {
		t.Fatalf("[]*T scan bug: both rows show %q (elements share one backing struct)", users[0].Name)
	}
}

func TestFakeConnection_AffectingStatements(t *testing.T) {
	db, st := newFakeDB(t)
	st.On("insert into users (name) values (?)", &fakeResult{Affected: 1, LastID: 42})
	st.On("update users set name = ? where id = ?", &fakeResult{Affected: 1})
	st.On("delete from users where id = ?", &fakeResult{Affected: 1})
	st.On("create table x (id int)", &fakeResult{Affected: 0})

	if id, n, err := db.Insert("insert into users (name) values (?)", "A"); err != nil || id != 42 || n != 1 {
		t.Fatalf("insert: id=%d n=%d err=%v", id, n, err)
	}
	if n, err := db.Update("update users set name = ? where id = ?", "B", 1); err != nil || n != 1 {
		t.Fatalf("update: n=%d err=%v", n, err)
	}
	if n, err := db.Delete("delete from users where id = ?", 1); err != nil || n != 1 {
		t.Fatalf("delete: n=%d err=%v", n, err)
	}
	if err := db.Statement("create table x (id int)"); err != nil {
		t.Fatal(err)
	}
}

func TestFakeConnection_ScanScalars(t *testing.T) {
	db, st := newFakeDB(t)
	st.On("select count(*) from users", &fakeResult{
		Columns: []string{"count(*)"},
		Rows:    [][]driver.Value{{int64(7)}},
	})
	var n int64
	if err := db.Scan("select count(*) from users", nil, &n); err != nil {
		t.Fatal(err)
	}
	if n != 7 {
		t.Fatalf("count = %d", n)
	}
}
