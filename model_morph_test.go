package torm

import (
	"database/sql/driver"
	"testing"
	"time"
)

// 多态目标：评论可挂在 post / video 上
type MorphComment struct {
	ID             int64  `torm:"primary_key;column:id"`
	Body           string `torm:"column:body"`
	CommentableID  int64  `torm:"column:commentable_id"`
	CommentableTyp string `torm:"column:commentable_type"`
}

func (c *MorphComment) TableName() string { return "comments" }

// 持有多态关系的父模型
type MorphPost struct {
	ID         int64          `torm:"primary_key;column:id"`
	Title      string         `torm:"column:title"`
	Comments   []MorphComment `torm:"-"`
	Latest     *MorphComment  `torm:"-"`
	Subject    interface{}    `torm:"-"` // MorphTo
	SubjectTyp string         `torm:"column:subject_type"`
	SubjectID  int64          `torm:"column:subject_id"`
}

func (p *MorphPost) TableName() string { return "posts" }

func TestMorphManyEagerLoad(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `posts`", &fakeResult{
		Columns: []string{"id", "title"},
		Rows:    [][]driver.Value{{int64(1), "p1"}, {int64(2), "p2"}},
	})
	relatedSQL := "select * from `comments` where `commentable_type` = ? and `commentable_id` in (?, ?)"
	st.On(relatedSQL, &fakeResult{
		Columns: []string{"id", "body", "commentable_id", "commentable_type"},
		Rows: [][]driver.Value{
			{int64(50), "c1", int64(1), "posts"},
			{int64(51), "c2", int64(2), "posts"},
		},
	})

	var posts []MorphPost
	if err := conn.Table("posts").With("comments", MorphMany{MorphName: "commentable", TargetModel: MorphComment{}}).Get(&posts); err != nil {
		t.Fatal(err)
	}
	if len(posts) != 2 {
		t.Fatalf("posts = %d", len(posts))
	}
	if len(posts[0].Comments) != 1 || posts[0].Comments[0].Body != "c1" {
		t.Errorf("post1 comments = %+v", posts[0].Comments)
	}
	if len(posts[1].Comments) != 1 || posts[1].Comments[0].Body != "c2" {
		t.Errorf("post2 comments = %+v", posts[1].Comments)
	}
	st.LastCall(t, relatedSQL)
}

func TestMorphOneEagerLoad(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `posts`", &fakeResult{
		Columns: []string{"id", "title"},
		Rows:    [][]driver.Value{{int64(1), "p1"}},
	})
	st.On("select * from `comments` where `commentable_type` = ? and `commentable_id` in (?)", &fakeResult{
		Columns: []string{"id", "body", "commentable_id", "commentable_type"},
		Rows:    [][]driver.Value{{int64(9), "first", int64(1), "posts"}},
	})

	var posts []MorphPost
	if err := conn.Table("posts").With("latest", MorphOne{MorphName: "commentable", TargetModel: MorphComment{}}).Get(&posts); err != nil {
		t.Fatal(err)
	}
	if posts[0].Latest == nil || posts[0].Latest.Body != "first" {
		t.Errorf("latest = %+v", posts[0].Latest)
	}
}

type MorphVideo struct {
	ID   int64  `torm:"primary_key;column:id"`
	Name string `torm:"column:name"`
}

func (v *MorphVideo) TableName() string { return "videos" }

func TestMorphToEagerLoad(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `posts`", &fakeResult{
		Columns: []string{"id", "subject_type", "subject_id"},
		Rows: [][]driver.Value{
			{int64(1), "videos", int64(7)},
			{int64(2), "videos", int64(8)},
		},
	})
	st.On("select * from `videos` where `id` in (?, ?)", &fakeResult{
		Columns: []string{"id", "name"},
		Rows: [][]driver.Value{
			{int64(7), "v7"},
			{int64(8), "v8"},
		},
	})

	var posts []MorphPost
	if err := conn.Table("posts").With("subject", MorphTo{
		TypeMap: map[string]any{"videos": &MorphVideo{}},
	}).Get(&posts); err != nil {
		t.Fatal(err)
	}
	if len(posts) != 2 {
		t.Fatalf("posts = %d", len(posts))
	}
	v1, ok := posts[0].Subject.(*MorphVideo)
	if !ok || v1.Name != "v7" {
		t.Errorf("subject1 = %+v", posts[0].Subject)
	}
	v2, ok := posts[1].Subject.(*MorphVideo)
	if !ok || v2.Name != "v8" {
		t.Errorf("subject2 = %+v", posts[1].Subject)
	}
}

// 多态多对多：Post morphToMany Tag（pivot: taggables）
type MorphTag struct {
	ID   int64  `torm:"primary_key;column:id"`
	Name string `torm:"column:name"`
}

func (r *MorphTag) TableName() string { return "tags" }

type MorphPostWithTags struct {
	ID    int64      `torm:"primary_key;column:id"`
	Title string     `torm:"column:title"`
	Tags  []MorphTag `torm:"-"`
}

func (p *MorphPostWithTags) TableName() string { return "posts" }

type MorphVideoWithTags struct {
	ID   int64      `torm:"primary_key;column:id"`
	Name string     `torm:"column:name"`
	Tags []MorphTag `torm:"-"`
}

func (v *MorphVideoWithTags) TableName() string { return "videos" }

func TestMorphToManyEagerLoad(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `posts`", &fakeResult{
		Columns: []string{"id", "title"},
		Rows:    [][]driver.Value{{int64(1), "p1"}},
	})
	joinSQL := "select `tags`.*, `taggables`.`taggable_id` as `pivot_taggable_id` from `tags` inner join `taggables` on `tags`.`id` = `taggables`.`tag_id` where `taggables`.`taggable_type` = ? and `taggables`.`taggable_id` in (?)"
	st.On(joinSQL, &fakeResult{
		Columns: []string{"id", "name", "pivot_taggable_id"},
		Rows: [][]driver.Value{
			{int64(7), "laravel", int64(1)},
			{int64(8), "go", int64(1)},
		},
	})

	var posts []MorphPostWithTags
	if err := conn.Table("posts").With("tags", MorphToMany{
		RelatedModel: MorphTag{},
		MorphName:    "taggable",
	}).Get(&posts); err != nil {
		t.Fatal(err)
	}
	if len(posts) != 1 || len(posts[0].Tags) != 2 {
		t.Fatalf("posts tags = %+v", posts)
	}
	if posts[0].Tags[0].Name != "laravel" || posts[0].Tags[1].Name != "go" {
		t.Errorf("tags = %+v", posts[0].Tags)
	}
	st.LastCall(t, joinSQL)
}

func TestMorphToManyVideoDirection(t *testing.T) {
	// morphedByMany：videos 复用同一 taggables pivot，类型串为 videos
	conn, st := newFakeDB(t)
	st.On("select * from `videos`", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:    [][]driver.Value{{int64(3), "intro"}},
	})
	joinSQL := "select `tags`.*, `taggables`.`taggable_id` as `pivot_taggable_id` from `tags` inner join `taggables` on `tags`.`id` = `taggables`.`tag_id` where `taggables`.`taggable_type` = ? and `taggables`.`taggable_id` in (?)"
	st.On(joinSQL, &fakeResult{
		Columns: []string{"id", "name", "pivot_taggable_id"},
		Rows:    [][]driver.Value{{int64(7), "laravel", int64(3)}},
	})

	var videos []MorphVideoWithTags
	if err := conn.Table("videos").With("tags", MorphedByMany{
		RelatedModel: MorphTag{},
		MorphName:    "taggable",
	}).Get(&videos); err != nil {
		t.Fatal(err)
	}
	if len(videos) != 1 || len(videos[0].Tags) != 1 || videos[0].Tags[0].Name != "laravel" {
		t.Fatalf("videos tags = %+v", videos)
	}
	call := st.LastCall(t, joinSQL)
	if call.Args[0] != "videos" {
		t.Errorf("morph type = %#v", call.Args[0])
	}
}

func TestMorphMapKeyResolution(t *testing.T) {
	SetMorphMap(map[string]any{
		"post":  &MorphPost{}, // 别名 ≠ 表名
		"video": &MorphVideo{},
	})
	defer ResetMorphMap()

	conn, st := newFakeDB(t)
	st.On("select * from `posts`", &fakeResult{
		Columns: []string{"id", "title"},
		Rows:    [][]driver.Value{{int64(1), "p1"}},
	})
	// MorphKey 未显式设置 → 类型列取 morph map 别名 "post"（而非表名 posts）
	relatedSQL := "select * from `comments` where `commentable_type` = ? and `commentable_id` in (?)"
	st.On(relatedSQL, &fakeResult{
		Columns: []string{"id", "body", "commentable_id", "commentable_type"},
		Rows:    [][]driver.Value{{int64(9), "first", int64(1), "post"}},
	})

	var posts []MorphPost
	if err := conn.Table("posts").With("comments", MorphMany{MorphName: "commentable", TargetModel: MorphComment{}}).Get(&posts); err != nil {
		t.Fatal(err)
	}
	if len(posts) != 1 || len(posts[0].Comments) != 1 {
		t.Fatalf("posts comments = %+v", posts)
	}
	call := st.LastCall(t, relatedSQL)
	if call.Args[0] != "post" {
		t.Errorf("morph type = %#v, want alias %q", call.Args[0], "post")
	}
}

func TestMorphToViaMorphMap(t *testing.T) {
	SetMorphMap(map[string]any{
		"videos": &MorphVideo{},
	})
	defer ResetMorphMap()

	conn, st := newFakeDB(t)
	st.On("select * from `posts`", &fakeResult{
		Columns: []string{"id", "subject_type", "subject_id"},
		Rows:    [][]driver.Value{{int64(1), "videos", int64(7)}},
	})
	st.On("select * from `videos` where `id` in (?)", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:    [][]driver.Value{{int64(7), "v7"}},
	})

	var posts []MorphPost
	// 无 TypeMap：类型串直接经全局 morph map 解析
	if err := conn.Table("posts").With("subject", MorphTo{}).Get(&posts); err != nil {
		t.Fatal(err)
	}
	v, ok := posts[0].Subject.(*MorphVideo)
	if !ok || v.Name != "v7" {
		t.Errorf("subject = %+v", posts[0].Subject)
	}
}

func TestMorphAliasOf(t *testing.T) {
	SetMorphMap(map[string]any{"post": &MorphPost{}})
	defer ResetMorphMap()

	alias, ok := MorphAliasOf(&MorphPost{})
	if !ok || alias != "post" {
		t.Errorf("alias = %q ok = %v", alias, ok)
	}
	if _, ok := MorphAliasOf(&User{}); ok {
		t.Error("unregistered model should not resolve")
	}
}

// ---- morphTo OwnerKey 可配 / MorphWith / WithTrashed（MorphTo.php:97-244）----

type MorphUuidOwner struct {
	ID   int64  `torm:"primary_key;column:id"`
	UUID string `torm:"column:uuid"`
	Name string `torm:"column:name"`
}

func (o *MorphUuidOwner) TableName() string { return "uuid_owners" }

// 字符串外键的父模型（uuid ownerKey 场景）
type MorphUuidPost struct {
	ID         int64       `torm:"primary_key;column:id"`
	SubjectTyp string      `torm:"column:subject_type"`
	SubjectUID string      `torm:"column:subject_id"`
	Subject    interface{} `torm:"-"`
}

func (p *MorphUuidPost) TableName() string { return "posts" }

func TestMorphToOwnerKey(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `posts`", &fakeResult{
		Columns: []string{"id", "subject_type", "subject_id"},
		Rows:    [][]driver.Value{{int64(1), "uuid_owners", "u-9"}},
	})
	// OwnerKey = uuid：按 uuid 列查询
	st.On("select * from `uuid_owners` where `uuid` in (?)", &fakeResult{
		Columns: []string{"id", "uuid", "name"},
		Rows:    [][]driver.Value{{int64(1), "u-9", "owner"}},
	})

	var posts []MorphUuidPost
	if err := conn.Table("posts").With("subject", MorphTo{
		TypeMap:  map[string]any{"uuid_owners": &MorphUuidOwner{}},
		OwnerKey: "uuid",
	}).Get(&posts); err != nil {
		t.Fatal(err)
	}
	if o, ok := posts[0].Subject.(*MorphUuidOwner); !ok || o.Name != "owner" {
		t.Errorf("subject = %#v", posts[0].Subject)
	}
	st.LastCall(t, "select * from `uuid_owners` where `uuid` in (?)")
}

type MorphSoftVideo struct {
	ID        int64      `torm:"primary_key;column:id"`
	Name      string     `torm:"column:name"`
	DeletedAt *time.Time `torm:"column:deleted_at"`
}

func (v *MorphSoftVideo) TableName() string { return "videos" }

func TestMorphToWithTrashedVariant(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `posts`", &fakeResult{
		Columns: []string{"id", "subject_type", "subject_id"},
		Rows:    [][]driver.Value{{int64(1), "videos", int64(7)}},
	})
	// 默认：软删 owner 模型追加 deleted_at is null（约束先于 whereIn 编译）
	defaultSQL := "select * from `videos` where `deleted_at` is null and `id` in (?)"
	st.On(defaultSQL, &fakeResult{
		Columns: []string{"id", "name", "deleted_at"},
		Rows:    [][]driver.Value{},
	})

	var posts []MorphPost
	rel := MorphTo{TypeMap: map[string]any{"videos": &MorphSoftVideo{}}}
	if err := conn.Table("posts").With("subject", rel).Get(&posts); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, defaultSQL)

	// WithTrashed 变体：跳过软删过滤
	trashedSQL := "select * from `videos` where `id` in (?)"
	st.On(trashedSQL, &fakeResult{
		Columns: []string{"id", "name", "deleted_at"},
		Rows:    [][]driver.Value{{int64(7), "v7", nil}},
	})
	var posts2 []MorphPost
	if err := conn.Table("posts").With("subject", rel).MorphToWithTrashed("subject", rel).Get(&posts2); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, trashedSQL)
}

func TestMorphWithPerTypeConstraints(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `posts`", &fakeResult{
		Columns: []string{"id", "subject_type", "subject_id"},
		Rows:    [][]driver.Value{{int64(1), "videos", int64(7)}},
	})
	// 按类型串附加约束（morphWith）
	withSQL := "select * from `videos` where `name` = ? and `id` in (?)"
	st.On(withSQL, &fakeResult{
		Columns: []string{"id", "name"},
		Rows:    [][]driver.Value{{int64(7), "v7"}},
	})

	var posts []MorphPost
	b := conn.Table("posts").With("subject", MorphTo{TypeMap: map[string]any{"videos": &MorphVideo{}}})
	if err := b.MorphWith("subject", MorphTo{TypeMap: map[string]any{"videos": &MorphVideo{}}}, map[string]func(*Builder){
		"videos": func(nb *Builder) { nb.Where("name", "=", "v7") },
	}).Get(&posts); err != nil {
		t.Fatal(err)
	}
	if v, ok := posts[0].Subject.(*MorphVideo); !ok || v.Name != "v7" {
		t.Errorf("subject = %#v", posts[0].Subject)
	}
	st.LastCall(t, withSQL)
}

func TestMorphToGetResults(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `videos` where `id` in (?)", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:    [][]driver.Value{{int64(7), "v7"}},
	})

	post := &MorphPost{ID: 1, SubjectTyp: "videos", SubjectID: 7}
	results, err := (MorphTo{TypeMap: map[string]any{"videos": &MorphVideo{}}}).GetResults(post, conn)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d", len(results))
	}
	if v, ok := results[0].(*MorphVideo); !ok || v.Name != "v7" {
		t.Errorf("result = %#v", results[0])
	}

	// 类型串未知 → 空结果不报错
	unknown := &MorphPost{ID: 2, SubjectTyp: "ghosts", SubjectID: 1}
	results2, err := (MorphTo{TypeMap: map[string]any{"videos": &MorphVideo{}}}).GetResults(unknown, conn)
	if err != nil || len(results2) != 0 {
		t.Errorf("unknown type results = %#v err=%v", results2, err)
	}
}

func TestMorphManyGetResults(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `comments` where `commentable_type` = ? and `commentable_id` in (?)", &fakeResult{
		Columns: []string{"id", "body", "commentable_id", "commentable_type"},
		Rows:    [][]driver.Value{{int64(50), "c1", int64(1), "posts"}},
	})

	post := &MorphPost{ID: 1}
	results, err := (MorphMany{MorphName: "commentable", TargetModel: MorphComment{}}).GetResults(post, conn)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d", len(results))
	}
	if c, ok := results[0].(*MorphComment); !ok || c.Body != "c1" {
		t.Errorf("result = %#v", results[0])
	}
}

func TestMorphToManyGetResults(t *testing.T) {
	conn, st := newFakeDB(t)
	joinSQL := "select `tags`.*, `taggables`.`taggable_id` as `pivot_taggable_id` from `tags` inner join `taggables` on `tags`.`id` = `taggables`.`tag_id` where `taggables`.`taggable_type` = ? and `taggables`.`taggable_id` in (?)"
	st.On(joinSQL, &fakeResult{
		Columns: []string{"id", "name", "pivot_taggable_id"},
		Rows:    [][]driver.Value{{int64(7), "laravel", int64(1)}},
	})

	post := &MorphPostWithTags{ID: 1}
	results, err := (MorphToMany{RelatedModel: MorphTag{}, MorphName: "taggable"}).GetResults(post, conn)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d", len(results))
	}
	if tag, ok := results[0].(*MorphTag); !ok || tag.Name != "laravel" {
		t.Errorf("result = %#v", results[0])
	}
}

// ---- MorphTo 软删变体 / constrain / morphWithCount / withDefault ----

func TestMorphToWithoutTrashedOnlyTrashedPreload(t *testing.T) {
	// 默认 / withoutTrashed：deleted_at is null
	conn, st := newFakeDB(t)
	st.On("select * from `posts`", &fakeResult{
		Columns: []string{"id", "subject_type", "subject_id"},
		Rows:    [][]driver.Value{{int64(1), "videos", int64(7)}},
	})
	baseSQL := "select * from `videos` where `deleted_at` is null and `id` in (?)"
	st.On(baseSQL, &fakeResult{
		Columns: []string{"id", "name", "deleted_at"},
		Rows:    [][]driver.Value{{int64(7), "v7", nil}},
	})
	var posts []MorphPost
	if err := conn.Table("posts").With("subject", MorphTo{
		TypeMap: map[string]any{"videos": &MorphSoftVideo{}},
	}).Get(&posts); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, baseSQL)

	// withoutTrashed 变体（显式，与默认一致）
	conn2, st2 := newFakeDB(t)
	st2.On("select * from `posts`", &fakeResult{
		Columns: []string{"id", "subject_type", "subject_id"},
		Rows:    [][]driver.Value{{int64(1), "videos", int64(7)}},
	})
	st2.On(baseSQL, &fakeResult{
		Columns: []string{"id", "name", "deleted_at"},
		Rows:    [][]driver.Value{},
	})
	var posts2 []MorphPost
	if err := conn2.Table("posts").MorphToWithoutTrashed("subject", MorphTo{
		TypeMap: map[string]any{"videos": &MorphSoftVideo{}},
	}).Get(&posts2); err != nil {
		t.Fatal(err)
	}
	st2.LastCall(t, baseSQL)

	// onlyTrashed 变体：deleted_at is not null
	conn3, st3 := newFakeDB(t)
	st3.On("select * from `posts`", &fakeResult{
		Columns: []string{"id", "subject_type", "subject_id"},
		Rows:    [][]driver.Value{{int64(1), "videos", int64(7)}},
	})
	onlySQL := "select * from `videos` where `deleted_at` is not null and `id` in (?)"
	st3.On(onlySQL, &fakeResult{
		Columns: []string{"id", "name", "deleted_at"},
		Rows:    [][]driver.Value{},
	})
	var posts3 []MorphPost
	if err := conn3.Table("posts").MorphToOnlyTrashed("subject", MorphTo{
		TypeMap: map[string]any{"videos": &MorphSoftVideo{}},
	}).Get(&posts3); err != nil {
		t.Fatal(err)
	}
	st3.LastCall(t, onlySQL)
}

func TestMorphToConstrainPreload(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `posts`", &fakeResult{
		Columns: []string{"id", "subject_type", "subject_id"},
		Rows:    [][]driver.Value{{int64(1), "videos", int64(7)}},
	})
	// constrain 全类型通用约束先于软删过滤与主键 in
	sql := "select * from `videos` where `name` = ? and `deleted_at` is null and `id` in (?)"
	st.On(sql, &fakeResult{
		Columns: []string{"id", "name", "deleted_at"},
		Rows:    [][]driver.Value{},
	})

	var posts []MorphPost
	if err := conn.Table("posts").MorphToConstrain("subject", MorphTo{
		TypeMap: map[string]any{"videos": &MorphSoftVideo{}},
	}, func(nb *Builder) { nb.Where("name", "=", "v7") }).Get(&posts); err != nil {
		t.Fatal(err)
	}
	call := st.LastCall(t, sql)
	if len(call.Args) != 2 || call.Args[0] != "v7" || call.Args[1] != int64(7) {
		t.Errorf("args = %#v", call.Args)
	}
}

func TestMorphToWithCountPreload(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `posts`", &fakeResult{
		Columns: []string{"id", "subject_type", "subject_id"},
		Rows:    [][]driver.Value{{int64(1), "videos", int64(7)}},
	})
	// morphWithCount：owner 查询带 comments_count 聚合（对应 morphWithCount([Video::class => ['comments']])）
	sql := "select *, (select count(*) from `comments` where `comments`.`video_id` = `videos`.`id`) as `comments_count` from `videos` where `deleted_at` is null and `id` in (?)"
	st.On(sql, &fakeResult{
		Columns: []string{"id", "name", "deleted_at", "comments_count"},
		Rows:    [][]driver.Value{{int64(7), "v7", nil, int64(3)}},
	})

	var posts []MorphPostWithCount
	if err := conn.Table("posts").MorphToWithCount("subject", MorphTo{
		TypeMap: map[string]any{"videos": &MorphSoftVideo{}},
	}, map[string][]MorphCountSpec{
		"videos": {{Name: "comments", Relation: HasMany{Model: MorphComment{}}}},
	}).Get(&posts); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, sql)
}

// 带计数属性字段的父模型（LoadAggregate 族会把聚合写入同名属性）
type MorphPostWithCount struct {
	ID            int64       `torm:"primary_key;column:id"`
	Title         string      `torm:"column:title"`
	Subject       interface{} `torm:"-"`
	SubjectTyp    string      `torm:"column:subject_type"`
	SubjectID     int64       `torm:"column:subject_id"`
	CommentsCount int64       `torm:"column:comments_count"`
}

func (p *MorphPostWithCount) TableName() string { return "posts" }

func TestMorphToGetResultsVariants(t *testing.T) {
	// 描述符级 onlyTrashed：GetResults 显式 whereNotNull
	conn, st := newFakeDB(t)
	sql := "select * from `videos` where `deleted_at` is not null and `id` in (?)"
	st.On(sql, &fakeResult{
		Columns: []string{"id", "name", "deleted_at"},
		Rows:    [][]driver.Value{},
	})
	post := &MorphPost{ID: 1, SubjectTyp: "videos", SubjectID: 7}
	results, err := (MorphTo{
		TypeMap:     map[string]any{"videos": &MorphSoftVideo{}},
		OnlyTrashed: true,
	}).GetResults(post, conn)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("results = %d", len(results))
	}
	st.LastCall(t, sql)

	// withDefault + 回调：类型列也为空时 inst 参为 nil，回调自行构造
	results2, err := (MorphTo{
		TypeMap: map[string]any{"videos": &MorphSoftVideo{}},
	}.WithDefault(func(inst any) any {
		if v, ok := inst.(*MorphSoftVideo); ok && v != nil {
			v.Name = "missing"
			return v
		}
		return &MorphSoftVideo{Name: "missing"}
	})).GetResults(&MorphPost{ID: 1}, conn)
	if err != nil {
		t.Fatal(err)
	}
	if len(results2) != 1 {
		t.Fatalf("default results = %d", len(results2))
	}
	if v, ok := results2[0].(*MorphSoftVideo); !ok || v.Name != "missing" {
		t.Errorf("default with callback = %#v", results2[0])
	}

	// 无回调且类型未解析：无法实例化，返回 nil（Go 侧 MorphTo 无目标类型）
	results3, err := (MorphTo{
		TypeMap:    map[string]any{"videos": &MorphSoftVideo{}},
		HasDefault: true,
	}).GetResults(&MorphPost{ID: 1}, conn)
	if err != nil || results3 != nil {
		t.Errorf("no-fn blank type = %#v err=%v", results3, err)
	}
}

// ★ MorphOne withDefault（SupportsDefaultModels，HasOne.php 同款实现）
func TestMorphOneWithDefault(t *testing.T) {
	conn, st := newFakeDB(t)
	emptySQL := "select * from `comments` where `commentable_type` = ? and `commentable_id` in (?)"
	st.On(emptySQL, &fakeResult{
		Columns: []string{"id", "body", "commentable_id", "commentable_type"},
		Rows:    [][]driver.Value{},
	})

	post := &MorphPost{ID: 1}
	// 无默认：空结果
	results, err := (MorphOne{MorphName: "commentable", TargetModel: MorphComment{}}).GetResults(post, conn)
	if err != nil || len(results) != 0 {
		t.Fatalf("plain morphOne = %v, %v", results, err)
	}
	// withDefault：返回目标模型新实例
	results, err = (MorphOne{MorphName: "commentable", TargetModel: MorphComment{}}.WithDefault()).GetResults(post, conn)
	if err != nil || len(results) != 1 {
		t.Fatalf("withDefault = %v, %v", results, err)
	}
	if c, ok := results[0].(*MorphComment); !ok || c == nil {
		t.Errorf("default result = %#v", results[0])
	}
	// DefaultFn 定制默认实例
	results, err = (MorphOne{MorphName: "commentable", TargetModel: MorphComment{}}.WithDefault(func(inst any) any {
		c := inst.(*MorphComment)
		c.Body = "fallback"
		return c
	})).GetResults(post, conn)
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := results[0].(*MorphComment); !ok || c.Body != "fallback" {
		t.Errorf("default fn result = %#v", results[0])
	}
}

// ★ MorphOne one-of-many 主键 tie-break：GetResults SQL 含 `, `id` desc`
func TestMorphOneOfManyTieBreak(t *testing.T) {
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
	st.LastCall(t, latestSQL)
	if len(results) != 1 {
		t.Fatalf("results = %d", len(results))
	}
}
