package torm

import (
	"database/sql/driver"
	"errors"
	"math/big"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/thinkoner/torm/grammar"
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

// Article 另一个软删模型（带业务列，供 firstOrCreate 族/观察者测试使用）。
type Article struct {
	ID        int64      `torm:"primary_key;column:id"`
	Slug      string     `torm:"column:slug"`
	Title     string     `torm:"column:title"`
	CreatedAt time.Time  `torm:"column:created_at"`
	UpdatedAt time.Time  `torm:"column:updated_at"`
	DeletedAt *time.Time `torm:"column:deleted_at"`
}

func (a *Article) TableName() string { return "articles" }

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

// 第 3 条：create 时用户显式设置的 updated_at 保留，created_at 未设置才写 now。
func TestModelCreateKeepsUserTimestamps(t *testing.T) {
	conn, st := newFakeDB(t)
	insertSql := "insert into `posts` (`created_at`, `title`, `updated_at`) values (?, ?, ?)"
	st.On(insertSql, &fakeResult{Affected: 1, LastID: 1})

	userAt := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := conn.Create(&Post{Title: "keep", UpdatedAt: userAt}); err != nil {
		t.Fatal(err)
	}
	call := st.LastCall(t, insertSql)
	if len(call.Args) != 3 {
		t.Fatalf("args = %#v", call.Args)
	}
	if tv, ok := call.Args[2].(time.Time); !ok || !tv.Equal(userAt) {
		t.Errorf("updated_at arg = %#v, want user value %v", call.Args[2], userAt)
	}
	tv, ok := call.Args[0].(time.Time)
	if !ok || tv.IsZero() || tv.Equal(userAt) {
		t.Errorf("created_at arg = %#v, want fresh now", call.Args[0])
	}
}

// 第 2 条：Create 事件顺序 saving → creating →（时间戳）insert → created → saved，
// creating 观察者修改属性应进入 insert。
func TestModelCreateEventOrderAndObserverMutation(t *testing.T) {
	conn, st := newFakeDB(t)
	insertSql := "insert into `articles` (`created_at`, `slug`, `title`, `updated_at`) values (?, ?, ?, ?)"
	st.On(insertSql, &fakeResult{Affected: 1, LastID: 1})

	var order []string
	ob := &Observer{
		Saving:   func(any) error { order = append(order, "saving"); return nil },
		Creating: func(m any) error { order = append(order, "creating"); m.(*Article).Slug = "mutated"; return nil },
		Created:  func(any) error { order = append(order, "created"); return nil },
		Saved:    func(any) error { order = append(order, "saved"); return nil },
	}
	conn.Observe(&Article{}, ob)
	defer conn.Observe(&Article{}, nil)

	if err := conn.Create(&Article{Title: "t"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"saving", "creating", "created", "saved"}
	if len(order) != len(want) {
		t.Fatalf("event order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("event order = %v, want %v", order, want)
		}
	}
	call := st.LastCall(t, insertSql)
	if call.Args[1] != "mutated" {
		t.Errorf("slug arg = %#v, want creating-observer mutation", call.Args[1])
	}
}

// 第 2 条：Save 更新顺序 saving → updating →（时间戳）UPDATE → updated → saved，
// updating 观察者修改属性应进入 UPDATE。
func TestModelSaveEventOrderAndObserverMutation(t *testing.T) {
	conn, st := newFakeDB(t)
	sel := "select * from `articles` where `id` = ? and `articles`.`deleted_at` is null limit 1"
	st.On(sel, &fakeResult{Columns: []string{"id", "slug", "title"}, Rows: [][]driver.Value{{int64(1), "s", "orig"}}})
	upd := "update `articles` set `slug` = ?, `title` = ?, `updated_at` = ? where `id` = ?"
	st.On(upd, &fakeResult{Affected: 1})

	var order []string
	ob := &Observer{
		Saving:   func(any) error { order = append(order, "saving"); return nil },
		Updating: func(m any) error { order = append(order, "updating"); m.(*Article).Slug = "mutated"; return nil },
		Updated:  func(any) error { order = append(order, "updated"); return nil },
		Saved:    func(any) error { order = append(order, "saved"); return nil },
	}
	conn.Observe(&Article{}, ob)
	defer conn.Observe(&Article{}, nil)

	a := &Article{}
	if err := conn.Model(Article{}).Where("id", int64(1)).First(a); err != nil {
		t.Fatal(err)
	}
	a.Title = "changed"
	if err := conn.Save(a); err != nil {
		t.Fatal(err)
	}
	want := []string{"saving", "updating", "updated", "saved"}
	if len(order) != len(want) {
		t.Fatalf("event order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("event order = %v, want %v", order, want)
		}
	}
	call := st.LastCall(t, upd)
	if call.Args[1] != "changed" || call.Args[0] != "mutated" {
		t.Errorf("args = %#v, want slug=mutated(观察者修改生效) title=changed", call.Args)
	}
}

// 第 1 条：Save 无脏列时不发 UPDATE 业务列；带时间戳的模型仍会触碰 updated_at
// （对齐 Laravel updateTimestamps 的 touch 语义），无时间戳的干净模型则完全无 SQL。
func TestModelSaveCleanSkipsSQL(t *testing.T) {
	conn, st := newFakeDB(t)
	sel := "select * from `posts` where `id` = ? and `posts`.`deleted_at` is null limit 1"
	st.On(sel, &fakeResult{Columns: []string{"id", "title"}, Rows: [][]driver.Value{{int64(1), "a"}}})

	p := &Post{}
	if err := conn.Model(Post{}).Where("id", int64(1)).First(p); err != nil {
		t.Fatal(err)
	}
	var updated, saved int
	conn.Observe(&Post{}, &Observer{
		Updated: func(any) error { updated++; return nil },
		Saved:   func(any) error { saved++; return nil },
	})
	defer conn.Observe(&Post{}, nil)

	// 干净保存：Laravel save() 先判 isDirty——不发任何 SQL（含 touch）、
	// 不触发 updating/updated，仅 finishSave 的 saved 事件。
	if err := conn.Save(p); err != nil {
		t.Fatal(err)
	}
	if updated != 0 || saved != 1 {
		t.Errorf("clean save events updated=%d saved=%d, want 0/1", updated, saved)
	}

	// 修改后仅更新脏列
	upd := "update `posts` set `title` = ?, `updated_at` = ? where `id` = ?"
	st.On(upd, &fakeResult{Affected: 1})
	p.Title = "b"
	if err := conn.Save(p); err != nil {
		t.Fatal(err)
	}
	call := st.LastCall(t, upd)
	if len(call.Args) != 3 || call.Args[0] != "b" || call.Args[2] != int64(1) {
		t.Errorf("dirty args = %#v", call.Args)
	}
}

// 第 1 条（无时间戳模型）：干净保存完全无 SQL、无 updated/saved 事件。
func TestModelSaveCleanWithoutTimestampsSkipsSQL(t *testing.T) {
	type Tag struct {
		ID   int64  `torm:"primary_key;column:id"`
		Name string `torm:"column:name"`
	}
	conn, st := newFakeDB(t)
	sel := "select * from `tag` where `id` = ? limit 1"
	st.On(sel, &fakeResult{Columns: []string{"id", "name"}, Rows: [][]driver.Value{{int64(1), "go"}}})

	tag := &Tag{}
	if err := conn.Model(Tag{}).Where("id", int64(1)).First(tag); err != nil {
		t.Fatal(err)
	}
	var updated, saved int
	conn.Observe(&Tag{}, &Observer{
		Updated: func(any) error { updated++; return nil },
		Saved:   func(any) error { saved++; return nil },
	})
	defer conn.Observe(&Tag{}, nil)

	before := len(st.Calls())
	if err := conn.Save(tag); err != nil {
		t.Fatal(err)
	}
	if len(st.Calls()) != before {
		t.Errorf("clean save must not issue SQL, calls=%v", st.Calls()[before:])
	}
	if updated != 0 || saved != 1 {
		t.Errorf("clean save: updated must not fire, saved must fire once; got %d/%d", updated, saved)
	}

	upd := "update `tag` set `name` = ? where `id` = ?"
	st.On(upd, &fakeResult{Affected: 1})
	tag.Name = "torm"
	if err := conn.Save(tag); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, upd)
}

// Laravel save() 以 exists 决定 insert/update：手工构造（未水合）的模型即便
// 主键有值也走 INSERT（主键列一并写入，对齐 Model.php:1441）。
func TestModelSaveHandBuiltInserts(t *testing.T) {
	conn, st := newFakeDB(t)
	now := time.Now()
	p := Post{ID: 2, Title: "t", CreatedAt: now, UpdatedAt: now}
	st.On("BEGIN", &fakeResult{})
	insertSql := "insert into `posts` (`created_at`, `id`, `title`, `updated_at`) values (?, ?, ?, ?)"
	st.On(insertSql, &fakeResult{Affected: 1})
	st.On("COMMIT", &fakeResult{})

	err := conn.Transaction(func(c *Connection) error {
		return c.Save(&p)
	})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, call := range st.Calls() {
		if call.Sql == insertSql {
			found = true
		}
	}
	if !found {
		t.Errorf("expected insert (hand-built model), calls=%v", st.Calls())
	}
}

// 第 6 条：软删 scope 列名带表限定。
func TestModelSoftDeleteScope(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `posts` where `posts`.`deleted_at` is null", &fakeResult{Columns: []string{"id", "title"}, Rows: [][]driver.Value{{int64(1), "a"}}})

	var posts []Post
	if err := conn.Model(Post{}).Get(&posts); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, "select * from `posts` where `posts`.`deleted_at` is null")
}

// 第 6 条：WithTrashed / OnlyTrashed / WithoutTrashed 宏语义，
// 且同一 Builder 上切换模式后每次执行都重新应用。
func TestModelSoftDeleteScopeMacros(t *testing.T) {
	conn, st := newFakeDB(t)
	all := "select * from `posts`"
	only := "select * from `posts` where `posts`.`deleted_at` is not null"
	none := "select * from `posts` where `posts`.`deleted_at` is null"
	st.On(all, &fakeResult{Columns: []string{"id"}, Rows: nil})
	st.On(only, &fakeResult{Columns: []string{"id"}, Rows: nil})
	st.On(none, &fakeResult{Columns: []string{"id"}, Rows: nil})

	var posts []Post

	// withTrashed：无软删约束
	if err := conn.Model(Post{}).WithTrashed().Get(&posts); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, all)

	// onlyTrashed：deleted_at IS NOT NULL
	if err := conn.Model(Post{}).OnlyTrashed().Get(&posts); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, only)

	// withoutTrashed：deleted_at IS NULL
	if err := conn.Model(Post{}).WithoutTrashed().Get(&posts); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, none)

	// 同一 Builder：默认 → withTrashed → withoutTrashed，每次 Get 都按新模式应用
	b := conn.Model(Post{})
	if err := b.Get(&posts); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, none)
	if err := b.WithTrashed().Get(&posts); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, all)
	if err := b.WithoutTrashed().Get(&posts); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, none)

	// 克隆继承已物化状态：Chunk/lazy 式克隆不重复追加约束
	c2 := b.Clone()
	if err := c2.Get(&posts); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, none)
}

// 第 4/13 号事件：软删同步 updated_at，并触发 trashed 事件。
func TestModelDeleteSoftTouchesUpdatedAtAndFiresTrashed(t *testing.T) {
	conn, st := newFakeDB(t)
	upd := "update `posts` set `deleted_at` = ?, `updated_at` = ? where `id` = ?"
	st.On(upd, &fakeResult{Affected: 1})

	var trashed int
	conn.Observe(&Post{}, &Observer{Trashed: func(any) error { trashed++; return nil }})
	defer conn.Observe(&Post{}, nil)

	// 先水合模型（Laravel delete() 对 exists=false 的模型静默返回）
	sel := "select * from `posts` where `id` = ? and `posts`.`deleted_at` is null limit 1"
	st.On(sel, &fakeResult{Columns: []string{"id", "title"}, Rows: [][]driver.Value{{int64(3), "t"}}})
	p := Post{ID: 3, Title: "t"}
	if err := conn.Model(Post{}).Where("id", int64(3)).First(&p); err != nil {
		t.Fatal(err)
	}
	if err := conn.DeleteModel(&p); err != nil {
		t.Fatal(err)
	}
	call := st.LastCall(t, upd)
	if len(call.Args) != 3 || call.Args[2] != int64(3) {
		t.Errorf("args = %#v", call.Args)
	}
	if tv, ok := call.Args[0].(time.Time); !ok || tv.IsZero() {
		t.Errorf("deleted_at arg = %#v, want non-zero", call.Args[0])
	}
	if tv, ok := call.Args[1].(time.Time); !ok || tv.IsZero() {
		t.Errorf("updated_at arg = %#v, want non-zero", call.Args[1])
	}
	if p.DeletedAt == nil || p.UpdatedAt.IsZero() {
		t.Errorf("model attrs not synced back: deleted_at=%v updated_at=%v", p.DeletedAt, p.UpdatedAt)
	}
	if trashed != 1 {
		t.Errorf("trashed events = %d", trashed)
	}
}

// 第 5 条：Restore 走完整生命周期（Restoring → save：saving/updating/updated/saved → Restored），
// UPDATE 同时写 deleted_at = NULL 与 updated_at = now；Restoring 中断则不发 SQL。
func TestModelDeleteSoftAndRestore(t *testing.T) {
	conn, st := newFakeDB(t)
	upd := "update `posts` set `deleted_at` = ?, `updated_at` = ? where `id` = ?"
	st.On(upd, &fakeResult{Affected: 1})

	var order []string
	conn.Observe(&Post{}, &Observer{
		Restoring: func(any) error { order = append(order, "restoring"); return nil },
		Saving:    func(any) error { order = append(order, "saving"); return nil },
		Updating:  func(any) error { order = append(order, "updating"); return nil },
		Updated:   func(any) error { order = append(order, "updated"); return nil },
		Saved:     func(any) error { order = append(order, "saved"); return nil },
		Restored:  func(any) error { order = append(order, "restored"); return nil },
	})
	defer conn.Observe(&Post{}, nil)

	// 先水合模型（Laravel delete() 对 exists=false 的模型静默返回）
	sel := "select * from `posts` where `id` = ? and `posts`.`deleted_at` is null limit 1"
	st.On(sel, &fakeResult{Columns: []string{"id", "title"}, Rows: [][]driver.Value{{int64(3), "t"}}})
	p := Post{ID: 3, Title: "t"}
	if err := conn.Model(Post{}).Where("id", int64(3)).First(&p); err != nil {
		t.Fatal(err)
	}
	if err := conn.DeleteModel(&p); err != nil {
		t.Fatal(err)
	}
	if err := conn.Restore(&p); err != nil {
		t.Fatal(err)
	}
	rCall := st.LastCall(t, upd)
	if len(rCall.Args) != 3 || rCall.Args[2] != int64(3) {
		t.Errorf("restore args = %#v", rCall.Args)
	}
	if rCall.Args[0] != nil {
		t.Errorf("deleted_at 应置 NULL, got %#v", rCall.Args[0])
	}
	if tv, ok := rCall.Args[1].(time.Time); !ok || tv.IsZero() {
		t.Errorf("updated_at arg = %#v, want non-zero", rCall.Args[1])
	}
	want := []string{"restoring", "saving", "updating", "updated", "saved", "restored"}
	if len(order) != len(want) {
		t.Fatalf("event order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("event order = %v, want %v", order, want)
		}
	}
}

// 第 5 条：Restoring 返回 error 中断 restore，不发 SQL。
func TestModelRestoreAbortsOnRestoringError(t *testing.T) {
	conn, st := newFakeDB(t)
	conn.Observe(&Post{}, &Observer{Restoring: func(any) error { return errors.New("halt") }})
	defer conn.Observe(&Post{}, nil)

	p := Post{ID: 3}
	if err := conn.Restore(&p); err == nil {
		t.Fatal("expected restoring hook error to abort restore")
	}
	for _, call := range st.Calls() {
		if call.Sql != "BEGIN" {
			t.Errorf("no SQL expected, got %v", call.Sql)
		}
	}
}

func TestModelForceDelete(t *testing.T) {
	conn, st := newFakeDB(t)
	// 先水合（Laravel forceDelete→delete() 对 !exists 静默返回，forceDeleted 不触发）
	sel := "select * from `posts` where `id` = ? and `posts`.`deleted_at` is null limit 1"
	st.On(sel, &fakeResult{Columns: []string{"id", "title"}, Rows: [][]driver.Value{{int64(5), "t"}}})
	p := Post{ID: 5, Title: "t"}
	if err := conn.Model(Post{}).Where("id", int64(5)).First(&p); err != nil {
		t.Fatal(err)
	}
	st.On("delete from `posts` where `id` = ?", &fakeResult{Affected: 1})
	if err := conn.ForceDelete(&p); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, "delete from `posts` where `id` = ?")
}

// 第 8 条：Destroy 主键为空返回错误，绝不整表 DELETE。
func TestModelDestroyRequiresPrimaryKey(t *testing.T) {
	conn, st := newFakeDB(t)
	p := Post{Title: "no-pk"}
	if err := conn.Destroy(&p); err == nil {
		t.Fatal("expected error for blank primary key")
	}
	if err := conn.ForceDelete(&p); err == nil {
		t.Fatal("expected error for blank primary key")
	}
	for _, call := range st.Calls() {
		if call.Sql != "BEGIN" {
			t.Errorf("no SQL expected, got %v", call.Sql)
		}
	}
}

// 第 8 条：DestroyByIDs 按 ids 逐个加载模型并走删除生命周期（软删模型软删）。
func TestModelDestroyByIDs(t *testing.T) {
	conn, st := newFakeDB(t)
	sel := "select * from `posts` where `id` = ? and `posts`.`deleted_at` is null limit 1"
	// 两个 id：id=7 命中，id=99 未命中（stub 按注册顺序消费）
	st.On(sel, &fakeResult{Columns: []string{"id", "title"}, Rows: [][]driver.Value{{int64(7), "x"}}})
	st.On(sel, &fakeResult{Columns: []string{"id", "title"}, Rows: nil})
	upd := "update `posts` set `deleted_at` = ?, `updated_at` = ? where `id` = ?"
	st.On(upd, &fakeResult{Affected: 1})

	n, err := conn.DestroyByIDs(&Post{}, int64(7), int64(99))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("destroyed = %d, want 1", n)
	}
	call := st.LastCall(t, upd)
	if len(call.Args) != 3 || call.Args[2] != int64(7) {
		t.Errorf("delete args = %#v", call.Args)
	}

	// 空 ids：直接 0
	if n3, _ := conn.DestroyByIDs(&Post{}); n3 != 0 {
		t.Errorf("destroyed empty = %d, want 0", n3)
	}
}

// 第 7 条：模型 Builder.Delete 走软删（update deleted_at = now，带表限定 + 软删 scope 条件）。
func TestModelBuilderDeleteSoftDeletes(t *testing.T) {
	conn, st := newFakeDB(t)
	upd := "update `posts` set `deleted_at` = ?, `updated_at` = ? where `title` = ? and `posts`.`deleted_at` is null"
	st.On(upd, &fakeResult{Affected: 2})

	var deleted, trashed int
	bus := NewEventBus()
	conn.SetEventDispatcher(bus)
	// Laravel builder 级 onDelete 不分发任何模型事件（无模型实例）：
	// 两个计数应保持 0

	// exists 守卫：需先水合模型（Laravel delete() 对 !exists 静默返回）
	sel := "select * from `posts` where `title` = ? and `posts`.`deleted_at` is null"
	st.On(sel, &fakeResult{Columns: []string{"id", "title"}, Rows: [][]driver.Value{{int64(1), "x"}, {int64(2), "x"}}})
	var dp []Post
	if err := conn.Model(Post{}).Where("title", "x").Get(&dp); err != nil {
		t.Fatal(err)
	}
	n, err := conn.Model(Post{}).Where("title", "x").Delete()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("affected = %d", n)
	}
	call := st.LastCall(t, upd)
	if len(call.Args) != 3 {
		t.Errorf("args = %#v", call.Args)
	}
	if tv, ok := call.Args[0].(time.Time); !ok || tv.IsZero() {
		t.Errorf("deleted_at arg = %#v", call.Args[0])
	}
	// builder 级删除无模型实例：Laravel onDelete 不分发模型事件
	if deleted != 0 || trashed != 0 {
		t.Errorf("events deleting=%d trashed=%d, want 0/0", deleted, trashed)
	}
}

// 第 7 条：无模型无条件时 Builder.Delete 返回错误而非裸 DELETE 整表。
func TestModelBuilderDeleteRequiresWhere(t *testing.T) {
	conn, _ := newFakeDB(t)
	if _, err := conn.Table("users").Delete(); err == nil {
		t.Fatal("expected error for unconditional delete")
	}
	// 硬删模型 + 条件：仍是 DELETE
	conn2, st := newFakeDB(t)
	del := "delete from `users` where `name` = ?"
	st.On(del, &fakeResult{Affected: 1})
	if _, err := conn2.Model(User{}).Where("name", "n").Delete(); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, del)
}

func TestModelFirstOrCreate(t *testing.T) {
	conn, st := newFakeDB(t)

	// 未命中 → 创建
	st.On("select * from `posts` where `title` = ? and `posts`.`deleted_at` is null limit 1", &fakeResult{
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
	st.LastCall(t, "select * from `posts` where `title` = ? and `posts`.`deleted_at` is null limit 1")

	// 命中 → 不创建
	conn2, st2 := newFakeDB(t)
	st2.On("select * from `posts` where `title` = ? and `posts`.`deleted_at` is null limit 1", &fakeResult{
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

// 第 9 条：FirstOrCreate / FirstOrNew 的 values 参数（未命中时合并 attributes+values）。
func TestModelFirstOrCreateAndFirstOrNewWithValues(t *testing.T) {
	conn, st := newFakeDB(t)
	sel := "select * from `articles` where `slug` = ? and `articles`.`deleted_at` is null limit 1"
	st.On(sel, &fakeResult{Columns: []string{"id"}, Rows: nil})
	insert := "insert into `articles` (`created_at`, `slug`, `title`, `updated_at`) values (?, ?, ?, ?)"
	st.On(insert, &fakeResult{Affected: 1, LastID: 3})

	a := &Article{}
	if err := conn.FirstOrCreate(a, map[string]interface{}{"slug": "k1"}, map[string]interface{}{"title": "from-values"}); err != nil {
		t.Fatal(err)
	}
	call := st.LastCall(t, insert)
	// args: created_at, slug, title, updated_at
	if call.Args[1] != "k1" || call.Args[2] != "from-values" {
		t.Errorf("insert args = %#v", call.Args)
	}
	if a.ID != 3 {
		t.Errorf("id = %d", a.ID)
	}

	// FirstOrNew：未命中仅构造，values 合并进模型，不落库
	conn2, st2 := newFakeDB(t)
	st2.On(sel, &fakeResult{Columns: []string{"id"}, Rows: nil})
	a2 := &Article{}
	if err := conn2.FirstOrNew(a2, map[string]interface{}{"slug": "k2"}, map[string]interface{}{"title": "draft"}); err != nil {
		t.Fatal(err)
	}
	if a2.Slug != "k2" || a2.Title != "draft" {
		t.Errorf("model = %+v", a2)
	}
	for _, c := range st2.Calls() {
		if len(c.Sql) >= 6 && c.Sql[:6] == "insert" {
			t.Error("FirstOrNew must not persist")
		}
	}

	// 命中：values 不参与
	conn3, st3 := newFakeDB(t)
	st3.On(sel, &fakeResult{Columns: []string{"id", "slug", "title"}, Rows: [][]driver.Value{{int64(5), "k1", "found"}}})
	a3 := &Article{}
	if err := conn3.FirstOrCreate(a3, map[string]interface{}{"slug": "k1"}, map[string]interface{}{"title": "ignored"}); err != nil {
		t.Fatal(err)
	}
	if a3.Title != "found" {
		t.Errorf("title = %q, want existing value", a3.Title)
	}
	for _, c := range st3.Calls() {
		if len(c.Sql) >= 6 && c.Sql[:6] == "insert" {
			t.Error("FirstOrCreate hit must not insert")
		}
	}
}

// 第 9 条：CreateOrFirst 先创建，唯一约束冲突时回退查询。
func TestModelCreateOrFirst(t *testing.T) {
	insert := "insert into `articles` (`created_at`, `slug`, `title`, `updated_at`) values (?, ?, ?, ?)"
	sel := "select * from `articles` where `slug` = ? and `articles`.`deleted_at` is null limit 1"

	// 冲突 → 回退查询命中
	conn, st := newFakeDB(t)
	st.On(insert, &fakeResult{Err: errors.New("Error 1062: Duplicate entry 'k1' for key 'uniq_slug'")})
	st.On(sel, &fakeResult{Columns: []string{"id", "slug", "title"}, Rows: [][]driver.Value{{int64(8), "k1", "existing"}}})

	a := &Article{}
	if err := conn.CreateOrFirst(a, map[string]interface{}{"slug": "k1"}, map[string]interface{}{"title": "new"}); err != nil {
		t.Fatal(err)
	}
	if a.ID != 8 {
		t.Errorf("id = %d, want conflict fallback find", a.ID)
	}
	st.LastCall(t, sel)

	// 冲突且查不到 → 返回原始错误
	conn2, st2 := newFakeDB(t)
	st2.On(insert, &fakeResult{Err: errors.New("Error 1062: Duplicate entry 'k9' for key 'uniq_slug'")})
	st2.On(sel, &fakeResult{Columns: []string{"id"}, Rows: nil})
	a2 := &Article{}
	if err := conn2.CreateOrFirst(a2, map[string]interface{}{"slug": "k9"}); err == nil {
		t.Fatal("expected unique constraint error when fallback find misses")
	}

	// 无冲突：直接创建
	conn3, st3 := newFakeDB(t)
	st3.On(insert, &fakeResult{Affected: 1, LastID: 6})
	a3 := &Article{}
	if err := conn3.CreateOrFirst(a3, map[string]interface{}{"slug": "k3"}); err != nil {
		t.Fatal(err)
	}
	if a3.ID != 6 {
		t.Errorf("id = %d", a3.ID)
	}
	st3.LastCall(t, insert)
}

// 第 9 条：RestoreOrCreate —— withTrashed 查找，命中已软删行则恢复。
func TestModelRestoreOrCreate(t *testing.T) {
	sel := "select * from `articles` where `slug` = ? limit 1"
	upd := "update `articles` set `deleted_at` = ?, `updated_at` = ? where `id` = ?"

	// 命中已软删行 → restore
	conn, st := newFakeDB(t)
	deletedAt := time.Date(2021, 5, 1, 0, 0, 0, 0, time.UTC)
	st.On(sel, &fakeResult{Columns: []string{"id", "slug", "title", "deleted_at"}, Rows: [][]driver.Value{{int64(4), "k1", "old", deletedAt}}})
	st.On(upd, &fakeResult{Affected: 1})

	a := &Article{}
	if err := conn.RestoreOrCreate(a, map[string]interface{}{"slug": "k1"}); err != nil {
		t.Fatal(err)
	}
	if a.ID != 4 {
		t.Errorf("id = %d", a.ID)
	}
	call := st.LastCall(t, upd)
	if call.Args[0] != nil || call.Args[2] != int64(4) {
		t.Errorf("restore args = %#v", call.Args)
	}

	// 未命中 → 创建（values 合并）
	conn2, st2 := newFakeDB(t)
	st2.On(sel, &fakeResult{Columns: []string{"id"}, Rows: nil})
	insert := "insert into `articles` (`created_at`, `slug`, `title`, `updated_at`) values (?, ?, ?, ?)"
	st2.On(insert, &fakeResult{Affected: 1, LastID: 2})
	// 创建后的 restore 触碰 updated_at（Laravel restore() 语义）
	touch := "update `articles` set `updated_at` = ? where `id` = ?"
	st2.On(touch, &fakeResult{Affected: 1})

	a2 := &Article{}
	if err := conn2.RestoreOrCreate(a2, map[string]interface{}{"slug": "k2"}, map[string]interface{}{"title": "made"}); err != nil {
		t.Fatal(err)
	}
	if a2.ID != 2 || a2.Title != "made" {
		t.Errorf("created = %+v", a2)
	}
	st2.LastCall(t, insert)
}

// 第 9 条：CreateOrRestore —— 先创建，唯一冲突时 withTrashed 查回并恢复。
func TestModelCreateOrRestore(t *testing.T) {
	insert := "insert into `articles` (`created_at`, `slug`, `title`, `updated_at`) values (?, ?, ?, ?)"
	sel := "select * from `articles` where `slug` = ? limit 1"
	upd := "update `articles` set `deleted_at` = ?, `updated_at` = ? where `id` = ?"

	conn, st := newFakeDB(t)
	st.On(insert, &fakeResult{Err: errors.New("Error 1062: Duplicate entry 'k1' for key 'uniq_slug'")})
	deletedAt := time.Date(2021, 5, 1, 0, 0, 0, 0, time.UTC)
	st.On(sel, &fakeResult{Columns: []string{"id", "slug", "title", "deleted_at"}, Rows: [][]driver.Value{{int64(4), "k1", "old", deletedAt}}})
	st.On(upd, &fakeResult{Affected: 1})

	a := &Article{}
	if err := conn.CreateOrRestore(a, map[string]interface{}{"slug": "k1"}, map[string]interface{}{"title": "new"}); err != nil {
		t.Fatal(err)
	}
	if a.ID != 4 {
		t.Errorf("id = %d, want restored existing row", a.ID)
	}
	call := st.LastCall(t, upd)
	if call.Args[0] != nil || call.Args[2] != int64(4) {
		t.Errorf("restore args = %#v", call.Args)
	}
}

func TestModelUpdateOrCreate(t *testing.T) {
	conn, st := newFakeDB(t)
	sel := "select * from `posts` where `title` = ? and `posts`.`deleted_at` is null limit 1"
	st.On(sel, &fakeResult{
		Columns: []string{"id", "title"}, Rows: [][]driver.Value{{int64(4), "old"}},
	})
	st.On("BEGIN", &fakeResult{})
	upd := "update `posts` set `title` = ?, `updated_at` = ? where `id` = ?"
	st.On(upd, &fakeResult{Affected: 1})

	err := conn.Transaction(func(c *Connection) error {
		p := Post{}
		return c.UpdateOrCreate(&p, map[string]interface{}{"title": "old"}, map[string]interface{}{"title": "updated"})
	})
	if err != nil {
		t.Fatal(err)
	}
	call := st.LastCall(t, upd)
	if len(call.Args) != 3 || call.Args[0] != "updated" || call.Args[2] != int64(4) {
		t.Errorf("update args = %#v", call.Args)
	}
}

func TestModelFirstOrNew(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `posts` where `title` = ? and `posts`.`deleted_at` is null limit 1", &fakeResult{
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

// ---- R2-A：脏跟踪导出 API（IsDirty/IsClean/GetDirty/GetOriginal/GetChanges/WasChanged/DiscardChanges）----

func TestModelDirtyTrackingAPI(t *testing.T) {
	conn, st := newFakeDB(t)
	sel := "select * from `articles` where `id` = ? and `articles`.`deleted_at` is null limit 1"
	st.On(sel, &fakeResult{Columns: []string{"id", "slug", "title"}, Rows: [][]driver.Value{{int64(1), "s", "orig"}}})
	upd := "update `articles` set `title` = ?, `updated_at` = ? where `id` = ?"
	st.On(upd, &fakeResult{Affected: 1})

	a := &Article{}
	if err := conn.Model(Article{}).Where("id", int64(1)).First(a); err != nil {
		t.Fatal(err)
	}

	// 水合后干净
	if !IsClean(a) || IsDirty(a) {
		t.Fatal("hydrated model should be clean")
	}
	if !IsClean(a, "title") || IsDirty(a, "title") {
		t.Fatal("title should be clean after hydration")
	}
	if v := GetOriginal(a, "title"); v != "orig" {
		t.Errorf("GetOriginal(title) = %#v, want orig", v)
	}
	if dirty := GetDirty(a); len(dirty) != 0 {
		t.Errorf("GetDirty = %#v", dirty)
	}

	// 修改后：IsDirty/GetDirty/GetOriginal
	a.Title = "changed"
	if !IsDirty(a) || !IsDirty(a, "title") || IsClean(a, "title") {
		t.Fatal("title should be dirty after mutation")
	}
	if dirty := GetDirty(a); len(dirty) != 1 || dirty["title"] != "changed" {
		t.Errorf("GetDirty = %#v", dirty)
	}

	// DiscardChanges 回滚，不发 SQL
	before := len(st.Calls())
	if err := DiscardChanges(a); err != nil {
		t.Fatal(err)
	}
	if a.Title != "orig" {
		t.Errorf("DiscardChanges: title = %q, want orig", a.Title)
	}
	if IsDirty(a) {
		t.Error("model should be clean after discard")
	}
	if len(st.Calls()) != before {
		t.Error("DiscardChanges must not issue SQL")
	}

	// Save：GetChanges/WasChanged，保存后回到 clean
	a.Title = "changed"
	if err := conn.Save(a); err != nil {
		t.Fatal(err)
	}
	if !WasChanged(a) || !WasChanged(a, "title") || WasChanged(a, "slug") {
		t.Errorf("WasChanged mismatch, changes = %#v", GetChanges(a))
	}
	changes := GetChanges(a)
	if changes["title"] != "changed" {
		t.Errorf("GetChanges(title) = %#v", changes["title"])
	}
	if IsDirty(a, "title") {
		t.Error("title should be clean after save")
	}

	// SyncOriginal / SyncChanges 手动维护口径
	a.Title = "next"
	if IsClean(a, "title") {
		t.Fatal("title should be dirty")
	}
	SyncOriginal(a)
	if !IsClean(a) {
		t.Error("SyncOriginal should clear dirty state")
	}
	a.Title = "next2"
	SyncChanges(a)
	if !WasChanged(a, "title") {
		t.Errorf("SyncChanges should record title, changes = %#v", GetChanges(a))
	}

	// ForgetModel 释放状态（防指针键泄漏）
	ForgetModel(a)
	if Exists(a) || WasRecentlyCreated(a) || WasChanged(a) {
		t.Error("state should be released after ForgetModel")
	}
}

// ---- R2-A：质量赋值守卫（fillable/guarded tag + SetFillable/SetGuarded/Fill/ForceFill）----

type guardProbe struct {
	ID     int64  `torm:"primary_key;column:id"`
	Title  string `torm:"column:title"`
	Secret string `torm:"column:secret;guarded"`
}

func TestModelMassAssignmentGuards(t *testing.T) {
	// 默认无守卫：全部可填
	a := &Article{}
	if !IsFillable(a, "slug") || IsGuarded(a, "slug") || TotallyGuarded(a) {
		t.Fatal("unguarded model should allow all fields")
	}
	if err := Fill(a, map[string]any{"slug": "s", "title": "t"}); err != nil {
		t.Fatal(err)
	}
	if a.Slug != "s" || a.Title != "t" {
		t.Errorf("fill = %+v", a)
	}

	// tag guarded：secret 被剔除并报错（MassAssignmentException 语义）
	p2 := &guardProbe{}
	if !IsGuarded(p2, "secret") || IsFillable(p2, "secret") {
		t.Fatal("guarded tag should protect secret")
	}
	if !IsFillable(p2, "title") {
		t.Fatal("non-guarded fields stay fillable when guarded list is partial")
	}
	err := Fill(p2, map[string]any{"title": "t", "secret": "s"})
	var mass *MassAssignmentError
	if !errors.As(err, &mass) || mass.Key != "secret" {
		t.Fatalf("Fill guarded err = %v, want *MassAssignmentError(secret)", err)
	}
	if p2.Secret != "" || p2.Title != "" {
		t.Errorf("Fill must not write any field on guard violation, got %+v", p2)
	}
	// ForceFill 绕过守卫
	if err := ForceFill(p2, map[string]any{"secret": "s"}); err != nil {
		t.Fatal(err)
	}
	if p2.Secret != "s" {
		t.Errorf("forceFill = %+v", p2)
	}

	// SetGuarded("*")：完全守卫
	p3 := &guardProbe{}
	SetGuarded(p3)
	if !TotallyGuarded(p3) {
		t.Fatal("SetGuarded() with no keys should totally guard")
	}
	if IsFillable(p3, "title") {
		t.Error("totally guarded model must reject title")
	}
	if err := Fill(p3, map[string]any{"title": "t"}); err == nil {
		t.Error("Fill on totally guarded model must fail")
	}

	// SetFillable 白名单
	p4 := &guardProbe{}
	SetFillable(p4, "title")
	if !IsFillable(p4, "title") || IsFillable(p4, "secret") {
		t.Fatal("fillable whitelist mismatch")
	}
	if err := Fill(p4, map[string]any{"title": "ok"}); err != nil {
		t.Fatal(err)
	}
	if p4.Title != "ok" {
		t.Errorf("fill = %+v", p4)
	}
}

// ---- R2-A：隐藏字段（hidden tag + MakeHidden/MakeVisible/GetHidden）----

type hiddenProbe struct {
	ID     int64  `torm:"primary_key;column:id"`
	Title  string `torm:"column:title"`
	Secret string `torm:"column:secret;hidden"`
}

func TestModelHiddenAttributes(t *testing.T) {
	h := &hiddenProbe{}
	hidden := GetHidden(h)
	if len(hidden) != 1 || hidden[0] != "secret" {
		t.Fatalf("GetHidden = %#v, want [secret]", hidden)
	}
	if !IsHidden(h, "secret") || IsHidden(h, "title") {
		t.Fatal("IsHidden mismatch")
	}

	MakeHidden(h, "title")
	if !IsHidden(h, "title") || len(GetHidden(h)) != 2 {
		t.Fatal("MakeHidden should append")
	}
	MakeVisible(h, "secret", "title")
	if IsHidden(h, "secret") || IsHidden(h, "title") {
		t.Fatal("MakeVisible should remove")
	}
	if len(GetHidden(h)) != 0 {
		t.Fatalf("GetHidden = %#v", GetHidden(h))
	}
}

// ---- R2-A：Quietly 族（抑制观察者与事件分发）----

func TestModelQuietlyFamily(t *testing.T) {
	// SaveQuietly：SQL 执行，观察者与事件全部不触发
	conn, st := newFakeDB(t)
	sel := "select * from `articles` where `id` = ? and `articles`.`deleted_at` is null limit 1"
	// 三次水合（a/a2/a3）按注册顺序消费
	st.On(sel, &fakeResult{Columns: []string{"id", "slug", "title"}, Rows: [][]driver.Value{{int64(1), "s", "orig"}}})
	st.On(sel, &fakeResult{Columns: []string{"id", "slug", "title"}, Rows: [][]driver.Value{{int64(2), "s0", "t0"}}})
	st.On(sel, &fakeResult{Columns: []string{"id", "slug", "title"}, Rows: [][]driver.Value{{int64(3), "s", "t"}}})
	upd := "update `articles` set `title` = ?, `updated_at` = ? where `id` = ?"
	st.On(upd, &fakeResult{Affected: 1})

	var hooks int
	conn.Observe(&Article{}, &Observer{
		Saving:   func(any) error { hooks++; return nil },
		Updating: func(any) error { hooks++; return nil },
		Updated:  func(any) error { hooks++; return nil },
		Saved:    func(any) error { hooks++; return nil },
	})
	defer conn.Observe(&Article{}, nil)
	bus := NewEventBus()
	conn.SetEventDispatcher(bus)
	var dispatched int
	bus.Listen(EventSaved, func(e Event) { dispatched++ })
	bus.Listen(EventUpdated, func(e Event) { dispatched++ })

	a := &Article{}
	if err := conn.Model(Article{}).Where("id", int64(1)).First(a); err != nil {
		t.Fatal(err)
	}
	a.Title = "quiet"
	if err := conn.SaveQuietly(a); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, upd)
	if hooks != 0 || dispatched != 0 {
		t.Errorf("SaveQuietly must suppress events: hooks=%d dispatched=%d", hooks, dispatched)
	}

	// DeleteQuietly
	del := "update `articles` set `deleted_at` = ?, `updated_at` = ? where `id` = ?"
	st.On(del, &fakeResult{Affected: 1})
	if err := conn.DeleteQuietly(a); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, del)
	if hooks != 0 || dispatched != 0 {
		t.Errorf("DeleteQuietly must suppress events: hooks=%d dispatched=%d", hooks, dispatched)
	}

	// UpdateQuietly（先水合获得 original，仅脏列 + updated_at 入 UPDATE）
	a2 := &Article{}
	st.On("update `articles` set `slug` = ?, `title` = ?, `updated_at` = ? where `id` = ?", &fakeResult{Affected: 1})
	if err := conn.Model(Article{}).Where("id", int64(2)).First(a2); err != nil {
		t.Fatal(err)
	}
	if err := conn.UpdateQuietly(a2, map[string]any{"slug": "k", "title": "t"}); err != nil {
		t.Fatal(err)
	}
	call := st.LastCall(t, "update `articles` set `slug` = ?, `title` = ?, `updated_at` = ? where `id` = ?")
	if call.Args[0] != "k" || call.Args[1] != "t" || call.Args[3] != int64(2) {
		t.Errorf("update args = %#v", call.Args)
	}

	// TouchQuietly：仅刷新 updated_at
	a3 := &Article{}
	st.On("update `articles` set `updated_at` = ? where `id` = ?", &fakeResult{Affected: 1})
	if err := conn.Model(Article{}).Where("id", int64(3)).First(a3); err != nil {
		t.Fatal(err)
	}
	if err := conn.TouchQuietly(a3); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, "update `articles` set `updated_at` = ? where `id` = ?")

	// CreateManyQuietly
	conn2, st2 := newFakeDB(t)
	insert := "insert into `articles` (`created_at`, `slug`, `title`, `updated_at`) values (?, ?, ?, ?)"
	st2.On(insert, &fakeResult{Affected: 1, LastID: 1})
	if err := conn2.CreateManyQuietly(&Article{}, []map[string]any{
		{"slug": "a"}, {"slug": "b"},
	}); err != nil {
		t.Fatal(err)
	}
	if n := len(st2.Calls()); n < 2 {
		t.Fatalf("CreateManyQuietly should insert twice, calls = %v", st2.Calls())
	}
}

// ---- R2-A：OrFail 族（panic 包装 *FailError）----

func TestModelOrFailFamily(t *testing.T) {
	// 成功路径：事务内保存
	conn, st := newFakeDB(t)
	sel := "select * from `articles` where `id` = ? and `articles`.`deleted_at` is null limit 1"
	st.On(sel, &fakeResult{Columns: []string{"id", "slug", "title"}, Rows: [][]driver.Value{{int64(1), "s", "orig"}}})
	st.On("BEGIN", &fakeResult{})
	upd := "update `articles` set `title` = ?, `updated_at` = ? where `id` = ?"
	st.On(upd, &fakeResult{Affected: 1})
	st.On("COMMIT", &fakeResult{})

	a := &Article{}
	if err := conn.Model(Article{}).Where("id", int64(1)).First(a); err != nil {
		t.Fatal(err)
	}
	a.Title = "orfail"
	if err := conn.SaveOrFail(a); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, upd)
	st.LastCall(t, "COMMIT")

	// 失败路径：panic *FailError，Unwrap 取回原始错误
	conn2, st2 := newFakeDB(t)
	p := Post{ID: 9, Title: "x", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	st2.On("BEGIN", &fakeResult{})
	// 手工构造的模型（exists=false）→ INSERT
	failIns := "insert into `posts` (`created_at`, `id`, `title`, `updated_at`) values (?, ?, ?, ?)"
	st2.On(failIns, &fakeResult{Err: errors.New("boom")})
	st2.On("ROLLBACK", &fakeResult{})

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("SaveOrFail must panic on failure")
		}
		fe, ok := r.(*FailError)
		if !ok {
			t.Fatalf("panic payload = %#v, want *FailError", r)
		}
		if fe.Err == nil || !strings.Contains(fe.Err.Error(), "boom") {
			t.Errorf("FailError.Err = %v", fe.Err)
		}
		if errors.Unwrap(fe) != fe.Err {
			t.Error("FailError.Unwrap mismatch")
		}
	}()
	_ = conn2.SaveOrFail(&p)

	// DeleteOrFail
	conn3, st3 := newFakeDB(t)
	defer func() {
		if r := recover(); r == nil {
			t.Error("DeleteOrFail must panic on failure")
		}
	}()
	st3.On("delete from `posts` where `id` = ?", &fakeResult{Err: errors.New("boom2")})
	_ = conn3.DeleteOrFail(&Post{ID: 1})
}

// ---- R2-A：模型级 Increment/Decrement/IncrementEach/DecrementEach ----

type VoteDoc struct {
	ID        int64     `torm:"primary_key;column:id"`
	Votes     int64     `torm:"column:votes"`
	Views     int64     `torm:"column:views"`
	CreatedAt time.Time `torm:"column:created_at"`
	UpdatedAt time.Time `torm:"column:updated_at"`
}

func TestModelIncrementFiresUpdatedAndSyncs(t *testing.T) {
	conn, st := newFakeDB(t)
	upd := "update `vote_doc` set `updated_at` = ?, `votes` = `votes` + 2 where `id` = ?"
	st.On(upd, &fakeResult{Affected: 1})

	var order []string
	conn.Observe(&VoteDoc{}, &Observer{
		Updating: func(any) error { order = append(order, "updating"); return nil },
		Updated:  func(any) error { order = append(order, "updated"); return nil },
	})
	defer conn.Observe(&VoteDoc{}, nil)

	d := &VoteDoc{ID: 1, Votes: 1}
	if err := conn.Increment(d, "votes", 2); err != nil {
		t.Fatal(err)
	}
	call := st.LastCall(t, upd)
	if len(call.Args) != 2 || call.Args[1] != int64(1) {
		t.Errorf("increment args = %#v", call.Args)
	}
	if tv, ok := call.Args[0].(time.Time); !ok || tv.IsZero() {
		t.Errorf("updated_at arg = %#v, want non-zero", call.Args[0])
	}
	if d.Votes != 3 {
		t.Errorf("model votes = %d, want 3 (属性同步)", d.Votes)
	}
	if len(order) != 2 || order[0] != "updating" || order[1] != "updated" {
		t.Errorf("events = %v, want [updating updated]", order)
	}
	if !WasChanged(d, "votes") {
		t.Errorf("GetChanges = %#v", GetChanges(d))
	}
	if IsDirty(d, "votes") {
		t.Error("votes should be synced into original after increment")
	}

	// Decrement（时间戳模型同样触碰 updated_at）
	upd2 := "update `vote_doc` set `updated_at` = ?, `votes` = `votes` - 3 where `id` = ?"
	st.On(upd2, &fakeResult{Affected: 1})
	if err := conn.Decrement(d, "votes", 3); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, upd2)
	if d.Votes != 0 {
		t.Errorf("model votes = %d, want 0", d.Votes)
	}
}

func TestModelIncrementExtraAndWithoutTimestamps(t *testing.T) {
	conn, st := newFakeDB(t)
	// extra 附加列
	upd := "update `vote_doc` set `updated_at` = ?, `views` = `views` + 1, `votes` = `votes` + 1 where `id` = ?"
	st.On(upd, &fakeResult{Affected: 1})
	d := &VoteDoc{ID: 1, Votes: 1}
	if err := conn.Increment(d, "votes", 1, map[string]any{"views": grammar.NewExpression("`views` + 1")}); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, upd)

	// WithoutTimestamps 作用域：不带 updated_at
	upd2 := "update `vote_doc` set `votes` = `votes` + 1 where `id` = ?"
	st.On(upd2, &fakeResult{Affected: 1})
	WithoutTimestamps(func() {
		if !TimestampsIgnored() {
			t.Error("TimestampsIgnored should be true inside scope")
		}
		if err := conn.Increment(d, "votes", 1); err != nil {
			t.Fatal(err)
		}
	})
	st.LastCall(t, upd2)
	if TimestampsIgnored() {
		t.Error("TimestampsIgnored should reset after scope")
	}

	// IncrementEach / DecrementEach：单条 UPDATE 多列（时间戳模型带 updated_at）
	each := "update `vote_doc` set `updated_at` = ?, `views` = `views` + 5, `votes` = `votes` + 2 where `id` = ?"
	st.On(each, &fakeResult{Affected: 1})
	if err := conn.IncrementEach(d, map[string]int64{"votes": 2, "views": 5}); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, each)
	if d.Votes != 5 || d.Views != 5 {
		t.Errorf("each sync: votes=%d views=%d, want 5/5", d.Votes, d.Views)
	}

	eachDec := "update `vote_doc` set `updated_at` = ?, `views` = `views` - 1, `votes` = `votes` - 1 where `id` = ?"
	st.On(eachDec, &fakeResult{Affected: 1})
	if err := conn.DecrementEach(d, map[string]int64{"votes": 1, "views": 1}); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, eachDec)
	if d.Votes != 4 || d.Views != 4 {
		t.Errorf("each sync: votes=%d views=%d, want 4/4", d.Votes, d.Views)
	}
}

// ---- R2-A：exists / wasRecentlyCreated ----

func TestModelExistsAndWasRecentlyCreated(t *testing.T) {
	conn, st := newFakeDB(t)
	insert := "insert into `articles` (`created_at`, `slug`, `title`, `updated_at`) values (?, ?, ?, ?)"
	st.On(insert, &fakeResult{Affected: 1, LastID: 5})

	a := &Article{Title: "new"}
	var existsInCreating bool
	conn.Observe(&Article{}, &Observer{
		Creating: func(m any) error {
			existsInCreating = Exists(m.(*Article))
			return nil
		},
	})
	defer conn.Observe(&Article{}, nil)

	// creating 钩子内 exists=false（对齐 Laravel）
	if Exists(a) {
		t.Fatal("new model should not exist")
	}
	if err := conn.Create(a); err != nil {
		t.Fatal(err)
	}
	if existsInCreating {
		t.Error("exists must be false inside creating hook")
	}
	if !Exists(a) || !WasRecentlyCreated(a) {
		t.Error("after create: exists/wasRecentlyCreated should be true")
	}

	// 水合：exists=true, wasRecentlyCreated=false
	sel := "select * from `articles` where `id` = ? and `articles`.`deleted_at` is null limit 1"
	st.On(sel, &fakeResult{Columns: []string{"id", "slug", "title"}, Rows: [][]driver.Value{{int64(5), "s", "new"}}})
	b := &Article{}
	if err := conn.Model(Article{}).Where("id", int64(5)).First(b); err != nil {
		t.Fatal(err)
	}
	if !Exists(b) || WasRecentlyCreated(b) {
		t.Error("hydrated model: exists=true wasRecentlyCreated=false")
	}

	// Laravel runSoftDelete 不改 exists（SoftDeletes.php:137-158）：
	// 软删后 exists 保持 true（trashed 状态经 deleted_at 判断），wasRecentlyCreated=false
	del := "update `articles` set `deleted_at` = ?, `updated_at` = ? where `id` = ?"
	st.On(del, &fakeResult{Affected: 1})
	if err := conn.DeleteModel(b); err != nil {
		t.Fatal(err)
	}
	if !Exists(b) || WasRecentlyCreated(b) {
		t.Error("soft-deleted model must keep exists=true wasRecentlyCreated=false")
	}

	// 硬删后 exists=false
	hard := "delete from `articles` where `id` = ?"
	st.On(hard, &fakeResult{Affected: 1})
	if err := conn.ForceDelete(b); err != nil {
		t.Fatal(err)
	}
	if Exists(b) {
		t.Error("force-deleted model should not exist")
	}
}

// ---- R2-A：casts 扩展（date/datetime/decimal/bool/int/float/array + 空值语义）----

type CastDoc struct {
	ID        int64     `torm:"primary_key;column:id"`
	Birthday  time.Time `torm:"column:birthday;cast:date"`
	MeetingAt time.Time `torm:"column:meeting_at;cast:datetime"`
	Balance   string    `torm:"column:balance;cast:decimal:2"`
	Active    bool      `torm:"column:active;cast:bool"`
	Count     int       `torm:"column:count;cast:int"`
	Score     float64   `torm:"column:score;cast:float"`
	Tags      []any     `torm:"column:tags;cast:array"`
}

func TestModelCastWrite(t *testing.T) {
	conn, st := newFakeDB(t)
	// CastDoc 无时间戳字段 → 列表不含 created_at/updated_at
	insert := "insert into `cast_doc` (`active`, `balance`, `birthday`, `count`, `meeting_at`, `score`, `tags`) values (?, ?, ?, ?, ?, ?, ?)"
	st.On(insert, &fakeResult{Affected: 1, LastID: 1})

	birthday := time.Date(2000, 1, 2, 15, 30, 0, 0, time.UTC)
	meeting := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)
	d := &CastDoc{
		Birthday:  birthday,
		MeetingAt: meeting,
		Balance:   "1234.5",
		Active:    true,
		Count:     42,
		Score:     3.5,
	}
	if err := conn.Create(d); err != nil {
		t.Fatal(err)
	}
	call := st.LastCall(t, insert)
	if len(call.Args) != 7 {
		t.Fatalf("args = %#v", call.Args)
	}
	// args 排序：active, balance, birthday, count, meeting_at, score, tags
	if call.Args[0] != true {
		t.Errorf("active = %#v, want true", call.Args[0])
	}
	if call.Args[1] != "1234.50" {
		t.Errorf("balance = %#v, want 1234.50", call.Args[1])
	}
	if call.Args[2] != "2000-01-02" {
		t.Errorf("birthday = %#v, want 2000-01-02", call.Args[2])
	}
	if call.Args[3] != int64(42) {
		t.Errorf("count = %#v, want 42", call.Args[3])
	}
	if call.Args[4] != "2021-03-04 05:06:07" {
		t.Errorf("meeting_at = %#v, want 2021-03-04 05:06:07", call.Args[4])
	}
	if call.Args[5] != 3.5 {
		t.Errorf("score = %#v", call.Args[5])
	}
	if call.Args[6] != "[]" {
		t.Errorf("tags = %#v, want [] (空集合写 []，对齐 Laravel)", call.Args[6])
	}
}

func TestModelCastRead(t *testing.T) {
	conn, st := newFakeDB(t)
	sel := "select * from `cast_doc` where `id` = ? limit 1"
	st.On(sel, &fakeResult{
		Columns: []string{"id", "birthday", "meeting_at", "balance", "active", "count", "score", "tags"},
		Rows: [][]driver.Value{{
			int64(1),
			time.Date(2020, 5, 6, 14, 0, 0, 0, time.UTC),
			time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC),
			"99.5", int64(1), int64(7), 2.75, `["a","b"]`,
		}},
	})

	d := &CastDoc{}
	if err := conn.Table("cast_doc").Where("id", "=", 1).First(d); err != nil {
		t.Fatal(err)
	}
	// date cast：截断到当天零点（对齐 Laravel asDate）
	if want := time.Date(2020, 5, 6, 0, 0, 0, 0, time.UTC); !d.Birthday.Equal(want) {
		t.Errorf("birthday = %v, want %v", d.Birthday, want)
	}
	if want := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC); !d.MeetingAt.Equal(want) {
		t.Errorf("meeting_at = %v", d.MeetingAt)
	}
	if d.Balance != "99.50" {
		t.Errorf("balance = %q, want 99.50 (decimal:2 规范化)", d.Balance)
	}
	if d.Active != true {
		t.Errorf("active = %v, want true", d.Active)
	}
	if d.Count != 7 {
		t.Errorf("count = %d", d.Count)
	}
	if d.Score != 2.75 {
		t.Errorf("score = %v", d.Score)
	}
	if !reflect.DeepEqual(d.Tags, []any{"a", "b"}) {
		t.Errorf("tags = %#v, want [a b]", d.Tags)
	}
}

// json/array 空值写 null/[]（对齐 Laravel），修正"零值跳过写入"偏差。
func TestModelCastEmptyValueSemantics(t *testing.T) {
	conn, st := newFakeDB(t)
	insert := "insert into `app_setting` (`config`, `name`) values (?, ?)"
	st.On(insert, &fakeResult{Affected: 1, LastID: 1})

	if err := conn.Create(&AppSetting{Name: "empty"}); err != nil {
		t.Fatal(err)
	}
	call := st.LastCall(t, insert)
	if len(call.Args) != 2 {
		t.Fatalf("args = %#v", call.Args)
	}
	// 零值结构体 json cast 写 null（列参与写入而非跳过）
	if call.Args[0] != nil {
		t.Errorf("zero struct json arg = %#v, want nil", call.Args[0])
	}
}

// ---- R2-A：UUID/ULID 唯一 ID ----

type UuidDoc struct {
	ID    string `torm:"primary_key;column:id;unique_id"`
	Title string `torm:"column:title"`
}

type UlidDoc struct {
	ID    string `torm:"primary_key;column:id;cast:ulid"`
	Title string `torm:"column:title"`
}

func TestModelCreateGeneratesUUIDPrimaryKey(t *testing.T) {
	conn, st := newFakeDB(t)
	insert := "insert into `uuid_doc` (`id`, `title`) values (?, ?)"
	// LastID=77：UUID 主键不能被自增回填覆盖
	st.On(insert, &fakeResult{Affected: 1, LastID: 77})

	d := &UuidDoc{Title: "u"}
	if err := conn.Create(d); err != nil {
		t.Fatal(err)
	}
	call := st.LastCall(t, insert)
	id, _ := call.Args[0].(string)
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !re.MatchString(id) {
		t.Errorf("id binding = %#v, want uuid v4", id)
	}
	if d.ID != id {
		t.Errorf("model id = %q, binding = %q", d.ID, id)
	}

	// 已显式赋值不覆盖
	d2 := &UuidDoc{ID: "custom", Title: "u2"}
	if err := conn.Create(d2); err != nil {
		t.Fatal(err)
	}
	call2 := st.LastCall(t, insert)
	if call2.Args[0] != "custom" {
		t.Errorf("explicit id overwritten: %#v", call2.Args[0])
	}
}

func TestModelCreateGeneratesULIDPrimaryKey(t *testing.T) {
	conn, st := newFakeDB(t)
	insert := "insert into `ulid_doc` (`id`, `title`) values (?, ?)"
	st.On(insert, &fakeResult{Affected: 1, LastID: 1})

	d := &UlidDoc{Title: "u"}
	if err := conn.Create(d); err != nil {
		t.Fatal(err)
	}
	call := st.LastCall(t, insert)
	id, _ := call.Args[0].(string)
	if len(id) != 26 {
		t.Fatalf("ulid = %q, want 26 chars", id)
	}
	for i, c := range id {
		if !strings.ContainsRune(ulidEncoding, c) {
			t.Errorf("ulid[%d] = %q not in Crockford base32", i, c)
		}
	}
	if id[0] > '7' {
		t.Errorf("ulid first char = %q, want 0-7 (48-bit ms timestamp)", id[0])
	}
	// 可解析回时间戳
	raw := make([]byte, 16)
	n := new(big.Int)
	for _, c := range id {
		idx := strings.IndexRune(ulidEncoding, c)
		n.Lsh(n, 5)
		n.Or(n, big.NewInt(int64(idx)))
	}
	b := n.FillBytes(raw)
	ms := uint64(b[0])<<40 | uint64(b[1])<<32 | uint64(b[2])<<24 | uint64(b[3])<<16 | uint64(b[4])<<8 | uint64(b[5])
	if diff := time.Since(time.UnixMilli(int64(ms))); diff < -time.Minute || diff > time.Minute {
		t.Errorf("ulid timestamp off: %v", diff)
	}
}

// ---- R2-A：timestamps 开关（WithoutTimestamps 作用域 + timestamps=false tag）----

func TestModelWithoutTimestampsScope(t *testing.T) {
	conn, st := newFakeDB(t)
	// Create：created_at/updated_at 列整体不参与 insert
	insert := "insert into `posts` (`title`) values (?)"
	st.On(insert, &fakeResult{Affected: 1, LastID: 1})

	WithoutTimestamps(func() {
		if err := conn.Create(&Post{Title: "no-ts"}); err != nil {
			t.Fatal(err)
		}
	})
	call := st.LastCall(t, insert)
	if len(call.Args) != 1 || call.Args[0] != "no-ts" {
		t.Errorf("insert args = %#v, want only title", call.Args)
	}

	// Save：干净模型完全无 SQL（updated_at 也不触碰）
	conn2, st2 := newFakeDB(t)
	sel := "select * from `posts` where `id` = ? and `posts`.`deleted_at` is null limit 1"
	st2.On(sel, &fakeResult{Columns: []string{"id", "title"}, Rows: [][]driver.Value{{int64(1), "a"}}})
	p := &Post{}
	if err := conn2.Model(Post{}).Where("id", int64(1)).First(p); err != nil {
		t.Fatal(err)
	}
	before := len(st2.Calls())
	WithoutTimestamps(func() {
		if err := conn2.Save(p); err != nil {
			t.Fatal(err)
		}
	})
	if len(st2.Calls()) != before {
		t.Errorf("save under WithoutTimestamps must not issue SQL, calls=%v", st2.Calls()[before:])
	}
}

func TestModelTimestampsFalseTag(t *testing.T) {
	type NoStampDoc struct {
		ID       int64  `torm:"primary_key;column:id"`
		Name     string `torm:"column:name"`
		Disabled bool   `torm:"timestamps=false;-"`
	}
	conn, st := newFakeDB(t)
	insert := "insert into `no_stamp_doc` (`name`) values (?)"
	st.On(insert, &fakeResult{Affected: 1, LastID: 1})

	if err := conn.Create(&NoStampDoc{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	call := st.LastCall(t, insert)
	if len(call.Args) != 1 || call.Args[0] != "x" {
		t.Errorf("insert args = %#v", call.Args)
	}
}

var _ = errors.New


// 模型建器 Update 自动触碰 updated_at（Laravel Eloquent Builder::addUpdatedAtColumn）。
func TestModelBuilderUpdateTouchesUpdatedAt(t *testing.T) {
	conn, st := newFakeDB(t)
	upd := "update `posts` set `title` = ?, `updated_at` = ? where `id` = ? and `posts`.`deleted_at` is null"
	st.On(upd, &fakeResult{Affected: 1})

	p := Post{ID: 7, Title: "old"}
	if _, err := conn.Model(p).Where("id", int64(7)).Update(map[string]interface{}{"title": "new"}); err != nil {
		t.Fatal(err)
	}
	call := st.LastCall(t, upd)
	if len(call.Args) != 3 || call.Args[0] != "new" {
		t.Errorf("args = %#v", call.Args)
	}
	if tv, ok := call.Args[1].(time.Time); !ok || tv.IsZero() {
		t.Errorf("updated_at arg = %#v, want non-zero timestamp", call.Args[1])
	}
}

// ---- 模型级 Update（Laravel Model::update = fill + save）----

func TestModelUpdateModel(t *testing.T) {
	conn, st := newFakeDB(t)
	sel := "select * from `articles` where `id` = ? and `articles`.`deleted_at` is null limit 1"
	st.On(sel, &fakeResult{Columns: []string{"id", "slug", "title"}, Rows: [][]driver.Value{{int64(1), "s", "orig"}}})
	upd := "update `articles` set `title` = ?, `updated_at` = ? where `id` = ?"
	st.On(upd, &fakeResult{Affected: 1})

	a := &Article{}
	if err := conn.Model(Article{}).Where("id", int64(1)).First(a); err != nil {
		t.Fatal(err)
	}
	if err := conn.UpdateModel(a, map[string]any{"title": "filled"}); err != nil {
		t.Fatal(err)
	}
	call := st.LastCall(t, upd)
	if len(call.Args) != 3 || call.Args[0] != "filled" || call.Args[2] != int64(1) {
		t.Errorf("update args = %#v", call.Args)
	}
	if a.Title != "filled" {
		t.Errorf("title = %q", a.Title)
	}

	// 守卫规则生效：完全守卫模型 UpdateModel 报 MassAssignmentError 且不发 SQL
	// （typeMeta 按类型全局缓存，先清掉其它测试可能残留的 fillable 注册）
	p := &guardProbe{}
	SetFillable(p)
	SetGuarded(p)
	before := len(st.Calls())
	if err := conn.UpdateModel(p, map[string]any{"title": "x"}); err == nil {
		t.Fatal("expected mass assignment error on totally guarded model")
	}
	if len(st.Calls()) != before {
		t.Error("UpdateModel on guarded violation must not issue SQL")
	}
}

// ---- whereKey 族（Builder.php:278-347）----

type KeyDoc struct {
	UID  string `torm:"primary_key;column:uid"`
	Name string `torm:"column:name"`
}

func (k *KeyDoc) TableName() string { return "key_doc" }

func TestBuilderWhereKeyFamily(t *testing.T) {
	conn, st := newFakeDB(t)

	// 同 SQL 多桩按注册顺序消费，需一次性注册（单桩会被复用）
	selEq := "select * from `posts` where `id` = ? and `posts`.`deleted_at` is null limit 1"
	st.On(selEq, &fakeResult{Columns: []string{"id", "title"}, Rows: [][]driver.Value{{int64(1), "a"}}})
	st.On(selEq, &fakeResult{Columns: []string{"id", "title"}, Rows: [][]driver.Value{{int64(2), "b"}}})

	// WhereKey 单值
	var p Post
	if err := conn.Model(Post{}).WhereKey(int64(1)).First(&p); err != nil {
		t.Fatal(err)
	}
	if p.ID != 1 {
		t.Errorf("id = %d", p.ID)
	}

	// WhereKey 模型实例 → 取其主键
	if err := conn.Model(Post{}).WhereKey(&Post{ID: 2}).First(&p); err != nil {
		t.Fatal(err)
	}
	if p.ID != 2 {
		t.Errorf("id = %d", p.ID)
	}

	// WhereKey 切片 → whereIn
	selIn := "select * from `posts` where `id` in (?, ?) and `posts`.`deleted_at` is null"
	st.On(selIn, &fakeResult{Columns: []string{"id", "title"}, Rows: [][]driver.Value{{int64(1), "a"}, {int64(2), "b"}}})
	var posts []Post
	if err := conn.Model(Post{}).WhereKey([]int64{1, 2}).Get(&posts); err != nil {
		t.Fatal(err)
	}
	if len(posts) != 2 {
		t.Errorf("posts = %d", len(posts))
	}

	// WhereKeyNot 单值 → !=
	selNe := "select * from `posts` where `id` != ? and `posts`.`deleted_at` is null limit 1"
	st.On(selNe, &fakeResult{Columns: []string{"id", "title"}, Rows: nil})
	if err := conn.Model(Post{}).WhereKeyNot(int64(9)).First(&p); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, selNe)

	// WhereKeyNot 切片 → whereNotIn
	selNotIn := "select * from `posts` where `id` not in (?) and `posts`.`deleted_at` is null"
	st.On(selNotIn, &fakeResult{Columns: []string{"id", "title"}, Rows: nil})
	if err := conn.Model(Post{}).WhereKeyNot([]int64{9}).Get(&posts); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, selNotIn)

	// OrWhereKey / OrWhereKeyNot：嵌套括号分组（软删 scope 后置）
	selOr := "select * from `posts` where (`id` = ?) and `posts`.`deleted_at` is null limit 1"
	st.On(selOr, &fakeResult{Columns: []string{"id", "title"}, Rows: nil})
	if err := conn.Model(Post{}).OrWhereKey(int64(1)).First(&p); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, selOr)

	selOrNot := "select * from `posts` where (`id` != ?) and `posts`.`deleted_at` is null limit 1"
	st.On(selOrNot, &fakeResult{Columns: []string{"id", "title"}, Rows: nil})
	if err := conn.Model(Post{}).OrWhereKeyNot(int64(1)).First(&p); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, selOrNot)

	// 组合：where(title) + orWhereKey
	selMix := "select * from `posts` where `title` = ? or (`id` = ?) and `posts`.`deleted_at` is null limit 1"
	st.On(selMix, &fakeResult{Columns: []string{"id", "title"}, Rows: nil})
	if err := conn.Model(Post{}).Where("title", "x").OrWhereKey(int64(1)).First(&p); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, selMix)

	// 普通表建器：无模型元数据时主键缺省 "id"
	tbl := "select * from `users` where `id` = ? limit 1"
	st.On(tbl, &fakeResult{Columns: []string{"id", "name"}, Rows: [][]driver.Value{{int64(3), "n"}}})
	var u fakeUser
	if err := conn.Table("users").WhereKey(int64(3)).First(&u); err != nil {
		t.Fatal(err)
	}

	// 自定义主键列模型
	keySel := "select * from `key_doc` where `uid` = ? limit 1"
	st.On(keySel, &fakeResult{Columns: []string{"uid", "name"}, Rows: [][]driver.Value{{"k1", "doc"}}})
	var kd KeyDoc
	if err := conn.Model(KeyDoc{}).WhereKey("k1").First(&kd); err != nil {
		t.Fatal(err)
	}
	if kd.UID != "k1" {
		t.Errorf("uid = %q", kd.UID)
	}
}

// ---- Builder 级 Find 族（Builder.php:589-715）----

func TestBuilderFindFamily(t *testing.T) {
	conn, st := newFakeDB(t)

	selEq := "select * from `posts` where `id` = ? and `posts`.`deleted_at` is null limit 1"
	// selEq 共 6 次查询，桩一次性按序注册
	st.On(selEq, &fakeResult{Columns: []string{"id", "title"}, Rows: [][]driver.Value{{int64(1), "a"}}})
	st.On(selEq, &fakeResult{Columns: []string{"id", "title"}, Rows: [][]driver.Value{{int64(3), "c"}}})
	st.On(selEq, &fakeResult{Columns: []string{"id", "title"}, Rows: nil})
	st.On(selEq, &fakeResult{Columns: []string{"id", "title"}, Rows: [][]driver.Value{{int64(5), "e"}}})
	st.On(selEq, &fakeResult{Columns: []string{"id", "title"}, Rows: nil})
	st.On(selEq, &fakeResult{Columns: []string{"id", "title"}, Rows: nil})

	// Find（经 WhereKey，与既有 Find 测试同 SQL 形态）
	var p Post
	if err := conn.Model(Post{}).Find(int64(1), &p); err != nil {
		t.Fatal(err)
	}
	if p.ID != 1 {
		t.Errorf("Find id = %d", p.ID)
	}

	// FindMany
	selIn := "select * from `posts` where `id` in (?, ?) and `posts`.`deleted_at` is null"
	st.On(selIn, &fakeResult{Columns: []string{"id", "title"}, Rows: [][]driver.Value{{int64(1), "a"}, {int64(2), "b"}}})
	var posts []Post
	if err := conn.Model(Post{}).FindMany([]int64{1, 2}, &posts); err != nil {
		t.Fatal(err)
	}
	if len(posts) != 2 {
		t.Errorf("FindMany = %d", len(posts))
	}

	// FindMany 空集：直接空 dest，不发 SQL
	before := len(st.Calls())
	empty := []Post{}
	if err := conn.Model(Post{}).FindMany([]int64{}, &empty); err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 || len(st.Calls()) != before {
		t.Errorf("FindMany empty must not query, calls=%v", st.Calls()[before:])
	}

	// FindOrFail 命中
	if err := conn.Model(Post{}).FindOrFail(int64(3), &p); err != nil {
		t.Fatal(err)
	}
	// FindOrFail 未命中 → ErrNotFound（dest 需空白，miss 才可检测）
	var p404 Post
	err := conn.Model(Post{}).FindOrFail(int64(404), &p404)
	var nf ErrNotFound
	if !errors.As(err, &nf) {
		t.Fatalf("FindOrFail miss err = %v, want ErrNotFound", err)
	}

	// FindOrNew 命中
	var p2 Post
	if err := conn.Model(Post{}).FindOrNew(int64(5), &p2); err != nil {
		t.Fatal(err)
	}
	if p2.ID != 5 || p2.Title != "e" {
		t.Errorf("FindOrNew hit = %+v", p2)
	}
	// FindOrNew 未命中：空白实例 + 主键，不落库
	before = len(st.Calls())
	p3 := Post{}
	if err := conn.Model(Post{}).FindOrNew(int64(6), &p3); err != nil {
		t.Fatal(err)
	}
	if p3.ID != 6 || p3.Title != "" {
		t.Errorf("FindOrNew miss = %+v, want blank with pk=6", p3)
	}
	if len(st.Calls()) != before+1 {
		t.Errorf("FindOrNew miss should only run the select, calls=%v", st.Calls()[before:])
	}

	// FindOr 未命中执行 fallback
	called := false
	pOr := Post{}
	if err := conn.Model(Post{}).FindOr(int64(7), &pOr, func() error { called = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("FindOr fallback must run on miss")
	}
}

// ---- Builder 级 firstOrCreate 族（转发 Connection）----

func TestBuilderFirstOrCreateFamily(t *testing.T) {
	conn, st := newFakeDB(t)
	sel := "select * from `posts` where `title` = ? and `posts`.`deleted_at` is null limit 1"
	st.On(sel, &fakeResult{Columns: []string{"id", "title"}, Rows: nil})
	insert := "insert into `posts` (`created_at`, `title`, `updated_at`) values (?, ?, ?)"
	st.On(insert, &fakeResult{Affected: 1, LastID: 4})

	b := conn.Model(Post{})
	p := Post{}
	if err := b.FirstOrCreate(&p, map[string]interface{}{"title": "made"}); err != nil {
		t.Fatal(err)
	}
	if p.ID != 4 || p.Title != "made" {
		t.Errorf("created = %+v", p)
	}
	if !WasRecentlyCreated(&p) {
		t.Error("FirstOrCreate miss should mark wasRecentlyCreated")
	}

	// UpdateOrCreate 命中 → 更新（attributes 为 id，走 id 条件查询）
	selID := "select * from `posts` where `id` = ? and `posts`.`deleted_at` is null limit 1"
	st.On(selID, &fakeResult{Columns: []string{"id", "title"}, Rows: [][]driver.Value{{int64(4), "old"}}})
	upd := "update `posts` set `title` = ?, `updated_at` = ? where `id` = ?"
	st.On(upd, &fakeResult{Affected: 1})
	p2 := Post{}
	if err := b.UpdateOrCreate(&p2, map[string]interface{}{"id": int64(4)}, map[string]interface{}{"title": "upd"}); err != nil {
		t.Fatal(err)
	}
	call := st.LastCall(t, upd)
	if call.Args[0] != "upd" || call.Args[2] != int64(4) {
		t.Errorf("update args = %#v", call.Args)
	}

	// CreateOrFirst 无冲突直接创建
	conn3, st3 := newFakeDB(t)
	st3.On(insert, &fakeResult{Affected: 1, LastID: 6})
	p3 := Post{}
	if err := conn3.Model(Post{}).CreateOrFirst(&p3, map[string]interface{}{"title": "cof"}); err != nil {
		t.Fatal(err)
	}
	if p3.ID != 6 {
		t.Errorf("id = %d", p3.ID)
	}

	// FirstOrNew 未命中：仅构造
	conn4, st4 := newFakeDB(t)
	st4.On(sel, &fakeResult{Columns: []string{"id", "title"}, Rows: nil})
	p4 := Post{}
	if err := conn4.Model(Post{}).FirstOrNew(&p4, map[string]interface{}{"title": "draft"}); err != nil {
		t.Fatal(err)
	}
	if p4.Title != "draft" || WasRecentlyCreated(&p4) {
		t.Errorf("FirstOrNew = %+v", p4)
	}
	for _, c := range st4.Calls() {
		if strings.HasPrefix(c.Sql, "insert") {
			t.Error("FirstOrNew must not persist")
		}
	}
}

// ---- incrementOrCreate（Laravel 13 Builder.php:786）----

type CounterDoc struct {
	ID        int64     `torm:"primary_key;column:id"`
	Slug      string    `torm:"column:slug"`
	Count     int64     `torm:"column:count"`
	CreatedAt time.Time `torm:"column:created_at"`
	UpdatedAt time.Time `torm:"column:updated_at"`
}

func TestBuilderIncrementOrCreate(t *testing.T) {
	// 未命中 → 创建（column 取 default），不触发 increment
	conn, st := newFakeDB(t)
	sel := "select * from `counter_doc` where `slug` = ? limit 1"
	st.On(sel, &fakeResult{Columns: []string{"id"}, Rows: nil})
	insert := "insert into `counter_doc` (`count`, `created_at`, `slug`, `updated_at`) values (?, ?, ?, ?)"
	st.On(insert, &fakeResult{Affected: 1, LastID: 1})

	d := &CounterDoc{}
	if err := conn.Model(CounterDoc{}).IncrementOrCreate(d, map[string]interface{}{"slug": "new"}, "count", 5); err != nil {
		t.Fatal(err)
	}
	call := st.LastCall(t, insert)
	if call.Args[0] != int64(5) || call.Args[2] != "new" {
		t.Errorf("insert args = %#v, want count=5 slug=new", call.Args)
	}
	if d.Count != 5 {
		t.Errorf("count = %d, want 5", d.Count)
	}

	// 命中 → 自增 step
	conn2, st2 := newFakeDB(t)
	st2.On(sel, &fakeResult{Columns: []string{"id", "slug", "count"}, Rows: [][]driver.Value{{int64(9), "hit", int64(10)}}})
	upd := "update `counter_doc` set `count` = `count` + 3, `updated_at` = ? where `id` = ?"
	st2.On(upd, &fakeResult{Affected: 1})

	d2 := &CounterDoc{}
	if err := conn2.Model(CounterDoc{}).IncrementOrCreate(d2, map[string]interface{}{"slug": "hit"}, "count", 1, 3); err != nil {
		t.Fatal(err)
	}
	call2 := st2.LastCall(t, upd)
	if call2.Args[1] != int64(9) {
		t.Errorf("increment args = %#v", call2.Args)
	}
	if d2.Count != 13 {
		t.Errorf("count = %d, want 13", d2.Count)
	}
}

// ---- Builder touch（Builder.php:1343）----

func TestBuilderTouch(t *testing.T) {
	conn, st := newFakeDB(t)

	// 模型建器缺省列：updated_at
	upd := "update `posts` set `updated_at` = ? where `id` = ? and `posts`.`deleted_at` is null"
	st.On(upd, &fakeResult{Affected: 2})
	n, err := conn.Model(Post{}).Where("id", int64(3)).Touch()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("affected = %d", n)
	}
	call := st.LastCall(t, upd)
	if len(call.Args) != 2 || call.Args[1] != int64(3) {
		t.Errorf("args = %#v", call.Args)
	}
	if tv, ok := call.Args[0].(time.Time); !ok || tv.IsZero() {
		t.Errorf("updated_at arg = %#v", call.Args[0])
	}

	// 显式列：仅更新该列，不自动追加 updated_at
	upd2 := "update `posts` set `published_at` = ? where `id` = ? and `posts`.`deleted_at` is null"
	st.On(upd2, &fakeResult{Affected: 1})
	if _, err := conn.Model(Post{}).Where("id", int64(3)).Touch("published_at"); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, upd2)

	// 无时间戳模型缺省列 → 无 SQL
	before := len(st.Calls())
	n2, err := conn.Model(fakeUser{}).Touch()
	if err != nil || n2 != 0 {
		t.Errorf("touch without timestamps = (%d, %v)", n2, err)
	}
	if len(st.Calls()) != before {
		t.Errorf("touch without timestamps must not issue SQL, calls=%v", st.Calls()[before:])
	}

	// 普通表建器 + 显式列
	upd3 := "update `hits` set `seen_at` = ? where `k` = ?"
	st.On(upd3, &fakeResult{Affected: 1})
	if _, err := conn.Table("hits").Where("k", "x").Touch("seen_at"); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, upd3)
}

// ---- 模型实例级 loadAggregate 族（Model.php:900-1050）----

type CountUser struct {
	ID          int64  `torm:"primary_key;column:id"`
	Name        string `torm:"column:name"`
	PostsCount  int64  `torm:"column:posts_count"`
	VotesSum    int64  `torm:"column:posts_sum_votes"`
	PostsExists bool   `torm:"column:posts_exists"`
}

func (*CountUser) TableName() string { return "count_users" }

type CountPost struct {
	ID     int64 `torm:"primary_key;column:id"`
	UserID int64 `torm:"column:user_id"`
	Votes  int64 `torm:"column:votes"`
}

func (*CountPost) TableName() string { return "count_posts" }

func TestModelLoadAggregateFamily(t *testing.T) {
	conn, st := newFakeDB(t)
	u := &CountUser{ID: 1, Name: "u"}
	rel := HasMany{Model: CountPost{}, ForeignKey: "user_id"}

	// LoadCount
	cnt := "select (select count(*) from `count_posts` where `count_posts`.`user_id` = `count_users`.`id`) as `posts_count` from `count_users` where `id` = ? limit 1"
	st.On(cnt, &fakeResult{Columns: []string{"posts_count"}, Rows: [][]driver.Value{{int64(3)}}})
	if err := conn.LoadCount(u, "posts", rel); err != nil {
		t.Fatal(err)
	}
	if u.PostsCount != 3 {
		t.Errorf("posts_count = %d, want 3", u.PostsCount)
	}

	// LoadSum
	sum := "select (select sum(`votes`) from `count_posts` where `count_posts`.`user_id` = `count_users`.`id`) as `posts_sum_votes` from `count_users` where `id` = ? limit 1"
	st.On(sum, &fakeResult{Columns: []string{"posts_sum_votes"}, Rows: [][]driver.Value{{int64(42)}}})
	if err := conn.LoadSum(u, "posts", rel, "votes"); err != nil {
		t.Fatal(err)
	}
	if u.VotesSum != 42 {
		t.Errorf("posts_sum_votes = %d, want 42", u.VotesSum)
	}

	// LoadExists（int64 → bool 属性回填）
	exists := "select (select exists(select * from `count_posts` where `count_posts`.`user_id` = `count_users`.`id`)) as `posts_exists` from `count_users` where `id` = ? limit 1"
	st.On(exists, &fakeResult{Columns: []string{"posts_exists"}, Rows: [][]driver.Value{{int64(1)}}})
	if err := conn.LoadExists(u, "posts", rel); err != nil {
		t.Fatal(err)
	}
	if !u.PostsExists {
		t.Errorf("posts_exists = %v, want true", u.PostsExists)
	}

	// LoadMax / LoadMin / LoadAvg
	mx := "select (select max(`votes`) from `count_posts` where `count_posts`.`user_id` = `count_users`.`id`) as `posts_max_votes` from `count_users` where `id` = ? limit 1"
	st.On(mx, &fakeResult{Columns: []string{"posts_max_votes"}, Rows: [][]driver.Value{{int64(9)}}})
	if err := conn.LoadMax(u, "posts", rel, "votes"); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, mx)

	// 带约束闭包 + 自定义别名（WithAggregate 同款 rest 语义）
	aliased := "select (select count(*) from `count_posts` where `count_posts`.`user_id` = `count_users`.`id` and `votes` > ?) as `hot_count` from `count_users` where `id` = ? limit 1"
	st.On(aliased, &fakeResult{Columns: []string{"hot_count"}, Rows: [][]driver.Value{{int64(2)}}})
	if err := conn.LoadCount(u, "posts", rel, func(nb *Builder) { nb.Where("votes", ">", 5) }, "hot_count"); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, aliased)
}

// ---- 模型实例级 loadMorph / loadMorphCount ----

type MorphHost struct {
	ID            int64       `torm:"primary_key;column:id"`
	Comments      []*MorphCmt `torm:"-"`
	CommentsCount int64       `torm:"column:comments_count"`
}

func (*MorphHost) TableName() string { return "morph_hosts" }

type MorphCmt struct {
	ID              int64  `torm:"primary_key;column:id"`
	CommentableType string `torm:"column:commentable_type"`
	CommentableID   int64  `torm:"column:commentable_id"`
}

func (*MorphCmt) TableName() string { return "morph_cmts" }

func TestModelLoadMorphAndCount(t *testing.T) {
	conn, st := newFakeDB(t)
	host := &MorphHost{ID: 1}
	rel := MorphMany{MorphName: "commentable", TargetModel: MorphCmt{}}

	// LoadMorph：类型列 + id 列 in (父键)
	sel := "select * from `morph_cmts` where `commentable_type` = ? and `commentable_id` in (?)"
	st.On(sel, &fakeResult{Columns: []string{"id", "commentable_type", "commentable_id"},
		Rows: [][]driver.Value{{int64(5), "morph_hosts", int64(1)}, {int64(6), "morph_hosts", int64(1)}}})
	if err := conn.LoadMorph(host, "comments", rel); err != nil {
		t.Fatal(err)
	}
	if len(host.Comments) != 2 || host.Comments[0].ID != 5 || host.Comments[1].ID != 6 {
		t.Errorf("comments = %+v", host.Comments)
	}
	if !RelationLoaded(host, "comments") {
		t.Error("LoadMorph should populate the loaded-relation cache")
	}

	// LoadMorphCount
	cnt := "select (select count(*) from `morph_cmts` where `morph_cmts`.`commentable_id` = `morph_hosts`.`id` and `commentable_type` = 'morph_hosts') as `comments_count` from `morph_hosts` where `id` = ? limit 1"
	st.On(cnt, &fakeResult{Columns: []string{"comments_count"}, Rows: [][]driver.Value{{int64(2)}}})
	if err := conn.LoadMorphCount(host, "comments", rel); err != nil {
		t.Fatal(err)
	}
	if host.CommentsCount != 2 {
		t.Errorf("comments_count = %d, want 2", host.CommentsCount)
	}
}

// ---- fresh / refresh（Model.php:2089-2135）----

func TestModelFresh(t *testing.T) {
	conn, st := newFakeDB(t)
	// 水合
	sel := "select * from `articles` where `id` = ? and `articles`.`deleted_at` is null limit 1"
	st.On(sel, &fakeResult{Columns: []string{"id", "slug", "title"}, Rows: [][]driver.Value{{int64(1), "s", "orig"}}})
	a := &Article{}
	if err := conn.Model(Article{}).Where("id", int64(1)).First(a); err != nil {
		t.Fatal(err)
	}

	// fresh：无软删 scope 重查，返回新实例，原模型不变（两桩按序注册）
	freshSel := "select * from `articles` where `id` = ? limit 1"
	st.On(freshSel, &fakeResult{Columns: []string{"id", "slug", "title"}, Rows: [][]driver.Value{{int64(1), "s", "db-title"}}})
	st.On(freshSel, &fakeResult{Columns: []string{"id", "slug", "title"}, Rows: nil})
	f, err := conn.Fresh(a)
	if err != nil {
		t.Fatal(err)
	}
	fa, ok := f.(*Article)
	if !ok {
		t.Fatalf("Fresh type = %T", f)
	}
	if fa.Title != "db-title" {
		t.Errorf("fresh title = %q", fa.Title)
	}
	if a.Title != "orig" {
		t.Errorf("original mutated: %q", a.Title)
	}
	// fresh 不污染原模型的脏状态
	if IsDirty(a) {
		t.Error("fresh must not touch the original model state")
	}

	// 未持久化模型 → nil
	nf, err := conn.Fresh(&Article{})
	if err != nil || nf != nil {
		t.Errorf("Fresh on non-persisted = (%v, %v)", nf, err)
	}

	// 行未命中 → nil
	gone, err := conn.Fresh(a)
	if err != nil || gone != nil {
		t.Errorf("Fresh on missing row = (%v, %v)", gone, err)
	}
}

func TestModelRefresh(t *testing.T) {
	conn, st := newFakeDB(t)
	sel := "select * from `articles` where `id` = ? and `articles`.`deleted_at` is null limit 1"
	st.On(sel, &fakeResult{Columns: []string{"id", "slug", "title"}, Rows: [][]driver.Value{{int64(1), "s", "orig"}}})
	a := &Article{}
	if err := conn.Model(Article{}).Where("id", int64(1)).First(a); err != nil {
		t.Fatal(err)
	}

	// refresh：重查并刷新原模型属性，回到 clean（两桩按序注册）
	refreshSel := "select * from `articles` where `id` = ? limit 1"
	st.On(refreshSel, &fakeResult{Columns: []string{"id", "slug", "title"}, Rows: [][]driver.Value{{int64(1), "s", "db-title"}}})
	st.On(refreshSel, &fakeResult{Columns: []string{"id", "slug", "title"}, Rows: nil})
	a.Title = "changed"
	if err := conn.Refresh(a); err != nil {
		t.Fatal(err)
	}
	if a.Title != "db-title" {
		t.Errorf("refresh title = %q, want db-title", a.Title)
	}
	if IsDirty(a) || GetOriginal(a, "title") != "db-title" {
		t.Error("refresh should re-sync original snapshot")
	}

	// 行已消失 → ErrNotFound
	if err := conn.Refresh(a); !errors.As(err, new(ErrNotFound)) {
		t.Errorf("Refresh on missing row err = %v, want ErrNotFound", err)
	}

	// 已软删模型：refresh 不带软删 scope，仍可刷新
	conn2, st2 := newFakeDB(t)
	st2.On(sel, &fakeResult{Columns: []string{"id", "slug", "title"}, Rows: [][]driver.Value{{int64(2), "s", "live"}}})
	a2 := &Article{}
	if err := conn2.Model(Article{}).Where("id", int64(2)).First(a2); err != nil {
		t.Fatal(err)
	}
	softDel := "update `articles` set `deleted_at` = ?, `updated_at` = ? where `id` = ?"
	st2.On(softDel, &fakeResult{Affected: 1})
	if err := conn2.DeleteModel(a2); err != nil {
		t.Fatal(err)
	}
	deletedAt := time.Date(2021, 5, 1, 0, 0, 0, 0, time.UTC)
	st2.On(refreshSel, &fakeResult{Columns: []string{"id", "slug", "title", "deleted_at"},
		Rows: [][]driver.Value{{int64(2), "s", "trashed", deletedAt}}})
	if err := conn2.Refresh(a2); err != nil {
		t.Fatal(err)
	}
	if a2.DeletedAt == nil {
		t.Error("refresh on trashed model should restore deleted_at attribute")
	}

	// 未持久化模型：refresh 静默返回，不发 SQL
	before := len(st2.Calls())
	if err := conn2.Refresh(&Article{}); err != nil {
		t.Errorf("Refresh on non-persisted = %v", err)
	}
	if len(st2.Calls()) != before {
		t.Error("Refresh on non-persisted must not query")
	}
}

// ---- push / pushQuietly（Model.php:1329-1366）----

type PushDoc struct {
	ID   int64  `torm:"primary_key;column:id"`
	Name string `torm:"column:name"`
}

func (*PushDoc) TableName() string { return "push_doc" }

func TestModelPush(t *testing.T) {
	conn, st := newFakeDB(t)
	ins := "insert into `push_doc` (`name`) values (?)"
	st.On(ins, &fakeResult{Affected: 1, LastID: 1})
	st.On(ins, &fakeResult{Affected: 1, LastID: 2})

	parent := &PushDoc{Name: "p"}
	child := &PushDoc{Name: "c"}
	SetLoadedRelation(parent, "children", []any{child})
	if err := conn.Push(parent); err != nil {
		t.Fatal(err)
	}
	// 两次 insert：先父后子
	c1 := st.Calls()
	n := 0
	for _, c := range c1 {
		if c.Sql == ins {
			n++
		}
	}
	if n != 2 {
		t.Errorf("push inserts = %d, want 2", n)
	}
	if !Exists(child) {
		t.Error("child should be persisted after push")
	}

	// 循环引用防环：self 关系不会无限递归
	conn2, st2 := newFakeDB(t)
	st2.On(ins, &fakeResult{Affected: 1, LastID: 3})
	loop := &PushDoc{Name: "loop"}
	SetLoadedRelation(loop, "self", []any{loop})
	if err := conn2.Push(loop); err != nil {
		t.Fatal(err)
	}
	n2 := 0
	for _, c := range st2.Calls() {
		if c.Sql == ins {
			n2++
		}
	}
	if n2 != 1 {
		t.Errorf("cycle push inserts = %d, want 1", n2)
	}
}

func TestModelPushQuietly(t *testing.T) {
	conn, st := newFakeDB(t)
	ins := "insert into `push_doc` (`name`) values (?)"
	st.On(ins, &fakeResult{Affected: 1, LastID: 1})
	st.On(ins, &fakeResult{Affected: 1, LastID: 2})

	var hooks int
	conn.Observe(&PushDoc{}, &Observer{
		Saving: func(any) error { hooks++; return nil },
		Saved:  func(any) error { hooks++; return nil },
	})
	defer conn.Observe(&PushDoc{}, nil)

	parent := &PushDoc{Name: "p"}
	child := &PushDoc{Name: "c"}
	SetLoadedRelation(parent, "children", []any{child})
	if err := conn.PushQuietly(parent); err != nil {
		t.Fatal(err)
	}
	if hooks != 0 {
		t.Errorf("PushQuietly hooks = %d, want 0", hooks)
	}
}

// ---- GetOriginal 全量形态（HasAttributes.php:2084）----

func TestModelGetOriginalFullForm(t *testing.T) {
	conn, st := newFakeDB(t)
	sel := "select * from `articles` where `id` = ? and `articles`.`deleted_at` is null limit 1"
	st.On(sel, &fakeResult{Columns: []string{"id", "slug", "title"}, Rows: [][]driver.Value{{int64(1), "s", "orig"}}})
	a := &Article{}
	if err := conn.Model(Article{}).Where("id", int64(1)).First(a); err != nil {
		t.Fatal(err)
	}

	orig, ok := GetOriginal(a).(map[string]any)
	if !ok {
		t.Fatalf("GetOriginal() = %T, want map[string]any", GetOriginal(a))
	}
	if orig["title"] != "orig" || orig["slug"] != "s" || orig["id"] != int64(1) {
		t.Errorf("original map = %#v", orig)
	}

	// 副本语义：改动返回 map 不影响状态
	orig["title"] = "mutated"
	if v := GetOriginal(a, "title"); v != "orig" {
		t.Errorf("returned map must be a copy, title = %#v", v)
	}

	// 无快照模型：全量退回当前属性快照
	fresh := &Article{Title: "now"}
	m, ok := GetOriginal(fresh).(map[string]any)
	if !ok || m["title"] != "now" {
		t.Errorf("GetOriginal without snapshot = %#v", GetOriginal(fresh))
	}
}

// ---- has 计数阈值（QueriesRelationships.php:39-90）----

func TestBuilderHasCountThreshold(t *testing.T) {
	conn, _ := newFakeDB(t)
	rel := HasMany{Model: CountPost{}, ForeignKey: "user_id"}

	// Has(rel)：退化为 exists（canUseExistsForExistenceCheck）
	sql := conn.Model(CountUser{}).Has(rel).ToSql()
	want := "select * from `count_users` where exists (select * from `count_posts` where `count_users`.`id` = `count_posts`.`user_id`)"
	if sql != want {
		t.Errorf("Has(rel) sql = %s\nwant %s", sql, want)
	}

	// Has(rel, count)：默认 >=（计数子查询含 torm 方言的 aggregate 别名）
	sql = conn.Model(CountUser{}).Has(rel, 3).ToSql()
	want = "select * from `count_users` where (select count(*) as `aggregate` from `count_posts` where `count_users`.`id` = `count_posts`.`user_id`) >= 3"
	if sql != want {
		t.Errorf("Has(rel, 3) sql = %s\nwant %s", sql, want)
	}

	// Has(rel, op, count)
	sql = conn.Model(CountUser{}).Has(rel, ">", 2).ToSql()
	want = "select * from `count_users` where (select count(*) as `aggregate` from `count_posts` where `count_users`.`id` = `count_posts`.`user_id`) > 2"
	if sql != want {
		t.Errorf("Has(rel, '>', 2) sql = %s\nwant %s", sql, want)
	}

	// Has(rel, '<', 1) → not exists（doesntHave 语义）
	sql = conn.Model(CountUser{}).Has(rel, "<", 1).ToSql()
	want = "select * from `count_users` where not exists (select * from `count_posts` where `count_users`.`id` = `count_posts`.`user_id`)"
	if sql != want {
		t.Errorf("Has(rel, '<', 1) sql = %s\nwant %s", sql, want)
	}

	// Laravel canUseExistsForExistenceCheck 严格要求 count === 1（:1135-1138），
	// 无绝对值归一：负数阈值原样进入 count 子查询比较
	sql = conn.Model(CountUser{}).Has(rel, ">=", -5).ToSql()
	if !strings.Contains(sql, ">= -5") {
		t.Errorf("Has(rel, '>=', -5) sql = %s, want raw count -5", sql)
	}
}
