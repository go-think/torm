package torm

import (
	"database/sql/driver"
	"testing"

	"github.com/thinkoner/torm/grammar"
)

// README 示例回归测试：保证文档中的代码与当前 API 一致（示例即测试）。
// SQL 桩使用 internal/faketb 假驱动，不依赖真实数据库。

func TestReadme_QueryBuilder(t *testing.T) {
	conn, st := newFakeDB(t)
	sel := "select * from `users` where `gender` = ? and `name` like ? and `created_at` != `updated_at` and `addr` in (?, ?) and `birth_date` between ? and ?"
	st.On(sel, &fakeResult{
		Columns: []string{"name"},
		Rows:    [][]driver.Value{{"Alice"}},
	})

	var users []User
	err := conn.Table("users").
		Where("gender", "M").
		Where("name", "like", "%a%").
		WhereColumn("created_at", "!=", "updated_at").
		WhereIn("addr", []interface{}{"Columbia", "Alaska"}).
		WhereBetween("birth_date", []interface{}{"1990-01-01", "1999-12-31"}).
		Get(&users)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Name != "Alice" {
		t.Fatalf("users = %#v", users)
	}
}

func TestReadme_ModelCrud(t *testing.T) {
	conn, st := newFakeDB(t)
	ins := "insert into `posts` (`created_at`, `title`, `updated_at`) values (?, ?, ?)"
	st.On(ins, &fakeResult{Affected: 1, LastID: 7})
	sel := "select * from `posts` where `id` = ? and `posts`.`deleted_at` is null limit 1"
	st.On(sel, &fakeResult{
		Columns: []string{"id", "title"},
		Rows:    [][]driver.Value{{int64(7), "hello"}},
	})
	upd := "update `posts` set `title` = ?, `updated_at` = ? where `id` = ?"
	st.On(upd, &fakeResult{Affected: 1})

	// Create（自动时间戳 + 自增回填）
	var post Post
	if err := Fill(&post, map[string]any{"title": "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&post); err != nil {
		t.Fatal(err)
	}
	if post.ID != 7 || !WasRecentlyCreated(&post) {
		t.Fatalf("post = %#v", post)
	}

	// Retrieve
	var got Post
	if err := conn.Model(Post{}).Where("id", post.ID).First(&got); err != nil {
		t.Fatal(err)
	}
	if got.Title != "hello" {
		t.Fatalf("title = %q", got.Title)
	}

	// UpdateModel（Fill + Save）
	if err := conn.UpdateModel(&got, map[string]any{"title": "updated"}); err != nil {
		t.Fatal(err)
	}
	call := st.LastCall(t, upd)
	if call.Args[0] != "updated" {
		t.Errorf("args = %#v", call.Args)
	}
}

func TestReadme_Transaction(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("BEGIN", &fakeResult{})
	st.On("insert into users (name) values (?)", &fakeResult{Affected: 1})
	st.On("COMMIT", &fakeResult{})

	err := conn.Transaction(func(c *Connection) error {
		_, _, err := c.Insert("insert into users (name) values (?)", "A")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, "COMMIT")
}

func TestReadme_SchemaCreate(t *testing.T) {
	conn, st := newFakeDB(t)
	create := "create table `users` (`id` bigint unsigned not null auto_increment primary key, `email` varchar(255) not null)"
	st.On(create, &fakeResult{})
	alter := "alter table `users` add unique `users_email_unique`(`email`)"
	st.On(alter, &fakeResult{})

	s := conn.Schema()
	if err := s.Create("users", func(t *grammar.Blueprint) {
		t.ID()
		t.String("email")
		t.Unique([]string{"email"})
	}); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, alter)
}

func TestReadme_Paginate(t *testing.T) {
	conn, st := newFakeDB(t)
	total := "select count(*) as `aggregate` from `users`"
	st.On(total, &fakeResult{Columns: []string{"aggregate"}, Rows: [][]driver.Value{{int64(2)}}})
	sel := "select * from `users` limit 15 offset 0"
	st.On(sel, &fakeResult{Columns: []string{"id"}, Rows: [][]driver.Value{{int64(1)}, {int64(2)}}})

	var users []User
	page, err := conn.Table("users").Paginate(&users, 1, 15)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 {
		t.Errorf("total = %d", page.Total)
	}
}
