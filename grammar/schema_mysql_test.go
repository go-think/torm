package grammar

import (
	"reflect"
	"strings"
	"testing"
)

// MySQL 方言 golden 测试（SQL 期望取自既有 golden 基线）。

func mysqlG() *MySqlGrammar { return NewMySqlGrammar() }

func TestSchemaMySQLNewColumnTypes(t *testing.T) {
	b := NewBlueprint("users")
	b.Create = true
	b.ID()
	b.TinyText("a")
	b.MediumText("b")
	b.LongText("c")
	b.Float("f", 10)
	b.Float("f0")
	b.Double("d")
	b.Enum("e", []string{"x", "y"})
	b.Set("s", []string{"x", "y"})
	b.JSON("j")
	b.JSONB("jb")
	b.Time("ti")
	b.Time("ti3", 3)
	b.TimeTz("tiz")
	b.TimestampTz("tstz")
	b.DateTimeTz("dt")
	b.Year("y")
	b.Binary("bi")
	b.Binary("biv", 100)
	b.Binary("bif", 100).SetFixed()
	b.UUID("u")
	b.ForeignUuid("fu")
	b.Ulid("ul")
	b.ForeignUlid("ful")
	b.IpAddress()
	b.MacAddress()
	b.Geometry("geo")
	b.Geography("geog")
	b.Vector("v", 3)
	b.TinyInteger("tiny")
	b.SmallInteger("sm")
	b.MediumInteger("md")
	b.UnsignedBigInteger("ubi")

	got := b.ToSql(mysqlG())
	want := []string{"create table `users` (" +
		"`id` bigint unsigned not null auto_increment primary key, " +
		"`a` tinytext not null, `b` mediumtext not null, `c` longtext not null, " +
		"`f` float(10) not null, `f0` float not null, `d` double not null, " +
		"`e` enum('x', 'y') not null, `s` set('x', 'y') not null, " +
		"`j` json not null, `jb` json not null, " +
		"`ti` time not null, `ti3` time(3) not null, `tiz` time not null, " +
		"`tstz` timestamp not null, `dt` datetime not null, `y` year not null, " +
		"`bi` blob not null, `biv` varbinary(100) not null, `bif` binary(100) not null, " +
		"`u` char(36) not null, `fu` char(36) not null, " +
		"`ul` char(26) not null, `ful` char(26) not null, " +
		"`ip_address` varchar(45) not null, `mac_address` varchar(17) not null, " +
		"`geo` geometry not null, `geog` geometry not null, `v` vector(3) not null, " +
		"`tiny` tinyint not null, `sm` smallint not null, `md` mediumint not null, " +
		"`ubi` bigint unsigned not null)"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %s\nwant\n  %s", got[0], want[0])
	}
}

func TestSchemaMySQLUseCurrentAndOnUpdate(t *testing.T) {
	b := NewBlueprint("users")
	b.Create = true
	b.Timestamp("ts").SetUseCurrent().SetUseCurrentOnUpdate()
	b.DateTime("dt", 3).SetUseCurrent()
	b.Timestamp("ts2", 3)

	got := b.ToSql(mysqlG())
	want := []string{"create table `users` (" +
		"`ts` timestamp not null default CURRENT_TIMESTAMP on update CURRENT_TIMESTAMP, " +
		"`dt` datetime(3) not null default CURRENT_TIMESTAMP(3), " +
		"`ts2` timestamp(3) not null)"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %s\nwant\n  %s", got[0], want[0])
	}
}

func TestSchemaMySQLColumnModifiers(t *testing.T) {
	b := NewBlueprint("users")
	b.Create = true
	b.String("str", 100).SetCharset("ascii").SetCollation("ascii_bin").SetComment("hi'x")
	b.String("first_col").SetFirst()
	b.String("after_col").SetAfter("str")
	b.String("inv").SetInvisible()
	b.String("gen").SetStoredAs("concat(str, '!')")
	b.String("gen2").SetVirtualAs("upper(str)")

	got := b.ToSql(mysqlG())
	want := []string{"create table `users` (" +
		"`str` varchar(100) character set ascii collate 'ascii_bin' not null comment 'hi\\'x', " +
		"`first_col` varchar(255) not null first, " +
		"`after_col` varchar(255) not null after `str`, " +
		"`inv` varchar(255) not null invisible, " +
		"`gen` varchar(255) as (concat(str, '!')) stored, " +
		"`gen2` varchar(255) as (upper(str)))"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %s\nwant\n  %s", got[0], want[0])
	}
}

func TestSchemaMySQLComputedPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for computed column")
		}
	}()
	b := NewBlueprint("users")
	b.Create = true
	b.Computed("comp", "1+1")
	b.ToSql(mysqlG())
}

func TestSchemaMySQLTsvectorPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for tsvector column")
		}
	}()
	b := NewBlueprint("users")
	b.Create = true
	b.Tsvector("tv")
	b.ToSql(mysqlG())
}

func TestSchemaMySQLChange(t *testing.T) {
	b := NewBlueprint("users")
	b.String("email", 100).Nullable().Change()

	got := b.ToSql(mysqlG())
	want := []string{"alter table `users` modify `email` varchar(100) null"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSchemaMySQLDropRenameFamily(t *testing.T) {
	b := NewBlueprint("users")
	b.DropColumn("a", "b")
	b.RenameColumn("foo", "bar")
	b.Rename("admins")
	b.DropPrimary()
	b.DropUnique("users_email_unique")
	b.DropIndex([]string{"email"})
	b.DropFullText("users_body_fulltext")
	b.DropSpatialIndex([]string{"loc"})
	b.DropForeign("users_user_id_foreign")
	b.RenameIndex("a", "b")
	b.DropConstrainedForeignId("user_id")
	b.DropMorphs("taggable")

	got := b.ToSql(mysqlG())
	want := []string{
		"alter table `users` drop `a`, drop `b`",
		"alter table `users` rename column `foo` to `bar`",
		"rename table `users` to `admins`",
		"alter table `users` drop primary key",
		"alter table `users` drop index `users_email_unique`",
		"alter table `users` drop index `users_email_index`",
		"alter table `users` drop index `users_body_fulltext`",
		"alter table `users` drop index `users_loc_spatialindex`",
		"alter table `users` drop foreign key `users_user_id_foreign`",
		"alter table `users` rename index `a` to `b`",
		"alter table `users` drop foreign key `users_user_id_foreign`",
		"alter table `users` drop `user_id`",
		"alter table `users` drop index `users_taggable_type_taggable_id_index`",
		"alter table `users` drop `taggable_type`, drop `taggable_id`",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSchemaMySQLIndexes(t *testing.T) {
	b := NewBlueprint("users")
	b.FullText([]string{"body"})
	b.SpatialIndex([]string{"loc"})
	b.RawIndex("(function(col))", "raw_idx")
	b.VectorIndex("v")

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for vector index on mysql")
		}
	}()
	got := b.ToSql(mysqlG())
	want := []string{
		"alter table `users` add fulltext `users_body_fulltext`(`body`)",
		"alter table `users` add spatial index `users_loc_spatialindex`(`loc`)",
		"alter table `users` add index `raw_idx`((function(col)))",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSchemaMySQLFullTextCreate(t *testing.T) {
	b := NewBlueprint("users")
	b.Create = true
	b.String("body")
	b.FullText([]string{"body"})

	got := b.ToSql(mysqlG())
	if len(got) != 2 || got[1] != "alter table `users` add fulltext `users_body_fulltext`(`body`)" {
		t.Errorf("sql = %v", got)
	}
}

func TestSchemaMySQLForeignKeyModifiers(t *testing.T) {
	b := NewBlueprint("orders")
	b.Create = true
	b.ID()
	b.ForeignID("user_id")
	b.Foreign("user_id").Reference("id").On("users").CascadeOnUpdate().NoActionOnDelete()

	got := b.ToSql(mysqlG())
	if len(got) != 2 {
		t.Fatalf("statements = %v", got)
	}
	want := "alter table `orders` add constraint `orders_user_id_foreign` foreign key (`user_id`) references `users` (`id`) on delete no action on update cascade"
	if got[1] != want {
		t.Errorf("sql = %s\nwant %s", got[1], want)
	}
}

func TestSchemaMySQLCompositeForeignKey(t *testing.T) {
	b := NewBlueprint("orders")
	b.Foreign("a", "b").Reference("x", "y").On("other")

	got := b.ToSql(mysqlG())
	want := "alter table `orders` add constraint `orders_a_b_foreign` foreign key (`a`, `b`) references `other` (`x`, `y`)"
	if len(got) != 1 || got[0] != want {
		t.Errorf("sql = %v", got)
	}
}

func TestSchemaMySQLMorphsToSql(t *testing.T) {
	b := NewBlueprint("comments")
	b.Create = true
	b.ID()
	b.Morphs("commentable")

	got := b.ToSql(mysqlG())
	if len(got) != 2 {
		t.Fatalf("statements = %v", got)
	}
	if !strings.Contains(got[0], "`commentable_type` varchar(255) not null") ||
		!strings.Contains(got[0], "`commentable_id` bigint unsigned not null") {
		t.Errorf("create = %s", got[0])
	}
	if got[1] != "alter table `comments` add index `comments_commentable_type_commentable_id_index`(`commentable_type`, `commentable_id`)" {
		t.Errorf("index = %s", got[1])
	}
}

func TestSchemaMySQLLongIndexNameTruncated(t *testing.T) {
	b := NewBlueprint("users")
	cols := []string{
		"aaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbb", "cccccccccccccccccc", "dddddddddddddddddd",
	}
	name := b.CreateIndexName("index", cols)
	// 不截断：索引名长度保持原样（是否超限由 DB 报错）
	if len(name) != len("users_")+len(strings.Join(cols, "_"))+len("_index") {
		t.Errorf("index name len = %d, name = %q", len(name), name)
	}
}
