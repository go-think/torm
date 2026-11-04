package torm

import (
	"database/sql/driver"
	"reflect"
	"testing"

	"github.com/thinkoner/torm/grammar"
	"github.com/thinkoner/torm/query"
)

func asAny(vals []driver.Value) []any {
	out := make([]any, len(vals))
	for i, v := range vals {
		out[i] = v
	}
	return out
}

// 执行级 golden：断言 Connection 实际收到的 SQL 与绑定。
// 期望值逐条来自 laravel/framework DatabaseQueryBuilderTest 中
// ->getConnection()->expects('insert/update/delete')->with(...) 的断言。

func execBaseBuilder(t *testing.T, prefix ...string) (*Builder, *fakeStore) {
	t.Helper()
	conn, st := newFakeDB(t)
	g := grammar.NewQueryGrammar()
	if len(prefix) > 0 {
		g.SetTablePrefix(prefix[0])
	}
	b := conn.Query()
	b.grammar = g
	return b, st
}

func execMySQLBuilder(t *testing.T) (*Builder, *fakeStore) {
	t.Helper()
	conn, st := newFakeDB(t)
	b := conn.Query()
	b.grammar = grammar.NewMySqlGrammar()
	return b, st
}

func TestExecInsert(t *testing.T) {
	// insert into "users" ("email") values (?)  [foo]
	b, st := execBaseBuilder(t)
	st.On(`insert into "users" ("email") values (?)`, &fakeResult{Affected: 1})
	if _, _, err := b.From("users").Insert(map[string]interface{}{"email": "foo"}); err != nil {
		t.Fatal(err)
	}
	call := st.LastCall(t, `insert into "users" ("email") values (?)`)
	if !reflect.DeepEqual(asAny(call.Args), []any{"foo"}) {
		t.Errorf("args = %#v", call.Args)
	}

	// 多行、列序确定性（对应 Laravel upsert 用例的列序）
	bm, stm := execMySQLBuilder(t)
	sql := "insert into `users` (`email`, `name`) values (?, ?), (?, ?)"
	stm.On(sql, &fakeResult{Affected: 2})
	if _, _, err := bm.From("users").Inserts([]interface{}{
		query.Row{{Key: "email", Value: "foo"}, {Key: "name", Value: "bar"}},
		query.Row{{Key: "name", Value: "bar2"}, {Key: "email", Value: "foo2"}},
	}); err != nil {
		t.Fatal(err)
	}
	call = stm.LastCall(t, sql)
	if !reflect.DeepEqual(asAny(call.Args), []any{"foo", "bar", "foo2", "bar2"}) {
		t.Errorf("args = %#v", call.Args)
	}

	// 表达式值不进绑定 + 列序保持（Laravel: insertGetIdMethodRemovesExpressions）
	b2, st2 := execBaseBuilder(t)
	sql2 := `insert into "users" ("email", "bar") values (?, bar)`
	st2.On(sql2, &fakeResult{Affected: 1, LastID: 1})
	if _, err := b2.From("users").InsertGetId(query.Row{
		{Key: "email", Value: "foo"},
		{Key: "bar", Value: grammar.NewExpression("bar")},
	}, "id"); err != nil {
		t.Fatal(err)
	}
	call = st2.LastCall(t, sql2)
	if !reflect.DeepEqual(asAny(call.Args), []any{"foo"}) {
		t.Errorf("args = %#v", call.Args)
	}
}

func TestExecInsertGetId(t *testing.T) {
	b, st := execBaseBuilder(t)
	sql := `insert into "users" ("email") values (?)`
	st.On(sql, &fakeResult{Affected: 1, LastID: 7})
	id, err := b.From("users").InsertGetId(map[string]interface{}{"email": "foo"}, "id")
	if err != nil {
		t.Fatal(err)
	}
	if id != 7 {
		t.Errorf("id = %d", id)
	}

	// MySQL 空值 insert（Laravel: insertGetIdWithEmptyValues）
	bm, stm := execMySQLBuilder(t)
	sqlm := "insert into `users` () values ()"
	stm.On(sqlm, &fakeResult{Affected: 1, LastID: 1})
	if _, err := bm.From("users").InsertGetId(map[string]interface{}{}); err != nil {
		t.Fatal(err)
	}
	stm.LastCall(t, sqlm)
}

func TestExecInsertOrIgnore(t *testing.T) {
	bm, stm := execMySQLBuilder(t)
	sql := "insert ignore into `users` (`email`) values (?)"
	stm.On(sql, &fakeResult{Affected: 0})
	if _, err := bm.From("users").InsertOrIgnore([]interface{}{map[string]interface{}{"email": "foo"}}); err != nil {
		t.Fatal(err)
	}
	stm.LastCall(t, sql)
}

func TestExecInsertUsing(t *testing.T) {
	b, st := execBaseBuilder(t)
	sql := `insert into "table1" ("foo") select "bar" from "table2" where "foreign_id" = ?`
	st.On(sql, &fakeResult{Affected: 1})
	if _, err := b.From("table1").InsertUsing([]string{"foo"}, func(nq *Builder) *Builder {
		return nq.Select("bar").From("table2").Where("foreign_id", "=", 5)
	}); err != nil {
		t.Fatal(err)
	}
	call := st.LastCall(t, sql)
	if !reflect.DeepEqual(asAny(call.Args), []any{int64(5)}) {
		t.Errorf("args = %#v", call.Args)
	}

	// 空列 → insert into "table1" select * ...
	b2, st2 := execBaseBuilder(t)
	sql2 := `insert into "table1" select * from "table2" where "foreign_id" = ?`
	st2.On(sql2, &fakeResult{Affected: 1})
	if _, err := b2.From("table1").InsertUsing(nil, func(nq *Builder) *Builder {
		return nq.From("table2").Where("foreign_id", "=", 5)
	}); err != nil {
		t.Fatal(err)
	}
	st2.LastCall(t, sql2)
}

func TestExecUpsert(t *testing.T) {
	// MySQL on duplicate key update（use_upsert_alias=false 默认路径）
	bm, stm := execMySQLBuilder(t)
	sql := "insert into `users` (`email`, `name`) values (?, ?), (?, ?) on duplicate key update `email` = values(`email`), `name` = values(`name`)"
	stm.On(sql, &fakeResult{Affected: 2})
	if _, err := bm.From("users").Upsert([]interface{}{
		query.Row{{Key: "email", Value: "foo"}, {Key: "name", Value: "bar"}},
		query.Row{{Key: "name", Value: "bar2"}, {Key: "email", Value: "foo2"}},
	}, []string{"email"}, []string{"email", "name"}); err != nil {
		t.Fatal(err)
	}
	call := stm.LastCall(t, sql)
	if !reflect.DeepEqual(asAny(call.Args), []any{"foo", "bar", "foo2", "bar2"}) {
		t.Errorf("args = %#v", call.Args)
	}

	// 指定更新列
	bm2, stm2 := execMySQLBuilder(t)
	sql2 := "insert into `users` (`email`, `name`) values (?, ?), (?, ?) on duplicate key update `name` = values(`name`)"
	stm2.On(sql2, &fakeResult{Affected: 2})
	if _, err := bm2.From("users").Upsert([]interface{}{
		query.Row{{Key: "email", Value: "foo"}, {Key: "name", Value: "bar"}},
		query.Row{{Key: "name", Value: "bar2"}, {Key: "email", Value: "foo2"}},
	}, []string{"email"}, []string{"name"}); err != nil {
		t.Fatal(err)
	}
	stm2.LastCall(t, sql2)
}

func TestExecUpdate(t *testing.T) {
	// 基础 update：绑定顺序 = set 值（列序） + where
	b, st := execBaseBuilder(t)
	sql := `update "users" set "email" = ?, "name" = ? where "id" = ?`
	st.On(sql, &fakeResult{Affected: 1})
	if _, err := b.From("users").Where("id", "=", 1).Update(map[string]interface{}{"email": "foo", "name": "bar"}); err != nil {
		t.Fatal(err)
	}
	call := st.LastCall(t, sql)
	if !reflect.DeepEqual(asAny(call.Args), []any{"foo", "bar", int64(1)}) {
		t.Errorf("args = %#v", call.Args)
	}

	// MySQL：update 带 order by + limit
	bm, stm := execMySQLBuilder(t)
	sqlm := "update `users` set `email` = ?, `name` = ? where `id` = ? order by `foo` desc limit 5"
	stm.On(sqlm, &fakeResult{Affected: 1})
	if _, err := bm.From("users").Where("id", "=", 1).OrderBy("foo", "desc").Limit(5).
		Update(map[string]interface{}{"email": "foo", "name": "bar"}); err != nil {
		t.Fatal(err)
	}
	call = stm.LastCall(t, sqlm)
	if !reflect.DeepEqual(asAny(call.Args), []any{"foo", "bar", int64(1)}) {
		t.Errorf("args = %#v", call.Args)
	}

	// Raw 值不进绑定（Laravel: testUpdateMethodRespectsRaw）
	b2, st2 := execBaseBuilder(t)
	sql2 := `update "users" set "email" = foo where "id" = ?`
	st2.On(sql2, &fakeResult{Affected: 1})
	if _, err := b2.From("users").Where("id", "=", 1).Update(map[string]interface{}{
		"email": grammar.NewExpression("foo"),
	}); err != nil {
		t.Fatal(err)
	}
	call = st2.LastCall(t, sql2)
	if !reflect.DeepEqual(asAny(call.Args), []any{int64(1)}) {
		t.Errorf("args = %#v", call.Args)
	}
}

func TestExecIncrementDecrement(t *testing.T) {
	b, st := execBaseBuilder(t)
	sql := `update "users" set "balance" = "balance" + 1 where "id" = ?`
	st.On(sql, &fakeResult{Affected: 1})
	if _, err := b.From("users").Where("id", "=", 1).Increment("balance"); err != nil {
		t.Fatal(err)
	}
	call := st.LastCall(t, sql)
	if !reflect.DeepEqual(asAny(call.Args), []any{int64(1)}) {
		t.Errorf("args = %#v", call.Args)
	}

	b2, st2 := execBaseBuilder(t)
	sql2 := `update "users" set "balance" = "balance" - 5 where "id" = ?`
	st2.On(sql2, &fakeResult{Affected: 1})
	if _, err := b2.From("users").Where("id", "=", 1).Decrement("balance", 5); err != nil {
		t.Fatal(err)
	}
	st2.LastCall(t, sql2)

	// 附加列
	b3, st3 := execBaseBuilder(t)
	sql3 := `update "users" set "balance" = "balance" + 2, "name" = ? where "id" = ?`
	st3.On(sql3, &fakeResult{Affected: 1})
	if _, err := b3.From("users").Where("id", "=", 1).Increment("balance", 2, map[string]interface{}{"name": "x"}); err != nil {
		t.Fatal(err)
	}
	call = st3.LastCall(t, sql3)
	if !reflect.DeepEqual(asAny(call.Args), []any{"x", int64(1)}) {
		t.Errorf("args = %#v", call.Args)
	}
}

func TestExecDelete(t *testing.T) {
	// 带条件删除
	b, st := execBaseBuilder(t)
	sql := `delete from "users" where "email" = ?`
	st.On(sql, &fakeResult{Affected: 1})
	if _, err := b.From("users").Where("email", "=", "foo").Delete(); err != nil {
		t.Fatal(err)
	}
	call := st.LastCall(t, sql)
	if !reflect.DeepEqual(asAny(call.Args), []any{"foo"}) {
		t.Errorf("args = %#v", call.Args)
	}

	// delete(id)：where 限定主键且绑定只含 where 段（select 段被排除）
	b2, st2 := execBaseBuilder(t)
	sql2 := `delete from "users" where "users"."id" = ?`
	st2.On(`select ? from "users"`, &fakeResult{Columns: []string{"?"}, Rows: [][]driver.Value{{"ignore"}}})
	st2.On(sql2, &fakeResult{Affected: 1})
	if _, err := b2.From("users").SelectRaw("?", "ignore").Delete(1); err != nil {
		t.Fatal(err)
	}
	call = st2.LastCall(t, sql2)
	if !reflect.DeepEqual(asAny(call.Args), []any{int64(1)}) {
		t.Errorf("args = %#v", call.Args)
	}
}

func TestExecTruncate(t *testing.T) {
	b, st := execBaseBuilder(t)
	st.On(`truncate table "users"`, &fakeResult{})
	if err := b.From("users").Truncate(); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, `truncate table "users"`)
}

func TestExecExists(t *testing.T) {
	b, st := execBaseBuilder(t)
	sql := `select exists(select * from "users") as "exists"`
	st.On(sql, &fakeResult{Columns: []string{"exists"}, Rows: [][]driver.Value{{int64(1)}}})
	got, err := b.From("users").Exists()
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("expect exists")
	}

	b2, st2 := execBaseBuilder(t)
	st2.On(sql, &fakeResult{Columns: []string{"exists"}, Rows: [][]driver.Value{{int64(0)}}})
	got, err = b2.From("users").DoesntExist()
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("expect doesntExist")
	}
}
