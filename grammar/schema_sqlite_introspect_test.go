package grammar

import (
	"testing"
)

// SQLite 内省编译 golden 测试（覆盖 compileTables/compileViews/compileColumns/
// compileIndexes/compileForeignKeys/compileDropAllTables/compileDropAllViews/
// compileRebuild）。

func TestSchemaSQLiteIntrospectCompileTables(t *testing.T) {
	g := NewSqliteQueryGrammar()
	sql, args := g.CompileTables("")
	want := "select tl.name as name, tl.schema as schema from pragma_table_list as tl where" +
		" tl.type in ('table', 'virtual') and tl.name not like 'sqlite\\_%' escape '\\' " +
		"order by tl.schema, tl.name"
	if sql != want || len(args) != 0 {
		t.Errorf("tables =\n  %s\nwant\n  %s", sql, want)
	}
	sql, _ = g.CompileTables("main")
	want = "select tl.name as name, tl.schema as schema from pragma_table_list as tl where" +
		" tl.schema = 'main' and" +
		" tl.type in ('table', 'virtual') and tl.name not like 'sqlite\\_%' escape '\\' " +
		"order by tl.schema, tl.name"
	if sql != want {
		t.Errorf("tables(schema) =\n  %s\nwant\n  %s", sql, want)
	}
}

func TestSchemaSQLiteIntrospectCompileViews(t *testing.T) {
	sql, args := NewSqliteQueryGrammar().CompileViews("main")
	want := "select name, 'main' as schema, sql as definition from \"main\".sqlite_master where type = 'view' order by name"
	if sql != want || len(args) != 0 {
		t.Errorf("views =\n  %s\nwant\n  %s", sql, want)
	}
}

func TestSchemaSQLiteIntrospectCompileColumns(t *testing.T) {
	sql, args := NewSqliteQueryGrammar().CompileColumns("main", "users")
	want := "select name, type, not \"notnull\" as \"nullable\", dflt_value as \"default\", pk as \"primary\", hidden as \"extra\" " +
		"from pragma_table_xinfo('users', 'main') order by cid asc"
	if sql != want || len(args) != 0 {
		t.Errorf("columns =\n  %s\nwant\n  %s", sql, want)
	}
}

func TestSchemaSQLiteIntrospectCompileIndexes(t *testing.T) {
	sql, args := NewSqliteQueryGrammar().CompileIndexes("main", "users")
	want := "select 'primary' as name, group_concat(col) as columns, 1 as \"unique\", 1 as \"primary\" " +
		"from (select name as col from pragma_table_xinfo('users', 'main') where pk > 0 order by pk, cid) group by name " +
		"union select name, group_concat(col) as columns, \"unique\", origin = 'pk' as \"primary\" " +
		"from (select il.*, ii.name as col from pragma_index_list('users', 'main') il, pragma_index_info(il.name, 'main') ii order by il.seq, ii.seqno) " +
		"group by name, \"unique\", \"primary\""
	if sql != want || len(args) != 0 {
		t.Errorf("indexes =\n  %s\nwant\n  %s", sql, want)
	}
}

func TestSchemaSQLiteIntrospectCompileForeignKeys(t *testing.T) {
	sql, args := NewSqliteQueryGrammar().CompileForeignKeys("main", "posts")
	want := "select group_concat(\"from\") as columns, 'main' as foreign_schema, \"table\" as foreign_table, " +
		"group_concat(\"to\") as foreign_columns, on_update, on_delete " +
		"from (select * from pragma_foreign_key_list('posts', 'main') order by id desc, seq) " +
		"group by id, \"table\", on_update, on_delete"
	if sql != want || len(args) != 0 {
		t.Errorf("foreign keys =\n  %s\nwant\n  %s", sql, want)
	}
}

func TestSchemaSQLiteIntrospectDropAllAndRebuild(t *testing.T) {
	g := NewSqliteQueryGrammar()
	if sql := g.CompileDropAllTables("main"); sql != `delete from "main".sqlite_master where type in ('table', 'index', 'trigger')` {
		t.Errorf("drop all tables = %s", sql)
	}
	if sql := g.CompileDropAllViews("main"); sql != `delete from "main".sqlite_master where type in ('view')` {
		t.Errorf("drop all views = %s", sql)
	}
	if sql := g.CompileRebuild("main"); sql != `vacuum "main"` {
		t.Errorf("rebuild = %s", sql)
	}
}
