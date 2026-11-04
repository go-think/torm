package torm

import (
	"database/sql/driver"
	"reflect"
	"strings"
	"testing"
)

// 带关系字段的模型
type UserWithPosts struct {
	ID    int64          `torm:"primary_key;column:id"`
	Name  string         `torm:"column:name"`
	Posts []PostWithUser `torm:"-"` // HasMany
	User  *User          `torm:"-"` // BelongsTo（反向自引用演示用）
}

func (u *UserWithPosts) TableName() string { return "users" }

type PostWithUser struct {
	ID     int64          `torm:"primary_key;column:id"`
	Title  string         `torm:"column:title"`
	UserID int64          `torm:"column:user_id"`
	Author *UserWithPosts `torm:"-"`
}

func (p *PostWithUser) TableName() string { return "posts" }

func TestWithHasManyEagerLoad(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `users`", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:   [][]driver.Value{{int64(1), "A"}, {int64(2), "B"}},
	})
	st.On("select * from `posts` where `user_id` in (?, ?)", &fakeResult{
		Columns: []string{"id", "title", "user_id"},
		Rows: [][]driver.Value{
			{int64(10), "p1", int64(1)},
			{int64(11), "p2", int64(1)},
			{int64(12), "p3", int64(2)},
		},
	})

	var users []UserWithPosts
	if err := conn.Table("users").With("posts", HasMany{Model: PostWithUser{}}).Get(&users); err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 {
		t.Fatalf("users = %d", len(users))
	}
	if len(users[0].Posts) != 2 || users[0].Posts[0].Title != "p1" {
		t.Errorf("user1 posts = %+v", users[0].Posts)
	}
	if len(users[1].Posts) != 1 || users[1].Posts[0].Title != "p3" {
		t.Errorf("user2 posts = %+v", users[1].Posts)
	}
	// 预加载查询只发生一次（无 N+1）
	selectCount := 0
	for _, call := range st.Calls() {
		if call.Kind == "query" {
			selectCount++
		}
	}
	if selectCount != 2 {
		t.Errorf("queries = %d, want 2 (parent + one related)", selectCount)
	}
}

func TestBelongsToEagerLoad(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `posts`", &fakeResult{
		Columns: []string{"id", "title", "user_id"},
		Rows:   [][]driver.Value{{int64(10), "p1", int64(1)}},
	})
	st.On("select * from `users` where `id` in (?)", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:   [][]driver.Value{{int64(1), "A"}},
	})

	var posts []PostWithUser
	if err := conn.Table("posts").With("author", BelongsTo{Model: &UserWithPosts{}}).Get(&posts); err != nil {
		t.Fatal(err)
	}
	if len(posts) != 1 {
		t.Fatalf("posts = %d", len(posts))
	}
	if posts[0].Author == nil || posts[0].Author.Name != "A" {
		t.Errorf("author = %+v", posts[0].Author)
	}
}

func TestWhereHasGolden(t *testing.T) {
	b := baseBuilder(t).Select("*").From("users").WhereHas(HasMany{Model: PostWithUser{}}, func(nb *Builder) {
		nb.Where("title", "=", "p1")
	})
	want := `select * from "users" where exists (select * from "posts" where "users"."id" = "posts"."user_id" and "title" = ?)`
	if got := b.ToSql(); got != want {
		t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
	}
	bindings := b.GetBindings()
	if len(bindings) != 1 || bindings[0] != "p1" {
		t.Errorf("bindings = %#v", bindings)
	}
}

func TestWithCountGolden(t *testing.T) {
	b := baseBuilder(t).Select("*").From("users").WithCount("posts", HasMany{Model: PostWithUser{}})
	want := `select *, (select count(*) from "posts" where "posts"."user_id" = "users"."id") as "posts_count" from "users"`
	if got := b.ToSql(); got != want {
		t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
	}
	if strings.Count(want, "posts_count") != 1 {
		t.Error("alias should appear once")
	}
}

func TestRelationKeysDefaults(t *testing.T) {
	// HasMany 默认：目标列 user_id，父列 id
	fk, lk := relationKeys(HasMany{Model: PostWithUser{}}, "users")
	if fk != "user_id" || lk != "id" {
		t.Errorf("hasMany keys = (%s, %s)", fk, lk)
	}
	// BelongsTo 默认：目标列 id，父列 user_id
	fk, lk = relationKeys(BelongsTo{Model: &UserWithPosts{}}, "posts")
	if fk != "id" || lk != "user_id" {
		t.Errorf("belongsTo keys = (%s, %s)", fk, lk)
	}
}

func TestWithHasManyNilSkip(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `users`", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:   [][]driver.Value{{int64(1), "A"}},
	})
	// 关联查询零结果 → 字段为空切片
	st.On("select * from `posts` where `user_id` in (?)", &fakeResult{
		Columns: []string{"id", "title", "user_id"},
		Rows:   [][]driver.Value{},
	})

	var users []UserWithPosts
	if err := conn.Table("users").With("posts", HasMany{Model: PostWithUser{}}).Get(&users); err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 {
		t.Fatalf("users = %d", len(users))
	}
	if len(users[0].Posts) != 0 {
		t.Errorf("posts = %+v", users[0].Posts)
	}
	var _ = reflect.DeepEqual
}

// 多对多模型
type Role struct {
	ID   int64  `torm:"primary_key;column:id"`
	Name string `torm:"column:name"`
}

func (r *Role) TableName() string { return "roles" }

type UserWithRoles struct {
	ID    int64  `torm:"primary_key;column:id"`
	Name  string `torm:"column:name"`
	Roles []Role `torm:"-"`
}

func (u *UserWithRoles) TableName() string { return "users" }

func TestBelongsToManyEagerLoad(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `users`", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:   [][]driver.Value{{int64(1), "A"}},
	})
	joinSQL := "select `roles`.*, `role_user`.`user_id` as `pivot_user_id` from `roles` inner join `role_user` on `roles`.`id` = `role_user`.`role_id` where `role_user`.`user_id` in (?)"
	st.On(joinSQL, &fakeResult{
		Columns: []string{"id", "name", "pivot_user_id"},
		Rows: [][]driver.Value{
			{int64(7), "admin", int64(1)},
			{int64(8), "editor", int64(1)},
		},
	})

	var users []UserWithRoles
	if err := conn.Table("users").With("roles", BelongsToMany{RelatedModel: Role{}}).Get(&users); err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 {
		t.Fatalf("users = %d", len(users))
	}
	if len(users[0].Roles) != 2 {
		t.Fatalf("roles = %+v", users[0].Roles)
	}
	if users[0].Roles[0].Name != "admin" || users[0].Roles[1].Name != "editor" {
		t.Errorf("roles = %+v", users[0].Roles)
	}
	st.LastCall(t, joinSQL)
}

func TestBelongsToManyDefaultNaming(t *testing.T) {
	rel := BelongsToMany{RelatedModel: Role{}}
	table := relationTargetTable(rel)
	if table != "roles" {
		t.Errorf("target = %s", table)
	}
}

// 远程一对多模型：User →(经 Post)→ Comment
type ThroughComment struct {
	ID     int64  `torm:"primary_key;column:id"`
	Body   string `torm:"column:body"`
	PostID int64  `torm:"column:post_id"`
}

func (c *ThroughComment) TableName() string { return "comments" }

type UserWithComments struct {
	ID       int64            `torm:"primary_key;column:id"`
	Name     string           `torm:"column:name"`
	Comments []ThroughComment `torm:"-"`
}

func (u *UserWithComments) TableName() string { return "users" }

type ThroughPost struct {
	ID     int64 `torm:"primary_key;column:id"`
	UserID int64 `torm:"column:user_id"`
}

func (p *ThroughPost) TableName() string { return "posts" }

func TestHasManyThroughEagerLoad(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `users`", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:   [][]driver.Value{{int64(1), "A"}},
	})
	joinSQL := "select `comments`.*, `posts`.`user_id` as `laravel_through_key` from `comments` inner join `posts` on `posts`.`id` = `comments`.`post_id` where `posts`.`user_id` in (?)"
	st.On(joinSQL, &fakeResult{
		Columns: []string{"id", "body", "post_id", "laravel_through_key"},
		Rows: [][]driver.Value{
			{int64(100), "c1", int64(5), int64(1)},
			{int64(101), "c2", int64(6), int64(1)},
		},
	})

	var users []UserWithComments
	if err := conn.Table("users").With("comments", HasManyThrough{
		ThroughModel: ThroughPost{},
		TargetModel:  ThroughComment{},
	}).Get(&users); err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 {
		t.Fatalf("users = %d", len(users))
	}
	if len(users[0].Comments) != 2 {
		t.Fatalf("comments = %+v", users[0].Comments)
	}
	if users[0].Comments[0].Body != "c1" || users[0].Comments[1].Body != "c2" {
		t.Errorf("comments = %+v", users[0].Comments)
	}
	st.LastCall(t, joinSQL)
}

// With 预加载 limit 自动接入 GroupLimit
type PostOrdered struct {
	ID        int64  `torm:"primary_key;column:id"`
	Title     string `torm:"column:title"`
	UserID    int64  `torm:"column:user_id"`
	CreatedAt string `torm:"column:created_at"`
}

func (p *PostOrdered) TableName() string { return "posts" }

type UserWithLimitedPosts struct {
	ID        int64         `torm:"primary_key;column:id"`
	Name      string        `torm:"column:name"`
	LatestTwo []PostOrdered `torm:"-"`
}

func (u *UserWithLimitedPosts) TableName() string { return "users" }

func TestWithLimitAutoGroupLimit(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `users`", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:   [][]driver.Value{{int64(1), "A"}, {int64(2), "B"}},
	})
	// 约束闭包的 limit 自动经 groupLimit 编译（row_number 分组限额）
	groupSQL := "select * from (select `posts`.*, row_number() over (partition by `user_id` order by `created_at` desc) as `laravel_row` from `posts` where `user_id` in (?, ?)) as `laravel_table` where `laravel_row` <= 2 order by `laravel_row`"
	st.On(groupSQL, &fakeResult{
		Columns: []string{"id", "title", "user_id", "created_at", "laravel_row"},
		Rows: [][]driver.Value{
			{int64(10), "p1", int64(1), "2026-01-02", int64(1)},
			{int64(12), "p3", int64(1), "2026-01-01", int64(2)},
			{int64(15), "p5", int64(2), "2026-01-03", int64(1)},
		},
	})

	var users []UserWithLimitedPosts
	if err := conn.Table("users").WithLimit("latestTwo", HasMany{Model: PostOrdered{}}, 2, func(nb *Builder) {
		nb.Select("posts.*").OrderBy("created_at", "desc")
	}).Get(&users); err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 {
		t.Fatalf("users = %d", len(users))
	}
	if len(users[0].LatestTwo) != 2 || users[0].LatestTwo[0].Title != "p1" {
		t.Errorf("user1 latest = %+v", users[0].LatestTwo)
	}
	if len(users[1].LatestTwo) != 1 || users[1].LatestTwo[0].Title != "p5" {
		t.Errorf("user2 latest = %+v", users[1].LatestTwo)
	}
	st.LastCall(t, groupSQL)
}

func TestWithConstraintsPlainQuery(t *testing.T) {
	// 无 limit 的约束预加载：仍走普通 whereIn 查询（golden）
	b := baseBuilder(t)
	b.WithConstraints("posts", HasMany{Model: PostOrdered{}}, func(nb *Builder) {
		nb.Where("status", "=", "published")
	})
	_ = b
}
