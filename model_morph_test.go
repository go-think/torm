package torm

import (
	"database/sql/driver"
	"testing"
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
		Rows:   [][]driver.Value{{int64(1), "p1"}, {int64(2), "p2"}},
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
		Rows:   [][]driver.Value{{int64(1), "p1"}},
	})
	st.On("select * from `comments` where `commentable_type` = ? and `commentable_id` in (?)", &fakeResult{
		Columns: []string{"id", "body", "commentable_id", "commentable_type"},
		Rows:   [][]driver.Value{{int64(9), "first", int64(1), "posts"}},
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
		Rows:   [][]driver.Value{{int64(1), "p1"}},
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
		Rows:   [][]driver.Value{{int64(3), "intro"}},
	})
	joinSQL := "select `tags`.*, `taggables`.`taggable_id` as `pivot_taggable_id` from `tags` inner join `taggables` on `tags`.`id` = `taggables`.`tag_id` where `taggables`.`taggable_type` = ? and `taggables`.`taggable_id` in (?)"
	st.On(joinSQL, &fakeResult{
		Columns: []string{"id", "name", "pivot_taggable_id"},
		Rows:   [][]driver.Value{{int64(7), "laravel", int64(3)}},
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
		Rows:   [][]driver.Value{{int64(1), "p1"}},
	})
	// MorphKey 未显式设置 → 类型列取 morph map 别名 "post"（而非表名 posts）
	relatedSQL := "select * from `comments` where `commentable_type` = ? and `commentable_id` in (?)"
	st.On(relatedSQL, &fakeResult{
		Columns: []string{"id", "body", "commentable_id", "commentable_type"},
		Rows:   [][]driver.Value{{int64(9), "first", int64(1), "post"}},
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
		Rows:   [][]driver.Value{{int64(1), "videos", int64(7)}},
	})
	st.On("select * from `videos` where `id` in (?)", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:   [][]driver.Value{{int64(7), "v7"}},
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
