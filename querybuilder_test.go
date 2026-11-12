package torm

import (
	"database/sql/driver"
	"fmt"
	"reflect"
	"testing"

	"github.com/go-think/torm/grammar"
	"github.com/go-think/torm/query"
)

// 本文件的期望 SQL 逐条移植自 laravel/framework
// tests/Database/DatabaseQueryBuilderTest.php（Laravel 13）。
// 每条用例同时在两种方言下断言：基础 Grammar（双引号）与 MySqlGrammar（反引号）。

func baseBuilder(t *testing.T, prefix ...string) *Builder {
	t.Helper()
	g := grammar.NewQueryGrammar()
	if len(prefix) > 0 {
		g.SetTablePrefix(prefix[0])
	}
	return NewBuilder(nil, g)
}

func mysqlBuilder(t *testing.T, prefix ...string) *Builder {
	t.Helper()
	g := grammar.NewMySqlGrammar()
	if len(prefix) > 0 {
		g.SetTablePrefix(prefix[0])
	}
	return NewBuilder(nil, g)
}

type goldenCase struct {
	name      string
	build     func(b *Builder) *Builder
	wantBase  string
	wantMySQL string
	bindings  []interface{}
}

func runGolden(t *testing.T, cases []goldenCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name+"/base", func(t *testing.T) {
			if tc.wantBase == "" {
				t.Skip("n/a for base")
			}
			b := tc.build(baseBuilder(t))
			if got := b.ToSql(); got != tc.wantBase {
				t.Errorf("base sql =\n  %s\nwant\n  %s", got, tc.wantBase)
			}
		})
		t.Run(tc.name+"/mysql", func(t *testing.T) {
			if tc.wantMySQL == "" {
				t.Skip("n/a for mysql")
			}
			b := tc.build(mysqlBuilder(t))
			if got := b.ToSql(); got != tc.wantMySQL {
				t.Errorf("mysql sql =\n  %s\nwant\n  %s", got, tc.wantMySQL)
			}
		})
		if tc.bindings == nil {
			continue
		}
		t.Run(tc.name+"/bindings", func(t *testing.T) {
			b := tc.build(baseBuilder(t))
			got := b.GetBindings()
			if !reflect.DeepEqual(got, tc.bindings) {
				t.Errorf("bindings = %#v, want %#v", got, tc.bindings)
			}
		})
	}
}

func TestGoldenSelects(t *testing.T) {
	runGolden(t, []goldenCase{
		{
			name:     "basic select",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users") },
			wantBase: `select * from "users"`, wantMySQL: "select * from `users`",
		},
		{
			name:     "table wrapping protects quotation marks (base)",
			build:    func(b *Builder) *Builder { return b.Select("*").From(`some"table`) },
			wantBase: `select * from "some""table"`, wantMySQL: "",
		},
		{
			name:     "table wrapping protects quotation marks (mysql)",
			build:    func(b *Builder) *Builder { return b.Select("*").From("some`table") },
			wantBase: "", wantMySQL: "select * from `some``table`",
		},
		{
			name:     "alias wrapping as whole constant",
			build:    func(b *Builder) *Builder { return b.Select("x.y as foo.bar").From("baz") },
			wantBase: `select "x"."y" as "foo.bar" from "baz"`, wantMySQL: "select `x`.`y` as `foo.bar` from `baz`",
		},
		{
			name: "adding selects",
			build: func(b *Builder) *Builder {
				return b.Select("foo").AddSelect("bar").AddSelect("baz", "boom").AddSelect("bar").From("users")
			},
			wantBase:  `select "foo", "bar", "baz", "boom" from "users"`,
			wantMySQL: "select `foo`, `bar`, `baz`, `boom` from `users`",
		},
		{
			name:      "basic select distinct",
			build:     func(b *Builder) *Builder { return b.Distinct().Select("foo", "bar").From("users") },
			wantBase:  `select distinct "foo", "bar" from "users"`,
			wantMySQL: "select distinct `foo`, `bar` from `users`",
		},
		{
			name:     "basic alias",
			build:    func(b *Builder) *Builder { return b.Select("foo as bar").From("users") },
			wantBase: `select "foo" as "bar" from "users"`, wantMySQL: "select `foo` as `bar` from `users`",
		},
		{
			name:     "basic table wrapping",
			build:    func(b *Builder) *Builder { return b.Select("*").From("public.users") },
			wantBase: `select * from "public"."users"`, wantMySQL: "select * from `public`.`users`",
		},
	})
}

// 前缀用例单独跑（需要构造带前缀的 builder）
func TestGoldenSelectWithPrefix(t *testing.T) {
	b := baseBuilder(t, "prefix_").Select("*").From("users")
	if got := b.ToSql(); got != `select * from "prefix_users"` {
		t.Errorf("sql = %s", got)
	}
	bm := mysqlBuilder(t, "prefix_").Select("*").From("users")
	if got := bm.ToSql(); got != "select * from `prefix_users`" {
		t.Errorf("sql = %s", got)
	}
}

func TestGoldenAliasWithPrefix(t *testing.T) {
	bm := mysqlBuilder(t, "prefix_").Select("*").From("users as people")
	want := "select * from `prefix_users` as `prefix_people`"
	if got := bm.ToSql(); got != want {
		t.Errorf("sql = %s, want %s", got, want)
	}
	bj := mysqlBuilder(t, "prefix_").Select("*").From("services").Join("translations AS t", "t.item_id", "=", "services.id")
	wantJoin := "select * from `prefix_services` inner join `prefix_translations` as `prefix_t` on `prefix_t`.`item_id` = `prefix_services`.`id`"
	if got := bj.ToSql(); got != wantJoin {
		t.Errorf("sql = %s, want %s", got, wantJoin)
	}
}

func TestGoldenWheres(t *testing.T) {
	runGolden(t, []goldenCase{
		{
			name:     "basic wheres",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").Where("id", "=", 1) },
			wantBase: `select * from "users" where "id" = ?`, wantMySQL: "select * from `users` where `id` = ?",
			bindings: []interface{}{1},
		},
		{
			name:     "basic wheres two args",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").Where("id", 1) },
			wantBase: `select * from "users" where "id" = ?`, wantMySQL: "select * from `users` where `id` = ?",
			bindings: []interface{}{1},
		},
		{
			name: "basic or wheres",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").Where("id", "=", 1).OrWhere("email", "=", "foo")
			},
			wantBase:  `select * from "users" where "id" = ? or "email" = ?`,
			wantMySQL: "select * from `users` where `id` = ? or `email` = ?",
			bindings:  []interface{}{1, "foo"},
		},
		{
			name: "basic where not",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereNot("name", "=", "foo").WhereNot("name", "<>", "bar")
			},
			wantBase:  `select * from "users" where not "name" = ? and not "name" <> ?`,
			wantMySQL: "select * from `users` where not `name` = ? and not `name` <> ?",
			bindings:  []interface{}{"foo", "bar"},
		},
		{
			name: "raw wheres",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereRaw("id = ? or email = ?", 1, "foo")
			},
			wantBase:  `select * from "users" where id = ? or email = ?`,
			wantMySQL: "select * from `users` where id = ? or email = ?",
			bindings:  []interface{}{1, "foo"},
		},
		{
			name: "raw or wheres",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").Where("id", "=", 1).OrWhereRaw("email = ?", "foo")
			},
			wantBase:  `select * from "users" where "id" = ? or email = ?`,
			wantMySQL: "select * from `users` where `id` = ? or email = ?",
			bindings:  []interface{}{1, "foo"},
		},
		{
			name:      "basic where ins",
			build:     func(b *Builder) *Builder { return b.Select("*").From("users").WhereIn("id", []interface{}{1, 2, 3}) },
			wantBase:  `select * from "users" where "id" in (?, ?, ?)`,
			wantMySQL: "select * from `users` where `id` in (?, ?, ?)",
			bindings:  []interface{}{1, 2, 3},
		},
		{
			name: "or where ins",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").Where("id", "=", 1).OrWhereIn("id", []interface{}{1, 2, 3})
			},
			wantBase:  `select * from "users" where "id" = ? or "id" in (?, ?, ?)`,
			wantMySQL: "select * from `users` where `id` = ? or `id` in (?, ?, ?)",
			bindings:  []interface{}{1, 1, 2, 3},
		},
		{
			name:      "basic where not ins",
			build:     func(b *Builder) *Builder { return b.Select("*").From("users").WhereNotIn("id", []interface{}{1, 2, 3}) },
			wantBase:  `select * from "users" where "id" not in (?, ?, ?)`,
			wantMySQL: "select * from `users` where `id` not in (?, ?, ?)",
			bindings:  []interface{}{1, 2, 3},
		},
		{
			name: "raw where ins",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereIn("id", []interface{}{grammar.NewExpression(1)})
			},
			wantBase:  `select * from "users" where "id" in (1)`,
			wantMySQL: "select * from `users` where `id` in (1)",
			bindings:  []interface{}{},
		},
		{
			name:     "empty where ins",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").WhereIn("id", []interface{}{}) },
			wantBase: `select * from "users" where 0 = 1`, wantMySQL: "select * from `users` where 0 = 1",
		},
		{
			name:     "empty where not ins",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").WhereNotIn("id", []interface{}{}) },
			wantBase: `select * from "users" where 1 = 1`, wantMySQL: "select * from `users` where 1 = 1",
		},
		{
			name:      "where between",
			build:     func(b *Builder) *Builder { return b.Select("*").From("users").WhereBetween("id", []interface{}{1, 2}) },
			wantBase:  `select * from "users" where "id" between ? and ?`,
			wantMySQL: "select * from `users` where `id` between ? and ?",
			bindings:  []interface{}{1, 2},
		},
		{
			name: "where between with raw values",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereBetween("id", []interface{}{grammar.NewExpression(1), grammar.NewExpression(2)})
			},
			wantBase:  `select * from "users" where "id" between 1 and 2`,
			wantMySQL: "select * from `users` where `id` between 1 and 2",
			bindings:  []interface{}{},
		},
		{
			name: "where not between",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereNotBetween("id", []interface{}{1, 2})
			},
			wantBase:  `select * from "users" where "id" not between ? and ?`,
			wantMySQL: "select * from `users` where `id` not between ? and ?",
			bindings:  []interface{}{1, 2},
		},
		{
			name: "or where between",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").Where("id", "=", 1).OrWhereBetween("id", []interface{}{3, 5})
			},
			wantBase:  `select * from "users" where "id" = ? or "id" between ? and ?`,
			wantMySQL: "select * from `users` where `id` = ? or `id` between ? and ?",
			bindings:  []interface{}{1, 3, 5},
		},
		{
			name:     "wheres with array value",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").Where("id", []interface{}{12}) },
			wantBase: `select * from "users" where "id" = ?`, wantMySQL: "select * from `users` where `id` = ?",
			bindings: []interface{}{12},
		},
		{
			name:     "wheres with array value two args",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").Where("id", "!=", []interface{}{12, 30}) },
			wantBase: `select * from "users" where "id" != ?`, wantMySQL: "select * from `users` where `id` != ?",
			bindings: []interface{}{12},
		},
	})
}

func TestGoldenWhereNulls(t *testing.T) {
	runGolden(t, []goldenCase{
		{
			name:      "where null",
			build:     func(b *Builder) *Builder { return b.Select("*").From("users").WhereNull("admin_at") },
			wantBase:  `select * from "users" where "admin_at" is null`,
			wantMySQL: "select * from `users` where `admin_at` is null",
		},
		{
			name: "or where null",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").Where("id", "=", 1).OrWhereNull("admin_at")
			},
			wantBase:  `select * from "users" where "id" = ? or "admin_at" is null`,
			wantMySQL: "select * from `users` where `id` = ? or `admin_at` is null",
			bindings:  []interface{}{1},
		},
		{
			name:      "where not null",
			build:     func(b *Builder) *Builder { return b.Select("*").From("users").WhereNotNull("admin_at") },
			wantBase:  `select * from "users" where "admin_at" is not null`,
			wantMySQL: "select * from `users` where `admin_at` is not null",
		},
		{
			name: "or where not null",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereNull("admin_at").OrWhereNotNull("admin_at")
			},
			wantBase:  `select * from "users" where "admin_at" is null or "admin_at" is not null`,
			wantMySQL: "select * from `users` where `admin_at` is null or `admin_at` is not null",
		},
	})
}

func TestGoldenJoins(t *testing.T) {
	runGolden(t, []goldenCase{
		{
			name: "basic join",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").Join("contacts", "users.id", "=", "contacts.user_id")
			},
			wantBase:  `select * from "users" inner join "contacts" on "users"."id" = "contacts"."user_id"`,
			wantMySQL: "select * from `users` inner join `contacts` on `users`.`id` = `contacts`.`user_id`",
		},
		{
			name: "left join",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").LeftJoin("contacts", "users.id", "=", "contacts.user_id")
			},
			wantBase:  `select * from "users" left join "contacts" on "users"."id" = "contacts"."user_id"`,
			wantMySQL: "select * from `users` left join `contacts` on `users`.`id` = `contacts`.`user_id`",
		},
		{
			name: "right join",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").RightJoin("contacts", "users.id", "=", "contacts.user_id")
			},
			wantBase:  `select * from "users" right join "contacts" on "users"."id" = "contacts"."user_id"`,
			wantMySQL: "select * from `users` right join `contacts` on `users`.`id` = `contacts`.`user_id`",
		},
		{
			name: "cross join",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").CrossJoin("contacts", "users.id", "=", "contacts.user_id")
			},
			wantBase:  `select * from "users" cross join "contacts" on "users"."id" = "contacts"."user_id"`,
			wantMySQL: "select * from `users` cross join `contacts` on `users`.`id` = `contacts`.`user_id`",
		},
		{
			name: "join where",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").JoinWhere("contacts", "users.id", "=", 1)
			},
			wantBase:  `select * from "users" inner join "contacts" on "users"."id" = ?`,
			wantMySQL: "select * from `users` inner join `contacts` on `users`.`id` = ?",
			bindings:  []interface{}{1},
		},
		{
			name: "left join where",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").LeftJoinWhere("contacts", "users.id", "=", 1)
			},
			wantBase:  `select * from "users" left join "contacts" on "users"."id" = ?`,
			wantMySQL: "select * from `users` left join `contacts` on `users`.`id` = ?",
			bindings:  []interface{}{1},
		},
	})
}

func TestGoldenLimitOffset(t *testing.T) {
	runGolden(t, []goldenCase{
		{
			name:     "limit",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").Limit(5) },
			wantBase: `select * from "users" limit 5`, wantMySQL: "select * from `users` limit 5",
		},
		{
			name:     "offset only",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").Offset(5) },
			wantBase: `select * from "users" offset 5`, wantMySQL: "select * from `users` offset 5",
		},
		{
			name:     "limit and offset",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").Limit(5).Offset(4) },
			wantBase: `select * from "users" limit 5 offset 4`, wantMySQL: "select * from `users` limit 5 offset 4",
		},
	})
}

func TestGoldenAggregates(t *testing.T) {
	runGolden(t, []goldenCase{
		{
			name:     "count",
			build:    func(b *Builder) *Builder { b.From("users").SetAggregate("count", []interface{}{"*"}); return b },
			wantBase: `select count(*) as "aggregate" from "users"`, wantMySQL: "select count(*) as `aggregate` from `users`",
		},
		{
			name: "count with distinct star stays plain",
			build: func(b *Builder) *Builder {
				b.From("users").Distinct().SetAggregate("count", []interface{}{"*"})
				return b
			},
			wantBase:  `select count(*) as "aggregate" from "users"`,
			wantMySQL: "select count(*) as `aggregate` from `users`",
		},
		{
			name: "count distinct column",
			build: func(b *Builder) *Builder {
				b.From("users").Distinct().SetAggregate("count", []interface{}{"email"})
				return b
			},
			wantBase:  `select count(distinct "email") as "aggregate" from "users"`,
			wantMySQL: "select count(distinct `email`) as `aggregate` from `users`",
		},
		{
			name:     "min",
			build:    func(b *Builder) *Builder { b.From("users").SetAggregate("min", []interface{}{"id"}); return b },
			wantBase: `select min("id") as "aggregate" from "users"`, wantMySQL: "select min(`id`) as `aggregate` from `users`",
		},
		{
			name:     "max",
			build:    func(b *Builder) *Builder { b.From("users").SetAggregate("max", []interface{}{"id"}); return b },
			wantBase: `select max("id") as "aggregate" from "users"`, wantMySQL: "select max(`id`) as `aggregate` from `users`",
		},
		{
			name: "sum with where",
			build: func(b *Builder) *Builder {
				b.From("users").Where("id", ">", 5).SetAggregate("sum", []interface{}{"balance"})
				return b
			},
			wantBase:  `select sum("balance") as "aggregate" from "users" where "id" > ?`,
			wantMySQL: "select sum(`balance`) as `aggregate` from `users` where `id` > ?",
			bindings:  []interface{}{5},
		},
	})
}

func TestGoldenCompileExistsDirectly(t *testing.T) {
	for _, tc := range []struct {
		name  string
		g     grammar.Grammar
		begin string
		end   string
	}{
		{"base", grammar.NewQueryGrammar(), `"users"`, `"exists"`},
		{"mysql", grammar.NewMySqlGrammar(), "`users`", "`exists`"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := NewBuilder(nil, tc.g).From("users")
			want := "select exists(select * from " + tc.begin + ") as " + tc.end
			if got := tc.g.CompileExists(b.Query); got != want {
				t.Errorf("sql = %s, want %s", got, want)
			}
		})
	}
}

func TestGoldenOrderByGroupBy(t *testing.T) {
	runGolden(t, []goldenCase{
		{
			name:     "order by",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").OrderBy("email") },
			wantBase: `select * from "users" order by "email" asc`, wantMySQL: "select * from `users` order by `email` asc",
		},
		{
			name:     "order by desc",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").OrderByDesc("email") },
			wantBase: `select * from "users" order by "email" desc`, wantMySQL: "select * from `users` order by `email` desc",
		},
		{
			name:     "order by raw",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").OrderByRaw("email desc") },
			wantBase: `select * from "users" order by email desc`, wantMySQL: "select * from `users` order by email desc",
		},
		{
			name:     "group by",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").GroupBy("id", "email") },
			wantBase: `select * from "users" group by "id", "email"`, wantMySQL: "select * from `users` group by `id`, `email`",
		},
		{
			name:      "group by and having",
			build:     func(b *Builder) *Builder { return b.Select("*").From("users").GroupBy("id").Having("email", ">", 1) },
			wantBase:  `select * from "users" group by "id" having "email" > ?`,
			wantMySQL: "select * from `users` group by `id` having `email` > ?",
			bindings:  []interface{}{1},
		},
	})
}

// orWhere/orWhereIn 等链式（避免与 Go 导出规则混淆的小写别名已由导出方法承担，
// 这里用导出方法验证。）
func TestGoldenChainAliases(t *testing.T) {
	b := baseBuilder(t).Select("*").From("users").Where("id", "=", 1).OrWhere("email", "=", "foo")
	want := `select * from "users" where "id" = ? or "email" = ?`
	if got := b.ToSql(); got != want {
		t.Errorf("sql = %s, want %s", got, want)
	}
}

// 嵌套分组
func TestGoldenNestedWheres(t *testing.T) {
	runGolden(t, []goldenCase{
		{
			name: "nested or group",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").Where("email", "=", "foo").WhereNested(func(nq *Builder) {
					nq.Where("id", "=", 1).OrWhere("id", "=", 2)
				})
			},
			wantBase:  `select * from "users" where "email" = ? and ("id" = ? or "id" = ?)`,
			wantMySQL: "select * from `users` where `email` = ? and (`id` = ? or `id` = ?)",
			bindings:  []interface{}{"foo", 1, 2},
		},
		{
			name: "nested group leading",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereNested(func(nq *Builder) {
					nq.Where("name", "=", "foo").Where("age", ">", 18)
				}).OrWhere("email", "=", "bar")
			},
			wantBase:  `select * from "users" where ("name" = ? and "age" > ?) or "email" = ?`,
			wantMySQL: "select * from `users` where (`name` = ? and `age` > ?) or `email` = ?",
			bindings:  []interface{}{"foo", 18, "bar"},
		},
	})
}

// 子查询：selectSub / fromSub / whereIn(sub) / whereSub
func TestGoldenSubqueries(t *testing.T) {
	lastPosts := func(parent *Builder) *Builder {
		sub := NewBuilder(parent.Connection, parent.GetGrammar())
		return sub.Select("id").From("posts").Where("status", "=", "published").OrderByDesc("created_at").Limit(1)
	}
	runGolden(t, []goldenCase{
		{
			name: "select sub",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").SelectSub(func(nb *Builder) *Builder {
					return nb.Select("id").From("posts").Limit(1)
				}, "post_id")
			},
			wantBase:  `select *, (select "id" from "posts" limit 1) as "post_id" from "users"`,
			wantMySQL: "select *, (select `id` from `posts` limit 1) as `post_id` from `users`",
		},
		{
			name: "from sub",
			build: func(b *Builder) *Builder {
				return b.FromSub(func(nb *Builder) *Builder {
					return nb.Select("id").From("users").Where("active", "=", 1)
				}, "u").Select("*")
			},
			wantBase:  `select * from (select "id" from "users" where "active" = ?) as "u"`,
			wantMySQL: "select * from (select `id` from `users` where `active` = ?) as `u`",
			bindings:  []interface{}{1},
		},
		{
			name: "where in sub",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereIn("id", lastPosts(b))
			},
			wantBase:  `select * from "users" where "id" in (select "id" from "posts" where "status" = ? order by "created_at" desc limit 1)`,
			wantMySQL: "select * from `users` where `id` in (select `id` from `posts` where `status` = ? order by `created_at` desc limit 1)",
			bindings:  []interface{}{"published"},
		},
		{
			name: "where sub",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").Where("created_at", ">", lastPosts(b))
			},
			wantBase:  `select * from "users" where "created_at" > (select "id" from "posts" where "status" = ? order by "created_at" desc limit 1)`,
			wantMySQL: "select * from `users` where `created_at` > (select `id` from `posts` where `status` = ? order by `created_at` desc limit 1)",
			bindings:  []interface{}{"published"},
		},
		{
			name: "where between sub column",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereBetween(lastPosts(b), []interface{}{1, 2})
			},
			wantBase:  `select * from "users" where (select "id" from "posts" where "status" = ? order by "created_at" desc limit 1) between ? and ?`,
			wantMySQL: "select * from `users` where (select `id` from `posts` where `status` = ? order by `created_at` desc limit 1) between ? and ?",
			bindings:  []interface{}{"published", 1, 2},
		},
	})
}

// ---- R4: unions ----

func TestGoldenUnions(t *testing.T) {
	runGolden(t, []goldenCase{
		{
			name: "union",
			build: func(b *Builder) *Builder {
				b.Select("*").From("users").Where("id", "=", 1)
				b.Union(func(nb *Builder) *Builder {
					return nb.Select("*").From("users").Where("id", "=", 2)
				})
				return b
			},
			wantBase:  `(select * from "users" where "id" = ?) union (select * from "users" where "id" = ?)`,
			wantMySQL: "(select * from `users` where `id` = ?) union (select * from `users` where `id` = ?)",
			bindings:  []interface{}{1, 2},
		},
		{
			name: "union all",
			build: func(b *Builder) *Builder {
				b.Select("*").From("users").Where("id", "=", 1)
				b.UnionAll(func(nb *Builder) *Builder {
					return nb.Select("*").From("users").Where("id", "=", 2)
				})
				return b
			},
			wantBase:  `(select * from "users" where "id" = ?) union all (select * from "users" where "id" = ?)`,
			wantMySQL: "(select * from `users` where `id` = ?) union all (select * from `users` where `id` = ?)",
			bindings:  []interface{}{1, 2},
		},
		{
			name: "union with order and limit",
			build: func(b *Builder) *Builder {
				b.Select("a").From("t1").Where("a", "=", 10).Where("b", "=", 1)
				b.Union(func(nb *Builder) *Builder {
					return nb.Select("a").From("t2").Where("a", "=", 11).Where("b", "=", 2)
				})
				b.OrderBy("a").Limit(10)
				return b
			},
			wantBase:  `(select "a" from "t1" where "a" = ? and "b" = ?) union (select "a" from "t2" where "a" = ? and "b" = ?) order by "a" asc limit 10`,
			wantMySQL: "(select `a` from `t1` where `a` = ? and `b` = ?) union (select `a` from `t2` where `a` = ? and `b` = ?) order by `a` asc limit 10",
			bindings:  []interface{}{10, 1, 11, 2},
		},
		{
			name: "union aggregate",
			build: func(b *Builder) *Builder {
				b.Select("*").From("users").Where("id", "=", 1)
				b.Union(func(nb *Builder) *Builder {
					return nb.Select("*").From("users").Where("id", "=", 2)
				})
				b.SetAggregate("count", []interface{}{"*"})
				return b
			},
			wantBase:  `select count(*) as "aggregate" from ((select * from "users" where "id" = ?) union (select * from "users" where "id" = ?)) as "temp_table"`,
			wantMySQL: "select count(*) as `aggregate` from ((select * from `users` where `id` = ?) union (select * from `users` where `id` = ?)) as `temp_table`",
			bindings:  []interface{}{1, 2},
		},
	})
}

// ---- R4: joinSub ----

func TestGoldenJoinSub(t *testing.T) {
	runGolden(t, []goldenCase{
		{
			name: "join sub",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").JoinSub(func(nb *Builder) *Builder {
					return nb.Select("user_id").SelectRaw("count(*) as count").From("contact_user").GroupBy("user_id")
				}, "contacts", "users.id", "=", "contacts.user_id")
			},
			wantBase:  `select * from "users" inner join (select "user_id", count(*) as count from "contact_user" group by "user_id") as "contacts" on "users"."id" = "contacts"."user_id"`,
			wantMySQL: "select * from `users` inner join (select `user_id`, count(*) as count from `contact_user` group by `user_id`) as `contacts` on `users`.`id` = `contacts`.`user_id`",
		},
		{
			name: "left join sub",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").LeftJoinSub(func(nb *Builder) *Builder {
					return nb.Select("user_id").From("contact_user")
				}, "contacts", "users.id", "=", "contacts.user_id")
			},
			wantBase:  `select * from "users" left join (select "user_id" from "contact_user") as "contacts" on "users"."id" = "contacts"."user_id"`,
			wantMySQL: "select * from `users` left join (select `user_id` from `contact_user`) as `contacts` on `users`.`id` = `contacts`.`user_id`",
		},
	})
}

// ---- R5: 日期 / JSON / fulltext where ----

func TestGoldenDateWheres(t *testing.T) {
	runGolden(t, []goldenCase{
		{
			name: "where date mysql",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereDate("created_at", "=", "2015-12-21")
			},
			wantBase:  `select * from "users" where date("created_at") = ?`,
			wantMySQL: "select * from `users` where date(`created_at`) = ?",
			bindings:  []interface{}{"2015-12-21"},
		},
		{
			name:      "where date two args mysql",
			build:     func(b *Builder) *Builder { return b.Select("*").From("users").WhereDate("created_at", 1) },
			wantBase:  `select * from "users" where date("created_at") = ?`,
			wantMySQL: "select * from `users` where date(`created_at`) = ?",
			bindings:  []interface{}{1},
		},
		{
			name:      "where day mysql",
			build:     func(b *Builder) *Builder { return b.Select("*").From("users").WhereDay("created_at", "=", 1) },
			wantBase:  `select * from "users" where day("created_at") = ?`,
			wantMySQL: "select * from `users` where day(`created_at`) = ?",
			bindings:  []interface{}{1},
		},
		{
			name: "or where day mysql",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereDay("created_at", "=", 1).OrWhereDay("created_at", "=", 2)
			},
			wantBase:  `select * from "users" where day("created_at") = ? or day("created_at") = ?`,
			wantMySQL: "select * from `users` where day(`created_at`) = ? or day(`created_at`) = ?",
			bindings:  []interface{}{1, 2},
		},
		{
			name: "where month and year",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereMonth("created_at", "=", 5).OrWhereYear("created_at", "=", 2020)
			},
			wantBase:  `select * from "users" where month("created_at") = ? or year("created_at") = ?`,
			wantMySQL: "select * from `users` where month(`created_at`) = ? or year(`created_at`) = ?",
			bindings:  []interface{}{5, 2020},
		},
		{
			name:      "where time",
			build:     func(b *Builder) *Builder { return b.Select("*").From("users").WhereTime("created_at", ">=", "22:00") },
			wantBase:  `select * from "users" where time("created_at") >= ?`,
			wantMySQL: "select * from `users` where time(`created_at`) >= ?",
			bindings:  []interface{}{"22:00"},
		},
		{
			name: "date where expression not bound",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereDate("created_at", "=", grammar.NewExpression("NOW()")).Where("admin", "=", true)
			},
			wantBase:  `select * from "users" where date("created_at") = NOW() and "admin" = ?`,
			wantMySQL: "select * from `users` where date(`created_at`) = NOW() and `admin` = ?",
			bindings:  []interface{}{true},
		},
	})
}

func TestGoldenJsonWheres(t *testing.T) {
	runGolden(t, []goldenCase{
		{
			name: "where json contains mysql",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereJsonContains("options", []interface{}{"en"})
			},
			wantBase: "", wantMySQL: "select * from `users` where json_contains(`options`, ?)",
			bindings: []interface{}{`["en"]`},
		},
		{
			name: "where json contains path mysql",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereJsonContains("users.options->languages", []interface{}{"en"})
			},
			wantBase: "", wantMySQL: "select * from `users` where json_contains(`users`.`options`, ?, '$.\"languages\"')",
			bindings: []interface{}{`["en"]`},
		},
		{
			name: "or where json contains raw mysql",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").Where("id", "=", 1).
					OrWhereJsonContains("options->languages", grammar.NewExpression(`'["en"]'`))
			},
			wantBase: "", wantMySQL: "select * from `users` where `id` = ? or json_contains(`options`, '[\"en\"]', '$.\"languages\"')",
			bindings: []interface{}{1},
		},
		{
			name: "where json contains key mysql",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereJsonContainsKey("users.options->languages")
			},
			wantBase: "", wantMySQL: "select * from `users` where ifnull(json_contains_path(`users`.`options`, 'one', '$.\"languages\"'), 0)",
		},
		{
			name: "where json contains key index mysql",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereJsonContainsKey("options->languages[0][1]")
			},
			wantBase: "", wantMySQL: "select * from `users` where ifnull(json_contains_path(`options`, 'one', '$.\"languages\"[0][1]'), 0)",
		},
		{
			name:     "where json length mysql",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").WhereJsonLength("options", 0) },
			wantBase: "", wantMySQL: "select * from `users` where json_length(`options`) = ?",
			bindings: []interface{}{0},
		},
		{
			name: "where json length operator mysql",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereJsonLength("users.options->languages", ">", 0)
			},
			wantBase: "", wantMySQL: "select * from `users` where json_length(`users`.`options`, '$.\"languages\"') > ?",
			bindings: []interface{}{0},
		},
		{
			name: "where json overlaps mysql",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereJsonOverlaps("options", []interface{}{"en", "fr"})
			},
			wantBase: "", wantMySQL: "select * from `users` where json_overlaps(`options`, ?)",
			bindings: []interface{}{`["en","fr"]`},
		},
		{
			name: "where json doesnt contain mysql",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereJsonDoesntContain("options", []interface{}{"en"})
			},
			wantBase: "", wantMySQL: "select * from `users` where not json_contains(`options`, ?)",
		},
	})
}

func TestGoldenFullText(t *testing.T) {
	runGolden(t, []goldenCase{
		{
			name:     "fulltext natural language",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").WhereFullText("body", "Hello World") },
			wantBase: "", wantMySQL: "select * from `users` where match (`body`) against (? in natural language mode)",
			bindings: []interface{}{"Hello World"},
		},
		{
			name: "fulltext expansion",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereFullText("body", "Hello World", map[string]interface{}{"expanded": true})
			},
			wantBase: "", wantMySQL: "select * from `users` where match (`body`) against (? in natural language mode with query expansion)",
		},
		{
			name: "fulltext boolean",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereFullText("body", "Hello World", map[string]interface{}{"mode": "boolean"})
			},
			wantBase: "", wantMySQL: "select * from `users` where match (`body`) against (? in boolean mode)",
		},
		{
			name: "fulltext multiple columns",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereFullText([]string{"body", "title"}, "Hello World")
			},
			wantBase: "", wantMySQL: "select * from `users` where match (`body`, `title`) against (? in natural language mode)",
		},
		{
			name: "or where fulltext",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").Where("id", "=", 1).OrWhereFullText("body", "Hello")
			},
			wantBase: "", wantMySQL: "select * from `users` where `id` = ? or match (`body`) against (? in natural language mode)",
			bindings: []interface{}{1, "Hello"},
		},
	})
}

// ---- R8: Postgres 方言 ----

func pgBuilder(t *testing.T, prefix ...string) *Builder {
	t.Helper()
	g := grammar.NewPostgresGrammar()
	if len(prefix) > 0 {
		g.SetTablePrefix(prefix[0])
	}
	return NewBuilder(nil, g)
}

func runGoldenPG(t *testing.T, cases []goldenCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.build(pgBuilder(t))
			if got := b.ToSql(); got != tc.wantBase {
				t.Errorf("pg sql =\n  %s\nwant\n  %s", got, tc.wantBase)
			}
		})
	}
}

func TestGoldenPostgres(t *testing.T) {
	runGoldenPG(t, []goldenCase{
		{
			name:     "distinct on columns",
			build:    func(b *Builder) *Builder { return b.Distinct("foo").Select("foo", "bar").From("users") },
			wantBase: `select distinct on ("foo") "foo", "bar" from "users"`,
		},
		{
			name: "where date pg",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereDate("created_at", "=", "2015-12-21")
			},
			wantBase: `select * from "users" where "created_at"::date = ?`,
		},
		{
			name:     "where day pg",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").WhereDay("created_at", "=", 1) },
			wantBase: `select * from "users" where extract(day from "created_at") = ?`,
		},
		{
			name:     "like forces text cast",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").Where("name", "like", "%foo%") },
			wantBase: `select * from "users" where "name"::text like ?`,
		},
		{
			name: "where json contains pg",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereJsonContains("options", []interface{}{"en"})
			},
			wantBase: `select * from "users" where ("options")::jsonb @> ?`,
		},
		{
			name:     "where json length pg",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").WhereJsonLength("options", 0) },
			wantBase: `select * from "users" where jsonb_array_length(("options")::jsonb) = ?`,
		},
		{
			name:     "lock for update pg",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").Where("id", "=", 1).LockForUpdate() },
			wantBase: `select * from "users" where "id" = ? for update`,
		},
		{
			name:     "shared lock pg",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").SharedLock() },
			wantBase: `select * from "users" for share`,
		},
		{
			name:     "random pg",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").InRandomOrder() },
			wantBase: `select * from "users" order by RANDOM()`,
		},
		{
			name:     "fulltext pg",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").WhereFullText("body", "Hello") },
			wantBase: `select * from "users" where (to_tsvector('english', "body")) @@ plainto_tsquery('english', ?)`,
		},
		{
			name:     "json selector pg",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").Where("meta->tags", "=", "a") },
			wantBase: `select * from "users" where "meta"->>'tags' = ?`,
		},
		{
			name:     "json nested selector pg",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").Where("meta->a->b", "=", "x") },
			wantBase: `select * from "users" where "meta"->'a'->>'b' = ?`,
		},
	})
}

// Postgres 写入执行级 golden（独立断言，走 grammar 直接编译）
func TestGoldenPostgresStatements(t *testing.T) {
	g := grammar.NewPostgresGrammar()
	b := NewBuilder(nil, g).From("users")

	row := query.Row{{Key: "email", Value: "foo"}}
	sql, bindings := g.CompileInsertGetId(b.Query, []query.Row{row}, "id")
	want := `insert into "users" ("email") values (?) returning "id"`
	if sql != want {
		t.Errorf("sql = %s, want %s", sql, want)
	}
	if len(bindings) != 1 || bindings[0] != "foo" {
		t.Errorf("bindings = %#v", bindings)
	}

	b2 := NewBuilder(nil, g).From("users")
	rows := []query.Row{
		{{Key: "email", Value: "foo"}, {Key: "name", Value: "bar"}},
		{{Key: "name", Value: "bar2"}, {Key: "email", Value: "foo2"}},
	}
	sql2, _ := g.CompileUpsert(b2.Query, rows, []string{"email"}, []string{"email", "name"})
	want2 := `insert into "users" ("email", "name") values (?, ?), (?, ?) on conflict ("email") do update set "email" = "excluded"."email", "name" = "excluded"."name"`
	if sql2 != want2 {
		t.Errorf("sql =\n  %s\nwant\n  %s", sql2, want2)
	}
}

// ---- R9: 内省与 index hint ----

func TestGoldenToRawSql(t *testing.T) {
	b := baseBuilder(t).Select("*").From("users").Where("email", "=", "foo")
	want := `select * from "users" where "email" = 'foo'`
	if got := b.ToRawSql(); got != want {
		t.Errorf("sql = %s, want %s", got, want)
	}

	b2 := baseBuilder(t).Select("*").From("users").Where("id", "=", 1).Where("name", "=", "it's")
	want2 := `select * from "users" where "id" = 1 and "name" = 'it''s'`
	if got := b2.ToRawSql(); got != want2 {
		t.Errorf("sql = %s, want %s", got, want2)
	}
}

func TestGoldenIndexHints(t *testing.T) {
	runGolden(t, []goldenCase{
		{
			name:     "use index",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").UseIndex("idx_email") },
			wantBase: "", wantMySQL: "select * from `users` use index (idx_email)",
		},
		{
			name: "force index",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").ForceIndex("idx_email").Where("email", "=", "x")
			},
			wantBase: "", wantMySQL: "select * from `users` force index (idx_email) where `email` = ?",
		},
		{
			name:     "ignore index",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").IgnoreIndex("idx_email") },
			wantBase: "", wantMySQL: "select * from `users` ignore index (idx_email)",
		},
	})
}

func TestGoldenLockMySQL(t *testing.T) {
	runGolden(t, []goldenCase{
		{
			name:      "lock for update mysql",
			build:     func(b *Builder) *Builder { return b.Select("*").From("users").Where("id", "=", 1).LockForUpdate() },
			wantBase:  `select * from "users" where "id" = ?`,
			wantMySQL: "select * from `users` where `id` = ? for update",
		},
		{
			name:      "shared lock mysql",
			build:     func(b *Builder) *Builder { return b.Select("*").From("users").SharedLock() },
			wantBase:  `select * from "users"`,
			wantMySQL: "select * from `users` lock in share mode",
		},
	})
}

func TestGoldenReorderAndOldest(t *testing.T) {
	runGolden(t, []goldenCase{
		{
			name:      "oldest default",
			build:     func(b *Builder) *Builder { return b.Select("*").From("users").Oldest() },
			wantBase:  `select * from "users" order by "created_at" asc`,
			wantMySQL: "select * from `users` order by `created_at` asc",
		},
		{
			name:      "latest custom column",
			build:     func(b *Builder) *Builder { return b.Select("*").From("users").Latest("published_at") },
			wantBase:  `select * from "users" order by "published_at" desc`,
			wantMySQL: "select * from `users` order by `published_at` desc",
		},
		{
			name:      "reorder resets orders",
			build:     func(b *Builder) *Builder { return b.Select("*").From("users").OrderBy("email").Reorder("name", "desc") },
			wantBase:  `select * from "users" order by "name" desc`,
			wantMySQL: "select * from `users` order by `name` desc",
		},
		{
			name:      "reorder empty",
			build:     func(b *Builder) *Builder { return b.Select("*").From("users").OrderBy("email").Reorder() },
			wantBase:  `select * from "users"`,
			wantMySQL: "select * from `users`",
		},
	})
}

// ---- R20: SqlServer 方言子集 ----

func sqlServerBuilder(t *testing.T) *Builder {
	t.Helper()
	return NewBuilder(nil, grammar.NewSqlServerGrammar())
}

func TestGoldenSqlServer(t *testing.T) {
	cases := []struct {
		name  string
		build func(b *Builder) *Builder
		want  string
	}{
		{
			name:  "basic select bracket wrapping",
			build: func(b *Builder) *Builder { return b.Select("*").From("users") },
			want:  "select * from [users]",
		},
		{
			name:  "bracket escaping",
			build: func(b *Builder) *Builder { return b.Select("*").From("user]s") },
			want:  "select * from [user]]s]",
		},
		{
			name:  "where",
			build: func(b *Builder) *Builder { return b.Select("*").From("users").Where("email", "=", "foo") },
			want:  "select * from [users] where [email] = ?",
		},
		{
			name:  "random order NEWID",
			build: func(b *Builder) *Builder { return b.Select("*").From("users").InRandomOrder() },
			want:  "select * from [users] order by NEWID()",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.build(sqlServerBuilder(t))
			if got := b.ToSql(); got != tc.want {
				t.Errorf("sql =\n  %s\nwant\n  %s", got, tc.want)
			}
		})
	}
}

func TestSqlServerInsertGetIdOutput(t *testing.T) {
	g := grammar.NewSqlServerGrammar()
	b := NewBuilder(nil, g).From("users")
	row := query.Row{{Key: "email", Value: "foo"}}
	sql, bindings := g.CompileInsertGetId(b.Query, []query.Row{row}, "")
	want := "insert into [users] ([email]) output inserted.[id] values (?)"
	if sql != want {
		t.Errorf("sql =\n  %s\nwant\n  %s", sql, want)
	}
	if len(bindings) != 1 || bindings[0] != "foo" {
		t.Errorf("bindings = %#v", bindings)
	}
}

func TestSqlServerEmptyInsertDefaultValues(t *testing.T) {
	g := grammar.NewSqlServerGrammar()
	b := NewBuilder(nil, g).From("users")
	sql, _ := g.CompileInsert(b.Query, nil)
	want := "insert into [users] default values"
	if sql != want {
		t.Errorf("sql = %s, want %s", sql, want)
	}
}

func TestSqlServerDateWhere(t *testing.T) {
	b := sqlServerBuilder(t).Select("*").From("users").WhereYear("created_at", "=", 2020)
	want := "select * from [users] where year([created_at]) = ?"
	if got := b.ToSql(); got != want {
		t.Errorf("sql = %s, want %s", got, want)
	}
}

// ---- R23-R25: 集合 / factory / SqlServer 分页 / MariaDB ----

func TestCollectionBasics(t *testing.T) {
	users := []User{
		{Id: 1, Name: "A"},
		{Id: 2, Name: "B"},
		{Id: 3, Name: "C"},
	}
	col := Col(users)

	got := col.Filter(func(u User) bool { return u.Id > 1 })
	if len(got) != 2 || got[0].Name != "B" {
		t.Errorf("filter = %+v", got)
	}

	names := col.Map(func(u User) string { return u.Name })
	if len(names) != 3 || names[2] != "C" {
		t.Errorf("map = %v", names)
	}

	first, ok := col.First(func(u User) bool { return u.Id == 2 })
	if !ok || first.Name != "B" {
		t.Errorf("first = %+v ok=%v", first, ok)
	}

	keys := col.ModelKeys()
	if len(keys) != 3 || keys[0] != int64(1) {
		t.Errorf("modelKeys = %#v", keys)
	}

	pluck := col.Pluck("name")
	if len(pluck) != 3 || pluck[0] != "A" {
		t.Errorf("pluck = %#v", pluck)
	}
}

func TestFactoryAndSeeder(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("BEGIN", &fakeResult{})
	st.On("COMMIT", &fakeResult{})
	insertSQL := "insert into `posts` (`created_at`, `title`, `updated_at`) values (?, ?, ?)"
	st.On(insertSQL, &fakeResult{Affected: 1, LastID: 1})

	factory := Factory[*Post]{
		Definition: func(seq int) *Post {
			return &Post{Title: fmt.Sprintf("post-%d", seq)}
		},
	}
	seedCalled := false
	err := Seed(conn, func(c *Connection) error {
		_, cerr := factory.Create(c, 3)
		return cerr
	}, func(c *Connection) error {
		seedCalled = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !seedCalled {
		t.Error("second seeder should run")
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

func TestSqlServerTopAndOffsetRows(t *testing.T) {
	b := sqlServerBuilder(t).Select("*").From("users").Limit(5)
	want := "select top 5 * from [users]"
	if got := b.ToSql(); got != want {
		t.Errorf("sql = %s, want %s", got, want)
	}

	b2 := sqlServerBuilder(t).Select("*").From("users").Limit(5).Offset(10)
	// P0-2：offset 无 order by 时注入 order by (SELECT 0) 兜底（SqlServerGrammar.php:51-59）
	want2 := "select * from [users] order by (SELECT 0) offset 10 rows fetch next 5 rows only"
	if got := b2.ToSql(); got != want2 {
		t.Errorf("sql = %s, want %s", got, want2)
	}
}

func TestMariaDbBasic(t *testing.T) {
	g := grammar.NewMariaDbGrammar()
	b := NewBuilder(nil, g).Select("*").From("users").Where("email", "=", "foo")
	want := "select * from `users` where `email` = ?"
	if got := b.ToSql(); got != want {
		t.Errorf("sql = %s, want %s", got, want)
	}
}

// ---- R27: groupLimit ----

func TestGoldenGroupLimit(t *testing.T) {
	cases := []struct {
		name string
		g    grammar.Grammar
		want string
	}{
		{
			name: "base",
			g:    grammar.NewQueryGrammar(),
			want: `select * from (select "posts".*, row_number() over (partition by "user_id" order by "created_at" desc) as "laravel_row" from "posts") as "laravel_table" where "laravel_row" <= 2 order by "laravel_row"`,
		},
		{
			name: "mysql",
			g:    grammar.NewMySqlGrammar(),
			want: "select * from (select `posts`.*, row_number() over (partition by `user_id` order by `created_at` desc) as `laravel_row` from `posts`) as `laravel_table` where `laravel_row` <= 2 order by `laravel_row`",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := NewBuilder(nil, tc.g).Select("posts.*").From("posts").OrderBy("created_at", "desc")
			b.Query.GroupLimit = &query.GroupLimit{Column: "user_id", Value: 2}
			if got := b.ToSql(); got != tc.want {
				t.Errorf("sql =\n  %s\nwant\n  %s", got, tc.want)
			}
		})
	}
}

func TestGoldenGroupLimitWithOffset(t *testing.T) {
	b := baseBuilder(t).Select("posts.*").From("posts").OrderBy("created_at", "desc").Offset(10)
	b.Query.GroupLimit = &query.GroupLimit{Column: "user_id", Value: 2}
	want := `select * from (select "posts".*, row_number() over (partition by "user_id" order by "created_at" desc) as "laravel_row" from "posts") as "laravel_table" where "laravel_row" <= 12 and "laravel_row" > 10 order by "laravel_row"`
	if got := b.ToSql(); got != want {
		t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
	}
}

// ---- P0/P1 修复回归 golden ----

// P0-1：PG JSON 路径数组下标取方括号内内容（PostgresGrammar.php:825-840）
func TestGoldenPostgresJsonArrayPath(t *testing.T) {
	runGoldenPG(t, []goldenCase{
		{
			name:     "array index selector",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").Where("data->tags[0]", "=", "x") },
			wantBase: `select * from "users" where "data"->'tags'->>0 = ?`,
		},
		{
			name:     "nested array indexes",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").Where("meta->a[0][1]->name", "=", "x") },
			wantBase: `select * from "users" where "meta"->'a'->0->1->>'name' = ?`,
		},
	})
}

func TestGoldenPostgresJsonUpdateColumnArrayPath(t *testing.T) {
	g := grammar.NewPostgresGrammar()
	b := NewBuilder(nil, g).From("users").Where("id", "=", 1)
	sql := g.CompileUpdate(b.Query, map[string]any{"data->tags[0]": "x"})
	want := `update "users" set "data" = jsonb_set("data"::jsonb, '{"tags",0}', ?) where "id" = ?`
	if sql != want {
		t.Errorf("sql =\n  %s\nwant\n  %s", sql, want)
	}
}

// P0-5：PG 带 joins/limit 的 delete 走 ctid 重写（PostgresGrammar.php:693-717）
func TestGoldenPostgresDeleteCtid(t *testing.T) {
	g := grammar.NewPostgresGrammar()

	// limit → delete ... where ctid in (select ... limit 1)
	b := NewBuilder(nil, g).From("users").Where("email", "=", "foo").Limit(1)
	want := `delete from "users" where "ctid" in (select "users"."ctid" from "users" where "email" = ? limit 1)`
	if got := g.CompileDelete(b.Query); got != want {
		t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
	}

	// joins → 子查询保留 join
	b2 := NewBuilder(nil, g).From("users").
		Join("contacts", "users.id", "=", "contacts.user_id").
		Where("email", "=", "foo")
	want2 := `delete from "users" where "ctid" in (select "users"."ctid" from "users" inner join "contacts" on "users"."id" = "contacts"."user_id" where "email" = ?)`
	if got := g.CompileDelete(b2.Query); got != want2 {
		t.Errorf("sql =\n  %s\nwant\n  %s", got, want2)
	}

	// 无 joins/limit → 普通 delete
	b3 := NewBuilder(nil, g).From("users").Where("email", "=", "foo")
	want3 := `delete from "users" where "email" = ?`
	if got := g.CompileDelete(b3.Query); got != want3 {
		t.Errorf("sql =\n  %s\nwant\n  %s", got, want3)
	}
}

// P0-2：SqlServer offset 无 order by 时注入 order by (SELECT 0)
func TestGoldenSqlServerOffsetOrderFallback(t *testing.T) {
	b := sqlServerBuilder(t).Select("*").From("users").Offset(10)
	want := "select * from [users] order by (SELECT 0) offset 10 rows"
	if got := b.ToSql(); got != want {
		t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
	}

	// 已有 order by → 不注入
	b2 := sqlServerBuilder(t).Select("*").From("users").OrderBy("email").Offset(10)
	want2 := "select * from [users] order by [email] asc offset 10 rows"
	if got := b2.ToSql(); got != want2 {
		t.Errorf("sql =\n  %s\nwant\n  %s", got, want2)
	}
}

// P0-3：SqlServer compileExists → select top 1 1 [exists]
func TestGoldenSqlServerCompileExists(t *testing.T) {
	g := grammar.NewSqlServerGrammar()
	b := sqlServerBuilder(t).From("users").Where("email", "=", "foo")
	want := "select top 1 1 [exists] from [users] where [email] = ?"
	if got := g.CompileExists(b.Query); got != want {
		t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
	}

	// offset 无 orders 时 exists 内同样注入兜底（limit(1) 经 offset 组件输出 fetch next）
	b2 := sqlServerBuilder(t).From("users").Offset(10)
	want2 := "select 1 [exists] from [users] order by (SELECT 0) offset 10 rows fetch next 1 rows only"
	if got := g.CompileExists(b2.Query); got != want2 {
		t.Errorf("sql =\n  %s\nwant\n  %s", got, want2)
	}
}

// P0-4：SqlServer lock 拼进 from 之后，语句末尾无 lock
func TestGoldenSqlServerLockInFrom(t *testing.T) {
	b := sqlServerBuilder(t).Select("*").From("users").Where("email", "=", "foo").LockForUpdate()
	want := "select * from [users] with(rowlock,updlock,holdlock) where [email] = ?"
	if got := b.ToSql(); got != want {
		t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
	}

	b2 := sqlServerBuilder(t).Select("*").From("users").SharedLock()
	want2 := "select * from [users] with(rowlock,holdlock)"
	if got := b2.ToSql(); got != want2 {
		t.Errorf("sql =\n  %s\nwant\n  %s", got, want2)
	}

	b3 := sqlServerBuilder(t).Select("*").From("users").Lock("with (nolock)")
	want3 := "select * from [users] with (nolock)"
	if got := b3.ToSql(); got != want3 {
		t.Errorf("sql =\n  %s\nwant\n  %s", got, want3)
	}
}

// P1-5：reorder 清 unionOrders 与 order/unionOrder 段绑定
func TestGoldenReorderClearsUnionOrders(t *testing.T) {
	b := baseBuilder(t).Select("*").From("users")
	b.Union(func(nb *Builder) *Builder {
		return nb.Select("*").From("contacts")
	})
	b.OrderByRaw("email = ?", 1) // 有 union → 进 unionOrders / unionOrder 绑定
	b.Reorder()

	want := `(select * from "users") union (select * from "contacts")`
	if got := b.ToSql(); got != want {
		t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
	}
	if got := b.GetBindings(); len(got) != 0 {
		t.Errorf("bindings = %#v, want empty", got)
	}
}

// ★ whereLike 族（Builder.php:1379-1432）
func TestGoldenWhereLike(t *testing.T) {
	runGolden(t, []goldenCase{
		{
			name:     "where like case insensitive base",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").WhereLike("name", "%foo%", false) },
			wantBase: `select * from "users" where "name" like ?`, wantMySQL: "select * from `users` where `name` like ?",
			bindings: []interface{}{"%foo%"},
		},
		{
			name:     "where like case sensitive mysql",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").WhereLike("name", "%foo%", true) },
			wantBase: "", wantMySQL: "select * from `users` where `name` like binary ?",
		},
		{
			name:     "where not like",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").WhereNotLike("name", "%foo%", false) },
			wantBase: `select * from "users" where "name" not like ?`, wantMySQL: "select * from `users` where `name` not like ?",
		},
		{
			name: "or where not like",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").Where("id", "=", 1).OrWhereNotLike("name", "%foo%", false)
			},
			wantBase: `select * from "users" where "id" = ? or "name" not like ?`,
		},
	})
}

func TestGoldenWhereLikePostgres(t *testing.T) {
	runGoldenPG(t, []goldenCase{
		{
			name:     "pg ilike default",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").WhereLike("name", "%foo%", false) },
			wantBase: `select * from "users" where "name"::text ilike ?`,
		},
		{
			name:     "pg like case sensitive",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").WhereLike("name", "%foo%", true) },
			wantBase: `select * from "users" where "name"::text like ?`,
		},
		{
			name:     "pg not like case sensitive",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").WhereNotLike("name", "%foo%", true) },
			wantBase: `select * from "users" where "name"::text not like ?`,
		},
	})
}

// 基础方言 caseSensitive 的 like 抛错（Query\Grammars\Grammar.php:349-353）
func TestBaseWhereLikeCaseSensitivePanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expect panic for case sensitive like on base grammar")
		}
	}()
	baseBuilder(t).Select("*").From("users").WhereLike("name", "%foo%", true).ToSql()
}

// ★ whereBetweenColumns 族（Builder.php:1696-1778）
func TestGoldenWhereBetweenColumns(t *testing.T) {
	runGolden(t, []goldenCase{
		{
			name: "where between columns",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereBetweenColumns("created_at", []interface{}{"updated_at", "deleted_at"})
			},
			wantBase:  `select * from "users" where "created_at" between "updated_at" and "deleted_at"`,
			wantMySQL: "select * from `users` where `created_at` between `updated_at` and `deleted_at`",
		},
		{
			name: "or where not between columns",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").Where("id", "=", 1).
					OrWhereNotBetweenColumns("created_at", []interface{}{"updated_at", "deleted_at"})
			},
			wantBase: `select * from "users" where "id" = ? or "created_at" not between "updated_at" and "deleted_at"`,
		},
		{
			name: "where not between columns",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereNotBetweenColumns("created_at", []interface{}{"updated_at", "deleted_at"})
			},
			wantBase: `select * from "users" where "created_at" not between "updated_at" and "deleted_at"`,
		},
	})
}

// ★ whereExists 族 + addWhereExistsQuery（Builder.php:2199-2266）
func TestGoldenWhereExists(t *testing.T) {
	existsSub := func(nb *Builder) *Builder {
		return nb.SelectRaw("1").From("orders").WhereColumn("orders.user_id", "users.id")
	}
	runGolden(t, []goldenCase{
		{
			name:     "where exists",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").WhereExists(existsSub) },
			wantBase: `select * from "users" where exists (select 1 from "orders" where "orders"."user_id" = "users"."id")`,
		},
		{
			name: "or where exists",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").Where("id", "=", 1).OrWhereExists(existsSub)
			},
			wantBase: `select * from "users" where "id" = ? or exists (select 1 from "orders" where "orders"."user_id" = "users"."id")`,
		},
		{
			name:     "where not exists",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").WhereNotExists(existsSub) },
			wantBase: `select * from "users" where not exists (select 1 from "orders" where "orders"."user_id" = "users"."id")`,
		},
		{
			name: "or where not exists",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").Where("id", "=", 1).OrWhereNotExists(existsSub)
			},
			wantBase: `select * from "users" where "id" = ? or not exists (select 1 from "orders" where "orders"."user_id" = "users"."id")`,
		},
		{
			name: "exists sub bindings",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereExists(func(nb *Builder) *Builder {
					return nb.SelectRaw("1").From("orders").Where("orders.total", ">", 100)
				})
			},
			wantBase: `select * from "users" where exists (select 1 from "orders" where "orders"."total" > ?)`,
			bindings: []interface{}{100},
		},
	})
}

// ★ whereRowValues（Builder.php:2279-2313）
func TestGoldenWhereRowValues(t *testing.T) {
	runGolden(t, []goldenCase{
		{
			name: "where row values",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").
					WhereRowValues([]interface{}{"a", "b"}, "=", []interface{}{1, 2})
			},
			wantBase:  `select * from "users" where ("a", "b") = (?, ?)`,
			wantMySQL: "select * from `users` where (`a`, `b`) = (?, ?)",
			bindings:  []interface{}{1, 2},
		},
		{
			name: "or where row values",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").Where("id", "=", 1).
					OrWhereRowValues([]interface{}{"a", "b"}, "<", []interface{}{1, 2})
			},
			wantBase: `select * from "users" where "id" = ? or ("a", "b") < (?, ?)`,
		},
	})
}

func TestWhereRowValuesCountMismatchPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expect panic when columns/values length mismatch")
		}
	}()
	baseBuilder(t).From("users").WhereRowValues([]interface{}{"a"}, "=", []interface{}{1, 2})
}

// ★ having 族（Builder.php:2830-2968）
func TestGoldenHavingFamily(t *testing.T) {
	runGolden(t, []goldenCase{
		{
			name: "having null",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").GroupBy("email").HavingNull("email")
			},
			wantBase:  `select * from "users" group by "email" having "email" is null`,
			wantMySQL: "select * from `users` group by `email` having `email` is null",
		},
		{
			name: "or having not null",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").GroupBy("email").HavingNull("email").OrHavingNotNull("email")
			},
			wantBase: `select * from "users" group by "email" having "email" is null or "email" is not null`,
		},
		{
			name: "having between",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").GroupBy("category").HavingBetween("category", []interface{}{1, 5})
			},
			wantBase: `select * from "users" group by "category" having "category" between ? and ?`,
			bindings: []interface{}{1, 5},
		},
		{
			name: "having not between",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").GroupBy("category").HavingNotBetween("category", []interface{}{1, 5})
			},
			wantBase: `select * from "users" group by "category" having "category" not between ? and ?`,
		},
		{
			name: "having nested",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").GroupBy("email").HavingNested(func(nb *Builder) {
					nb.Having("email", "=", "foo").OrHaving("email", "=", "bar")
				})
			},
			wantBase: `select * from "users" group by "email" having ("email" = ? or "email" = ?)`,
			bindings: []interface{}{"foo", "bar"},
		},
	})
}

// ★ whereIntegerInRaw 族（Builder.php:1560-1604）
func TestGoldenWhereIntegerInRaw(t *testing.T) {
	runGolden(t, []goldenCase{
		{
			name: "where integer in raw",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereIntegerInRaw("id", []interface{}{1, "2", 3})
			},
			wantBase:  `select * from "users" where "id" in (1, 2, 3)`,
			wantMySQL: "select * from `users` where `id` in (1, 2, 3)",
		},
		{
			name: "where integer not in raw",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereIntegerNotInRaw("id", []interface{}{1, 2})
			},
			wantBase: `select * from "users" where "id" not in (1, 2)`,
		},
		{
			name: "or where integer in raw",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").Where("email", "=", "foo").OrWhereIntegerInRaw("id", []interface{}{1})
			},
			wantBase: `select * from "users" where "email" = ? or "id" in (1)`,
			bindings: []interface{}{"foo"},
		},
	})
}

// ★ whereNull 多列 + boolean 参数（Builder.php:1624-1633）
func TestGoldenWhereNullMultipleColumns(t *testing.T) {
	runGolden(t, []goldenCase{
		{
			name: "where null multi columns",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereNull([]string{"first_name", "last_name"})
			},
			wantBase:  `select * from "users" where "first_name" is null and "last_name" is null`,
			wantMySQL: "select * from `users` where `first_name` is null and `last_name` is null",
		},
		{
			name: "where not null with or boolean",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereNull("first_name").WhereNotNull("last_name", "or")
			},
			wantBase: `select * from "users" where "first_name" is null or "last_name" is not null`,
		},
		{
			name: "or where null",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").Where("id", "=", 1).OrWhereNull("first_name")
			},
			wantBase: `select * from "users" where "id" = ? or "first_name" is null`,
		},
	})
}

// ★ WhereRaw / HavingRaw 的 boolean 参数（Builder.php:1291/3000）
func TestGoldenRawBooleanParameter(t *testing.T) {
	runGolden(t, []goldenCase{
		{
			name: "where raw or boolean",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").Where("id", "=", 1).WhereRaw("email = ?", "foo", "or")
			},
			wantBase: `select * from "users" where "id" = ? or email = ?`,
			bindings: []interface{}{1, "foo"},
		},
		{
			name: "or where raw",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").Where("id", "=", 1).OrWhereRaw("email = ?", "foo")
			},
			wantBase: `select * from "users" where "id" = ? or email = ?`,
		},
		{
			name: "having raw or boolean",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").GroupBy("email").Having("email", "=", "x").HavingRaw("email = ?", "foo", "or")
			},
			wantBase: `select * from "users" group by "email" having "email" = ? or email = ?`,
		},
	})
}

// P1-7：insertOrIgnoreUsing golden（编译形态）
func TestGoldenInsertOrIgnoreUsingPostgres(t *testing.T) {
	b := pgBuilder(t).From("users")
	sql, bindings := b.createSub(func(nb *Builder) *Builder {
		return nb.Select("email").From("contacts").Where("x", "=", 5)
	})
	stmt := b.GetGrammar().CompileInsertOrIgnoreUsing(b.Query, []any{"email"}, sql)
	want := `insert into "users" ("email") select "email" from "contacts" where "x" = ? on conflict do nothing`
	if stmt != want {
		t.Errorf("sql =\n  %s\nwant\n  %s", stmt, want)
	}
	if got := cleanBindings(bindings); len(got) != 1 || got[0] != 5 {
		t.Errorf("bindings = %#v", got)
	}
}

// ---- R3-A: SQLite 方言 ----

func sqliteBuilder(t *testing.T, prefix ...string) *Builder {
	t.Helper()
	g := grammar.NewSqliteQueryGrammar()
	if len(prefix) > 0 {
		g.SetTablePrefix(prefix[0])
	}
	return NewBuilder(nil, g)
}

func TestGoldenSqlite(t *testing.T) {
	cases := []struct {
		name  string
		build func(b *Builder) *Builder
		want  string
	}{
		{
			name:  "compile lock is empty",
			build: func(b *Builder) *Builder { return b.Select("*").From("users").LockForUpdate() },
			want:  `select * from "users"`,
		},
		{
			name: "where date strftime",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereDate("created_at", "=", "2015-12-21")
			},
			want: `select * from "users" where strftime('%Y-%m-%d', "created_at") = cast(? as text)`,
		},
		{
			name: "where time strftime",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereTime("created_at", "=", "12:00:00")
			},
			want: `select * from "users" where strftime('%H:%M:%S', "created_at") = cast(? as text)`,
		},
		{
			name:  "where day month year strftime",
			build: func(b *Builder) *Builder { return b.Select("*").From("users").WhereDay("created_at", "=", 1).WhereMonth("created_at", "=", 2).WhereYear("created_at", "=", 2015) },
			want:  `select * from "users" where strftime('%d', "created_at") = cast(? as text) and strftime('%m', "created_at") = cast(? as text) and strftime('%Y', "created_at") = cast(? as text)`,
		},
		{
			name:  "where like case insensitive falls back",
			build: func(b *Builder) *Builder { return b.Select("*").From("users").WhereLike("name", "foo", false) },
			want:  `select * from "users" where "name" like ?`,
		},
		{
			name:  "where like case sensitive uses glob",
			build: func(b *Builder) *Builder { return b.Select("*").From("users").WhereLike("name", "foo%", true) },
			want:  `select * from "users" where "name" glob ?`,
		},
		{
			name:  "where not like case sensitive uses not glob",
			build: func(b *Builder) *Builder { return b.Select("*").From("users").WhereNotLike("name", "foo", true) },
			want:  `select * from "users" where "name" not glob ?`,
		},
		{
			name:  "where null safe equals uses is",
			build: func(b *Builder) *Builder { return b.Select("*").From("users").Where("a", "<=>", 1) },
			want:  `select * from "users" where "a" is ?`,
		},
		{
			name:  "union wraps with select * from",
			build: func(b *Builder) *Builder { return b.Select("id").From("users").Union(func(nb *Builder) *Builder { return nb.Select("id").From("archives") }) },
			want:  `select * from (select "id" from "users") union select * from (select "id" from "archives")`,
		},
		{
			name:  "json contains uses json_each",
			build: func(b *Builder) *Builder { return b.Select("*").From("users").WhereJsonContains("options->tags", "x") },
			want:  `select * from "users" where exists (select 1 from json_each("options", '$."tags"') where "json_each"."value" is ?)`,
		},
		{
			name:  "json contains key uses json_type",
			build: func(b *Builder) *Builder { return b.Select("*").From("users").WhereJsonContainsKey("options->tags") },
			want:  `select * from "users" where json_type("options", '$."tags"') is not null`,
		},
		{
			name:  "json length uses json_array_length",
			build: func(b *Builder) *Builder { return b.Select("*").From("users").WhereJsonLength("options->tags", ">", 1) },
			want:  `select * from "users" where json_array_length("options", '$."tags"') > ?`,
		},
		{
			name:  "force index hint",
			build: func(b *Builder) *Builder { return b.Select("*").From("users").ForceIndex("idx_name") },
			want:  `select * from "users" indexed by idx_name`,
		},
		{
			name:  "use index hint compiles empty",
			build: func(b *Builder) *Builder { return b.Select("*").From("users").UseIndex("idx_name") },
			want:  `select * from "users"`,
		},
		{
			name:  "in order of",
			build: func(b *Builder) *Builder { return b.Select("*").From("users").InOrderOf("id", []interface{}{3, 1, 2}) },
			want:  `select * from "users" order by case when "id" = ? then 0 when "id" = ? then 1 when "id" = ? then 2 else 3 end`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.want == "" {
				t.Skip("n/a")
			}
			b := tc.build(sqliteBuilder(t))
			if got := b.ToSql(); got != tc.want {
				t.Errorf("sqlite sql =\n  %s\nwant\n  %s", got, tc.want)
			}
		})
	}
}

func TestGoldenSqliteStatements(t *testing.T) {
	t.Run("insert or ignore", func(t *testing.T) {
		b := sqliteBuilder(t).From("users")
		sql, bindings := b.GetGrammar().CompileInsertOrIgnore(b.Query, []query.Row{query.ToRow(map[string]any{"email": "foo"})})
		if sql != `insert or ignore into "users" ("email") values (?)` {
			t.Errorf("sql = %s", sql)
		}
		if !reflect.DeepEqual(bindings, []any{"foo"}) {
			t.Errorf("bindings = %#v", bindings)
		}
	})

	t.Run("insert or ignore using", func(t *testing.T) {
		b := sqliteBuilder(t).From("users")
		stmt := b.GetGrammar().CompileInsertOrIgnoreUsing(b.Query, []any{"email"}, `select "email" from "contacts"`)
		if stmt != `insert or ignore into "users" ("email") select "email" from "contacts"` {
			t.Errorf("sql = %s", stmt)
		}
	})

	t.Run("upsert on conflict do update", func(t *testing.T) {
		b := sqliteBuilder(t).From("users")
		sql, _ := b.GetGrammar().CompileUpsert(b.Query, []query.Row{query.ToRow(map[string]any{"email": "foo", "name": "bar"})}, []string{"email"}, []string{"name"})
		want := `insert into "users" ("email", "name") values (?, ?) on conflict ("email") do update set "name" = "excluded"."name"`
		if sql != want {
			t.Errorf("sql =\n  %s\nwant\n  %s", sql, want)
		}
	})

	t.Run("truncate two statements", func(t *testing.T) {
		b := sqliteBuilder(t, "prefix_").From("users")
		stmts := b.GetGrammar().CompileTruncate(b.Query)
		if len(stmts) != 2 {
			t.Fatalf("stmts = %d", len(stmts))
		}
		// Laravel 先 delete from 表，再清 sqlite_sequence
		if stmts[0].Sql != `delete from "prefix_users"` {
			t.Errorf("sql[0] = %s", stmts[0].Sql)
		}
		if stmts[1].Sql != `delete from "sqlite_sequence" where name = ?` {
			t.Errorf("sql[1] = %s", stmts[1].Sql)
		}
		if !reflect.DeepEqual(stmts[1].Bindings, []any{"prefix_users"}) {
			t.Errorf("bindings[1] = %#v", stmts[1].Bindings)
		}
	})

	t.Run("update with limit rewrites to rowid in", func(t *testing.T) {
		b := sqliteBuilder(t).From("users").Where("email", "=", "foo").Limit(1)
		sql := b.GetGrammar().CompileUpdate(b.Query, map[string]any{"name": "bar"})
		want := `update "users" set "name" = ? where "rowid" in (select "users"."rowid" from "users" where "email" = ? limit 1)`
		if sql != want {
			t.Errorf("sql =\n  %s\nwant\n  %s", sql, want)
		}
		if got := b.GetGrammar().PrepareBindingsForUpdate(b.GetRawBindings(), map[string]any{"name": "bar"}); !reflect.DeepEqual(got, []any{"bar", "foo"}) {
			t.Errorf("bindings = %#v", got)
		}
	})

	t.Run("delete with limit rewrites to rowid in", func(t *testing.T) {
		b := sqliteBuilder(t).From("users").Where("email", "=", "foo").Limit(1)
		sql := b.GetGrammar().CompileDelete(b.Query)
		want := `delete from "users" where "rowid" in (select "users"."rowid" from "users" where "email" = ? limit 1)`
		if sql != want {
			t.Errorf("sql =\n  %s\nwant\n  %s", sql, want)
		}
	})

	t.Run("update with json patch", func(t *testing.T) {
		b := sqliteBuilder(t).From("users")
		sql := b.GetGrammar().CompileUpdate(b.Query, map[string]any{"name": "x", "options->theme": "dark"})
		want := `update "users" set "name" = ?, "options" = json_patch(ifnull("options", json('{}')), json(?))`
		if sql != want {
			t.Errorf("sql =\n  %s\nwant\n  %s", sql, want)
		}
		bindings := b.GetGrammar().PrepareBindingsForUpdate(b.GetRawBindings(), map[string]any{"name": "x", "options->theme": "dark"})
		wantBindings := []any{"x", `{"theme":"dark"}`}
		_ = want
		if !reflect.DeepEqual(bindings, wantBindings) {
			t.Errorf("bindings = %#v, want %#v", bindings, wantBindings)
		}
	})

	t.Run("group limit legacy branch", func(t *testing.T) {
		g := grammar.NewSqliteQueryGrammar()
		g.SetUseLegacyGroupLimit(true)
		b := NewBuilder(nil, g).From("users").Select("*")
		b.Query.GroupLimit = &query.GroupLimit{Column: "id", Value: 2}
		b.OrderBy("created_at")
		if got := b.ToSql(); got != `select * from "users" order by "created_at" asc` {
			t.Errorf("legacy groupLimit sql = %s", got)
		}

		g2 := grammar.NewSqliteQueryGrammar()
		b2 := NewBuilder(nil, g2).From("users").Select("*")
		b2.Query.GroupLimit = &query.GroupLimit{Column: "id", Value: 2}
		b2.OrderBy("created_at")
		want := `select * from (select *, row_number() over (partition by "id" order by "created_at" asc) as "laravel_row" from "users") as "laravel_table" where "laravel_row" <= 2 order by "laravel_row"`
		if got := b2.ToSql(); got != want {
			t.Errorf("groupLimit sql =\n  %s\nwant\n  %s", got, want)
		}
	})

	t.Run("prepare where like binding glob conversion", func(t *testing.T) {
		g := grammar.NewSqliteQueryGrammar()
		if got := g.PrepareWhereLikeBinding("a%b_c*d?e", true); got != "a*b?c[*]d[?]e" {
			t.Errorf("binding = %v", got)
		}
		if got := g.PrepareWhereLikeBinding("a%b", false); got != "a%b" {
			t.Errorf("binding = %v", got)
		}
	})

	t.Run("prepare binding for json contains passthrough", func(t *testing.T) {
		g := grammar.NewSqliteQueryGrammar()
		v := []any{1, 2}
		if got := g.PrepareBindingForJsonContains(v); !reflect.DeepEqual(got, v) {
			t.Errorf("binding changed: %#v", got)
		}
	})

	t.Run("wrap union aggregate", func(t *testing.T) {
		bb := sqliteBuilder(t).From("users").Select("id")
		bb.Query.Unions = append(bb.Query.Unions, &query.Union{Query: (&query.Query{From: "archives"})})
		bb.Query.Aggregate = &query.Aggregate{Function: "count", Columns: []any{"*"}}
		want := `select count(*) as "aggregate" from (select * from (select "id" from "users") union select * from (select * from "archives")) as "temp_table"`
		if got := bb.ToSql(); got != want {
			t.Errorf("union aggregate sql =\n  %s\nwant\n  %s", got, want)
		}
	})
}

// ---- R3-A: UpdateFrom / InOrderOf / Timeout / 排序扩展 ----

func TestGoldenUpdateFrom(t *testing.T) {
	t.Run("joins only", func(t *testing.T) {
		b := pgBuilder(t).From("users").Join("contacts", "users.id", "=", "contacts.user_id")
		sql := b.GetGrammar().(interface {
			CompileUpdateFrom(*query.Query, map[string]any) string
		}).CompileUpdateFrom(b.Query, map[string]any{"name": "foo"})
		want := `update "users" set "name" = ? from "contacts" where "users"."id" = "contacts"."user_id"`
		if sql != want {
			t.Errorf("sql =\n  %s\nwant\n  %s", sql, want)
		}
	})

	t.Run("joins with base wheres", func(t *testing.T) {
		b := pgBuilder(t).From("users").Where("email", "=", "x").Join("contacts", "users.id", "=", "contacts.user_id")
		sql := b.GetGrammar().(interface {
			CompileUpdateFrom(*query.Query, map[string]any) string
		}).CompileUpdateFrom(b.Query, map[string]any{"name": "foo"})
		want := `update "users" set "name" = ? from "contacts" where "email" = ? and "users"."id" = "contacts"."user_id"`
		if sql != want {
			t.Errorf("sql =\n  %s\nwant\n  %s", sql, want)
		}
		bindings := b.GetGrammar().(interface {
			PrepareBindingsForUpdateFrom(map[string][]any, map[string]any) []any
		}).PrepareBindingsForUpdateFrom(b.GetRawBindings(), map[string]any{"name": "foo"})
		if !reflect.DeepEqual(bindings, []any{"foo", "x"}) {
			t.Errorf("bindings = %#v", bindings)
		}
	})
}

func TestGoldenInOrderOf(t *testing.T) {
	cases := []goldenCase{
		{
			name:     "in order of",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").InOrderOf("id", []interface{}{3, 1, 2}) },
			wantBase: `select * from "users" order by case when "id" = ? then 0 when "id" = ? then 1 when "id" = ? then 2 else 3 end`,
			wantMySQL: "select * from `users` order by case when `id` = ? then 0 when `id` = ? then 1 when `id` = ? then 2 else 3 end",
			bindings: []interface{}{3, 1, 2},
		},
		{
			name:     "in order of empty values is noop",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").InOrderOf("id", nil) },
			wantBase: `select * from "users"`,
		},
		{
			name:     "in order of with union order",
			build:    func(b *Builder) *Builder { return b.Select("id").From("users").Union(func(n *Builder) *Builder { return n.Select("id").From("archives") }).InOrderOf("id", []interface{}{2, 1}) },
			wantBase: `(select "id" from "users") union (select "id" from "archives") order by case when "id" = ? then 0 when "id" = ? then 1 else 2 end`,
		},
	}
	runGolden(t, cases)
}

func TestGoldenTimeout(t *testing.T) {
	t.Run("mysql max execution time", func(t *testing.T) {
		b := mysqlBuilder(t).Select("*").From("users").Where("id", "=", 1).Timeout(5)
		want := "select /*+ MAX_EXECUTION_TIME(5000) */ * from `users` where `id` = ?"
		if got := b.ToSql(); got != want {
			t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
		}
	})

	t.Run("base dialect ignores timeout", func(t *testing.T) {
		b := baseBuilder(t).Select("*").From("users").Timeout(5)
		want := `select * from "users"`
		if got := b.ToSql(); got != want {
			t.Errorf("sql = %s", got)
		}
	})

	t.Run("timeout must be positive", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("expect panic on zero timeout")
			}
		}()
		baseBuilder(t).Select("*").Timeout(0)
	})
}

func TestGoldenOrderBySubQuery(t *testing.T) {
	cases := []goldenCase{
		{
			name: "order by sub query column",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").OrderBy(func(n *Builder) *Builder {
					return n.SelectRaw("count(*)").From("orders").WhereColumn("orders.user_id", "=", "users.id")
				})
			},
			wantBase: `select * from "users" order by (select count(*) from "orders" where "orders"."user_id" = "users"."id") asc`,
			bindings: []interface{}{},
		},
	}
	runGolden(t, cases)
}

func TestGoldenForPageBeforeId(t *testing.T) {
	cases := []goldenCase{
		{
			name: "for page before id",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").OrderBy("email").ForPageBeforeId(15, 10)
			},
			wantBase: `select * from "users" where "id" < ? order by "email" asc, "id" desc limit 15`,
			bindings: []interface{}{10},
		},
		{
			name: "for page before id nil last id",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").ForPageBeforeId(15, nil)
			},
			wantBase: `select * from "users" where "id" is not null order by "id" desc limit 15`,
		},
		{
			name: "reorder desc",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").OrderBy("email").OrderBy("id").ReorderDesc("name")
			},
			wantBase: `select * from "users" order by "name" desc`,
		},
	}
	runGolden(t, cases)
}

// ---- R3-A: JoinClause 体系 ----

func TestGoldenJoinClosure(t *testing.T) {
	t.Run("on and or on", func(t *testing.T) {
		b := baseBuilder(t).Select("*").From("users").Join("contacts", func(j *Builder) *Builder {
			return j.On("users.id", "=", "contacts.id").OrOn("users.name", "=", "contacts.name")
		})
		want := `select * from "users" inner join "contacts" on "users"."id" = "contacts"."id" or "users"."name" = "contacts"."name"`
		if got := b.ToSql(); got != want {
			t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
		}
	})

	t.Run("nested closure on", func(t *testing.T) {
		b := baseBuilder(t).Select("*").From("users").Join("contacts", func(j *Builder) *Builder {
			return j.On("users.id", "=", "contacts.id").OrOn(func(n *Builder) *Builder {
				return n.On("users.name", "=", "contacts.name").OrOn("users.type", "=", "contacts.type")
			})
		})
		want := `select * from "users" inner join "contacts" on "users"."id" = "contacts"."id" or ("users"."name" = "contacts"."name" or "users"."type" = "contacts"."type")`
		if got := b.ToSql(); got != want {
			t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
		}
	})

	t.Run("on with where binding", func(t *testing.T) {
		b := baseBuilder(t).Select("*").From("users").Join("contacts", func(j *Builder) *Builder {
			return j.On("users.id", "=", "contacts.id").Where("contacts.active", 1)
		})
		want := `select * from "users" inner join "contacts" on "users"."id" = "contacts"."id" and "contacts"."active" = ?`
		if got := b.ToSql(); got != want {
			t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
		}
		if got := b.GetBindings(); !reflect.DeepEqual(got, []interface{}{1}) {
			t.Errorf("bindings = %#v", got)
		}
	})
}

func TestGoldenJoinLateral(t *testing.T) {
	sub := func(n *Builder) *Builder { return n.Select("id", "name").From("contacts").WhereColumn("contacts.user_id", "=", "users.id") }

	t.Run("mysql", func(t *testing.T) {
		b := mysqlBuilder(t).Select("*").From("users").JoinLateral(sub, "contacts")
		want := "select * from `users` inner join lateral (select `id`, `name` from `contacts` where `contacts`.`user_id` = `users`.`id`) as `contacts` on true"
		if got := b.ToSql(); got != want {
			t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
		}
	})

	t.Run("mysql left lateral", func(t *testing.T) {
		b := mysqlBuilder(t).Select("*").From("users").LeftJoinLateral(sub, "contacts")
		want := "select * from `users` left join lateral (select `id`, `name` from `contacts` where `contacts`.`user_id` = `users`.`id`) as `contacts` on true"
		if got := b.ToSql(); got != want {
			t.Errorf("sql = %s", got)
		}
	})

	t.Run("postgres", func(t *testing.T) {
		b := pgBuilder(t).Select("*").From("users").JoinLateral(sub, "contacts")
		want := `select * from "users" inner join lateral (select "id", "name" from "contacts" where "contacts"."user_id" = "users"."id") as "contacts" on true`
		if got := b.ToSql(); got != want {
			t.Errorf("sql = %s", got)
		}
	})

	t.Run("sqlserver cross apply", func(t *testing.T) {
		g := grammar.NewSqlServerGrammar()
		b := NewBuilder(nil, g).Select("*").From("users").JoinLateral(sub, "contacts")
		want := "select * from [users] cross apply (select [id], [name] from [contacts] where [contacts].[user_id] = [users].[id]) as [contacts]"
		if got := b.ToSql(); got != want {
			t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
		}
	})

	t.Run("sqlserver outer apply", func(t *testing.T) {
		g := grammar.NewSqlServerGrammar()
		b := NewBuilder(nil, g).Select("*").From("users").LeftJoinLateral(sub, "contacts")
		want := "select * from [users] outer apply (select [id], [name] from [contacts] where [contacts].[user_id] = [users].[id]) as [contacts]"
		if got := b.ToSql(); got != want {
			t.Errorf("sql = %s", got)
		}
	})

	t.Run("base dialect panics", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("expect panic")
			}
		}()
		baseBuilder(t).Select("*").From("users").JoinLateral(sub, "contacts").ToSql()
	})

	t.Run("mariadb panics", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("expect panic")
			}
		}()
		g := grammar.NewMariaDbGrammar()
		NewBuilder(nil, g).Select("*").From("users").JoinLateral(sub, "contacts").ToSql()
	})
}

func TestGoldenCrossJoinSub(t *testing.T) {
	cases := []goldenCase{
		{
			name: "cross join sub",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").CrossJoinSub(func(n *Builder) *Builder { return n.Select("id").From("contacts") }, "contacts")
			},
			wantBase:  `select * from "users" cross join (select "id" from "contacts") as "contacts"`,
			wantMySQL: "select * from `users` cross join (select `id` from `contacts`) as `contacts`",
		},
	}
	runGolden(t, cases)
}

func TestGoldenStraightJoin(t *testing.T) {
	t.Run("mysql supported", func(t *testing.T) {
		b := mysqlBuilder(t).Select("*").From("users").StraightJoin("contacts", "users.id", "=", "contacts.user_id")
		want := "select * from `users` straight_join `contacts` on `users`.`id` = `contacts`.`user_id`"
		if got := b.ToSql(); got != want {
			t.Errorf("sql = %s", got)
		}
	})

	t.Run("base dialect panics", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("expect panic")
			}
		}()
		baseBuilder(t).Select("*").From("users").StraightJoin("contacts", "users.id", "=", "contacts.user_id").ToSql()
	})
}

// ---- R3-A: whereAll / whereAny / whereNone / whereExpression / bitwise ----

func TestGoldenWhereAllAnyNone(t *testing.T) {
	cases := []goldenCase{
		{
			name:     "where all",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").WhereAll([]interface{}{"a", "b"}, "=", 1) },
			wantBase: `select * from "users" where ("a" = ? and "b" = ?)`,
			wantMySQL: "select * from `users` where (`a` = ? and `b` = ?)",
			bindings: []interface{}{1, 1},
		},
		{
			name:     "or where all",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").Where("id", 1).OrWhereAll([]interface{}{"a", "b"}, "=", 1) },
			wantBase: `select * from "users" where "id" = ? or ("a" = ? and "b" = ?)`,
		},
		{
			name:     "where any",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").WhereAny([]interface{}{"a", "b"}, "=", 1) },
			wantBase: `select * from "users" where ("a" = ? or "b" = ?)`,
		},
		{
			name:     "or where any",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").Where("id", 1).OrWhereAny([]interface{}{"a", "b"}, "=", 1) },
			wantBase: `select * from "users" where "id" = ? or ("a" = ? or "b" = ?)`,
		},
		{
			name:     "where none",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").WhereNone([]interface{}{"a", "b"}, "=", 1) },
			wantBase: `select * from "users" where not ("a" = ? or "b" = ?)`,
		},
		{
			name:     "or where none",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").Where("id", 1).OrWhereNone([]interface{}{"a", "b"}, "=", 1) },
			wantBase: `select * from "users" where "id" = ? or not ("a" = ? or "b" = ?)`,
		},
	}
	runGolden(t, cases)
}

func TestGoldenWhereExpression(t *testing.T) {
	cases := []goldenCase{
		{
			name:     "where expression",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").WhereExpression("ISNULL(`name`)", "or") },
			wantBase: `select * from "users" where ISNULL(` + "`name`" + `)`,
		},
	}
	runGolden(t, cases)
}

func TestGoldenBitwise(t *testing.T) {
	cases := []goldenCase{
		{
			name:     "base bitwise where compiles basic",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").Where("flags", "&", 1) },
			wantBase: `select * from "users" where "flags" & ?`,
			wantMySQL: "select * from `users` where `flags` & ?",
		},
		{
			name:  "postgres bitwise casts bool",
			build: func(b *Builder) *Builder { return pgBuilder(t).Select("*").From("users").Where("flags", "&", 1) },
			wantBase: `select * from "users" where ("flags" & ?)::bool`,
		},
		{
			name:  "postgres bitwise escaped operator",
			build: func(b *Builder) *Builder { return pgBuilder(t).Select("*").From("users").Where("flags", ">>", 1) },
			wantBase: `select * from "users" where ("flags" >> ?)::bool`,
		},
		{
			name:  "sqlserver bitwise != 0",
			build: func(b *Builder) *Builder {
				g := grammar.NewSqlServerGrammar()
				return NewBuilder(nil, g).Select("*").From("users").Where("flags", "&", 1)
			},
			wantBase: "select * from [users] where ([flags] & ?) != 0",
		},
	}
	runGolden(t, cases)

	t.Run("postgres bitwise having", func(t *testing.T) {
		b := pgBuilder(t).Select("*").From("users").GroupBy("id").Having("flags", "&", 1)
		want := `select * from "users" group by "id" having ("flags" & ?)::bool`
		if got := b.ToSql(); got != want {
			t.Errorf("sql = %s", got)
		}
	})

	t.Run("sqlserver bitwise having", func(t *testing.T) {
		g := grammar.NewSqlServerGrammar()
		b := NewBuilder(nil, g).Select("*").From("users").GroupBy("id").Having("flags", "&", 1)
		want := "select * from [users] group by [id] having ([flags] & ?) != 0"
		if got := b.ToSql(); got != want {
			t.Errorf("sql = %s", got)
		}
	})
}

// ---- R3-A: 方言残余 ----

func TestGoldenSqlServerResiduals(t *testing.T) {
	sqlserver := func(t *testing.T) *Builder {
		return NewBuilder(nil, grammar.NewSqlServerGrammar())
	}

	t.Run("upsert merge", func(t *testing.T) {
		b := sqlserver(t).From("users")
		sql, bindings := b.GetGrammar().CompileUpsert(b.Query, []query.Row{
			query.ToRow(map[string]any{"email": "foo", "name": "bar"}),
		}, []string{"email"}, []string{"name"})
		want := "merge [users] using (values (?, ?)) [laravel_source] ([email], [name]) on [laravel_source].[email] = [users].[email] when matched then update set [name] = [laravel_source].[name] when not matched then insert ([email], [name]) values ([email], [name]);"
		if sql != want {
			t.Errorf("sql =\n  %s\nwant\n  %s", sql, want)
		}
		if !reflect.DeepEqual(bindings, []any{"foo", "bar"}) {
			t.Errorf("bindings = %#v", bindings)
		}
	})

	t.Run("update top n", func(t *testing.T) {
		b := sqlserver(t).From("users").Where("email", "=", "foo").Limit(5)
		sql := b.GetGrammar().CompileUpdate(b.Query, map[string]any{"name": "bar"})
		want := "update top (5) [users] set [name] = ? where [email] = ?"
		if sql != want {
			t.Errorf("sql =\n  %s\nwant\n  %s", sql, want)
		}
	})

	t.Run("update with joins uses alias from", func(t *testing.T) {
		b := sqlserver(t).From("users").Join("contacts", "users.id", "=", "contacts.user_id")
		sql := b.GetGrammar().CompileUpdate(b.Query, map[string]any{"name": "bar"})
		want := "update [users] set [name] = ? from [users] inner join [contacts] on [users].[id] = [contacts].[user_id]"
		if sql != want {
			t.Errorf("sql =\n  %s\nwant\n  %s", sql, want)
		}
	})

	t.Run("delete top n", func(t *testing.T) {
		b := sqlserver(t).From("users").Where("email", "=", "foo").Limit(5)
		sql := b.GetGrammar().CompileDelete(b.Query)
		want := "delete top (5) from [users] where [email] = ?"
		if sql != want {
			t.Errorf("sql = %s", sql)
		}
	})

	t.Run("wrap union as temp table", func(t *testing.T) {
		b := sqlserver(t).Select("id").From("users").Union(func(n *Builder) *Builder { return n.Select("id").From("archives") })
		want := "select * from (select [id] from [users]) as [temp_table] union select * from (select [id] from [archives]) as [temp_table]"
		if got := b.ToSql(); got != want {
			t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
		}
	})

	t.Run("row number empty orders fallback", func(t *testing.T) {
		g := grammar.NewSqlServerGrammar()
		b := NewBuilder(nil, g).From("users").Select("*")
		b.Query.GroupLimit = &query.GroupLimit{Column: "id", Value: 2}
		want := `select * from (select *, row_number() over (partition by [id] order by (select 0)) as [laravel_row] from [users]) as [laravel_table] where [laravel_row] <= 2 order by [laravel_row]`
		if got := b.ToSql(); got != want {
			t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
		}
	})

	t.Run("save transaction savepoints", func(t *testing.T) {
		g := grammar.NewSqlServerGrammar()
		if got := g.CompileSavepoint("sp1"); got != "SAVE TRANSACTION sp1" {
			t.Errorf("savepoint = %s", got)
		}
		if got := g.CompileSavepointRollBack("sp1"); got != "ROLLBACK TRANSACTION sp1" {
			t.Errorf("rollback = %s", got)
		}
	})

	t.Run("date format with milliseconds", func(t *testing.T) {
		g := grammar.NewSqlServerGrammar()
		if got := g.GetDateFormat(); got != "Y-m-d H:i:s.v" {
			t.Errorf("date format = %s", got)
		}
	})

	t.Run("null safe equals intersect", func(t *testing.T) {
		b := sqlserver(t).Select("*").From("users").Where("a", "<=>", 1)
		want := "select * from [users] where exists (select [a] intersect select ?)"
		if got := b.ToSql(); got != want {
			t.Errorf("sql = %s", got)
		}
	})

	t.Run("json value cast", func(t *testing.T) {
		g := grammar.NewSqlServerGrammar()
		if got := g.CompileJsonValueCast("?"); got != "json_query(?)" {
			t.Errorf("cast = %s", got)
		}
	})
}

func TestGoldenMySqlResiduals(t *testing.T) {
	t.Run("upsert alias", func(t *testing.T) {
		g := grammar.NewMySqlGrammar()
		g.SetUseUpsertAlias(true)
		b := NewBuilder(nil, g).From("users")
		sql, _ := b.GetGrammar().CompileUpsert(b.Query, []query.Row{query.ToRow(map[string]any{"email": "foo", "name": "bar"})}, []string{"email"}, []string{"name"})
		want := "insert into `users` (`email`, `name`) values (?, ?) as laravel_upsert_alias on duplicate key update `name` = `laravel_upsert_alias`.`name`"
		if sql != want {
			t.Errorf("sql =\n  %s\nwant\n  %s", sql, want)
		}
	})

	t.Run("prepare bindings for update json", func(t *testing.T) {
		g := grammar.NewMySqlGrammar()
		bindings := map[string][]any{
			"join":  {"j1"},
			"where": {"w1"},
		}
		values := map[string]any{
			"options->active": true,
			"tags":            []any{"a", "b"},
			"name":            "x",
		}
		got := g.PrepareBindingsForUpdate(bindings, values)
		// bool 值被剔除；数组 json_encode；顺序：join、values（sorted keys）、where
		want := []any{"j1", "x", `["a","b"]`, "w1"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("bindings = %#v, want %#v", got, want)
		}
	})

	t.Run("json value cast", func(t *testing.T) {
		g := grammar.NewMySqlGrammar()
		if got := g.CompileJsonValueCast("?"); got != "cast(? as json)" {
			t.Errorf("cast = %s", got)
		}
	})
}

func TestGoldenMariaDbResiduals(t *testing.T) {
	g := grammar.NewMariaDbGrammar()
	b := NewBuilder(nil, g).Select("*").From("users").Where("options->theme", "=", "dark")
	want := "select * from `users` where json_value(`options`, '$.\"theme\"') = ?"
	if got := b.ToSql(); got != want {
		t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
	}
	if got := g.CompileJsonValueCast("?"); got != "json_query(?, '$')" {
		t.Errorf("cast = %s", got)
	}
}

func TestGoldenPostgresResiduals(t *testing.T) {
	t.Run("truncate cascade", func(t *testing.T) {
		b := pgBuilder(t).From("users")
		stmts := b.GetGrammar().CompileTruncate(b.Query)
		if len(stmts) != 1 || stmts[0].Sql != `truncate "users" restart identity cascade` {
			t.Errorf("stmts = %#v", stmts)
		}
	})

	t.Run("full text language whitelist", func(t *testing.T) {
		b := pgBuilder(t).Select("*").From("users").WhereFullText("body", "hello", map[string]any{"language": "klingon"})
		want := `select * from "users" where (to_tsvector('english', "body")) @@ plainto_tsquery('english', ?)`
		if got := b.ToSql(); got != want {
			t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
		}
		b2 := pgBuilder(t).Select("*").From("users").WhereFullText("body", "hello", map[string]any{"language": "russian"})
		want2 := `select * from "users" where (to_tsvector('russian', "body")) @@ plainto_tsquery('russian', ?)`
		if got := b2.ToSql(); got != want2 {
			t.Errorf("sql = %s", got)
		}
	})

	t.Run("substitute bindings into raw sql unescapes operators", func(t *testing.T) {
		g := grammar.NewPostgresGrammar()
		got := g.SubstituteBindingsIntoRawSql("data ?? 'key' and id = ?", []any{1})
		want := "data ? 'key' and id = 1"
		if got != want {
			t.Errorf("sql = %s, want %s", got, want)
		}
		got2 := g.SubstituteBindingsIntoRawSql("arr ??| ? and id = ?", []any{"a", 1})
		want2 := "arr ?| 'a' and id = 1"
		if got2 != want2 {
			t.Errorf("sql = %s, want %s", got2, want2)
		}
	})

	t.Run("prepare bindings for update json encoding", func(t *testing.T) {
		g := grammar.NewPostgresGrammar()
		bindings := map[string][]any{"join": {"j1"}, "where": {"w1"}}
		values := map[string]any{"tags": []any{"a"}, "options->x": "y", "name": "n"}
		got := g.PrepareBindingsForUpdate(bindings, values)
		want := []any{"n", `"y"`, `["a"]`, "j1", "w1"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("bindings = %#v, want %#v", got, want)
		}
	})
}

// ---- 20261106 报告查询层补全（WhereValueBetween 族 / NullSafeEquals /
// orWhereJsonDoesntOverlap / straightJoinSub / average / ddRawSql /
// GroupLimit / insertOrIgnoreReturning / 操作符表）----

// ★ whereValueBetween 族（Builder.php:1789-1843）：值介于两列之间
func TestGoldenWhereValueBetween(t *testing.T) {
	cases := []goldenCase{
		{
			name: "where value between",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereValueBetween(7, []interface{}{"min_salary", "max_salary"})
			},
			wantBase:  `select * from "users" where ? between "min_salary" and "max_salary"`,
			wantMySQL: "select * from `users` where ? between `min_salary` and `max_salary`",
			bindings:  []interface{}{7},
		},
		{
			name: "or where value between",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").Where("id", "=", 1).
					OrWhereValueBetween(7, []interface{}{"min_salary", "max_salary"})
			},
			wantBase: `select * from "users" where "id" = ? or ? between "min_salary" and "max_salary"`,
			bindings: []interface{}{1, 7},
		},
		{
			name: "where value not between",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereValueNotBetween(7, []interface{}{"a", "b"})
			},
			wantBase: `select * from "users" where ? not between "a" and "b"`,
			bindings: []interface{}{7},
		},
		{
			name: "or where value not between",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").Where("id", "=", 1).
					OrWhereValueNotBetween(7, []interface{}{"a", "b"})
			},
			wantBase: `select * from "users" where "id" = ? or ? not between "a" and "b"`,
			bindings: []interface{}{1, 7},
		},
	}
	runGolden(t, cases)
}

// ★ whereNullSafeEquals（Builder.php:1442-1467）
func TestGoldenWhereNullSafeEquals(t *testing.T) {
	cases := []goldenCase{
		{
			name: "where null safe equals",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereNullSafeEquals("score", 10)
			},
			wantBase:  `select * from "users" where "score" is not distinct from ?`,
			wantMySQL: "select * from `users` where `score` <=> ?",
			bindings:  []interface{}{10},
		},
		{
			name: "or where null safe equals",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").Where("id", "=", 1).OrWhereNullSafeEquals("score", nil)
			},
			wantBase: `select * from "users" where "id" = ? or "score" is not distinct from ?`,
			bindings: []interface{}{1, nil},
		},
	}
	runGolden(t, cases)

	// SqlServer 方言：nullSafeEquals 编译为 =（null 值时 is）
	b := NewBuilder(nil, grammar.NewSqlServerGrammar()).Select("*").From("users").WhereNullSafeEquals("score", 10)
	if got := b.ToSql(); got != "select * from [users] where exists (select [score] intersect select ?)" {
		t.Errorf("sqlserver sql = %s", got)
	}
}

// ★ orWhereJsonDoesntOverlap（Builder.php:2420）与 whereJsonOverlaps 族对齐
func TestGoldenWhereJsonOverlaps(t *testing.T) {
	cases := []goldenCase{
		{
			name: "where json overlaps",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").WhereJsonOverlaps("tags", []interface{}{"a", "b"})
			},
			wantMySQL: "select * from `users` where json_overlaps(`tags`, ?)",
			bindings:  []interface{}{`["a","b"]`},
		},
		{
			name: "or where json doesnt overlap",
			build: func(b *Builder) *Builder {
				return b.Select("*").From("users").Where("id", "=", 1).
					OrWhereJsonDoesntOverlap("tags", []interface{}{"a"})
			},
			wantMySQL: "select * from `users` where `id` = ? or not json_overlaps(`tags`, ?)",
			bindings:  []interface{}{1, `["a"]`},
		},
	}
	runGolden(t, cases)
}

// ★ straightJoinSub（Builder.php:874）
func TestGoldenStraightJoinSub(t *testing.T) {
	sub := func(nb *Builder) *Builder {
		return nb.Select("*").From("contacts")
	}
	b := mysqlBuilder(t).Select("*").From("users").
		StraightJoinSub(sub, "contacts", "users.id", "=", "contacts.user_id")
	want := "select * from `users` straight_join (select * from `contacts`) as `contacts` on `users`.`id` = `contacts`.`user_id`"
	if got := b.ToSql(); got != want {
		t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
	}

	// 基础方言：straight_join 不支持，编译时 panic
	defer func() {
		if recover() == nil {
			t.Error("expect panic on base dialect")
		}
	}()
	baseBuilder(t).Select("*").From("users").
		StraightJoinSub(sub, "contacts", "users.id", "=", "contacts.user_id").ToSql()
}

// ★ average = avg 别名（Builder.php:4112）
func TestBuilderAverageAlias(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select avg(`price`) as `aggregate` from `products`", &fakeResult{
		Columns: []string{"aggregate"},
		Rows:    [][]driver.Value{{"9.75"}},
	})
	var avg float64
	if err := conn.Table("products").Average("price", &avg); err != nil {
		t.Fatal(err)
	}
	if avg != 9.75 {
		t.Errorf("average = %v", avg)
	}
}

// ★ GroupLimit 公开入口（Builder.php:3247）
func TestBuilderGroupLimitEntry(t *testing.T) {
	b := baseBuilder(t).Select("posts.*").From("posts").OrderBy("created_at", "desc").GroupLimit(2, "user_id")
	want := `select * from (select "posts".*, row_number() over (partition by "user_id" order by "created_at" desc) as "laravel_row" from "posts") as "laravel_table" where "laravel_row" <= 2 order by "laravel_row"`
	if got := b.ToSql(); got != want {
		t.Errorf("sql =\n  %s\nwant\n  %s", got, want)
	}
	// 负值不设置（对齐 Laravel if ($value >= 0)）
	nb := baseBuilder(t).Select("*").From("posts").GroupLimit(-1, "user_id")
	if nb.Query.GroupLimit != nil {
		t.Errorf("negative groupLimit should be ignored, got %+v", nb.Query.GroupLimit)
	}
}

// ★ insertOrIgnoreReturning（Builder.php:4262）：方言钩子未接线时 panic
// ErrUnsupported；空 values 不发 SQL
func TestBuilderInsertOrIgnoreReturning(t *testing.T) {
	b := mysqlBuilder(t).From("users")
	if rows, err := b.InsertOrIgnoreReturning(nil, []string{"id"}); err != nil || len(rows) != 0 {
		t.Errorf("empty values = %v, %v", rows, err)
	}

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expect ErrUnsupported panic without grammar hook")
			}
		}()
		_, _ = mysqlBuilder(t).From("users").InsertOrIgnoreReturning(
			[]interface{}{map[string]interface{}{"name": "a"}}, []string{"id"})
	}()

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expect panic for empty returning")
			}
		}()
		_, _ = mysqlBuilder(t).From("users").InsertOrIgnoreReturning(
			[]interface{}{map[string]interface{}{"name": "a"}}, nil)
	}()

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expect panic for empty uniqueBy")
			}
		}()
		_, _ = mysqlBuilder(t).From("users").InsertOrIgnoreReturning(
			[]interface{}{map[string]interface{}{"name": "a"}}, []string{"id"}, []string{})
	}()
}

// ★ 操作符表补全（Builder.php:248-255：is / is not / &~ / ~~* / !~~* 等）
func TestGoldenOperatorTable(t *testing.T) {
	cases := []goldenCase{
		{
			name:     "is operator",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").Where("flags", "is", grammar.NewExpression("null")) },
			wantBase: `select * from "users" where "flags" is null`,
		},
		{
			name:     "is not operator",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").Where("flags", "is not", grammar.NewExpression("null")) },
			wantBase: `select * from "users" where "flags" is not null`,
		},
		{
			name:     "tilde tilde star operator",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").Where("name", "~~*", "a%") },
			wantBase: `select * from "users" where "name" ~~* ?`,
			bindings: []interface{}{"a%"},
		},
		{
			name:     "bang tilde tilde operator",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").Where("name", "!~~", "a%") },
			wantBase: `select * from "users" where "name" !~~ ?`,
			bindings: []interface{}{"a%"},
		},
		{
			name:     "and tilde bitwise operator",
			build:    func(b *Builder) *Builder { return b.Select("*").From("users").Where("flags", "&~", 1) },
			wantBase: `select * from "users" where "flags" &~ ?`,
			bindings: []interface{}{1},
		},
	}
	runGolden(t, cases)

	// &~ 是位运算符（Laravel $bitwiseOperators），PG 编译追加 ::bool
	b := pgBuilder(t).Select("*").From("users").Where("flags", "&~", 1)
	if got := b.ToSql(); got != `select * from "users" where ("flags" &~ ?)::bool` {
		t.Errorf("pg bitwise and-tilde = %s", got)
	}
}

// ★ DDRawSql：打印内联绑定 SQL 后返回 builder（不做 os.Exit，与 Laravel dd 的
// 差异见 DDRawSql 注释）
func TestBuilderDDRawSql(t *testing.T) {
	b := baseBuilder(t).Select("*").From("users").Where("id", "=", 1)
	if got := b.DDRawSql(); got != b {
		t.Error("DDRawSql should return the builder")
	}
}

// CompileInsertOrIgnoreReturning：PG/SQLite 方言已接线（Laravel
// PostgresGrammar.php:406-414 / SQLiteGrammar.php:300-309）；SqlServer 无此
// 编译（Laravel 亦无），panic 即正确行为。
func TestGoldenInsertOrIgnoreReturningWired(t *testing.T) {
	pg := grammar.NewPostgresGrammar()
	q := &query.Query{From: "users"}
	sql, bindings := pg.CompileInsertOrIgnoreReturning(q,
		[]query.Row{toRow(map[string]interface{}{"email": "foo"})},
		[]string{"id", "email"}, []string{"email"})
	want := `insert into "users" ("email") values (?) on conflict ("email") do nothing returning "id", "email"`
	if sql != want {
		t.Errorf("pg sql = %s\nwant %s", sql, want)
	}
	if len(bindings) != 1 || bindings[0] != "foo" {
		t.Errorf("bindings = %#v", bindings)
	}

	sq := grammar.NewSqliteQueryGrammar()
	sql2, _ := sq.CompileInsertOrIgnoreReturning(q,
		[]query.Row{toRow(map[string]interface{}{"email": "foo"})},
		[]string{"id"}, nil)
		// SQLiteGrammar.php:300-309：compileInsertOrIgnoreReturning =
	// compileInsertOrIgnore（insert or ignore，无 on conflict）+ returning
	want2 := `insert or ignore into "users" ("email") values (?) returning "id"`
	if sql2 != want2 {
		t.Errorf("sqlite sql = %s\nwant %s", sql2, want2)
	}
}
