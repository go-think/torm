package torm

import (
	"testing"
)

// ---- Factory 增强测试（对照 Factories/Factory.php）----

type FacTag struct {
	ID     int64  `torm:"primary_key;column:id"`
	Name   string `torm:"column:name"`
	PostID int64  `torm:"column:post_id"`
}

func (t *FacTag) TableName() string { return "fac_tags" }

func newPostFactory() Factory[*Post] {
	return Factory[*Post]{
		Definition: func(seq int) *Post {
			return &Post{Title: "post-" + itoa(seq)}
		},
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func TestFactoryStateAndAfterMaking(t *testing.T) {
	published := false
	f := newPostFactory().
		State("published", func(p *Post) { p.Title = "published-" + p.Title }).
		AfterMaking(func(p *Post) { published = true })

	made := f.Make(2)
	if len(made) != 2 {
		t.Fatalf("make = %d", len(made))
	}
	if made[0].Title != "published-post-1" || made[1].Title != "published-post-2" {
		t.Errorf("state not applied: %q %q", made[0].Title, made[1].Title)
	}
	if !published {
		t.Error("afterMaking hook not fired")
	}
	// 值语义：原工厂不受影响
	plain := newPostFactory().Make(1)
	if plain[0].Title != "post-1" {
		t.Errorf("original factory mutated: %q", plain[0].Title)
	}
}

func TestFactorySequenceRotates(t *testing.T) {
	f := newPostFactory().Sequence(func(seq int, p *Post) {
		p.Title = "seq-" + itoa(seq%2)
	})
	made := f.Make(3)
	if made[0].Title != "seq-1" || made[1].Title != "seq-0" || made[2].Title != "seq-1" {
		t.Errorf("sequence rotation = %q %q %q", made[0].Title, made[1].Title, made[2].Title)
	}

	f2 := newPostFactory().SequenceAttrs(
		map[string]any{"title": "first"},
		map[string]any{"title": "second"},
	)
	m2 := f2.Make(2)
	if m2[0].Title != "first" || m2[1].Title != "second" {
		t.Errorf("sequenceAttrs = %q %q", m2[0].Title, m2[1].Title)
	}
}

type FacComment struct {
	ID     int64  `torm:"primary_key;column:id"`
	Body   string `torm:"column:body"`
	PostID int64  `torm:"column:post_id"`
}

func (c *FacComment) TableName() string { return "fac_comments" }

func TestFactoryForSetsForeignKey(t *testing.T) {
	parent := &Post{ID: 42, Title: "parent"}
	f := Factory[*FacComment]{
		Definition: func(seq int) *FacComment { return &FacComment{Body: "c"} },
	}.For(parent)
	made := f.Make(1)
	if made[0].PostID != 42 {
		t.Errorf("for fk = %d", made[0].PostID)
	}
	// 显式外键列名
	f2 := Factory[*FacComment]{
		Definition: func(seq int) *FacComment { return &FacComment{Body: "c"} },
	}.For(parent, "post_id")
	if made2 := f2.Make(1); made2[0].PostID != 42 {
		t.Errorf("for explicit fk = %d", made2[0].PostID)
	}
}

func TestFactoryCountAndMakeManyCreateMany(t *testing.T) {
	f := newPostFactory().Count(3)
	if got := len(f.MakeMany()); got != 3 {
		t.Errorf("makeMany = %d", got)
	}

	conn, st := newFakeDB(t)
	insertSQL := "insert into `posts` (`created_at`, `title`, `updated_at`) values (?, ?, ?)"
	st.On(insertSQL, &fakeResult{Affected: 1, LastID: 1})
	created, err := f.CreateMany(conn)
	if err != nil || len(created) != 3 {
		t.Fatalf("createMany = %d err=%v", len(created), err)
	}
	inserts := 0
	for _, call := range st.Calls() {
		if call.Sql == insertSQL {
			inserts++
		}
	}
	if inserts != 3 {
		t.Errorf("inserts = %d", inserts)
	}
}

func TestFactoryAfterCreatingHook(t *testing.T) {
	conn, st := newFakeDB(t)
	insertSQL := "insert into `posts` (`created_at`, `title`, `updated_at`) values (?, ?, ?)"
	st.On(insertSQL, &fakeResult{Affected: 1, LastID: 7})

	var seenID int64
	f := newPostFactory().AfterCreating(func(p *Post) { seenID = p.ID })
	created, err := f.Create(conn, 1)
	if err != nil {
		t.Fatal(err)
	}
	if created[0].ID != 7 || seenID != 7 {
		t.Errorf("afterCreating id = %d/%d", created[0].ID, seenID)
	}
}

func TestFactoryHasCreatesChildren(t *testing.T) {
	conn, st := newFakeDB(t)
	postInsert := "insert into `posts` (`created_at`, `title`, `updated_at`) values (?, ?, ?)"
	st.On(postInsert, &fakeResult{Affected: 1, LastID: 5})
	tagInsert := "insert into `fac_tags` (`name`, `post_id`) values (?, ?)"
	st.On(tagInsert, &fakeResult{Affected: 1, LastID: 1})

	childFactory := Factory[*FacTag]{
		Definition: func(seq int) *FacTag { return &FacTag{Name: "tag-" + itoa(seq)} },
	}
	parent, children, err := FactoryHas(conn, newPostFactory(), childFactory, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if parent.ID != 5 {
		t.Errorf("parent id = %d", parent.ID)
	}
	if len(children) != 2 {
		t.Fatalf("children = %d", len(children))
	}
	for _, c := range children {
		if c.ID != 1 {
			// LastID 固定为 1，两个子模型同 id 可接受（fake 驱动限制）
			break
		}
	}
	// 子模型外键已回填为父键 5
	if children[0].Name != "tag-1" || children[1].Name != "tag-2" {
		t.Errorf("children = %+v", children)
	}
	tagCalls := 0
	for _, call := range st.Calls() {
		if call.Sql == tagInsert {
			tagCalls++
			if len(call.Args) != 2 || call.Args[0] != "tag-"+itoa(tagCalls) || call.Args[1] != int64(5) {
				t.Errorf("tag args = %#v", call.Args)
			}
		}
	}
	if tagCalls != 2 {
		t.Errorf("tag inserts = %d", tagCalls)
	}
}

func TestFactoryLegacyMakeCreateCompat(t *testing.T) {
	f := newPostFactory()
	if got := len(f.Make(2)); got != 2 {
		t.Errorf("make(2) = %d", got)
	}
	if got := len(f.Make(0)); got != 0 {
		t.Errorf("make(0) = %d", got)
	}
	conn, st := newFakeDB(t)
	insertSQL := "insert into `posts` (`created_at`, `title`, `updated_at`) values (?, ?, ?)"
	st.On(insertSQL, &fakeResult{Affected: 1, LastID: 1})
	if _, err := f.Create(conn, 2); err != nil {
		t.Fatal(err)
	}
	one, err := f.CreateOne(conn)
	if err != nil || one.ID != 1 {
		t.Errorf("createOne = %+v err=%v", one, err)
	}
}
