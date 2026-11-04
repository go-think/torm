package torm

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/thinkoner/torm/grammar"
	"github.com/thinkoner/torm/query"
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
	want2 := "select * from [users] offset 10 rows fetch next 5 rows only"
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
