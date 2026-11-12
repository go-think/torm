package torm

import (
	"database/sql/driver"
	"reflect"
	"strings"
	"testing"
	"time"
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
		Rows:    [][]driver.Value{{int64(1), "A"}, {int64(2), "B"}},
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
		Rows:    [][]driver.Value{{int64(10), "p1", int64(1)}},
	})
	st.On("select * from `users` where `id` in (?)", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:    [][]driver.Value{{int64(1), "A"}},
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
		Rows:    [][]driver.Value{{int64(1), "A"}},
	})
	// 关联查询零结果 → 字段为空切片
	st.On("select * from `posts` where `user_id` in (?)", &fakeResult{
		Columns: []string{"id", "title", "user_id"},
		Rows:    [][]driver.Value{},
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
		Rows:    [][]driver.Value{{int64(1), "A"}},
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
		Rows:    [][]driver.Value{{int64(1), "A"}},
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
		Rows:    [][]driver.Value{{int64(1), "A"}, {int64(2), "B"}},
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

// ---- WhereHas 全类型通用化（QueriesRelationships.php）----

func TestWhereHasBelongsToManyGolden(t *testing.T) {
	// exists 经 pivot 中间表（BelongsToMany）
	b := baseBuilder(t).Select("*").From("users").WhereHas(BelongsToMany{RelatedModel: Role{}}, nil)
	want := `select * from "users" where exists (select * from "roles" where exists (select * from "role_user" where "users"."id" = "role_user"."user_id" and "role_user"."role_id" = "roles"."id"))`
	if got := b.ToSql(); got != want {
		t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
	}
}

func TestWhereHasHasManyThroughGolden(t *testing.T) {
	b := baseBuilder(t).Select("*").From("users").WhereHas(HasManyThrough{
		ThroughModel: ThroughPost{},
		TargetModel:  ThroughComment{},
	}, nil)
	want := `select * from "users" where exists (select * from "comments" where exists (select * from "posts" where "posts"."id" = "comments"."post_id" and "users"."id" = "posts"."user_id"))`
	if got := b.ToSql(); got != want {
		t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
	}
}

func TestWhereHasMorphManyGolden(t *testing.T) {
	b := baseBuilder(t).Select("*").From("posts").WhereHas(MorphMany{MorphName: "commentable", TargetModel: MorphComment{}}, func(nb *Builder) {
		nb.Where("body", "=", "hi")
	})
	want := `select * from "posts" where exists (select * from "comments" where "posts"."id" = "comments"."commentable_id" and "body" = ? and "commentable_type" = ?)`
	if got := b.ToSql(); got != want {
		t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
	}
	bindings := b.GetBindings()
	if len(bindings) != 2 || bindings[0] != "hi" || bindings[1] != "posts" {
		t.Errorf("bindings = %#v", bindings)
	}
}

func TestWhereHasMorphToNoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("whereHas(morphTo) panicked: %v", r)
		}
	}()
	b := baseBuilder(t).Select("*").From("posts").WhereHas(MorphTo{MorphName: "subject"}, nil)
	want := `select * from "posts"`
	if got := b.ToSql(); got != want {
		t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
	}
}

func TestOrHasDoesntHaveGolden(t *testing.T) {
	b := baseBuilder(t).Select("*").From("users").Where("active", "=", 1).OrHas(HasMany{Model: PostOrdered{}})
	want := `select * from "users" where "active" = ? or exists (select * from "posts" where "users"."id" = "posts"."user_id")`
	if got := b.ToSql(); got != want {
		t.Errorf("orHas sql =\n  %s\nwant\n  %s", got, want)
	}

	d := baseBuilder(t).Select("*").From("users").DoesntHave(HasMany{Model: PostOrdered{}})
	wantD := `select * from "users" where not exists (select * from "posts" where "users"."id" = "posts"."user_id")`
	if got := d.ToSql(); got != wantD {
		t.Errorf("doesntHave sql =\n  %s\nwant\n  %s", got, wantD)
	}

	wd := baseBuilder(t).Select("*").From("users").WhereDoesntHave(HasMany{Model: PostOrdered{}}, func(nb *Builder) {
		nb.Where("title", "=", "x")
	})
	wantWD := `select * from "users" where not exists (select * from "posts" where "users"."id" = "posts"."user_id" and "title" = ?)`
	if got := wd.ToSql(); got != wantWD {
		t.Errorf("whereDoesntHave sql =\n  %s\nwant\n  %s", got, wantWD)
	}
	if len(wd.GetBindings()) != 1 || wd.GetBindings()[0] != "x" {
		t.Errorf("bindings = %#v", wd.GetBindings())
	}

	owd := baseBuilder(t).Select("*").From("users").OrWhereDoesntHave(HasMany{Model: PostOrdered{}}, nil)
	if got := owd.ToSql(); got != `select * from "users" where not exists (select * from "posts" where "users"."id" = "posts"."user_id")` {
		t.Errorf("orWhereDoesntHave sql =\n  %s", got)
	}
}

func TestWhereHasMorphTypesGolden(t *testing.T) {
	b := baseBuilder(t).Select("*").From("posts").WhereHasMorph(
		MorphMany{MorphName: "commentable", TargetModel: MorphComment{}},
		[]string{"users", "videos"},
		func(nb *Builder) { nb.Where("body", "=", "hi") },
	)
	want := `select * from "posts" where exists (select * from "comments" where "posts"."id" = "comments"."commentable_id" and "body" = ? and "commentable_type" in (?, ?))`
	if got := b.ToSql(); got != want {
		t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
	}
	bindings := b.GetBindings()
	if len(bindings) != 3 || bindings[1] != "users" || bindings[2] != "videos" {
		t.Errorf("bindings = %#v", bindings)
	}
}

func TestWhereMorphedToGolden(t *testing.T) {
	// 类零值：仅匹配类型列
	b := baseBuilder(t).Select("*").From("comments").WhereMorphedTo("commentable", MorphVideo{})
	want := `select * from "comments" where "commentable_type" = ?`
	if got := b.ToSql(); got != want {
		t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
	}
	// 已持久化实例：类型 + id
	b2 := baseBuilder(t).Select("*").From("comments").WhereMorphedTo("commentable", &MorphVideo{ID: 7})
	want2 := `select * from "comments" where "commentable_type" = ? and "commentable_id" = ?`
	if got := b2.ToSql(); got != want2 {
		t.Errorf("sql =\n  %s\nwant\n  %s", got, want2)
	}
	if len(b2.GetBindings()) != 2 || b2.GetBindings()[1] != int64(7) {
		t.Errorf("bindings = %#v", b2.GetBindings())
	}
	// morph map 别名优先
	SetMorphMap(map[string]any{"video": &MorphVideo{}})
	defer ResetMorphMap()
	b3 := baseBuilder(t).Select("*").From("comments").WhereMorphedTo("commentable", MorphVideo{})
	if got := b3.ToSql(); got != `select * from "comments" where "commentable_type" = ?` {
		t.Errorf("sql =\n  %s", got)
	}
	if b3.GetBindings()[0] != "video" {
		t.Errorf("morph key = %#v", b3.GetBindings()[0])
	}
}

func TestWhereBelongsToGolden(t *testing.T) {
	parent := &UserWithPosts{ID: 3, Name: "U"}
	b := baseBuilder(t).Select("*").From("posts").WhereBelongsTo(parent)
	want := `select * from "posts" where "user_id" = ?`
	if got := b.ToSql(); got != want {
		t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
	}
	if b.GetBindings()[0] != int64(3) {
		t.Errorf("binding = %#v", b.GetBindings()[0])
	}
	// 显式外键
	b2 := baseBuilder(t).Select("*").From("posts").WhereBelongsTo(parent, "owner_id")
	if got := b2.ToSql(); got != `select * from "posts" where "owner_id" = ?` {
		t.Errorf("sql =\n  %s", got)
	}
}

// ---- WithCount 聚合族（QueriesRelationships.php:850-1053）----

func TestWithCountConstraintsGolden(t *testing.T) {
	b := baseBuilder(t).Select("*").From("users").WithCount("posts", HasMany{Model: PostOrdered{}}, func(nb *Builder) {
		nb.Where("status", "=", "published")
	})
	want := `select *, (select count(*) from "posts" where "posts"."user_id" = "users"."id" and "status" = ?) as "posts_count" from "users"`
	if got := b.ToSql(); got != want {
		t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
	}
	if len(b.GetBindings()) != 1 || b.GetBindings()[0] != "published" {
		t.Errorf("bindings = %#v", b.GetBindings())
	}
}

func TestWithCountMultipleAndNestedGolden(t *testing.T) {
	b := baseBuilder(t).Select("*").From("users").
		WithCount("posts", HasMany{Model: PostOrdered{}}).
		WithCount("comments", HasMany{Model: ThroughComment{}})
	want := `select *, (select count(*) from "posts" where "posts"."user_id" = "users"."id") as "posts_count", (select count(*) from "comments" where "comments"."user_id" = "users"."id") as "comments_count" from "users"`
	if got := b.ToSql(); got != want {
		t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
	}

	n := baseBuilder(t).Select("*").From("users").WithCount("posts.comments",
		HasMany{Model: PostOrdered{}}, HasMany{Model: ThroughComment{}})
	wantN := `select *, (select count(*) from "posts" where "posts"."user_id" = "users"."id" and exists (select * from "comments" where "comments"."post_id" = "posts"."id")) as "posts_comments_count" from "users"`
	if got := n.ToSql(); got != wantN {
		t.Errorf("nested sql =\n  %s\nwant\n  %s", got, wantN)
	}
}

func TestWithAggregateFamilyGolden(t *testing.T) {
	mk := baseBuilder(t).Select("*").From("users").WithMax("posts", HasMany{Model: PostOrdered{}}, "id")
	wantMk := `select *, (select max("id") from "posts" where "posts"."user_id" = "users"."id") as "posts_max_id" from "users"`
	if got := mk.ToSql(); got != wantMk {
		t.Errorf("max sql =\n  %s\nwant\n  %s", got, wantMk)
	}
	mn := baseBuilder(t).Select("*").From("users").WithMin("posts", HasMany{Model: PostOrdered{}}, "score")
	if got := mn.ToSql(); got != `select *, (select min("score") from "posts" where "posts"."user_id" = "users"."id") as "posts_min_score" from "users"` {
		t.Errorf("min sql =\n  %s", got)
	}
	sm := baseBuilder(t).Select("*").From("users").WithSum("posts", HasMany{Model: PostOrdered{}}, "score")
	if got := sm.ToSql(); got != `select *, (select sum("score") from "posts" where "posts"."user_id" = "users"."id") as "posts_sum_score" from "users"` {
		t.Errorf("sum sql =\n  %s", got)
	}
	av := baseBuilder(t).Select("*").From("users").WithAvg("posts", HasMany{Model: PostOrdered{}}, "score")
	if got := av.ToSql(); got != `select *, (select avg("score") from "posts" where "posts"."user_id" = "users"."id") as "posts_avg_score" from "users"` {
		t.Errorf("avg sql =\n  %s", got)
	}
	ex := baseBuilder(t).Select("*").From("users").WithExists("posts", HasMany{Model: PostOrdered{}})
	wantEx := `select *, (select exists(select * from "posts" where "posts"."user_id" = "users"."id")) as "posts_exists" from "users"`
	if got := ex.ToSql(); got != wantEx {
		t.Errorf("exists sql =\n  %s\nwant\n  %s", got, wantEx)
	}
	// 自定义 alias
	ca := baseBuilder(t).Select("*").From("users").WithMax("posts", HasMany{Model: PostOrdered{}}, "id", "best_post")
	if got := ca.ToSql(); got != `select *, (select max("id") from "posts" where "posts"."user_id" = "users"."id") as "best_post" from "users"` {
		t.Errorf("custom alias sql =\n  %s", got)
	}
}

// ---- 惰性加载 ----

// 关系写入测试模型（时间戳为 time.Time 以走完整 Create 生命周期）
type RelationPost struct {
	ID        int64     `torm:"primary_key;column:id"`
	Title     string    `torm:"column:title"`
	UserID    int64     `torm:"column:user_id"`
	CreatedAt time.Time `torm:"column:created_at"`
	UpdatedAt time.Time `torm:"column:updated_at"`
}

func (p *RelationPost) TableName() string { return "posts" }

func TestLazyLoadRelation(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `posts` where `id` = ? limit 1", &fakeResult{
		Columns: []string{"id", "title", "user_id"},
		Rows:    [][]driver.Value{{int64(10), "p1", int64(1)}},
	})
	// Load 的 owner 查询为普通 where（无 limit）
	st.On("select * from `users` where `id` = ?", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:    [][]driver.Value{{int64(1), "A"}},
	})

	post := &PostWithUser{ID: 10}
	if err := conn.Table("posts").Where("id", "=", 10).First(post); err != nil {
		t.Fatal(err)
	}
	if RelationLoaded(post, "author") {
		t.Error("should not be loaded yet")
	}
	if err := conn.Load(post, "author", BelongsTo{Model: &UserWithPosts{}}); err != nil {
		t.Fatal(err)
	}
	if !RelationLoaded(post, "author") {
		t.Error("loaded relation flag missing")
	}
	results, ok := LoadedRelation(post, "author")
	if !ok || len(results) != 1 {
		t.Fatalf("loaded = %#v ok=%v", results, ok)
	}
	author, isUser := results[0].(*UserWithPosts)
	if !isUser || author.Name != "A" {
		t.Errorf("author = %#v", results[0])
	}
	// 关系字段同步回填
	if post.Author == nil || post.Author.Name != "A" {
		t.Errorf("field fill = %+v", post.Author)
	}
}

func TestLazyLoadHasMany(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `users` where `id` = ? limit 1", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:    [][]driver.Value{{int64(1), "A"}},
	})
	st.On("select * from `posts` where `user_id` in (?)", &fakeResult{
		Columns: []string{"id", "title", "user_id"},
		Rows: [][]driver.Value{
			{int64(10), "p1", int64(1)},
			{int64(11), "p2", int64(1)},
		},
	})

	user := &UserWithPosts{ID: 1}
	if err := conn.Table("users").Where("id", "=", 1).First(user); err != nil {
		t.Fatal(err)
	}
	if err := conn.Load(user, "posts", HasMany{Model: PostWithUser{}}); err != nil {
		t.Fatal(err)
	}
	results, _ := LoadedRelation(user, "posts")
	if len(results) != 2 {
		t.Fatalf("loaded = %d", len(results))
	}
	if p, ok := results[0].(*PostWithUser); !ok || p.Title != "p1" {
		t.Errorf("result0 = %#v", results[0])
	}
	if len(user.Posts) != 2 || user.Posts[1].Title != "p2" {
		t.Errorf("field fill = %+v", user.Posts)
	}
}

func TestRelationGetResults(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `posts` where `user_id` in (?)", &fakeResult{
		Columns: []string{"id", "title", "user_id"},
		Rows:    [][]driver.Value{{int64(10), "p1", int64(1)}},
	})

	user := &UserWithPosts{ID: 1}
	results, err := (HasMany{Model: PostWithUser{}}).GetResults(user, conn)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("related = %d", len(results))
	}
	if p, ok := results[0].(*PostWithUser); !ok || p.Title != "p1" {
		t.Errorf("result0 = %#v", results[0])
	}

	// BelongsTo GetResults
	st.On("select * from `users` where `id` = ?", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:    [][]driver.Value{{int64(1), "A"}},
	})
	post := &PostWithUser{ID: 10, UserID: 1}
	owners, err := (BelongsTo{Model: &UserWithPosts{}}).GetResults(post, conn)
	if err != nil || len(owners) != 1 {
		t.Fatalf("belongsTo results = %#v err=%v", owners, err)
	}
	if u, ok := owners[0].(*UserWithPosts); !ok || u.Name != "A" {
		t.Errorf("owner = %#v", owners[0])
	}
}

// ---- associate / dissociate ----

func TestAssociateDissociate(t *testing.T) {
	user := &UserWithPosts{ID: 3, Name: "U"}
	post := &PostWithUser{ID: 10, Title: "p"}

	// 不自动 save：只写属性
	if err := Associate(post, user); err != nil {
		t.Fatal(err)
	}
	if post.UserID != 3 {
		t.Errorf("associate fk = %d", post.UserID)
	}

	if err := Dissociate(post, "user_id"); err != nil {
		t.Fatal(err)
	}
	if post.UserID != 0 {
		t.Errorf("dissociate fk = %d", post.UserID)
	}

	// 未注册主键的 owner 报错
	if err := Associate(post, &UserWithPosts{}); err == nil {
		t.Error("blank owner key should error")
	}
}

// 多态 associate 模型（morph 侧复用 MorphPost）
func TestMorphToAssociateDissociate(t *testing.T) {
	video := &MorphVideo{ID: 7, Name: "v"}
	post := &MorphPost{ID: 1}

	if err := MorphToAssociate(post, video, "subject"); err != nil {
		t.Fatal(err)
	}
	if post.SubjectID != 7 || post.SubjectTyp != "videos" {
		t.Errorf("morphTo associate = (%v, %s)", post.SubjectID, post.SubjectTyp)
	}

	// morph map 别名优先于表名
	SetMorphMap(map[string]any{"video": &MorphVideo{}})
	defer ResetMorphMap()
	if err := MorphToAssociate(post, video, "subject"); err != nil {
		t.Fatal(err)
	}
	if post.SubjectTyp != "video" {
		t.Errorf("morph alias = %s", post.SubjectTyp)
	}

	if err := MorphToDissociate(post, "subject"); err != nil {
		t.Fatal(err)
	}
	if post.SubjectID != 0 || post.SubjectTyp != "" {
		t.Errorf("morphTo dissociate = (%v, %s)", post.SubjectID, post.SubjectTyp)
	}
}

// ---- 关系级写方法 ----

func TestRelationCreate(t *testing.T) {
	conn, st := newFakeDB(t)
	insertSQL := "insert into `posts` (`created_at`, `title`, `updated_at`, `user_id`) values (?, ?, ?, ?)"
	st.On(insertSQL, &fakeResult{Affected: 1, LastID: 99})

	parent := &UserWithPosts{ID: 1, Name: "U"}
	created, err := conn.RelationCreate(parent, HasMany{Model: RelationPost{}}, map[string]any{"title": "new"})
	if err != nil {
		t.Fatal(err)
	}
	p := created.(*RelationPost)
	if p.Title != "new" || p.UserID != 1 {
		t.Errorf("created = %+v", p)
	}
	if p.ID != 99 {
		t.Errorf("id backfill = %d", p.ID)
	}
	call := st.LastCall(t, insertSQL)
	if len(call.Args) != 4 || call.Args[3] != int64(1) {
		t.Errorf("insert args = %#v", call.Args)
	}
}

func TestRelationCreateMorph(t *testing.T) {
	conn, st := newFakeDB(t)
	insertSQL := "insert into `comments` (`body`, `commentable_id`, `commentable_type`) values (?, ?, ?)"
	st.On(insertSQL, &fakeResult{Affected: 1, LastID: 1})

	parent := &MorphPost{ID: 5}
	created, err := conn.RelationCreate(parent, MorphMany{MorphName: "commentable", TargetModel: MorphComment{}}, map[string]any{"body": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	c := created.(*MorphComment)
	if c.CommentableID != 5 || c.CommentableTyp != "posts" {
		t.Errorf("morph fk = (%v, %s)", c.CommentableID, c.CommentableTyp)
	}
	st.LastCall(t, insertSQL)
}

func TestRelationSave(t *testing.T) {
	conn, st := newFakeDB(t)
	insertSQL := "insert into `posts` (`created_at`, `title`, `updated_at`, `user_id`) values (?, ?, ?, ?)"
	st.On(insertSQL, &fakeResult{Affected: 1, LastID: 3})

	parent := &UserWithPosts{ID: 1}
	related := &RelationPost{Title: "saved"}
	if err := conn.RelationSave(parent, HasMany{Model: RelationPost{}}, related); err != nil {
		t.Fatal(err)
	}
	if related.UserID != 1 || related.ID != 3 {
		t.Errorf("saved = %+v", related)
	}
	st.LastCall(t, insertSQL)
}

func TestRelationFirstOrCreate(t *testing.T) {
	conn, st := newFakeDB(t)
	selectSQL := "select * from `posts` where `title` = ? and `user_id` = ? limit 1"
	st.On(selectSQL, &fakeResult{
		Columns: []string{"id", "title", "user_id", "created_at"},
		Rows:    [][]driver.Value{{int64(8), "found", int64(1), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}},
	})

	parent := &UserWithPosts{ID: 1}
	inst, err := conn.RelationFirstOrCreate(parent, HasMany{Model: RelationPost{}}, map[string]any{"title": "found"})
	if err != nil {
		t.Fatal(err)
	}
	p := inst.(*RelationPost)
	if p.ID != 8 || p.Title != "found" {
		t.Errorf("firstOrCreate = %+v", p)
	}
	st.LastCall(t, selectSQL)
}

func TestRelationUpdateOrCreate(t *testing.T) {
	conn, st := newFakeDB(t)
	selectSQL := "select * from `posts` where `title` = ? and `user_id` = ? limit 1"
	st.On(selectSQL, &fakeResult{
		Columns: []string{"id", "title", "user_id", "created_at"},
		Rows:    [][]driver.Value{{int64(8), "old", int64(1), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}},
	})
	updateSQL := "update `posts` set `title` = ?, `updated_at` = ? where `id` = ?"
	st.On(updateSQL, &fakeResult{Affected: 1})

	parent := &UserWithPosts{ID: 1}
	inst, err := conn.RelationUpdateOrCreate(parent, HasMany{Model: RelationPost{}},
		map[string]any{"title": "old"}, map[string]any{"title": "updated"})
	if err != nil {
		t.Fatal(err)
	}
	p := inst.(*RelationPost)
	if p.ID != 8 || p.Title != "updated" {
		t.Errorf("updateOrCreate = %+v", p)
	}
	st.LastCall(t, updateSQL)
}

// ---- BelongsToMany 读侧 pivot 增强 ----

func TestBelongsToManyWithPivotGolden(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `users`", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:    [][]driver.Value{{int64(1), "A"}},
	})
	joinSQL := "select `roles`.*, `role_user`.`user_id` as `pivot_user_id`, `role_user`.`role_id` as `pivot_role_id`, `role_user`.`status` as `pivot_status`, `role_user`.`created_at` as `pivot_created_at`, `role_user`.`updated_at` as `pivot_updated_at` from `roles` inner join `role_user` on `roles`.`id` = `role_user`.`role_id` where `role_user`.`user_id` in (?) and `role_user`.`status` = ? order by `role_user`.`name` desc"
	st.On(joinSQL, &fakeResult{
		Columns: []string{"id", "name", "pivot_user_id", "pivot_role_id", "pivot_status", "pivot_created_at", "pivot_updated_at"},
		Rows: [][]driver.Value{
			{int64(7), "admin", int64(1), int64(7), "on", "2026-01-01", "2026-01-02"},
		},
	})

	var users []UserWithRoles
	rel := BelongsToMany{RelatedModel: Role{}}.
		WithPivot("role_id", "status").
		WithTimestamps().
		WherePivot("status", "=", "on").
		OrderByPivot("name", "desc")
	if err := conn.Table("users").With("roles", rel).Get(&users); err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || len(users[0].Roles) != 1 {
		t.Fatalf("roles = %+v", users)
	}
	call := st.LastCall(t, joinSQL)
	if len(call.Args) != 2 || call.Args[0] != int64(1) || call.Args[1] != "on" {
		t.Errorf("args = %#v", call.Args)
	}
}

func TestBelongsToManyOrWherePivotGolden(t *testing.T) {
	rel := BelongsToMany{RelatedModel: Role{}}.WherePivot("status", "=", "on").OrWherePivot("priority", ">", 5)
	conn, st := newFakeDB(t)
	st.On("select * from `users`", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:    [][]driver.Value{{int64(1), "A"}},
	})
	joinSQL := "select `roles`.*, `role_user`.`user_id` as `pivot_user_id` from `roles` inner join `role_user` on `roles`.`id` = `role_user`.`role_id` where (`role_user`.`user_id` in (?) and `role_user`.`status` = ? or `role_user`.`priority` > ?)"
	st.On(joinSQL, &fakeResult{
		Columns: []string{"id", "name", "pivot_user_id"},
		Rows:    [][]driver.Value{},
	})
	var users []UserWithRoles
	if err := conn.Table("users").With("roles", rel).Get(&users); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, joinSQL)
}

func TestBelongsToManyWherePivotInGolden(t *testing.T) {
	rel := BelongsToMany{RelatedModel: Role{}}.WherePivotIn("status", []any{"on", "trial"})
	conn, st := newFakeDB(t)
	st.On("select * from `users`", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:    [][]driver.Value{{int64(1), "A"}},
	})
	joinSQL := "select `roles`.*, `role_user`.`user_id` as `pivot_user_id` from `roles` inner join `role_user` on `roles`.`id` = `role_user`.`role_id` where `role_user`.`user_id` in (?) and `role_user`.`status` in (?, ?)"
	st.On(joinSQL, &fakeResult{
		Columns: []string{"id", "name", "pivot_user_id"},
		Rows:    [][]driver.Value{},
	})
	var users []UserWithRoles
	if err := conn.Table("users").With("roles", rel).Get(&users); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, joinSQL)
}

// ---- BelongsToMany 写侧 ----

func TestAttachAndDetach(t *testing.T) {
	conn, st := newFakeDB(t)
	insertSQL := "insert into `role_user` (`user_id`, `role_id`) values (?, ?)"
	st.On(insertSQL, &fakeResult{Affected: 1})
	st.On("insert into `role_user` (`user_id`, `role_id`, `status`) values (?, ?, ?)", &fakeResult{Affected: 1})
	deleteSQL := "delete from `role_user` where `user_id` = ? and `role_id` in (?)"
	st.On(deleteSQL, &fakeResult{Affected: 1})

	user := &UserWithRoles{ID: 1}
	if err := conn.Attach(user, BelongsToMany{RelatedModel: Role{}}, []any{int64(7), int64(8)}, nil); err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for _, call := range st.Calls() {
		if call.Sql != insertSQL {
			continue
		}
		if call.Args[0] != int64(1) {
			t.Errorf("attach args = %#v", call.Args)
		}
		seen[call.Args[1].(int64)] = true
	}
	if !seen[7] || !seen[8] {
		t.Errorf("attached ids missing: %v", seen)
	}

	// 带 pivot 附加属性
	if err := conn.Attach(user, BelongsToMany{RelatedModel: Role{}}, []any{int64(9)}, map[string]any{"status": "on"}); err != nil {
		t.Fatal(err)
	}
	ac := st.LastCall(t, "insert into `role_user` (`user_id`, `role_id`, `status`) values (?, ?, ?)")
	if ac.Args[0] != int64(1) || ac.Args[1] != int64(9) || ac.Args[2] != "on" {
		t.Errorf("attach attrs = %#v", ac.Args)
	}

	n, err := conn.Detach(user, BelongsToMany{RelatedModel: Role{}}, int64(8))
	if err != nil || n != 1 {
		t.Errorf("detach = %d err=%v", n, err)
	}
	st.LastCall(t, deleteSQL)

	// 全量 detach（无 ids）
	allSQL := "delete from `role_user` where `user_id` = ?"
	st.On(allSQL, &fakeResult{Affected: 2})
	if _, err := conn.Detach(user, BelongsToMany{RelatedModel: Role{}}); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, allSQL)
}

func TestAttachWithTimestamps(t *testing.T) {
	conn, st := newFakeDB(t)
	insertSQL := "insert into `role_user` (`user_id`, `role_id`, `created_at`, `updated_at`) values (?, ?, ?, ?)"
	st.On(insertSQL, &fakeResult{Affected: 1})

	rel := BelongsToMany{RelatedModel: Role{}}.WithTimestamps()
	if err := conn.Attach(&UserWithRoles{ID: 1}, rel, []any{int64(7)}, nil); err != nil {
		t.Fatal(err)
	}
	call := st.LastCall(t, insertSQL)
	if len(call.Args) != 4 {
		t.Errorf("args = %#v", call.Args)
	}
}

func TestSyncPivot(t *testing.T) {
	conn, st := newFakeDB(t)
	selectSQL := "select `role_id` from `role_user` where `user_id` = ?"
	st.On(selectSQL, &fakeResult{
		Columns: []string{"role_id"},
		Rows:    [][]driver.Value{{int64(2)}, {int64(3)}},
	})
	attachSQL := "insert into `role_user` (`user_id`, `role_id`) values (?, ?)"
	st.On(attachSQL, &fakeResult{Affected: 1})
	st.On("insert into `role_user` (`user_id`, `role_id`, `status`) values (?, ?, ?)", &fakeResult{Affected: 1})
	detachSQL := "delete from `role_user` where `user_id` = ? and `role_id` in (?)"
	st.On(detachSQL, &fakeResult{Affected: 1})
	updateSQL := "update `role_user` set `status` = ? where `user_id` = ? and `role_id` = ?"
	st.On(updateSQL, &fakeResult{Affected: 1})

	user := &UserWithRoles{ID: 1}
	res, err := conn.Sync(user, BelongsToMany{RelatedModel: Role{}}, []any{int64(1), int64(2)}, map[string]any{"status": "on"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Attached) != 1 || res.Attached[0] != int64(1) {
		t.Errorf("attached = %#v", res.Attached)
	}
	if len(res.Detached) != 1 || res.Detached[0] != int64(3) {
		t.Errorf("detached = %#v", res.Detached)
	}
	// id=2 现存 → 更新
	if len(res.Updated) != 1 || res.Updated[0] != int64(2) {
		t.Errorf("updated = %#v", res.Updated)
	}
	st.LastCall(t, updateSQL)

	// SyncWithoutDetaching：不解除
	st.On(selectSQL, &fakeResult{
		Columns: []string{"role_id"},
		Rows:    [][]driver.Value{{int64(2)}},
	})
	res2, err := conn.SyncWithoutDetaching(user, BelongsToMany{RelatedModel: Role{}}, []any{int64(2), int64(5)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Detached) != 0 || len(res2.Attached) != 1 {
		t.Errorf("syncWithoutDetaching = %#v", res2)
	}
}

func TestTogglePivot(t *testing.T) {
	conn, st := newFakeDB(t)
	selectSQL := "select `role_id` from `role_user` where `user_id` = ?"
	st.On(selectSQL, &fakeResult{
		Columns: []string{"role_id"},
		Rows:    [][]driver.Value{{int64(2)}},
	})
	attachSQL := "insert into `role_user` (`user_id`, `role_id`) values (?, ?)"
	st.On(attachSQL, &fakeResult{Affected: 1})
	detachSQL := "delete from `role_user` where `user_id` = ? and `role_id` in (?)"
	st.On(detachSQL, &fakeResult{Affected: 1})

	user := &UserWithRoles{ID: 1}
	res, err := conn.Toggle(user, BelongsToMany{RelatedModel: Role{}}, []any{int64(2), int64(5)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Detached) != 1 || res.Detached[0] != int64(2) {
		t.Errorf("detached = %#v", res.Detached)
	}
	if len(res.Attached) != 1 || res.Attached[0] != int64(5) {
		t.Errorf("attached = %#v", res.Attached)
	}
}

func TestUpdateExistingPivot(t *testing.T) {
	conn, st := newFakeDB(t)
	updateSQL := "update `role_user` set `status` = ? where `user_id` = ? and `role_id` = ?"
	st.On(updateSQL, &fakeResult{Affected: 1})

	n, err := conn.UpdateExistingPivot(&UserWithRoles{ID: 1}, BelongsToMany{RelatedModel: Role{}}, int64(2), map[string]any{"status": "off"})
	if err != nil || n != 1 {
		t.Fatalf("update = %d err=%v", n, err)
	}
	call := st.LastCall(t, updateSQL)
	if call.Args[0] != "off" || call.Args[1] != int64(1) || call.Args[2] != int64(2) {
		t.Errorf("args = %#v", call.Args)
	}
}

// ---- hasManyThrough 中间表软删 ----

type ThroughSoftPost struct {
	ID        int64      `torm:"primary_key;column:id"`
	UserID    int64      `torm:"column:user_id"`
	DeletedAt *time.Time `torm:"column:deleted_at"`
}

func (p *ThroughSoftPost) TableName() string { return "posts" }

func TestHasManyThroughSoftDeletedThrough(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `users`", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:    [][]driver.Value{{int64(1), "A"}},
	})
	// 中间模型带 deleted_at → join 条件追加 whereNull
	joinSQL := "select `comments`.*, `posts`.`user_id` as `laravel_through_key` from `comments` inner join `posts` on `posts`.`id` = `comments`.`post_id` where `posts`.`user_id` in (?) and `posts`.`deleted_at` is null"
	st.On(joinSQL, &fakeResult{
		Columns: []string{"id", "body", "post_id", "laravel_through_key"},
		Rows:    [][]driver.Value{},
	})

	var users []UserWithComments
	if err := conn.Table("users").With("comments", HasManyThrough{
		ThroughModel: ThroughSoftPost{},
		TargetModel:  ThroughComment{},
	}).Get(&users); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, joinSQL)

	// WithTrashedParents 跳过软删过滤
	joinSQL2 := "select `comments`.*, `posts`.`user_id` as `laravel_through_key` from `comments` inner join `posts` on `posts`.`id` = `comments`.`post_id` where `posts`.`user_id` in (?)"
	st.On(joinSQL2, &fakeResult{
		Columns: []string{"id", "body", "post_id", "laravel_through_key"},
		Rows:    [][]driver.Value{},
	})
	if err := conn.Table("users").With("comments", HasManyThrough{
		ThroughModel: ThroughSoftPost{},
		TargetModel:  ThroughComment{},
	}.WithTrashed()).Get(&users); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, joinSQL2)
}

// ---- HasOneThrough（远程一对一）----

// 单值字段的远程一对一模型：User HasOneThrough LatestComment（经 Post）
type UserWithOneThrough struct {
	ID            int64           `torm:"primary_key;column:id"`
	Name          string          `torm:"column:name"`
	LatestComment *ThroughComment `torm:"-"`
}

func (u *UserWithOneThrough) TableName() string { return "users" }

func TestHasOneThroughGetResults(t *testing.T) {
	conn, st := newFakeDB(t)
	joinSQL := "select `comments`.*, `posts`.`user_id` as `laravel_through_key` from `comments` inner join `posts` on `posts`.`id` = `comments`.`post_id` where `posts`.`user_id` in (?)"
	st.On(joinSQL, &fakeResult{
		Columns: []string{"id", "body", "post_id", "laravel_through_key"},
		Rows: [][]driver.Value{
			{int64(100), "c1", int64(5), int64(1)},
			{int64(101), "c2", int64(6), int64(1)},
		},
	})

	user := &UserWithComments{ID: 1}
	results, err := (HasOneThrough{ThroughModel: ThroughPost{}, TargetModel: ThroughComment{}}).GetResults(user, conn)
	if err != nil {
		t.Fatal(err)
	}
	// 只取首条
	if len(results) != 1 {
		t.Fatalf("results = %d", len(results))
	}
	if c, ok := results[0].(*ThroughComment); !ok || c.Body != "c1" {
		t.Errorf("result0 = %#v", results[0])
	}

	// 父键为空：无结果
	blank, err := (HasOneThrough{ThroughModel: ThroughPost{}, TargetModel: ThroughComment{}}).GetResults(&UserWithComments{}, conn)
	if err != nil || len(blank) != 0 {
		t.Errorf("blank parent = %#v err=%v", blank, err)
	}
}

func TestHasOneThroughEagerLoad(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `users`", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:    [][]driver.Value{{int64(1), "A"}, {int64(2), "B"}},
	})
	joinSQL := "select `comments`.*, `posts`.`user_id` as `laravel_through_key` from `comments` inner join `posts` on `posts`.`id` = `comments`.`post_id` where `posts`.`user_id` in (?, ?)"
	st.On(joinSQL, &fakeResult{
		Columns: []string{"id", "body", "post_id", "laravel_through_key"},
		Rows: [][]driver.Value{
			{int64(100), "c1", int64(5), int64(1)},
			{int64(101), "c2", int64(6), int64(1)},
			{int64(102), "c3", int64(7), int64(2)},
		},
	})

	var users []UserWithOneThrough
	if err := conn.Table("users").With("latestComment", HasOneThrough{
		ThroughModel: ThroughPost{},
		TargetModel:  ThroughComment{},
	}).Get(&users); err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 {
		t.Fatalf("users = %d", len(users))
	}
	if users[0].LatestComment == nil || users[0].LatestComment.Body != "c1" {
		t.Errorf("user1 latestComment = %+v", users[0].LatestComment)
	}
	if users[1].LatestComment == nil || users[1].LatestComment.Body != "c3" {
		t.Errorf("user2 latestComment = %+v", users[1].LatestComment)
	}
	st.LastCall(t, joinSQL)
}

func TestWhereHasHasOneThroughGolden(t *testing.T) {
	b := baseBuilder(t).Select("*").From("users").WhereHas(HasOneThrough{
		ThroughModel: ThroughPost{},
		TargetModel:  ThroughComment{},
	}, nil)
	want := `select * from "users" where exists (select * from "comments" where exists (select * from "posts" where "posts"."id" = "comments"."post_id" and "users"."id" = "posts"."user_id"))`
	if got := b.ToSql(); got != want {
		t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
	}
}

// ---- HasOne/MorphOne one-of-many（latestOfMany/oldestOfMany/ofMany/one）----

type UserWithLatestPost struct {
	ID         int64        `torm:"primary_key;column:id"`
	Name       string       `torm:"column:name"`
	LatestPost *PostOrdered `torm:"-"`
}

func (u *UserWithLatestPost) TableName() string { return "users" }

func TestHasOneLatestOfManyGetResults(t *testing.T) {
	conn, st := newFakeDB(t)
	latestSQL := "select * from `posts` where `user_id` in (?) order by `created_at` desc, `id` desc limit 1"
	st.On(latestSQL, &fakeResult{
		Columns: []string{"id", "title", "user_id", "created_at"},
		Rows:    [][]driver.Value{{int64(12), "p3", int64(1), "2026-01-03"}},
	})

	user := &UserWithLatestPost{ID: 1}
	results, err := (HasOne{Model: PostOrdered{}}.LatestOfMany()).GetResults(user, conn)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d", len(results))
	}
	if p, ok := results[0].(*PostOrdered); !ok || p.Title != "p3" {
		t.Errorf("result0 = %#v", results[0])
	}
	st.LastCall(t, latestSQL)

	// oldestOfMany → asc
	oldestSQL := "select * from `posts` where `user_id` in (?) order by `created_at` asc, `id` asc limit 1"
	st.On(oldestSQL, &fakeResult{
		Columns: []string{"id", "title", "user_id", "created_at"},
		Rows:    [][]driver.Value{{int64(10), "p1", int64(1), "2026-01-01"}},
	})
	if _, err = (HasOne{Model: PostOrdered{}}.OldestOfMany()).GetResults(user, conn); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, oldestSQL)

	// 自定义 ofMany(column, aggregate)
	ofManySQL := "select * from `posts` where `user_id` in (?) order by `id` asc limit 1"
	st.On(ofManySQL, &fakeResult{
		Columns: []string{"id", "title", "user_id", "created_at"},
		Rows:    [][]driver.Value{{int64(10), "p1", int64(1), "2026-01-01"}},
	})
	if _, err = (HasOne{Model: PostOrdered{}}.OfMany("id", "min")).GetResults(user, conn); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, ofManySQL)
}

func TestHasOneLatestOfManyEagerLoad(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `users`", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:    [][]driver.Value{{int64(1), "A"}},
	})
	// one-of-many 预加载经 GroupLimit（row_number 分组取极值，对齐 Laravel）
	groupSQL := "select * from (select *, row_number() over (partition by `user_id` order by `created_at` desc, `id` desc) as `laravel_row` from `posts` where `user_id` in (?)) as `laravel_table` where `laravel_row` <= 1 order by `laravel_row`"
	st.On(groupSQL, &fakeResult{
		Columns: []string{"id", "title", "user_id", "created_at", "laravel_row"},
		Rows:    [][]driver.Value{{int64(12), "p3", int64(1), "2026-01-03", int64(1)}},
	})

	var users []UserWithLatestPost
	if err := conn.Table("users").With("latestPost", HasOne{Model: PostOrdered{}}.LatestOfMany()).Get(&users); err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].LatestPost == nil || users[0].LatestPost.Title != "p3" {
		t.Errorf("latestPost = %+v", users[0].LatestPost)
	}
	st.LastCall(t, groupSQL)
}

func TestMorphOneLatestOfManyGetResults(t *testing.T) {
	conn, st := newFakeDB(t)
	latestSQL := "select * from `comments` where `commentable_type` = ? and `commentable_id` in (?) order by `created_at` desc, `id` desc limit 1"
	st.On(latestSQL, &fakeResult{
		Columns: []string{"id", "body", "commentable_id", "commentable_type"},
		Rows:    [][]driver.Value{{int64(9), "latest", int64(1), "posts"}},
	})

	post := &MorphPost{ID: 1}
	results, err := (MorphOne{MorphName: "commentable", TargetModel: MorphComment{}}.LatestOfMany()).GetResults(post, conn)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d", len(results))
	}
	if c, ok := results[0].(*MorphComment); !ok || c.Body != "latest" {
		t.Errorf("result0 = %#v", results[0])
	}
	st.LastCall(t, latestSQL)

	// one()（partial）：只取单行
	partialSQL := "select * from `comments` where `commentable_type` = ? and `commentable_id` in (?) limit 1"
	st.On(partialSQL, &fakeResult{
		Columns: []string{"id", "body", "commentable_id", "commentable_type"},
		Rows:    [][]driver.Value{{int64(9), "first", int64(1), "posts"}},
	})
	if _, err = (MorphOne{MorphName: "commentable", TargetModel: MorphComment{}}.One()).GetResults(post, conn); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, partialSQL)
}

// ---- touches 级联（touchOwners）----

// touches 声明挂在 post_id 字段的 tag 上（tag 属性以 key:value 形式解析）
type TouchComment struct {
	ID     int64  `torm:"primary_key;column:id"`
	Body   string `torm:"column:body"`
	PostID int64  `torm:"column:post_id;touches:post"`
}

func (c *TouchComment) TableName() string { return "comments" }

func TestTouchesTagParsing(t *testing.T) {
	c := &TouchComment{ID: 1, PostID: 5}
	if !Touches(c, "post") {
		t.Error("touches(post) should be declared")
	}
	if Touches(c, "user") {
		t.Error("touches(user) should not be declared")
	}
	if names := modelTouchesRelations(c); len(names) != 1 || names[0] != "post" {
		t.Errorf("names = %#v", names)
	}
}

func TestTouchOwnersFallbackForeignKeyLookup(t *testing.T) {
	conn, st := newFakeDB(t)
	// 未加载关系：按 belongsTo 键约定反查（父表 posts，外键 post_id）
	touchSQL := "update `posts` set `updated_at` = ? where `id` = ?"
	st.On(touchSQL, &fakeResult{Affected: 1})

	c := &TouchComment{ID: 1, PostID: 5}
	if err := conn.TouchOwners(c); err != nil {
		t.Fatal(err)
	}
	call := st.LastCall(t, touchSQL)
	if len(call.Args) != 2 || call.Args[1] != int64(5) {
		t.Errorf("touch args = %#v", call.Args)
	}
}

func TestTouchOwnersLoadedRelation(t *testing.T) {
	conn, st := newFakeDB(t)
	touchSQL := "update `posts` set `updated_at` = ? where `id` = ?"
	st.On(touchSQL, &fakeResult{Affected: 1})

	c := &TouchComment{ID: 1, PostID: 5}
	parent := &RelationPost{ID: 5, Title: "p"}
	setExists(parent, true, false)
	syncOriginal(parent)
	SetLoadedRelation(c, "post", []any{parent})
	defer ForgetLoadedRelations(c)

	if err := conn.TouchOwners(c); err != nil {
		t.Fatal(err)
	}
	call := st.LastCall(t, touchSQL)
	if len(call.Args) != 2 || call.Args[1] != int64(5) {
		t.Errorf("touch args = %#v", call.Args)
	}
	// 父模型 updated_at 已刷新并同步快照
	if parent.UpdatedAt.IsZero() {
		t.Error("parent updated_at not touched")
	}
}

// ---- 关系级 find / first / firstOrFail / firstOr / sole ----

func TestRelationFindAndFirstFamily(t *testing.T) {
	relSQL := "select * from `posts` where `user_id` in (?)"
	user := &UserWithPosts{ID: 1}
	rel := HasMany{Model: PostWithUser{}}

	t.Run("find", func(t *testing.T) {
		conn, st := newFakeDB(t)
		st.On(relSQL, &fakeResult{
			Columns: []string{"id", "title", "user_id"},
			Rows: [][]driver.Value{
				{int64(10), "p1", int64(1)},
				{int64(11), "p2", int64(1)},
			},
		})
		found, err := conn.RelationFind(user, rel, int64(11))
		if err != nil {
			t.Fatal(err)
		}
		if p, ok := found.(*PostWithUser); !ok || p.Title != "p2" {
			t.Errorf("find = %#v", found)
		}
		if missing, err := conn.RelationFind(user, rel, int64(99)); err != nil || missing != nil {
			t.Errorf("find missing = %#v err=%v", missing, err)
		}
	})

	t.Run("first", func(t *testing.T) {
		conn, st := newFakeDB(t)
		st.On(relSQL, &fakeResult{
			Columns: []string{"id", "title", "user_id"},
			Rows: [][]driver.Value{
				{int64(10), "p1", int64(1)},
				{int64(11), "p2", int64(1)},
			},
		})
		first, err := conn.RelationFirst(user, rel)
		if err != nil {
			t.Fatal(err)
		}
		if p, ok := first.(*PostWithUser); !ok || p.Title != "p1" {
			t.Errorf("first = %#v", first)
		}
		// first 带约束闭包（键形态关系做 SQL 下推）
		st.On("select * from `posts` where `user_id` in (?) and `title` = ?",
			&fakeResult{
				Columns: []string{"id", "title", "user_id"},
				Rows:    [][]driver.Value{{int64(11), "p2", int64(1)}},
			})
		constrained, err := conn.RelationFirst(user, rel, func(nb *Builder) { nb.Where("title", "=", "p2") })
		if err != nil {
			t.Fatal(err)
		}
		if p, ok := constrained.(*PostWithUser); !ok || p.Title != "p2" {
			t.Errorf("first constrained = %#v", constrained)
		}
	})

	t.Run("sole", func(t *testing.T) {
		conn, st := newFakeDB(t)
		// 桩按调用顺序消费：第一次 sole 得 2 条（ErrMultiple），第二次得 1 条
		st.On(relSQL, &fakeResult{
			Columns: []string{"id", "title", "user_id"},
			Rows: [][]driver.Value{
				{int64(10), "p1", int64(1)},
				{int64(11), "p2", int64(1)},
			},
		})
		st.On(relSQL, &fakeResult{
			Columns: []string{"id", "title", "user_id"},
			Rows:    [][]driver.Value{{int64(10), "p1", int64(1)}},
		})
		if _, err := conn.RelationSole(user, rel); err == nil {
			t.Error("sole with 2 results should error")
		}
		sole, err := conn.RelationSole(user, rel)
		if err != nil {
			t.Fatal(err)
		}
		if p, ok := sole.(*PostWithUser); !ok || p.Title != "p1" {
			t.Errorf("sole = %#v", sole)
		}
	})

	t.Run("firstOrFail", func(t *testing.T) {
		conn, st := newFakeDB(t)
		st.On(relSQL, &fakeResult{
			Columns: []string{"id", "title", "user_id"},
			Rows:    [][]driver.Value{},
		})
		defer func() {
			r := recover()
			fe, ok := r.(*FailError)
			if !ok {
				t.Fatalf("expect *FailError, got %#v", r)
			}
			if fe.Op != "firstOrFail" {
				t.Errorf("op = %s", fe.Op)
			}
			if _, isNotFound := fe.Unwrap().(ErrNotFound); !isNotFound {
				t.Errorf("unwrap = %#v", fe.Unwrap())
			}
		}()
		conn.RelationFirstOrFail(user, rel)
	})

	t.Run("firstOr", func(t *testing.T) {
		conn, st := newFakeDB(t)
		st.On(relSQL, &fakeResult{
			Columns: []string{"id", "title", "user_id"},
			Rows:    [][]driver.Value{},
		})
		got := false
		res, err := conn.RelationFirstOr(user, rel, func() (any, error) {
			got = true
			return "fallback", nil
		})
		if err != nil || !got || res != "fallback" {
			t.Errorf("firstOr = %#v err=%v got=%v", res, err, got)
		}
	})
}

// ---- withDefault（SupportsDefaultModels）----

func TestBelongsToWithDefault(t *testing.T) {
	// 外键为空：返回默认实例而非 nil
	post := &PostWithUser{ID: 10}
	conn, _ := newFakeDB(t)
	results, err := (BelongsTo{Model: &UserWithPosts{}}.WithDefault()).GetResults(post, conn)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d", len(results))
	}
	if u, ok := results[0].(*UserWithPosts); !ok || u.ID != 0 {
		t.Errorf("default = %#v", results[0])
	}

	// 带回调：定制默认实例
	post2 := &PostWithUser{ID: 10}
	conn2, _ := newFakeDB(t)
	results2, err := (BelongsTo{Model: &UserWithPosts{}}.WithDefault(func(inst any) any {
		u := inst.(*UserWithPosts)
		u.Name = "guest"
		return u
	})).GetResults(post2, conn2)
	if err != nil {
		t.Fatal(err)
	}
	if u, ok := results2[0].(*UserWithPosts); !ok || u.Name != "guest" {
		t.Errorf("default with callback = %#v", results2[0])
	}

	// 未启用 withDefault：外键为空仍返回 nil
	conn3, _ := newFakeDB(t)
	blank, err := (BelongsTo{Model: &UserWithPosts{}}).GetResults(&PostWithUser{ID: 10}, conn3)
	if err != nil || blank != nil {
		t.Errorf("no default = %#v err=%v", blank, err)
	}

	// 查询无命中：返回默认实例
	conn, st := newFakeDB(t)
	st.On("select * from `users` where `id` = ?", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:    [][]driver.Value{},
	})
	post3 := &PostWithUser{ID: 10, UserID: 99}
	results3, err := (BelongsTo{Model: &UserWithPosts{}}.WithDefault()).GetResults(post3, conn)
	if err != nil {
		t.Fatal(err)
	}
	if len(results3) != 1 {
		t.Errorf("miss-with-default results = %d", len(results3))
	}
}

// ---- pivot where 变体全族 ----

func TestBelongsToManyWherePivotVariantsGolden(t *testing.T) {
	cases := []struct {
		name string
		rel  BelongsToMany
		want string // where 子句（fpk in 部分之后的追加段）
		wrap bool   // 存在 or 布尔时整组 where 加括号
		args []driver.Value
	}{
		{
			name: "between",
			rel:  BelongsToMany{RelatedModel: Role{}}.WherePivotBetween("priority", []any{1, 5}),
			want: " and `role_user`.`priority` between ? and ?",
			args: []driver.Value{int64(1), int64(5)},
		},
		{
			name: "notBetween",
			rel:  BelongsToMany{RelatedModel: Role{}}.WherePivotNotBetween("priority", []any{1, 5}),
			want: " and `role_user`.`priority` not between ? and ?",
			args: []driver.Value{int64(1), int64(5)},
		},
		{
			name: "orWhereBetween",
			rel:  BelongsToMany{RelatedModel: Role{}}.WherePivotBetween("priority", []any{1, 5}).OrWherePivotBetween("priority", []any{9, 10}),
			want: " and `role_user`.`priority` between ? and ? or `role_user`.`priority` between ? and ?",
			wrap: true,
			args: []driver.Value{int64(1), int64(5), int64(9), int64(10)},
		},
		{
			name: "notIn",
			rel:  BelongsToMany{RelatedModel: Role{}}.WherePivotNotIn("status", []any{"off"}),
			want: " and `role_user`.`status` not in (?)",
			args: []driver.Value{"off"},
		},
		{
			name: "orWhereIn",
			rel:  BelongsToMany{RelatedModel: Role{}}.WherePivotIn("status", []any{"on"}).OrWherePivotIn("status", []any{"trial"}),
			want: " and `role_user`.`status` in (?) or `role_user`.`status` in (?)",
			wrap: true,
			args: []driver.Value{"on", "trial"},
		},
		{
			name: "null",
			rel:  BelongsToMany{RelatedModel: Role{}}.WherePivotNull("expires_at"),
			want: " and `role_user`.`expires_at` is null",
		},
		{
			name: "notNull",
			rel:  BelongsToMany{RelatedModel: Role{}}.WherePivotNotNull("expires_at"),
			want: " and `role_user`.`expires_at` is not null",
		},
		{
			name: "orNullNotNull",
			rel:  BelongsToMany{RelatedModel: Role{}}.WherePivotNull("expires_at").OrWherePivotNotNull("expires_at"),
			want: " and `role_user`.`expires_at` is null or `role_user`.`expires_at` is not null",
			wrap: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn, st := newFakeDB(t)
			st.On("select * from `users`", &fakeResult{
				Columns: []string{"id", "name"},
				Rows:    [][]driver.Value{{int64(1), "A"}},
			})
			where := "`role_user`.`user_id` in (?)" + tc.want
			if tc.wrap {
				where = "(" + where + ")"
			}
			joinSQL := "select `roles`.*, `role_user`.`user_id` as `pivot_user_id` from `roles` inner join `role_user` on `roles`.`id` = `role_user`.`role_id` where " + where
			st.On(joinSQL, &fakeResult{
				Columns: []string{"id", "name", "pivot_user_id"},
				Rows:    [][]driver.Value{},
			})
			var users []UserWithRoles
			if err := conn.Table("users").With("roles", tc.rel).Get(&users); err != nil {
				t.Fatal(err)
			}
			call := st.LastCall(t, joinSQL)
			if len(call.Args) != 1+len(tc.args) {
				t.Fatalf("args = %#v, want %d bindings", call.Args, 1+len(tc.args))
			}
			for i, want := range tc.args {
				if call.Args[1+i] != want {
					t.Errorf("arg[%d] = %#v, want %#v", 1+i, call.Args[1+i], want)
				}
			}
		})
	}

	// 空集合 not in：恒真
	conn, st := newFakeDB(t)
	st.On("select * from `users`", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:    [][]driver.Value{{int64(1), "A"}},
	})
	joinSQL := "select `roles`.*, `role_user`.`user_id` as `pivot_user_id` from `roles` inner join `role_user` on `roles`.`id` = `role_user`.`role_id` where `role_user`.`user_id` in (?) and 1 = 1"
	st.On(joinSQL, &fakeResult{
		Columns: []string{"id", "name", "pivot_user_id"},
		Rows:    [][]driver.Value{},
	})
	var users []UserWithRoles
	if err := conn.Table("users").With("roles", BelongsToMany{RelatedModel: Role{}}.WherePivotNotIn("status", nil)).Get(&users); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, joinSQL)
}

// ---- pivot 写侧 OrFail 族 + SyncWithPivotValues ----

func TestSyncWithPivotValues(t *testing.T) {
	conn, st := newFakeDB(t)
	selectSQL := "select `role_id` from `role_user` where `user_id` = ?"
	st.On(selectSQL, &fakeResult{
		Columns: []string{"role_id"},
		Rows:    [][]driver.Value{{int64(2)}},
	})
	attachSQL := "insert into `role_user` (`user_id`, `role_id`, `status`) values (?, ?, ?)"
	st.On(attachSQL, &fakeResult{Affected: 1})
	updateSQL := "update `role_user` set `status` = ? where `user_id` = ? and `role_id` = ?"
	st.On(updateSQL, &fakeResult{Affected: 1})

	user := &UserWithRoles{ID: 1}
	// 现存 id=2 更新 values，缺失 id=5 附加；默认不解除其它关联
	res, err := conn.SyncWithPivotValues(user, BelongsToMany{RelatedModel: Role{}}, []any{int64(2), int64(5)}, map[string]any{"status": "on"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Attached) != 1 || res.Attached[0] != int64(5) {
		t.Errorf("attached = %#v", res.Attached)
	}
	if len(res.Updated) != 1 || res.Updated[0] != int64(2) {
		t.Errorf("updated = %#v", res.Updated)
	}
	if len(res.Detached) != 0 {
		t.Errorf("detached = %#v", res.Detached)
	}
	uc := st.LastCall(t, updateSQL)
	if uc.Args[0] != "on" || uc.Args[1] != int64(1) || uc.Args[2] != int64(2) {
		t.Errorf("update args = %#v", uc.Args)
	}
	ac := st.LastCall(t, attachSQL)
	if ac.Args[1] != int64(5) || ac.Args[2] != "on" {
		t.Errorf("attach args = %#v", ac.Args)
	}
}

func TestPivotOrFailFamily(t *testing.T) {
	conn, st := newFakeDB(t)
	selectSQL := "select `role_id` from `role_user` where `user_id` = ?"
	attachSQL := "insert into `role_user` (`user_id`, `role_id`) values (?, ?)"
	detachSQL := "delete from `role_user` where `user_id` = ? and `role_id` in (?)"
	updateSQL := "update `role_user` set `status` = ? where `user_id` = ? and `role_id` = ?"
	st.On(attachSQL, &fakeResult{Affected: 1})
	st.On(detachSQL, &fakeResult{Affected: 1})
	st.On(updateSQL, &fakeResult{Affected: 1})

	user := &UserWithRoles{ID: 1}
	rel := BelongsToMany{RelatedModel: Role{}}

	// 成功路径：不 panic
	conn.AttachOrFail(user, rel, []any{int64(7)})
	if n := conn.DetachOrFail(user, rel, int64(7)); n != 1 {
		t.Errorf("detach = %d", n)
	}
	// 桩按调用顺序消费：sync 的 current 查询得空集，toggle 得 [8]
	st.On(selectSQL, &fakeResult{
		Columns: []string{"role_id"},
		Rows:    [][]driver.Value{},
	})
	st.On(selectSQL, &fakeResult{
		Columns: []string{"role_id"},
		Rows:    [][]driver.Value{{int64(8)}},
	})
	if res := conn.SyncOrFail(user, rel, []any{int64(8)}, nil); len(res.Attached) != 1 {
		t.Errorf("sync = %#v", res)
	}
	if res := conn.ToggleOrFail(user, rel, []any{int64(8)}); len(res.Detached) != 1 {
		t.Errorf("toggle = %#v", res)
	}
	if n := conn.UpdateExistingPivotOrFail(user, rel, int64(8), map[string]any{"status": "off"}); n != 1 {
		t.Errorf("updateExistingPivot = %d", n)
	}

	// 失败路径：panic *FailError，Unwrap 取回原始错误（未打桩的查询报 fake 错误）
	func() {
		defer func() {
			r := recover()
			fe, ok := r.(*FailError)
			if !ok {
				t.Fatalf("expect *FailError, got %#v", r)
			}
			if fe.Op != "attach" {
				t.Errorf("op = %s", fe.Op)
			}
			if fe.Unwrap() == nil {
				t.Error("unwrap = nil")
			}
		}()
		conn.AttachOrFail(user, rel, []any{int64(7)}, map[string]any{"status": "on"}) // 未打桩 → 失败
	}()
}

// ---- 20261106 模型层 D 组补全（SaveOrIgnore / LoadMissing / withPivotValue /
// HasOne withDefault / one-of-many tie-break）----

// ★ SaveOrIgnore（Model.php:1432 saveOrIgnore → performInsertOrIgnore）
func TestConnectionSaveOrIgnore(t *testing.T) {
	conn, st := newFakeDB(t)
	insertIgnore := "insert ignore into `posts` (`created_at`, `title`, `updated_at`) values (?, ?, ?)"
	st.On(insertIgnore, &fakeResult{Affected: 1, LastID: 33})
	st.On(insertIgnore, &fakeResult{Affected: 0}) // 第二次：唯一约束冲突被忽略

	p := &Post{Title: "hello"}
	saved, err := conn.SaveOrIgnore(p)
	if err != nil || !saved {
		t.Fatalf("saveOrIgnore = %v, %v", saved, err)
	}
	if p.ID != 33 {
		t.Errorf("backfilled id = %d", p.ID)
	}
	if !Exists(p) || !WasRecentlyCreated(p) {
		t.Errorf("exists=%v recentlyCreated=%v", Exists(p), WasRecentlyCreated(p))
	}

	// 冲突被忽略：返回 false，不置 exists
	q := &Post{Title: "dup"}
	saved, err = conn.SaveOrIgnore(q)
	if err != nil || saved {
		t.Fatalf("ignored saveOrIgnore = %v, %v", saved, err)
	}
	if Exists(q) || WasRecentlyCreated(q) {
		t.Error("ignored insert should not mark exists")
	}
	st.LastCall(t, insertIgnore)

	// 已持久化模型：报错（Laravel LogicException）
	if _, err := conn.SaveOrIgnore(p); err == nil {
		t.Error("existing model should error")
	}
}

// ★ LoadMissing（Model.php loadMissing：已加载则跳过）
func TestConnectionLoadMissing(t *testing.T) {
	conn, st := newFakeDB(t)
	relSQL := "select * from `posts` where `user_id` in (?)"
	st.On(relSQL, &fakeResult{
		Columns: []string{"id", "title", "user_id", "created_at"},
		Rows:    [][]driver.Value{{int64(5), "p1", int64(1), "2026-01-01"}},
	})

	user := &UserWithLatestPost{ID: 1}
	rel := HasOne{Model: PostOrdered{}}
	if err := conn.Load(user, "latestPost", rel); err != nil {
		t.Fatal(err)
	}
	n := len(st.Calls())
	// 已加载：LoadMissing 直接返回，不发查询
	if err := conn.LoadMissing(user, "latestPost", rel); err != nil {
		t.Fatal(err)
	}
	if len(st.Calls()) != n {
		t.Errorf("LoadMissing should not query, calls %d -> %d", n, len(st.Calls()))
	}
	// 未加载：照常加载
	if err := conn.LoadMissing(user, "other", rel); err != nil {
		t.Fatal(err)
	}
	if len(st.Calls()) == n {
		t.Error("LoadMissing should load unloaded relation")
	}
	ForgetLoadedRelations(user)
}

// ★ HasOne withDefault（SupportsDefaultModels，HasOne.php 亦实现）
func TestHasOneWithDefault(t *testing.T) {
	conn, st := newFakeDB(t)
	emptySQL := "select * from `posts` where `user_id` in (?)"
	st.On(emptySQL, &fakeResult{
		Columns: []string{"id", "title", "user_id", "created_at"},
		Rows:    [][]driver.Value{},
	})

	user := &UserWithLatestPost{ID: 1}
	// 无默认：空结果
	results, err := (HasOne{Model: PostOrdered{}}).GetResults(user, conn)
	if err != nil || len(results) != 0 {
		t.Fatalf("plain hasOne = %v, %v", results, err)
	}
	// withDefault：返回目标模型新实例
	results, err = (HasOne{Model: PostOrdered{}}.WithDefault()).GetResults(user, conn)
	if err != nil || len(results) != 1 {
		t.Fatalf("withDefault = %v, %v", results, err)
	}
	if p, ok := results[0].(*PostOrdered); !ok || p == nil {
		t.Errorf("default result = %#v", results[0])
	}
	// DefaultFn 定制默认实例
	results, err = (HasOne{Model: PostOrdered{}}.WithDefault(func(inst any) any {
		p := inst.(*PostOrdered)
		p.Title = "fallback"
		return p
	})).GetResults(user, conn)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := results[0].(*PostOrdered); !ok || p.Title != "fallback" {
		t.Errorf("default fn result = %#v", results[0])
	}
}

// ★ one-of-many 主键 tie-break（CanBeOneOfMany.php:70-176 ofMany 强制追加
// keyName => aggregate）
func TestOfManyOrdersTieBreak(t *testing.T) {
	orders, ok := ofManyOrders(HasOne{Model: PostOrdered{}}.LatestOfMany())
	if !ok || len(orders) != 2 ||
		orders[0].column != "created_at" || orders[0].direction != "desc" ||
		orders[1].column != "id" || orders[1].direction != "desc" {
		t.Errorf("latestOfMany orders = %+v ok=%v", orders, ok)
	}
	// oldestOfMany：主键同向（asc）
	orders, ok = ofManyOrders(HasOne{Model: PostOrdered{}}.OldestOfMany())
	if !ok || len(orders) != 2 || orders[1].column != "id" || orders[1].direction != "asc" {
		t.Errorf("oldestOfMany orders = %+v ok=%v", orders, ok)
	}
	// 极值列即主键：不重复追加
	orders, ok = ofManyOrders(HasOne{Model: PostOrdered{}}.OfMany("id", "min"))
	if !ok || len(orders) != 1 || orders[0].column != "id" {
		t.Errorf("ofMany(id) orders = %+v ok=%v", orders, ok)
	}
	// GetResults SQL 含主键 tie-break
	conn, st := newFakeDB(t)
	latestSQL := "select * from `posts` where `user_id` in (?) order by `created_at` desc, `id` desc limit 1"
	st.On(latestSQL, &fakeResult{
		Columns: []string{"id", "title", "user_id", "created_at"},
		Rows:    [][]driver.Value{{int64(13), "p-newest", int64(1), "2026-01-03"}},
	})
	user := &UserWithLatestPost{ID: 1}
	results, err := (HasOne{Model: PostOrdered{}}.LatestOfMany()).GetResults(user, conn)
	if err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, latestSQL)
	if p, ok := results[0].(*PostOrdered); !ok || p.Title != "p-newest" {
		t.Errorf("tie-break result = %#v", results[0])
	}
}

// ★ withPivotValue（BelongsToMany.php:494）：attach/sync 每行附加固定 pivot 值
func TestBelongsToManyWithPivotValue(t *testing.T) {
	conn, st := newFakeDB(t)
	insertSQL := "insert into `role_user` (`user_id`, `role_id`, `status`) values (?, ?, ?)"
	st.On(insertSQL, &fakeResult{Affected: 1})
	insertAll := "insert into `role_user` (`user_id`, `role_id`, `note`, `status`) values (?, ?, ?, ?)"
	st.On(insertAll, &fakeResult{Affected: 1})

	user := &UserWithRoles{ID: 1}
	rel := BelongsToMany{RelatedModel: Role{}}.WithPivotValue("status", "active")

	// attach：每行带固定值
	if err := conn.Attach(user, rel, []any{int64(7), int64(8)}); err != nil {
		t.Fatal(err)
	}
	calls := st.Calls()
	n := 0
	for i := range calls {
		if calls[i].Sql == insertSQL {
			if calls[i].Args[2] != "active" {
				t.Errorf("pivot value = %#v", calls[i].Args)
			}
			n++
		}
	}
	if n != 2 {
		t.Errorf("attach rows = %d", n)
	}

	// 显式 pivotAttrs 优先于固定值
	if err := conn.Attach(user, rel, []any{int64(9)}, map[string]any{"status": "off"}); err != nil {
		t.Fatal(err)
	}
	ac := st.LastCall(t, insertSQL)
	if ac.Args[2] != "off" {
		t.Errorf("explicit attrs should override pivot value, got %#v", ac.Args)
	}

	// 固定值与显式 attrs 合并（列排序后 note 在 status 之后）
	if err := conn.Attach(user, rel, []any{int64(10)}, map[string]any{"note": "n"}); err != nil {
		t.Fatal(err)
	}
	ac = st.LastCall(t, insertAll)
	if ac.Args[2] != "n" || ac.Args[3] != "active" {
		t.Errorf("merged attrs = %#v", ac.Args)
	}
}
