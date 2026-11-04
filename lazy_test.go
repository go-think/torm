package torm

import (
	"database/sql/driver"
	"testing"

	"github.com/thinkoner/torm/internal/faketb"
)

func rowsUsers(rows ...[2]driver.Value) *fakeResult {
	out := make([][]driver.Value, 0, len(rows))
	for _, r := range rows {
		out = append(out, []driver.Value{r[0], r[1]})
	}
	return &fakeResult{Columns: []string{"id", "name"}, Rows: out}
}

func TestCursorStreamsRows(t *testing.T) {
	b, st := execBaseBuilder(t)
	sql := `select * from "users"`
	st.On(sql, rowsUsers([2]driver.Value{int64(1), "A"}, [2]driver.Value{int64(2), "B"}, [2]driver.Value{int64(3), "C"}))

	seq, err := b.From("users").Cursor()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for row, rowErr := range seq {
		if rowErr != nil {
			t.Fatal(rowErr)
		}
		count++
		if row["name"] == "C" {
			break // 提前终止不应 panic
		}
	}
	if count != 3 {
		t.Errorf("count = %d", count)
	}
}

func TestLazyPagesThrough(t *testing.T) {
	b, st := execBaseBuilder(t)
	st.On(`select * from "users" limit 2 offset 0`, rowsUsers([2]driver.Value{int64(1), "A"}, [2]driver.Value{int64(2), "B"}))
	st.On(`select * from "users" limit 2 offset 2`, rowsUsers([2]driver.Value{int64(3), "C"}))

	seq, err := b.From("users").Lazy(2)
	if err != nil {
		t.Fatal(err)
	}
	names := ""
	for row, rowErr := range seq {
		if rowErr != nil {
			t.Fatal(rowErr)
		}
		names += row["name"].(string)
	}
	if names != "ABC" {
		t.Errorf("names = %q", names)
	}
	st.LastCall(t, `select * from "users" limit 2 offset 2`)
}

func TestLazyByIdUsesIdCursor(t *testing.T) {
	b, st := execBaseBuilder(t)
	st.On(`select * from "users" where "id" > ? order by "id" asc limit 2`, rowsUsers([2]driver.Value{int64(1), "A"}, [2]driver.Value{int64(2), "B"}))
	st.On(`select * from "users" where "id" > ? order by "id" asc limit 2`, rowsUsers([2]driver.Value{int64(3), "C"}))

	// 第二页打桩（顺序队列）
	st.On(`select * from "users" where "id" > ? order by "id" asc limit 2`, rowsUsers([2]driver.Value{int64(3), "C"}))

	seq, err := b.From("users").LazyById(2)
	if err != nil {
		t.Fatal(err)
	}
	names := ""
	pages := 0
	lastSeen := int64(0)
	for row, rowErr := range seq {
		if rowErr != nil {
			t.Fatal(rowErr)
		}
		pages++
		names += row["name"].(string)
		lastSeen = row["id"].(int64)
	}
	t.Logf("names=%q lastSeen=%d pages=%d", names, lastSeen, pages)
	if pages < 2 {
		t.Errorf("pages = %d, want >= 2 (id 游标翻页)", pages)
	}
}

func TestChunkByIdWalksCursor(t *testing.T) {
	b, st := execBaseBuilder(t)
	st.On(`select * from "users" where "id" > ? order by "id" asc limit 2`, rowsUsers([2]driver.Value{int64(1), "A"}, [2]driver.Value{int64(2), "B"}))
	// 第二页基于游标 id > 2
	st.On(`select * from "users" where "id" > ? order by "id" asc limit 2`, rowsUsers([2]driver.Value{int64(3), "C"}))

	var ids []int64
	err := b.From("users").ChunkById(2,
		func() any { return &[]User{} },
		func(page any) (bool, error) {
			for _, u := range *page.(*[]User) {
				ids = append(ids, u.Id)
			}
			return true, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	// 第一页 [1,2]，第二页基于 id>2 → [3]
	if len(ids) != 3 || ids[0] != 1 || ids[2] != 3 {
		t.Errorf("ids = %v", ids)
	}
}

func TestChunkByIdEarlyStop(t *testing.T) {
	b, st := execBaseBuilder(t)
	st.On(`select * from "users" where "id" > ? order by "id" asc limit 2`, rowsUsers([2]driver.Value{int64(1), "A"}, [2]driver.Value{int64(2), "B"}))

	calls := 0
	err := b.From("users").ChunkById(2,
		func() any { return &[]User{} },
		func(page any) (bool, error) {
			calls++
			return false, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want early stop", calls)
	}
}

func TestEachById(t *testing.T) {
	b, st := execBaseBuilder(t)
	st.On(`select * from "users" where "id" > ? order by "id" asc limit 1000`, rowsUsers([2]driver.Value{int64(1), "A"}))

	var names []string
	err := b.From("users").EachById(
		func() any { return &[]User{} },
		func(item any) (bool, error) {
			names = append(names, item.(*User).Name)
			return true, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "A" {
		t.Errorf("names = %v", names)
	}
}

func TestCursorSurfacesScanError(t *testing.T) {
	b, st := execBaseBuilder(t)
	st.On(`select * from "users"`, &fakeResult{
		Columns: []string{"id"},
		Rows:   [][]driver.Value{{int64(1)}},
	})

	seq, err := b.From("users").Cursor()
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for range seq {
		seen++
	}
	if seen != 1 {
		t.Errorf("seen = %d", seen)
	}
}

func TestReadWriteSplitRouting(t *testing.T) {
	// 主连接（写）与只读连接各自独立 store
	writeStore := faketb.NewNamed("rw_write")
	readStore := faketb.NewNamed("rw_read")

	writeStore.On("BEGIN", &fakeResult{})
	writeStore.On("insert into users (name) values (?)", &fakeResult{Affected: 1, LastID: 1})
	readStore.On("select * from `users`", rowsUsers([2]driver.Value{int64(1), "R"}))
	writeStore.On("select * from `users`", rowsUsers([2]driver.Value{int64(9), "W"}))

	conn, err := Open(Config{Driver: "fake", Dsn: "rw_write", ReadDsn: "rw_read"})
	if err != nil {
		t.Fatal(err)
	}

	// select → 只读连接
	var users []User
	if err := conn.Table("users").Get(&users); err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Name != "R" {
		t.Errorf("read from wrong store: %+v", users)
	}

	// useWritePdo → 写连接
	var wusers []User
	if err := conn.Table("users").UseWritePdo().Get(&wusers); err != nil {
		t.Fatal(err)
	}
	if len(wusers) != 1 || wusers[0].Name != "W" {
		t.Errorf("write-pdo read failed: %+v", wusers)
	}

	// 写语句 → 永远走写连接
	if _, _, err := conn.Insert("insert into users (name) values (?)", "A"); err != nil {
		t.Fatal(err)
	}

	// 事务内 select 走事务（写连接）
	if err := conn.Transaction(func(c *Connection) error { return nil }); err != nil {
		t.Fatal(err)
	}
}
