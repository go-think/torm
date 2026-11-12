package grammar

import (
	"reflect"
	"strings"
	"testing"
)

// SQL Server 方言 golden 测试（SQL 期望移植自 Laravel 13 SqlServerGrammar 输出）。

func ssG() *SqlServerGrammar { return &SqlServerGrammar{} }

func init() {
	// SqlServerGrammar 查询侧在构造函数中注入 [标识符] wrap；
	// schema 测试直接构造时需保证 wrapValueFn 生效。
}

func TestSchemaSQLServerCreateAndTypes(t *testing.T) {
	g := NewSqlServerGrammar()
	b := NewBlueprint("users")
	b.Create = true
	b.ID()
	b.String("email", 100).Nullable()
	b.Timestamp("ts")
	b.Timestamp("ts3", 3)
	b.Time("ti")
	b.Binary("bi")
	b.UUID("u")
	b.Float("f", 10)
	b.Double("d")
	b.Year("y")
	b.IpAddress()
	b.JSON("j")
	b.Enum("e", []string{"x", "y"})
	b.Boolean("bo")

	got := b.ToSql(g)
	want := []string{"create table \"users\" (" +
		`"id" bigint not null identity primary key, "email" nvarchar(100) null, ` +
		`"ts" datetime not null, "ts3" datetime2(3) not null, "ti" time not null, ` +
		`"bi" varbinary(max) not null, "u" uniqueidentifier not null, ` +
		`"f" float(10) not null, "d" double precision not null, "y" int not null, ` +
		`"ip_address" nvarchar(45) not null, "j" nvarchar(max) not null, ` +
		`"e" nvarchar(255) check ("e" in (N'x', N'y')) not null, "bo" bit not null)`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %s\nwant\n  %s", got[0], want[0])
	}
}

func TestSchemaSQLServerDrops(t *testing.T) {
	g := NewSqlServerGrammar()
	b := NewBlueprint("users")
	b.DropPrimary()
	b.DropUnique("users_email_unique")
	b.DropIndex("users_email_index")
	b.DropForeign("users_user_id_foreign")
	b.DropColumn("a", "b")

	got := b.ToSql(g)
	want := []string{
		`alter table "users" drop constraint "users__dropprimary"`,
		`drop index "users_email_unique" on "users"`,
		`drop index "users_email_index" on "users"`,
		`alter table "users" drop constraint "users_user_id_foreign"`,
		// D5：drop column 前拼接默认约束删除块（对齐 SqlServerGrammar::compileDropColumn）
		`DECLARE @sql NVARCHAR(MAX) = '';` +
			`SELECT @sql += 'ALTER TABLE "users" DROP CONSTRAINT ' + OBJECT_NAME([default_object_id]) + ';' ` +
			`FROM sys.columns ` +
			`WHERE [object_id] = OBJECT_ID('"users"') AND [name] in ('a', 'b') AND [default_object_id] <> 0;` +
			`EXEC(@sql);alter table "users" drop column "a", "b"`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSchemaSQLServerRenames(t *testing.T) {
	g := NewSqlServerGrammar()
	b := NewBlueprint("users")
	b.Rename("admins")
	b.RenameColumn("a", "b")
	b.RenameIndex("a", "b")

	got := b.ToSql(g)
	want := []string{
		`sp_rename N'"users"', "admins"`,
		`sp_rename N'"users"."a"', "b", N'COLUMN'`,
		`sp_rename N'"users"."a"', "b", N'INDEX'`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSchemaSQLServerIndexAndPrimary(t *testing.T) {
	g := NewSqlServerGrammar()
	b := NewBlueprint("users")
	b.String("email")
	b.Index([]string{"email"})
	b.Primary([]string{"email"})

	got := b.ToSql(g)
	want := []string{
		`alter table "users" add "email" nvarchar(255) not null`,
		`create index "users_email_index" on "users" ("email")`,
		`alter table "users" add constraint "users_email_primary" primary key ("email")`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSchemaSQLServerMorphs(t *testing.T) {
	g := NewSqlServerGrammar()
	b := NewBlueprint("users")
	b.Morphs("tag")

	got := b.ToSql(g)
	want := []string{
		`alter table "users" add "tag_type" nvarchar(255) not null`,
		`alter table "users" add "tag_id" bigint not null`,
		`create index "users_tag_type_tag_id_index" on "users" ("tag_type", "tag_id")`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSchemaSQLServerVectorIndexPanics(t *testing.T) {
	g := NewSqlServerGrammar()
	b := NewBlueprint("users")
	b.VectorIndex("v")
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	b.ToSql(g)
}

func TestSchemaSQLServerIntrospection(t *testing.T) {
	g := NewSqlServerGrammar()
	sql, args := g.CompileTableExists("", "users")
	if !strings.Contains(sql, "object_id") {
		t.Errorf("table exists = %s", sql)
	}
	if len(args) != 0 {
		t.Errorf("args = %v", args)
	}
}
