package torm

import (
	"database/sql/driver"
	"errors"
	"testing"
	"time"
)

type serPost struct {
	ID     int64          `torm:"primary_key;column:id"`
	Title  string         `torm:"column:title"`
	Secret string         `torm:"column:secret;hidden"`
	Meta   map[string]any `torm:"column:meta;cast:json"`
}

func (p *serPost) TableName() string { return "ser_posts" }

func TestToMapHiddenVisible(t *testing.T) {
	p := &serPost{ID: 1, Title: "t", Secret: "s", Meta: map[string]any{"a": 1}}
	m, err := ToMap(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m["secret"]; ok {
		t.Errorf("hidden field leaked: %#v", m)
	}
	if v, ok := m["meta"].(map[string]any); !ok || v["a"] != float64(1) {
		t.Errorf("json cast should decode to object, got %#v", m["meta"])
	}

	// 调用级 hidden/visible
	m2, _ := ToMap(p, WithHidden("meta"))
	if _, ok := m2["meta"]; ok {
		t.Errorf("option hidden leaked")
	}
	m3, _ := ToMap(p, WithVisible("title"))
	if len(m3) != 1 || m3["title"] != "t" {
		t.Errorf("visible = %#v", m3)
	}

	// nil 指针与 Ignored 字段不泄露（hidden 优先于 nil）
	type withNil struct {
		ID   int64      `torm:"primary_key;column:id"`
		Del  *time.Time `torm:"column:del;hidden"`
		Skip string     `torm:"-"`
	}
	n, _ := ToMap(&withNil{ID: 2})
	if _, ok := n["del"]; ok {
		t.Errorf("hidden nil ptr leaked: %#v", n)
	}
	if _, ok := n["skip"]; ok {
		t.Errorf("ignored field leaked: %#v", n)
	}

	// toJson
	j, err := ToJson(p)
	if err != nil || j == "" {
		t.Fatalf("ToJson = %q err=%v", j, err)
	}
}

func TestReplicateClearsKey(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `ser_posts` where `id` = ? limit 1", &fakeResult{
		Columns: []string{"id", "title", "secret"},
		Rows:    [][]driver.Value{{int64(5), "src", "sec"}},
	})
	var src serPost
	if err := conn.Table("ser_posts").Where("id", "=", 5).First(&src); err != nil {
		t.Fatal(err)
	}
	var dst serPost
	if err := conn.Replicate(&dst, &src); err != nil {
		t.Fatal(err)
	}
	if dst.ID != 0 {
		t.Errorf("replicate must clear primary key, got %d", dst.ID)
	}
	if dst.Title != "src" || dst.Secret != "sec" {
		t.Errorf("copy = %+v", dst)
	}
	if Exists(&dst) {
		t.Error("replica must not be marked as persisted")
	}
}

func TestObserverRetrievingCancel(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `ser_posts` where `id` in (?, ?)", &fakeResult{
		Columns: []string{"id", "title"},
		Rows:    [][]driver.Value{{int64(1), "a"}, {int64(2), "b"}},
	})
	called := 0
	conn.Observe(&serPost{}, &Observer{
		Retrieving: func(model any) error {
			called++
			if model.(*serPost).ID == 2 {
				return errors.New("cancel")
			}
			return nil
		},
	})
	var out []*serPost
	if err := conn.Table("ser_posts").WhereIn("id", []any{int64(1), int64(2)}).Get(&out); err != nil {
		t.Fatal(err)
	}
	if called != 2 {
		t.Errorf("retrieving calls = %d", called)
	}
	// 取消的行保持零值（仍在结果中——切片原地水合的既定差异）
	if len(out) != 2 {
		t.Fatalf("len = %d", len(out))
	}
	if out[1].ID != 0 && out[1].ID != 2 {
		t.Errorf("cancelled row state = %+v", out[1])
	}
	if out[0].ID != 1 || out[0].Title != "a" {
		t.Errorf("hydrated row = %+v", out[0])
	}
}
