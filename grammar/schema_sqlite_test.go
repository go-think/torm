package grammar

import (
	"reflect"
	"strings"
	"testing"
)

// SQLite 方言 golden 测试（SQL 期望移植自 Laravel 13 SQLiteGrammar 输出）。

func sqliteG() *SQLiteGrammar { return &SQLiteGrammar{} }

func TestSchemaSQLiteCreateAndTypes(t *testing.T) {
	b := NewBlueprint("users")
	b.Create = true
	b.ID()
	b.String("email")
	b.UUID("u")
	b.Ulid("ul")
	b.JSON("j")
	b.JSONB("jb")
	b.TinyText("tt")
	b.LongText("lt")
	b.Time("ti")
	b.Year("y")
	b.Binary("bi")
	b.Enum("e", []string{"x", "y"})
	b.Float("f", 10)
	b.Double("d")
	b.IpAddress()
	b.TinyInteger("tiny")
	b.Timestamp("ts", 3)
	b.Boolean("bo")

	got := b.ToSql(sqliteG())
	want := []string{"create table \"users\" (" +
		`"id" integer primary key autoincrement not null, "email" varchar not null, ` +
		`"u" varchar not null, "ul" varchar not null, "j" text not null, "jb" text not null, ` +
		`"tt" text not null, "lt" text not null, "ti" time not null, "y" integer not null, ` +
		`"bi" blob not null, "e" varchar check ("e" in ('x', 'y')) not null, ` +
		`"f" float not null, "d" double not null, "ip_address" varchar not null, ` +
		`"tiny" integer not null, "ts" datetime not null, "bo" tinyint(1) not null)`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %s\nwant\n  %s", got[0], want[0])
	}
}

func TestSchemaSQLiteCreateIndexesSeparate(t *testing.T) {
	b := NewBlueprint("users")
	b.Create = true
	b.ID()
	b.String("email").Nullable()
	b.Unique([]string{"email"})

	got := b.ToSql(sqliteG())
	want := []string{
		`create table "users" ("id" integer primary key autoincrement not null, "email" varchar)`,
		`create unique index "users_email_unique" on "users" ("email")`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSchemaSQLiteCreateForeignInline(t *testing.T) {
	b := NewBlueprint("posts")
	b.Create = true
	b.ID()
	b.ForeignID("user_id")
	b.Foreign("user_id").Reference("id").On("users").CascadeOnDelete()

	got := b.ToSql(sqliteG())
	want := []string{
		`create table "posts" ("id" integer primary key autoincrement not null, "user_id" integer not null` +
			`, foreign key("user_id") references "users" ("id") on delete cascade)`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSchemaSQLiteAddAndDrops(t *testing.T) {
	b := NewBlueprint("users")
	b.String("nickname").Nullable()

	got := b.ToSql(sqliteG())
	want := []string{`alter table "users" add column "nickname" varchar`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql = %v", got)
	}

	b2 := NewBlueprint("users")
	b2.DropColumn("a", "b")
	got2 := b2.ToSql(sqliteG())
	want2 := []string{
		`alter table "users" drop column "a"`,
		`alter table "users" drop column "b"`,
	}
	if !reflect.DeepEqual(got2, want2) {
		t.Errorf("sql = %v", got2)
	}
}

func TestSchemaSQLiteRenameAndDropIndex(t *testing.T) {
	b := NewBlueprint("users")
	b.Rename("admins")
	b.RenameColumn("a", "b")
	b.DropIndex("users_email_index")

	got := b.ToSql(sqliteG())
	want := []string{
		`alter table "users" rename to "admins"`,
		`alter table "users" rename column "a" to "b"`,
		`drop index "users_email_index"`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql = %v", got)
	}
}

func TestSchemaSQLiteDropPrimaryPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("DropPrimary must panic on SQLite (Laravel RuntimeException)")
		}
	}()
	b := NewBlueprint("users")
	b.DropPrimary()
	b.ToSql(sqliteG())
}

func TestSchemaSQLiteUnsupportedPanics(t *testing.T) {
	cases := []func(){
		func() {
			b := NewBlueprint("users")
			b.Foreign("user_id")
			b.ToSql(sqliteG())
		},
		func() {
			b := NewBlueprint("users")
			b.DropForeign("users_user_id_foreign")
			b.ToSql(sqliteG())
		},
		func() {
			b := NewBlueprint("users")
			b.SpatialIndex([]string{"loc"})
			b.ToSql(sqliteG())
		},
		func() {
			b := NewBlueprint("users")
			b.VectorIndex("v")
			b.ToSql(sqliteG())
		},
		func() {
			b := NewBlueprint("users")
			b.String("email").Change()
			b.ToSql(sqliteG())
		},
	}
	for i, fn := range cases {
		func() {
			defer func() {
				if r := recover(); r == nil {
					t.Fatalf("case %d: expected panic", i)
				}
			}()
			fn()
		}()
	}
}

func TestSchemaSQLiteIntrospection(t *testing.T) {
	sql, args := sqliteG().CompileTableExists("", "users")
	if !strings.Contains(sql, "sqlite_master") || len(args) != 1 || args[0] != "users" {
		t.Errorf("table exists = %s %v", sql, args)
	}
	sql2, _ := sqliteG().CompileColumnListing("", "users")
	if !strings.Contains(sql2, "pragma_table_info") {
		t.Errorf("column listing = %s", sql2)
	}
}
