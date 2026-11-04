package torm

import (
	"database/sql/driver"
	"errors"
	"testing"
)

func TestFetchValue(t *testing.T) {
	b, st := execBaseBuilder(t)
	sql := `select "foo" from "users" where "id" = ? limit 1`
	st.On(sql, &fakeResult{Columns: []string{"foo"}, Rows: [][]driver.Value{{"bar"}}})

	v, err := b.From("users").Where("id", "=", 1).Value("foo")
	if err != nil {
		t.Fatal(err)
	}
	if v != "bar" {
		t.Errorf("value = %v", v)
	}
}

func TestFetchRawValue(t *testing.T) {
	b, st := execBaseBuilder(t)
	sql := `select UPPER("foo") from "users" where "id" = ? limit 1`
	st.On(sql, &fakeResult{Columns: []string{`UPPER("foo")`}, Rows: [][]driver.Value{{"BAR"}}})

	v, err := b.From("users").Where("id", "=", 1).RawValue(`UPPER("foo")`)
	if err != nil {
		t.Fatal(err)
	}
	if v != "BAR" {
		t.Errorf("value = %v", v)
	}
}

func TestFetchPluckImplode(t *testing.T) {
	b, st := execBaseBuilder(t)
	sql := `select "foo" from "users"`
	st.On(sql, &fakeResult{Columns: []string{"foo"}, Rows: [][]driver.Value{{"bar"}, {"baz"}}})

	var out []string
	if err := b.From("users").Pluck("foo", &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0] != "bar" || out[1] != "baz" {
		t.Errorf("pluck = %v", out)
	}

	s, err := b.From("users").Implode("foo", ",")
	if err != nil {
		t.Fatal(err)
	}
	if s != "bar,baz" {
		t.Errorf("implode = %s", s)
	}
}

func TestFetchSole(t *testing.T) {
	b, st := execBaseBuilder(t)
	sql := `select * from "users" where "email" = ?`
	st.On(sql, &fakeResult{Columns: []string{"id", "name"}, Rows: [][]driver.Value{{int64(1), "A"}}})

	var u User
	if err := b.From("users").Where("email", "=", "a@x.com").Sole(&u); err != nil {
		t.Fatal(err)
	}
	if u.Id != 1 || u.Name != "A" {
		t.Errorf("user = %+v", u)
	}

	// 多行 → ErrMultiple
	b2, st2 := execBaseBuilder(t)
	st2.On(sql, &fakeResult{Columns: []string{"id", "name"}, Rows: [][]driver.Value{{int64(1), "A"}, {int64(2), "B"}}})
	err := b2.From("users").Where("email", "=", "a@x.com").Sole(&u)
	var multi ErrMultiple
	if !errors.As(err, &multi) {
		t.Errorf("expect ErrMultiple, got %v", err)
	}

	// 零行 → ErrNotFound
	b3, st3 := execBaseBuilder(t)
	st3.On(sql, &fakeResult{Columns: []string{"id", "name"}, Rows: [][]driver.Value{}})
	err = b3.From("users").Where("email", "=", "a@x.com").Sole(&u)
	var notFound ErrNotFound
	if !errors.As(err, &notFound) {
		t.Errorf("expect ErrNotFound, got %v", err)
	}
}

func TestFetchFirstOrFail(t *testing.T) {
	b, st := execBaseBuilder(t)
	sql := `select * from "users" where "email" = ? limit 1`
	st.On(sql, &fakeResult{Columns: []string{"id", "name"}, Rows: [][]driver.Value{}})

	var u User
	err := b.From("users").Where("email", "=", "a@x.com").FirstOrFail(&u)
	var notFound ErrNotFound
	if !errors.As(err, &notFound) {
		t.Errorf("expect ErrNotFound, got %v", err)
	}
}

func TestFetchFirstOr(t *testing.T) {
	b, st := execBaseBuilder(t)
	sql := `select * from "users" where "email" = ? limit 1`
	st.On(sql, &fakeResult{Columns: []string{"id", "name"}, Rows: [][]driver.Value{}})

	fallbackRan := false
	err := b.From("users").Where("email", "=", "a@x.com").FirstOr(&User{}, func() error {
		fallbackRan = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !fallbackRan {
		t.Error("fallback should run when no row")
	}
}

func TestFetchChunk(t *testing.T) {
	b, st := execBaseBuilder(t)
	st.On(`select * from "users" limit 2 offset 0`, &fakeResult{Columns: []string{"id", "name"}, Rows: [][]driver.Value{{int64(1), "A"}, {int64(2), "B"}}})
	st.On(`select * from "users" limit 2 offset 2`, &fakeResult{Columns: []string{"id", "name"}, Rows: [][]driver.Value{{int64(3), "C"}}})

	var seen []int64
	pages := 0
	err := b.From("users").Chunk(2,
		func() any { return &[]User{} },
		func(page any) (bool, error) {
			pages++
			users := page.(*[]User)
			for _, u := range *users {
				seen = append(seen, u.Id)
			}
			return true, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if pages != 2 {
		t.Errorf("pages = %d", pages)
	}
	if len(seen) != 3 || seen[0] != 1 || seen[2] != 3 {
		t.Errorf("seen = %v", seen)
	}
}

func TestFetchChunkEarlyStop(t *testing.T) {
	b, st := execBaseBuilder(t)
	st.On(`select * from "users" limit 1 offset 0`, &fakeResult{Columns: []string{"id", "name"}, Rows: [][]driver.Value{{int64(1), "A"}}})

	pages := 0
	err := b.From("users").Chunk(1,
		func() any { return &[]User{} },
		func(page any) (bool, error) {
			pages++
			return false, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if pages != 1 {
		t.Errorf("pages = %d, want early stop at 1", pages)
	}
}
