package grammar

import (
	"reflect"
	"strings"
	"testing"
)

// Postgres 方言 golden 测试（SQL 期望移植自 Laravel 13 PostgresGrammar 输出）。

func pgG() *PostgresGrammar { return NewPostgresGrammar() }

func TestSchemaPGNewColumnTypes(t *testing.T) {
	b := NewBlueprint("users")
	b.Create = true
	b.ID()
	b.JSON("j")
	b.JSONB("jb")
	b.Time("ti")
	b.Time("ti3", 3)
	b.TimeTz("tiz")
	b.TimestampTz("ts")
	b.DateTimeTz("dt")
	b.Year("y")
	b.Binary("bi")
	b.IpAddress()
	b.MacAddress()
	b.Enum("e", []string{"x", "y"})
	b.Double("d")
	b.Float("f", 10)
	b.Float("f0")
	b.Vector("v", 3)
	b.Tsvector("tv")
	b.Geometry("geo", "point")
	b.Geometry("geo2", "point").SetSrid(4326)
	b.Geography("geog")
	b.TinyText("tt")
	b.MediumText("mt")
	b.LongText("lt")
	b.UUID("u")
	b.Ulid("ul")
	b.String("str", 100)

	got := b.ToSql(pgG())
	want := []string{"create table \"users\" (" +
		`"id" bigserial not null primary key, ` +
		`"j" json not null, "jb" jsonb not null, ` +
		`"ti" time(0) without time zone not null, "ti3" time(3) without time zone not null, ` +
		`"tiz" time(0) with time zone not null, ` +
		`"ts" timestamp(0) with time zone not null, "dt" timestamp(0) with time zone not null, ` +
		`"y" integer not null, "bi" bytea not null, ` +
		`"ip_address" inet not null, "mac_address" macaddr not null, ` +
		`"e" varchar(255) check ("e" in ('x', 'y')) not null, ` +
		`"d" double precision not null, "f" float(10) not null, "f0" float not null, ` +
		`"v" vector(3) not null, "tv" tsvector not null, ` +
		`"geo" geometry(point) not null, "geo2" geometry(point,4326) not null, ` +
		`"geog" geography not null, ` +
		`"tt" varchar(255) not null, "mt" text not null, "lt" text not null, ` +
		`"u" uuid not null, "ul" char(26) not null, "str" varchar(100) not null)`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %s\nwant\n  %s", got[0], want[0])
	}
}

func TestSchemaPGUseCurrent(t *testing.T) {
	b := NewBlueprint("users")
	b.Create = true
	b.Timestamp("ts").SetUseCurrent()
	b.TimestampTz("tstz").SetUseCurrent()
	b.Date("d").SetUseCurrent()

	got := b.ToSql(pgG())
	want := []string{"create table \"users\" (" +
		`"ts" timestamp(0) without time zone not null default CURRENT_TIMESTAMP, ` +
		`"tstz" timestamp(0) with time zone not null default CURRENT_TIMESTAMP, ` +
		`"d" date not null default CURRENT_DATE)`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %s\nwant\n  %s", got[0], want[0])
	}
}

func TestSchemaPGFullTextIndex(t *testing.T) {
	b := NewBlueprint("users")
	b.FullText([]string{"body"})
	got := b.ToSql(pgG())
	want := []string{`create index "users_body_fulltext" on "users" using gin ((to_tsvector('english', "body")))`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql = %v", got)
	}

	b2 := NewBlueprint("users")
	b2.FullText([]string{"a", "b"}, "custom_ft")
	idx := b2.Indexes[0]
	idx.Language = "simple"
	got2 := b2.ToSql(pgG())
	want2 := []string{`create index "custom_ft" on "users" using gin ((to_tsvector('simple', "a") || to_tsvector('simple', "b")))`}
	if !reflect.DeepEqual(got2, want2) {
		t.Errorf("sql = %v", got2)
	}
}

func TestSchemaPGSpatialAndVectorIndex(t *testing.T) {
	b := NewBlueprint("users")
	b.SpatialIndex([]string{"loc"})
	b.VectorIndex("v")

	got := b.ToSql(pgG())
	want := []string{
		`create index "users_loc_spatialindex" on "users" using gist ("loc")`,
		`create index "users_v_vectorindex" on "users" using hnsw ("v" vector_cosine_ops)`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSchemaPGDropRenameFamily(t *testing.T) {
	b := NewBlueprint("users")
	b.DropPrimary()
	b.DropUnique("users_email_unique")
	b.DropIndex("users_email_index")
	b.DropFullText("users_body_fulltext")
	b.DropSpatialIndex([]string{"loc"})
	b.DropVectorIndex("users_v_vectorindex")
	b.DropForeign("users_user_id_foreign")
	b.DropColumn("a", "b")
	b.Rename("admins")
	b.RenameIndex("a", "b")
	b.RenameColumn("c", "d")

	got := b.ToSql(pgG())
	want := []string{
		`alter table "users" drop constraint "users_pkey"`,
		`alter table "users" drop constraint "users_email_unique"`,
		`drop index "users_email_index"`,
		`drop index "users_body_fulltext"`,
		`drop index "users_loc_spatialindex"`,
		`drop index "users_v_vectorindex"`,
		`alter table "users" drop constraint "users_user_id_foreign"`,
		`alter table "users" drop column "a", drop column "b"`,
		`alter table "users" rename to "admins"`,
		`alter index "a" rename to "b"`,
		`alter table "users" rename column "c" to "d"`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSchemaPGChange(t *testing.T) {
	b := NewBlueprint("users")
	b.String("email", 100).Nullable().Change()

	got := b.ToSql(pgG())
	want := []string{
		`alter table "users" alter column "email" type varchar(100), alter column "email" drop not null, alter column "email" drop default, alter column "email" drop identity if exists`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSchemaPGGeneratedAs(t *testing.T) {
	b := NewBlueprint("users")
	b.Create = true
	b.ID()
	b.Computed("comp", "1 + 1")

	got := b.ToSql(pgG())
	want := []string{`create table "users" ("id" bigserial not null primary key, "comp" text generated always as (1 + 1) stored not null)`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %s\nwant\n  %s", got[0], want[0])
	}
}

func TestSchemaPGMorphsToSql(t *testing.T) {
	b := NewBlueprint("comments")
	b.Create = true
	b.ID()
	b.Morphs("commentable")

	got := b.ToSql(pgG())
	if len(got) != 2 {
		t.Fatalf("statements = %v", got)
	}
	if !strings.Contains(got[0], `"commentable_type" varchar(255) not null`) ||
		!strings.Contains(got[0], `"commentable_id" bigint not null`) {
		t.Errorf("create = %s", got[0])
	}
	if got[1] != `create index "comments_commentable_type_commentable_id_index" on "comments" ("commentable_type", "commentable_id")` {
		t.Errorf("index = %s", got[1])
	}
}

func TestSchemaPGRawIndex(t *testing.T) {
	b := NewBlueprint("users")
	b.RawIndex("(lower(email))", "raw_idx")
	got := b.ToSql(pgG())
	want := []string{`create index "raw_idx" on "users" ((lower(email)))`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql = %v", got)
	}
}
