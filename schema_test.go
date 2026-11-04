package torm

import (
	"database/sql/driver"
	"reflect"
	"strings"
	"testing"

	"github.com/thinkoner/torm/grammar"
)

// 原有模型字段映射测试
func TestNewSchema(t *testing.T) {
	schema, err := NewSchema(&User{})
	if err != nil {
		t.Error(err)
	}
	t.Log(schema.Fields)
}

// 期望 SQL 移植自 laravel/framework tests/Database/DatabaseMySqlSchemaGrammarTest.php 等。

func mysqlSchemaGrammar(prefix ...string) grammar.SchemaGrammar {
	g := grammar.NewMySqlGrammar()
	if len(prefix) > 0 {
		g.SetTablePrefix(prefix[0])
	}
	return g
}

func pgSchemaGrammar() grammar.SchemaGrammar {
	return grammar.NewPostgresGrammar()
}

func TestSchemaMySQLBasicCreate(t *testing.T) {
	b := grammar.NewBlueprint("users")
	b.Create = true
	b.Charset = "utf8"
	b.Collation = "utf8_unicode_ci"
	b.Increments("id")
	b.String("email")

	got := b.ToSql(mysqlSchemaGrammar())
	want := []string{"create table `users` (`id` int unsigned not null auto_increment primary key, `email` varchar(255) not null) default character set utf8 collate 'utf8_unicode_ci'"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSchemaMySQLAlter(t *testing.T) {
	b := grammar.NewBlueprint("users")
	b.Increments("id")
	b.String("email")

	got := b.ToSql(mysqlSchemaGrammar())
	want := []string{
		"alter table `users` add `id` int unsigned not null auto_increment primary key",
		"alter table `users` add `email` varchar(255) not null",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSchemaMySQLUuidPrimary(t *testing.T) {
	b := grammar.NewBlueprint("users")
	b.Create = true
	b.UUID("id").Primary()

	got := b.ToSql(mysqlSchemaGrammar())
	want := []string{"create table `users` (`id` char(36) not null, primary key (`id`))"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSchemaMySQLAutoIncrementStartingValue(t *testing.T) {
	b := grammar.NewBlueprint("users")
	b.ID().StartingValue(100)
	b.String("name")

	got := b.ToSql(mysqlSchemaGrammar())
	want := []string{
		"alter table `users` add `id` bigint unsigned not null auto_increment primary key",
		"alter table `users` add `name` varchar(255) not null",
		"alter table `users` auto_increment = 100",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSchemaMySQLEngine(t *testing.T) {
	b := grammar.NewBlueprint("users")
	b.Create = true
	b.Charset = "utf8"
	b.Collation = "utf8_unicode_ci"
	b.Engine = "InnoDB"
	b.Increments("id")
	b.String("email")

	got := b.ToSql(mysqlSchemaGrammar())
	want := []string{"create table `users` (`id` int unsigned not null auto_increment primary key, `email` varchar(255) not null) default character set utf8 collate 'utf8_unicode_ci' engine = InnoDB"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSchemaMySQLForeign(t *testing.T) {
	b := grammar.NewBlueprint("orders")
	b.Create = true
	b.ID()
	b.ForeignID("user_id")
	b.Foreign("user_id").References("id").On("users").CascadeOnDelete()

	got := b.ToSql(mysqlSchemaGrammar())
	if len(got) != 2 {
		t.Fatalf("statements = %v", got)
	}
	// 对齐 Laravel：MySQL 建表只含列，外键为独立 alter 语句
	if !strings.Contains(got[1], "alter table `orders` add constraint `orders_user_id_foreign` foreign key (`user_id`) references `users` (`id`) on delete cascade") {
		t.Errorf("sql = %s", got[1])
	}
}

func TestSchemaMySQLDrop(t *testing.T) {
	b := grammar.NewBlueprint("users")
	b.Drop()
	if got := b.ToSql(mysqlSchemaGrammar()); !reflect.DeepEqual(got, []string{"drop table `users`"}) {
		t.Errorf("sql = %v", got)
	}

	b2 := grammar.NewBlueprint("users")
	b2.DropIfExists()
	if got := b2.ToSql(mysqlSchemaGrammar()); !reflect.DeepEqual(got, []string{"drop table if exists `users`"}) {
		t.Errorf("sql = %v", got)
	}
}

func TestSchemaPGCreate(t *testing.T) {
	b := grammar.NewBlueprint("users")
	b.Create = true
	b.Increments("id")
	b.String("email")

	got := b.ToSql(pgSchemaGrammar())
	want := []string{`create table "users" ("id" serial not null primary key, "email" varchar(255) not null)`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSchemaPGTimestampsAndJSON(t *testing.T) {
	b := grammar.NewBlueprint("posts")
	b.Create = true
	b.ID()
	b.JSON("meta").Nullable()
	b.Timestamps()
	b.SoftDeletes()

	got := b.ToSql(pgSchemaGrammar())
	if len(got) != 1 {
		t.Fatalf("statements = %v", got)
	}
	for _, frag := range []string{`"id" bigserial not null primary key`, `"meta" jsonb null`, `"created_at" timestamp(0) without time zone null`, `"deleted_at" timestamp(0) without time zone null`} {
		if !strings.Contains(got[0], frag) {
			t.Errorf("missing %q in %s", frag, got[0])
		}
	}
}

func TestSchemaPGIndex(t *testing.T) {
	b := grammar.NewBlueprint("users")
	b.String("email")
	b.Unique([]string{"email"})

	got := b.ToSql(pgSchemaGrammar())
	want := []string{
		`alter table "users" add column "email" varchar(255) not null`,
		`create unique index "users_email_unique" on "users" ("email")`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSchemaPGDropIfExists(t *testing.T) {
	b := grammar.NewBlueprint("users")
	b.DropIfExists()
	if got := b.ToSql(pgSchemaGrammar()); !reflect.DeepEqual(got, []string{`drop table if exists "users"`}) {
		t.Errorf("sql = %v", got)
	}
}

// ---- SchemaBuilder 执行测试（fake driver）----

func TestSchemaBuilderExecutes(t *testing.T) {
	conn, st := newFakeDB(t)
	createSQL := "create table `users` (`id` bigint unsigned not null auto_increment primary key, `email` varchar(255) not null)"
	st.On(createSQL, &fakeResult{})
	st.On("drop table if exists `old_users`", &fakeResult{})

	if err := conn.Schema().CreateTable("users", func(t *grammar.Blueprint) {
		t.ID()
		t.String("email")
	}); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, createSQL)

	if err := conn.Schema().DropIfExists("old_users"); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, "drop table if exists `old_users`")
}

func TestSchemaBuilderHasTable(t *testing.T) {
	conn, st := newFakeDB(t)
	sql := "select * from information_schema.tables where table_schema = ? and table_name = ? and table_type = 'BASE TABLE'"
	st.On(sql, &fakeResult{Columns: []string{"table_name"}, Rows: [][]driver.Value{{"users"}}})

	ok, err := conn.Schema().HasTable("users")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expect table exists")
	}
	call := st.LastCall(t, sql)
	if len(call.Args) != 2 || call.Args[1] != "users" {
		t.Errorf("args = %#v", call.Args)
	}
}
