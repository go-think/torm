package torm

import (
	"database/sql/driver"
	"errors"
	"testing"
	"time"
)

// Post 带软删除字段
type Post struct {
	ID        int64      `torm:"primary_key;column:id"`
	Title     string     `torm:"column:title"`
	CreatedAt time.Time  `torm:"column:created_at"`
	UpdatedAt time.Time  `torm:"column:updated_at"`
	DeletedAt *time.Time `torm:"column:deleted_at"`
}

func (p *Post) TableName() string { return "posts" }

func TestModelCreateFillsTimestamps(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("BEGIN", &fakeResult{})
	st.On("insert into `posts` (`created_at`, `title`, `updated_at`) values (?, ?, ?)", &fakeResult{Affected: 1, LastID: 11})

	err := conn.Transaction(func(c *Connection) error {
		return c.Create(&Post{Title: "hello"})
	})
	if err != nil {
		t.Fatal(err)
	}

	var call *fakeCall
	calls := st.Calls()
	for i := range calls {
		if calls[i].Sql == "insert into `posts` (`created_at`, `title`, `updated_at`) values (?, ?, ?)" {
			call = &calls[i]
		}
	}
	if call == nil {
		t.Fatalf("no insert recorded, calls=%v", st.Calls())
	}
	if len(call.Args) != 3 {
		t.Fatalf("args = %#v", call.Args)
	}
	if call.Args[1] != "hello" {
		t.Errorf("title arg = %#v", call.Args[1])
	}
	for _, idx := range []int{0, 2} {
		if tv, ok := call.Args[idx].(time.Time); !ok || tv.IsZero() {
			t.Errorf("timestamp arg %d = %#v, want non-zero time", idx, call.Args[idx])
		}
	}
}

func TestModelSoftDeleteScope(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `posts` where `deleted_at` is null", &fakeResult{Columns: []string{"id", "title"}, Rows: [][]driver.Value{{int64(1), "a"}}})

	var posts []Post
	if err := conn.Model(Post{}).Get(&posts); err != nil {
		t.Fatal(err)
	}
	// 全局 scope：deleted_at IS NULL
	st.LastCall(t, "select * from `posts` where `deleted_at` is null")
}

func TestModelDeleteSoftAndRestore(t *testing.T) {
	conn, st := newFakeDB(t)
	p := Post{ID: 3, Title: "t"}
	st.On("update `posts` set `deleted_at` = ? where `id` = ?", &fakeResult{Affected: 1})
	if err := conn.DeleteModel(&p); err != nil {
		t.Fatal(err)
	}
	call := st.LastCall(t, "update `posts` set `deleted_at` = ? where `id` = ?")
	if len(call.Args) != 2 || call.Args[1] != int64(3) {
		t.Errorf("args = %#v", call.Args)
	}

	// Restore：绕过软删除 scope，置 deleted_at = NULL
	st.On("update `posts` set `deleted_at` = ? where `id` = ?", &fakeResult{Affected: 1})
	if err := conn.Restore(&p); err != nil {
		t.Fatal(err)
	}
	rCall := st.LastCall(t, "update `posts` set `deleted_at` = ? where `id` = ?")
	if len(rCall.Args) != 2 || rCall.Args[1] != int64(3) {
		t.Errorf("restore args = %#v", rCall.Args)
	}
	if rCall.Args[0] != nil {
		t.Errorf("deleted_at 应置 NULL, got %#v", rCall.Args[0])
	}
}

func TestModelForceDelete(t *testing.T) {
	conn, st := newFakeDB(t)
	p := Post{ID: 5, Title: "t"}
	st.On("delete from `posts` where `id` = ?", &fakeResult{Affected: 1})
	if err := conn.ForceDelete(&p); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, "delete from `posts` where `id` = ?")
}

func TestModelFirstOrCreate(t *testing.T) {
	conn, st := newFakeDB(t)

	// 未命中 → 创建
	st.On("select * from `posts` where `title` = ? and `deleted_at` is null limit 1", &fakeResult{
		Columns: []string{"id", "title"}, Rows: [][]driver.Value{},
	})
	st.On("insert into `posts` (`created_at`, `title`, `updated_at`) values (?, ?, ?)", &fakeResult{Affected: 1, LastID: 1})
	st.On("BEGIN", &fakeResult{})

	err := conn.Transaction(func(c *Connection) error {
		p := Post{}
		return c.FirstOrCreate(&p, map[string]interface{}{"title": "new"})
	})
	if err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, "select * from `posts` where `title` = ? and `deleted_at` is null limit 1")

	// 命中 → 不创建
	conn2, st2 := newFakeDB(t)
	st2.On("select * from `posts` where `title` = ? and `deleted_at` is null limit 1", &fakeResult{
		Columns: []string{"id", "title"}, Rows: [][]driver.Value{{int64(9), "found"}},
	})
	p2 := Post{}
	if err := conn2.FirstOrCreate(&p2, map[string]interface{}{"title": "found"}); err != nil {
		t.Fatal(err)
	}
	if p2.ID != 9 {
		t.Errorf("id = %d", p2.ID)
	}
}

func TestModelUpdateOrCreate(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `posts` where `title` = ? and `deleted_at` is null limit 1", &fakeResult{
		Columns: []string{"id", "title"}, Rows: [][]driver.Value{{int64(4), "old"}},
	})
	st.On("BEGIN", &fakeResult{})
	st.On("update `posts` set `created_at` = ?, `title` = ?, `updated_at` = ? where `id` = ?", &fakeResult{Affected: 1})

	err := conn.Transaction(func(c *Connection) error {
		p := Post{}
		return c.UpdateOrCreate(&p, map[string]interface{}{"title": "old"}, map[string]interface{}{"title": "updated"})
	})
	if err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, "update `posts` set `created_at` = ?, `title` = ?, `updated_at` = ? where `id` = ?")
}

func TestModelFirstOrNew(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `posts` where `title` = ? and `deleted_at` is null limit 1", &fakeResult{
		Columns: []string{"id", "title"}, Rows: [][]driver.Value{},
	})

	p := Post{}
	if err := conn.FirstOrNew(&p, map[string]interface{}{"title": "draft"}); err != nil {
		t.Fatal(err)
	}
	if p.Title != "draft" {
		t.Errorf("title = %q", p.Title)
	}
	// 不应落库
	for _, call := range st.Calls() {
		if len(call.Sql) >= 6 && call.Sql[:6] == "insert" {
			t.Error("FirstOrNew must not persist")
		}
	}
}

func TestModelWithoutSoftDeletesField(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `users`", &fakeResult{Columns: []string{"id", "name"}, Rows: [][]driver.Value{{int64(1), "n"}}})

	var users []User
	if err := conn.Model(User{}).Get(&users); err != nil {
		t.Fatal(err)
	}
	// User 无 deleted_at → 无全局 scope
	st.LastCall(t, "select * from `users`")
}

func TestModelSaveUpdateWhenExists(t *testing.T) {
	conn, st := newFakeDB(t)
	now := time.Now()
	p := Post{ID: 2, Title: "t", CreatedAt: now, UpdatedAt: now}
	st.On("BEGIN", &fakeResult{})
	updateSql := "update `posts` set `created_at` = ?, `title` = ?, `updated_at` = ? where `id` = ?"
	st.On(updateSql, &fakeResult{Affected: 1})

	err := conn.Transaction(func(c *Connection) error {
		return c.Save(&p)
	})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, call := range st.Calls() {
		if call.Sql == updateSql {
			found = true
		}
	}
	if !found {
		t.Errorf("expected update by primary key, calls=%v", st.Calls())
	}
}

var _ = errors.New
