package grammar

import (
	"reflect"
	"strings"
	"testing"
)

// SQL Server 内省编译 golden 测试（SQL 期望移植自 Laravel 13
// SqlServerGrammar.php compileSchemas/compileTables/compileViews/compileColumns/
// compileIndexes/compileForeignKeys/compileDropAll*）与 D5 默认约束前置块测试。

func TestSchemaSQLServerIntrospectCompileSchemas(t *testing.T) {
	sql, args := NewSqlServerGrammar().CompileSchemas()
	want := "select name, iif(schema_id = schema_id(), 1, 0) as [default] from sys.schemas " +
		"where name not in ('information_schema', 'sys') and name not like 'db[_]%' order by name"
	if sql != want || len(args) != 0 {
		t.Errorf("schemas =\n  %s\nwant\n  %s", sql, want)
	}
}

func TestSchemaSQLServerIntrospectCompileTables(t *testing.T) {
	g := NewSqlServerGrammar()
	sql, args := g.CompileTables("")
	want := "select t.name as name, schema_name(t.schema_id) as [schema], sum(u.total_pages) * 8 * 1024 as size " +
		"from sys.tables as t " +
		"join sys.partitions as p on p.object_id = t.object_id " +
		"join sys.allocation_units as u on u.container_id = p.hobt_id " +
		"where t.is_ms_shipped = 0 and t.name <> 'sysdiagrams'" +
		" group by t.name, t.schema_id " +
		"order by [schema], t.name"
	if sql != want || len(args) != 0 {
		t.Errorf("tables =\n  %s\nwant\n  %s", sql, want)
	}
	sql, _ = g.CompileTables("dbo")
	if !strings.Contains(sql, " and schema_name(t.schema_id) = 'dbo' group by") {
		t.Errorf("tables(dbo) = %s", sql)
	}
}

func TestSchemaSQLServerIntrospectCompileViews(t *testing.T) {
	sql, _ := NewSqlServerGrammar().CompileViews("dbo")
	want := "select name, schema_name(v.schema_id) as [schema], definition from sys.views as v " +
		"inner join sys.sql_modules as m on v.object_id = m.object_id " +
		"where v.is_ms_shipped = 0 and schema_name(v.schema_id) = 'dbo' order by [schema], name"
	if sql != want {
		t.Errorf("views =\n  %s\nwant\n  %s", sql, want)
	}
}

func TestSchemaSQLServerIntrospectCompileColumns(t *testing.T) {
	g := NewSqlServerGrammar()
	sql, _ := g.CompileColumns("", "users")
	if !strings.Contains(sql, "where obj.type in ('U', 'V') and obj.name = 'users' and scm.name = schema_name() order by col.column_id") {
		t.Errorf("columns(no schema) = %s", sql)
	}
	if !strings.Contains(sql, "def.definition as [default]") || !strings.Contains(sql, "com.definition as [expression]") ||
		!strings.Contains(sql, "prop.name = 'MS_Description'") {
		t.Errorf("columns metadata missing = %s", sql)
	}
	sql2, _ := g.CompileColumns("dbo", "users")
	if !strings.Contains(sql2, "scm.name = 'dbo'") {
		t.Errorf("columns(dbo) = %s", sql2)
	}
}

func TestSchemaSQLServerIntrospectCompileIndexes(t *testing.T) {
	sql, _ := NewSqlServerGrammar().CompileIndexes("dbo", "users")
	if !strings.Contains(sql, "string_agg(col.name, ',') within group (order by idxcol.key_ordinal) as columns") {
		t.Errorf("indexes string_agg = %s", sql)
	}
	if !strings.Contains(sql, "where tbl.name = 'users' and scm.name = 'dbo' ") {
		t.Errorf("indexes filter = %s", sql)
	}
}

func TestSchemaSQLServerIntrospectCompileForeignKeys(t *testing.T) {
	sql, _ := NewSqlServerGrammar().CompileForeignKeys("dbo", "orders")
	if !strings.Contains(sql, "fk.update_referential_action_desc as on_update") ||
		!strings.Contains(sql, "fk.delete_referential_action_desc as on_delete") {
		t.Errorf("foreign keys actions = %s", sql)
	}
	if !strings.Contains(sql, "where lt.name = 'orders' and ls.name = 'dbo' ") {
		t.Errorf("foreign keys filter = %s", sql)
	}
}

func TestSchemaSQLServerIntrospectDropAll(t *testing.T) {
	g := NewSqlServerGrammar()
	if sql := g.CompileDropAllTables(); sql != "EXEC sp_msforeachtable 'DROP TABLE ?'" {
		t.Errorf("drop all tables = %s", sql)
	}
	wantFk := "DECLARE @sql NVARCHAR(MAX) = N'';\n" +
		"            SELECT @sql += 'ALTER TABLE '\n" +
		"                + QUOTENAME(OBJECT_SCHEMA_NAME(parent_object_id)) + '.' + + QUOTENAME(OBJECT_NAME(parent_object_id))\n" +
		"                + ' DROP CONSTRAINT ' + QUOTENAME(name) + ';'\n" +
		"            FROM sys.foreign_keys;\n" +
		"\n" +
		"            EXEC sp_executesql @sql;"
	if sql := g.CompileDropAllForeignKeys(); sql != wantFk {
		t.Errorf("drop all foreign keys =\n  %s\nwant\n  %s", sql, wantFk)
	}
	wantViews := "DECLARE @sql NVARCHAR(MAX) = N'';\n" +
		"            SELECT @sql += 'DROP VIEW ' + QUOTENAME(OBJECT_SCHEMA_NAME(object_id)) + '.' + QUOTENAME(name) + ';'\n" +
		"            FROM sys.views;\n" +
		"\n" +
		"            EXEC sp_executesql @sql;"
	if sql := g.CompileDropAllViews(); sql != wantViews {
		t.Errorf("drop all views =\n  %s\nwant\n  %s", sql, wantViews)
	}
}

// D5：CompileChange 返回 [默认约束块, alter column]（带 default 时再追加
// compileDefault 流式命令）。
func TestSchemaSQLServerChangeWithDefaultConstraint(t *testing.T) {
	g := NewSqlServerGrammar()
	b := NewBlueprint("users")
	b.String("email").Change().Default("fix@laravel.com")

	got := b.ToSql(g)
	want := []string{
		`DECLARE @sql NVARCHAR(MAX) = '';` +
			`SELECT @sql += 'ALTER TABLE "users" DROP CONSTRAINT ' + OBJECT_NAME([default_object_id]) + ';' ` +
			`FROM sys.columns ` +
			`WHERE [object_id] = OBJECT_ID('"users"') AND [name] in ('email') AND [default_object_id] <> 0;` +
			`EXEC(@sql)`,
		`alter table "users" alter column "email" nvarchar(255) not null`,
		`alter table "users" add default 'fix@laravel.com' for "email"`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("change =\n  %v\nwant\n  %v", got, want)
	}

	// 原始表达式默认值（DefaultRaw 原样输出）
	b2 := NewBlueprint("users")
	b2.Timestamp("ts").Change().DefaultExpression("getdate()")
	got2 := b2.ToSql(g)
	if len(got2) != 3 || got2[2] != `alter table "users" add default getdate() for "ts"` {
		t.Errorf("change raw default = %v", got2)
	}

	// 不带 default 的 change 只返回两条
	b3 := NewBlueprint("users")
	b3.Integer("age").Change()
	got3 := b3.ToSql(g)
	if len(got3) != 2 || got3[0] != `DECLARE @sql NVARCHAR(MAX) = '';`+
		`SELECT @sql += 'ALTER TABLE "users" DROP CONSTRAINT ' + OBJECT_NAME([default_object_id]) + ';' `+
		`FROM sys.columns `+
		`WHERE [object_id] = OBJECT_ID('"users"') AND [name] in ('age') AND [default_object_id] <> 0;`+
		`EXEC(@sql)` {
		t.Errorf("change without default = %v", got3)
	}
}
