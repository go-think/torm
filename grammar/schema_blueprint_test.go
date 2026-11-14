package grammar

import (
	"testing"
)

// Blueprint 层测试：morphs 族、drop/rename 命令族、索引命名与列修饰符。

func TestBlueprintCreateIndexNameLong(t *testing.T) {
	b := NewBlueprint("users")
	cols := []string{
		"aaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbb", "cccccccccccccccccc", "dddddddddddddddddd",
	}
	name := b.CreateIndexName("index", cols)
	// createIndexName 不做长度截断：
	// 全小写、'-'/'.' → '_'，长名原样保留
	want := "users_aaaaaaaaaaaaaaaaaa_bbbbbbbbbbbbbbbbbb_cccccccccccccccccc_dddddddddddddddddd_index"
	if name != want {
		t.Errorf("name = %q, want %q", name, want)
	}
}

func TestBlueprintShortIndexNameUnchanged(t *testing.T) {
	b := NewBlueprint("users")
	if got := b.CreateIndexName("index", []string{"email"}); got != "users_email_index" {
		t.Fatalf("got %s", got)
	}
	if got := b.CreateIndexName("index", []string{"a-b", "c.d"}); got != "users_a_b_c_d_index" {
		t.Fatalf("got %s", got)
	}
}

func TestBlueprintMorphsFamily(t *testing.T) {
	cases := []struct {
		name     string
		build    func() *Blueprint
		idType   string
		nullable bool
	}{
		{"numeric", func() *Blueprint { b := NewBlueprint("posts"); b.Morphs("taggable"); return b }, "bigint", false},
		{"nullableNumeric", func() *Blueprint { b := NewBlueprint("posts"); b.NullableMorphs("taggable"); return b }, "bigint", true},
		{"uuid", func() *Blueprint { b := NewBlueprint("posts"); b.UuidMorphs("taggable"); return b }, "uuid", false},
		{"nullableUuid", func() *Blueprint { b := NewBlueprint("posts"); b.NullableUuidMorphs("taggable"); return b }, "uuid", true},
		{"ulid", func() *Blueprint { b := NewBlueprint("posts"); b.UlidMorphs("taggable"); return b }, "ulid", false},
		{"nullableUlid", func() *Blueprint { b := NewBlueprint("posts"); b.NullableUlidMorphs("taggable"); return b }, "ulid", true},
	}
	for _, tc := range cases {
		b := tc.build()
		// 两列 + 一个索引
		if len(b.Columns) != 2 {
			t.Fatalf("%s: columns = %d", tc.name, len(b.Columns))
		}
		if b.Columns[0].Name != "taggable_type" || b.Columns[0].Type != "varchar" {
			t.Fatalf("%s: type col = %+v", tc.name, b.Columns[0])
		}
		if b.Columns[1].Name != "taggable_id" || b.Columns[1].Type != tc.idType {
			t.Fatalf("%s: id col = %+v", tc.name, b.Columns[1])
		}
		if b.Columns[1].IsNullable != tc.nullable {
			t.Fatalf("%s: nullable = %v", tc.name, b.Columns[1].IsNullable)
		}
		if len(b.Indexes) != 1 || b.Indexes[0].Name != "posts_taggable_type_taggable_id_index" {
			t.Fatalf("%s: index = %+v", tc.name, b.Indexes)
		}
	}
}

func TestBlueprintNumericMorphsUnsigned(t *testing.T) {
	b := NewBlueprint("posts")
	b.NumericMorphs("taggable")
	if !b.Columns[1].IsUnsigned {
		t.Fatal("numeric morphs id should be unsigned")
	}
	b2 := NewBlueprint("posts")
	b2.NullableNumericMorphs("taggable")
	if !b2.Columns[1].IsNullable {
		t.Fatal("nullable numeric morphs id should be nullable")
	}
}

func TestBlueprintDropRenameCommands(t *testing.T) {
	b := NewBlueprint("users")
	b.DropColumn("a", "b")
	b.RenameColumn("from", "to")
	b.Rename("admins")
	b.DropPrimary()
	b.DropUnique("users_email_unique")
	b.DropIndex([]string{"email"})
	b.DropFullText("users_body_fulltext")
	b.DropSpatialIndex([]string{"loc"})
	b.DropVectorIndex("v")
	b.DropForeign("users_user_id_foreign")
	b.RenameIndex("a", "b")

	wantNames := []string{"dropColumn", "renameColumn", "rename", "dropPrimary", "dropUnique",
		"dropIndex", "dropFullText", "dropSpatialIndex", "dropVectorIndex", "dropForeign", "renameIndex"}
	if len(b.Commands) != len(wantNames) {
		t.Fatalf("commands = %d", len(b.Commands))
	}
	for i, w := range wantNames {
		if b.Commands[i].Name != w {
			t.Fatalf("cmd[%d] = %s, want %s", i, b.Commands[i].Name, w)
		}
	}
	if got := b.Commands[0].Columns; len(got) != 2 || got[0] != "a" {
		t.Fatalf("dropColumn cols = %v", got)
	}
	if b.Commands[1].From != "from" || b.Commands[1].To != "to" {
		t.Fatalf("renameColumn = %+v", b.Commands[1])
	}
	if b.Commands[2].To != "admins" {
		t.Fatalf("rename = %+v", b.Commands[2])
	}
	if b.Commands[3].Index != "users__dropprimary" {
		t.Fatalf("dropPrimary index = %s", b.Commands[3].Index)
	}
	if b.Commands[5].Index != "users_email_index" {
		t.Fatalf("dropIndex by cols = %s", b.Commands[5].Index)
	}
}

func TestBlueprintDropConvenience(t *testing.T) {
	b := NewBlueprint("users")
	b.DropTimestamps()
	b.DropSoftDeletes()
	b.DropSoftDeletesTz()
	b.DropRememberToken()
	b.DropMorphs("taggable")
	b.DropConstrainedForeignId("user_id")

	if b.Commands[0].Columns[0] != "created_at" || b.Commands[0].Columns[1] != "updated_at" {
		t.Fatalf("dropTimestamps = %+v", b.Commands[0])
	}
	if b.Commands[1].Columns[0] != "deleted_at" || b.Commands[2].Columns[0] != "deleted_at" {
		t.Fatalf("dropSoftDeletes = %+v %+v", b.Commands[1], b.Commands[2])
	}
	if b.Commands[3].Columns[0] != "remember_token" {
		t.Fatalf("dropRememberToken = %+v", b.Commands[3])
	}
	// dropMorphs：dropIndex + dropColumn
	if b.Commands[4].Name != "dropIndex" || b.Commands[4].Index != "users_taggable_type_taggable_id_index" {
		t.Fatalf("dropMorphs index cmd = %+v", b.Commands[4])
	}
	if b.Commands[5].Name != "dropColumn" || b.Commands[5].Columns[0] != "taggable_type" {
		t.Fatalf("dropMorphs col cmd = %+v", b.Commands[5])
	}
	// dropConstrainedForeignId：先 dropForeign 再 dropColumn
	if b.Commands[6].Name != "dropForeign" || b.Commands[6].Index != "users_user_id_foreign" {
		t.Fatalf("dropConstrainedForeignId fk = %+v", b.Commands[6])
	}
	if b.Commands[7].Name != "dropColumn" || b.Commands[7].Columns[0] != "user_id" {
		t.Fatalf("dropConstrainedForeignId col = %+v", b.Commands[7])
	}
}

func TestBlueprintForeignIdFor(t *testing.T) {
	b := NewBlueprint("posts")
	b.ForeignIdFor("App\\Models\\User")
	b.ForeignIdFor("App\\Models\\Team", "author_id")
	b.ForeignUuidFor("App\\Models\\Device")
	b.ForeignUlidFor("App\\Models\\Ticket")

	if b.Columns[0].Name != "user_id" || b.Columns[0].Type != "bigint" || !b.Columns[0].IsUnsigned {
		t.Fatalf("col0 = %+v", b.Columns[0])
	}
	if b.Columns[1].Name != "author_id" {
		t.Fatalf("col1 = %+v", b.Columns[1])
	}
	if b.Columns[2].Type != "uuid" || b.Columns[3].Type != "ulid" || b.Columns[3].Length != 26 {
		t.Fatalf("uuid/ulid cols = %+v %+v", b.Columns[2], b.Columns[3])
	}
}

func TestBlueprintIndexKinds(t *testing.T) {
	b := NewBlueprint("users")
	b.FullText([]string{"body"})
	b.FullText([]string{"title", "body"}, "custom_ft")
	b.SpatialIndex([]string{"loc"})
	b.VectorIndex("embedding")
	b.RawIndex("(function(col))", "raw_idx")

	if b.Indexes[0].Type != "fulltext" || b.Indexes[0].Name != "users_body_fulltext" {
		t.Fatalf("fullText = %+v", b.Indexes[0])
	}
	if b.Indexes[1].Name != "custom_ft" {
		t.Fatalf("fullText named = %+v", b.Indexes[1])
	}
	if b.Indexes[2].Type != "spatialindex" || b.Indexes[2].Name != "users_loc_spatialindex" {
		t.Fatalf("spatialIndex = %+v", b.Indexes[2])
	}
	if b.Indexes[3].Type != "vectorindex" || b.Indexes[3].Algorithm != "hnsw" ||
		b.Indexes[3].OperatorClass != "vector_cosine_ops" || b.Indexes[3].Name != "users_embedding_vectorindex" {
		t.Fatalf("vectorIndex = %+v", b.Indexes[3])
	}
	if b.Indexes[4].Expression != "(function(col))" || b.Indexes[4].Name != "raw_idx" {
		t.Fatalf("rawIndex = %+v", b.Indexes[4])
	}
}

func TestBlueprintForeignComposite(t *testing.T) {
	b := NewBlueprint("orders")
	b.Foreign("a", "b").Reference("x", "y").On("other").CascadeOnUpdate().NoActionOnDelete()
	idx := b.Indexes[0]
	if len(idx.ReferenceColumns) != 2 || idx.OnUpdate != "cascade" || idx.OnDelete != "no action" {
		t.Fatalf("composite fk = %+v", idx)
	}
	// 修饰符家族
	b.Foreign("c").On("o").RestrictOnUpdate().NullOnUpdate().NoActionOnUpdate().
		CascadeOnDelete().RestrictOnDelete().SetNullOnDelete().NoActionOnDelete()
	idx2 := b.Indexes[1]
	if idx2.OnUpdate != "no action" || idx2.OnDelete != "no action" {
		t.Fatalf("fk actions = %s / %s", idx2.OnUpdate, idx2.OnDelete)
	}
}

func TestBlueprintColumnModifiers(t *testing.T) {
	b := NewBlueprint("users")
	c := b.String("email").Change().SetUseCurrent().SetUseCurrentOnUpdate().
		SetStoredAs("concat(a,b)").SetVirtualAs("a+b").SetGeneratedAs("gen()").
		SetCharset("ascii").SetCollation("ascii_bin").SetFirst().SetAfter("name").
		SetComment("hi").SetOnUpdate("NOW()").SetSrid(4326).SetInvisible().
		SetPersisted().Unsigned().AutoIncrement()
	if !c.IsChange || !c.UseCurrent || !c.UseCurrentOnUpdate {
		t.Fatalf("modifiers = %+v", c)
	}
	if c.StoredAs != "concat(a,b)" || c.VirtualAs != "a+b" || c.GeneratedAs != "gen()" {
		t.Fatalf("generated = %+v", c)
	}
	if c.Charset != "ascii" || c.Collation != "ascii_bin" || !c.First || c.After != "name" {
		t.Fatalf("modifiers = %+v", c)
	}
	if c.CommentText != "hi" || c.OnUpdate != "NOW()" || c.Srid != 4326 {
		t.Fatalf("modifiers = %+v", c)
	}
	if !c.Invisible || !c.Persisted || !c.IsUnsigned || !c.IsAutoIncrement {
		t.Fatalf("modifiers = %+v", c)
	}
}

func TestBlueprintTimestampsTzAndSoftDeletesTz(t *testing.T) {
	b := NewBlueprint("users")
	b.TimestampsTz()
	b.SoftDeletesTz()
	if b.Columns[0].Type != "timestamptz" || b.Columns[2].Type != "timestamptz" {
		t.Fatalf("types = %s %s", b.Columns[0].Type, b.Columns[2].Type)
	}
}
