package torm

import (
	"database/sql/driver"
	"errors"
	"reflect"
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

// ---- R3-A: 分页族 ----

type pageUser struct {
	Id   int64  `torm:"column:id"`
	Name string `torm:"column:name"`
}

func TestFetchPaginate(t *testing.T) {
	t.Run("first page", func(t *testing.T) {
		b, st := execBaseBuilder(t)
		countSQL := `select count(*) as "aggregate" from "users"`
		pageSQL := `select * from "users" limit 15 offset 0`
		st.On(countSQL, &fakeResult{Columns: []string{"aggregate"}, Rows: [][]driver.Value{{int64(21)}}})
		st.On(pageSQL, &fakeResult{Columns: []string{"id", "name"}, Rows: [][]driver.Value{{int64(1), "a"}, {int64(2), "b"}}})

		var users []*pageUser
		p, err := b.From("users").Paginate(&users, 1, 15)
		if err != nil {
			t.Fatal(err)
		}
		if p.Total != 21 || p.PerPage != 15 || p.CurrentPage != 1 || p.LastPage != 2 {
			t.Errorf("paginator = %+v", p)
		}
		if len(users) != 2 {
			t.Errorf("items = %d", len(users))
		}
	})

	t.Run("second page", func(t *testing.T) {
		b, st := execBaseBuilder(t)
		countSQL := `select count(*) as "aggregate" from "users"`
		st.On(countSQL, &fakeResult{Columns: []string{"aggregate"}, Rows: [][]driver.Value{{int64(21)}}})
		page2 := `select * from "users" limit 15 offset 15`
		st.On(page2, &fakeResult{Columns: []string{"id", "name"}, Rows: [][]driver.Value{{int64(16), "p"}}})

		var users2 []*pageUser
		p2, err := b.From("users").Paginate(&users2, 2, 15)
		if err != nil {
			t.Fatal(err)
		}
		st.LastCall(t, page2)
		if p2.LastPage != 2 || p2.CurrentPage != 2 {
			t.Errorf("paginator = %+v", p2)
		}
	})

	t.Run("zero total skips fetch", func(t *testing.T) {
		b, st := execBaseBuilder(t)
		countSQL := `select count(*) as "aggregate" from "users"`
		st.On(countSQL, &fakeResult{Columns: []string{"aggregate"}, Rows: [][]driver.Value{{int64(0)}}})
		var users3 []*pageUser
		p3, err := b.From("users").Paginate(&users3, 1, 15)
		if err != nil {
			t.Fatal(err)
		}
		if p3.Total != 0 || p3.LastPage != 1 {
			t.Errorf("paginator = %+v", p3)
		}
	})
}

func TestFetchGetCountForPaginationWithGroups(t *testing.T) {
	b, st := execBaseBuilder(t)
	// group by 时包裹子查询计数
	inner := `select * from "users" group by "account_id"`
	countSQL := `select count(*) as "aggregate" from (` + inner + `) as "aggregate_table"`
	st.On(countSQL, &fakeResult{Columns: []string{"aggregate"}, Rows: [][]driver.Value{{int64(7)}}})

	n, err := b.From("users").GroupBy("account_id").GetCountForPagination()
	if err != nil {
		t.Fatal(err)
	}
	if n != 7 {
		t.Errorf("count = %d", n)
	}
	st.LastCall(t, countSQL)
}

func TestFetchSimplePaginate(t *testing.T) {
	b, st := execBaseBuilder(t)
	sql := `select * from "users" limit 3 offset 0`
	st.On(sql, &fakeResult{Columns: []string{"id", "name"}, Rows: [][]driver.Value{
		{int64(1), "a"}, {int64(2), "b"}, {int64(3), "c"},
	}})

	var users []*pageUser
	p, err := b.From("users").SimplePaginate(&users, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !p.HasMore {
		t.Error("expect hasMore")
	}
	// 多取的一条被截断
	if len(users) != 2 {
		t.Errorf("items = %d", len(users))
	}
	if p.PerPage != 2 || p.CurrentPage != 1 {
		t.Errorf("paginator = %+v", p)
	}
}

func TestFetchCursorPaginate(t *testing.T) {
	b, st := execBaseBuilder(t)
	sql := `select * from "users" order by "id" desc limit 3`
	st.On(sql, &fakeResult{Columns: []string{"id", "name"}, Rows: [][]driver.Value{
		{int64(5), "e"}, {int64(4), "d"}, {int64(3), "c"},
	}})

	var users []*pageUser
	p, err := b.From("users").OrderByDesc("id").CursorPaginate(&users, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !p.HasMore {
		t.Error("expect hasMore")
	}
	if len(users) != 2 || users[0].Id != 5 {
		t.Errorf("items = %+v", users)
	}
	if p.NextCursor == nil {
		t.Fatal("expect next cursor")
	}
	encoded := p.NextCursor.Encode()
	// 游标可解码回参数（base64(json)）
	cur := CursorFromEncoded(encoded)
	if cur == nil {
		t.Fatal("cursor roundtrip failed")
	}
	if v, ok := cur.Parameter("id").(int64); !ok || v != 4 {
		// next 游标取截断后页内末行（id=4），下一页严格小于 4 →
		// 旧实现取 extra 行（id=3）会导致每页边界丢一行
		t.Errorf("cursor param = %#v", cur.Parameter("id"))
	}
	if !cur.PointsToNextItems() {
		t.Error("expect points to next items")
	}

	// 带游标的下一页：id < 4（游标参数经 json 解码后数值保持整型）
	sql2 := `select * from "users" where ("id" < ?) order by "id" desc limit 3`
	st.On(sql2, &fakeResult{Columns: []string{"id", "name"}, Rows: [][]driver.Value{
		{int64(3), "c"}, {int64(2), "b"},
	}})
	var users2 []*pageUser
	p2, err := b.Clone().CursorPaginate(&users2, 2, cur)
	if err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, sql2)
	if p2.HasMore {
		t.Error("expect no more")
	}
	call := st.LastCall(t, sql2)
	if !reflect.DeepEqual(asAny(call.Args), []any{int64(4)}) {
		t.Errorf("args = %#v", call.Args)
	}
}

func TestFetchCursorPaginateRequiresOrderBy(t *testing.T) {
	b, _ := execBaseBuilder(t)
	var users []*pageUser
	defer func() {
		if recover() == nil {
			t.Error("expect panic without order by")
		}
	}()
	_, _ = b.From("users").CursorPaginate(&users, 2)
}

// ---- R3-A: Find / FindOr / SoleValue / ExistsOr ----

func TestFetchFindAndFindOr(t *testing.T) {
	t.Run("find hit", func(t *testing.T) {
		b, st := execBaseBuilder(t)
		sql := `select * from "users" where "id" = ? limit 1`
		st.On(sql, &fakeResult{Columns: []string{"id", "name"}, Rows: [][]driver.Value{{int64(1), "Andrew"}}})
		var u pageUser
		if err := b.From("users").Find(1, &u); err != nil {
			t.Fatal(err)
		}
		if u.Name != "Andrew" {
			t.Errorf("user = %+v", u)
		}
	})

	t.Run("find or hit", func(t *testing.T) {
		b, st := execBaseBuilder(t)
		sql := `select * from "users" where "id" = ? limit 1`
		st.On(sql, &fakeResult{Columns: []string{"id", "name"}, Rows: [][]driver.Value{{int64(1), "Andrew"}}})
		var u2 pageUser
		if err := b.From("users").FindOr(1, &u2, func() error { t.Error("fallback should not run"); return nil }); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("find or miss runs fallback", func(t *testing.T) {
		b, st := execBaseBuilder(t)
		sql := `select * from "users" where "id" = ? limit 1`
		st.On(sql, &fakeResult{Columns: []string{"id", "name"}, Rows: nil})
		var u3 pageUser
		fallbackRan := false
		if err := b.From("users").FindOr(99, &u3, func() error { fallbackRan = true; return nil }); err != nil {
			t.Fatal(err)
		}
		if !fallbackRan {
			t.Error("fallback not invoked")
		}
	})
}

func TestFetchSoleValue(t *testing.T) {
	t.Run("single row", func(t *testing.T) {
		b, st := execBaseBuilder(t)
		sql := `select "email" from "users" where "id" = ? limit 2`
		st.On(sql, &fakeResult{Columns: []string{"email"}, Rows: [][]driver.Value{{"foo@bar"}}})
		v, err := b.From("users").Where("id", "=", 1).SoleValue("email")
		if err != nil {
			t.Fatal(err)
		}
		if v != "foo@bar" {
			t.Errorf("value = %v", v)
		}
	})

	t.Run("multiple rows", func(t *testing.T) {
		b, st := execBaseBuilder(t)
		sql := `select "email" from "users" where "id" = ? limit 2`
		st.On(sql, &fakeResult{Columns: []string{"email"}, Rows: [][]driver.Value{{"a"}, {"b"}}})
		if _, err := b.From("users").Where("id", "=", 1).SoleValue("email"); err == nil {
			t.Error("expect multiple error")
		}
	})

	t.Run("no rows", func(t *testing.T) {
		b, st := execBaseBuilder(t)
		sql := `select "email" from "users" where "id" = ? limit 2`
		st.On(sql, &fakeResult{Columns: []string{"email"}, Rows: nil})
		if _, err := b.From("users").Where("id", "=", 1).SoleValue("email"); err == nil {
			t.Error("expect not found error")
		}
	})
}

func TestFetchExistsOr(t *testing.T) {
	b, st := execBaseBuilder(t)
	sql := `select exists(select * from "users") as "exists"`
	st.On(sql, &fakeResult{Columns: []string{"exists"}, Rows: [][]driver.Value{{int64(1)}}})

	ok, err := b.From("users").ExistsOr(func() error { t.Error("callback should not run"); return nil })
	if err != nil || !ok {
		t.Errorf("existsOr = %v, %v", ok, err)
	}

	b2, st2 := execBaseBuilder(t)
	st2.On(sql, &fakeResult{Columns: []string{"exists"}, Rows: [][]driver.Value{{int64(0)}}})
	ran := false
	ok, err = b2.From("users").DoesntExistOr(func() error { ran = true; return nil })
	// 不存在 → 直接返回 true，不执行回调（doesntExistOr 语义）
	if err != nil || !ok || ran {
		t.Errorf("doesntExistOr = %v, %v, ran=%v", ok, err, ran)
	}
}
