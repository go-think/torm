package grammar

import (
	"reflect"
	"testing"
)

// MariaDB Schema 编译继承 MySqlGrammar（对齐 Laravel 13 MariaDbGrammar：
// 未覆写任何 schema compile 方法；json 类型在 MariaDB 10.5+ 服务端即
// longtext + json_valid check 别名）。

func mariaG() *MariaDbGrammar { return NewMariaDbGrammar() }

func TestSchemaMariaDbJson(t *testing.T) {
	b := NewBlueprint("users")
	b.Create = true
	b.ID()
	b.JSON("j")
	b.JSONB("jb")

	got := b.ToSql(mariaG())
	want := []string{"create table `users` (`id` bigint unsigned not null auto_increment primary key, `j` json not null, `jb` json not null)"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %s\nwant\n  %s", got[0], want[0])
	}
}

func TestSchemaMariaDbMorphs(t *testing.T) {
	b := NewBlueprint("comments")
	b.Morphs("commentable")

	got := b.ToSql(mariaG())
	want := []string{
		"alter table `comments` add `commentable_type` varchar(255) not null",
		"alter table `comments` add `commentable_id` bigint unsigned not null",
		"alter table `comments` add index `comments_commentable_type_commentable_id_index`(`commentable_type`, `commentable_id`)",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSchemaMariaDbDropIndex(t *testing.T) {
	b := NewBlueprint("users")
	b.DropIndex([]string{"email"})
	got := b.ToSql(mariaG())
	want := []string{"alter table `users` drop index `users_email_index`"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql = %v", got)
	}
}
