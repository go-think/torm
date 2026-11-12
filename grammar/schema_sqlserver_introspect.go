package grammar

import (
	"fmt"
	"strings"
)

// ---- SQL Server Schema 内省编译（对齐 Illuminate\Database\Schema\Grammars\SqlServerGrammar.php:44-197 / :366-559） ----
//
// 方言说明：
//   - schema 为空时 Laravel 以 schema_name()（当前 schema）过滤（:121-142 等）；
//   - FK 动作值不做语义映射：SQL 直接输出 update_referential_action_desc /
//     delete_referential_action_desc 原始形态（NO_ACTION / CASCADE / SET_NULL ...），
//     下划线→空格 + 小写的处理在 processor 层（SqlServerProcessor.processForeignKeys）。

// ssQuoteString 内省 SQL 内联字面量（quoteString：单引号，内部单引号转义）。
func (g *SqlServerGrammar) ssQuoteString(v string) string {
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}

// ssSchemaWhereClause 对应 SqlServerGrammar::compileSchemaWhereClause（:98-114）：
// schema 非空时 and column = '...'，为空时无子句。
func ssSchemaWhereClause(schema, column string) string {
	if schema != "" {
		return " and " + column + " = " + "'" + strings.ReplaceAll(schema, "'", "''") + "'"
	}
	return ""
}

// CompileSchemas 对应 SqlServerGrammar::compileSchemas（:44-48）：
// sys.schemas 排除 information_schema/sys/db_*，default 标记当前 schema。
func (g *SqlServerGrammar) CompileSchemas() (string, []any) {
	return "select name, iif(schema_id = schema_id(), 1, 0) as [default] from sys.schemas " +
		"where name not in ('information_schema', 'sys') and name not like 'db[_]%' order by name", nil
}

// CompileTables 对应 SqlServerGrammar::compileTables（:71-81）：
// sys.tables join sys.partitions/sys.allocation_units 聚合 size（页数 × 8KB），
// 排除系统表与 sysdiagrams。
func (g *SqlServerGrammar) CompileTables(schema string) (string, []any) {
	return "select t.name as name, schema_name(t.schema_id) as [schema], sum(u.total_pages) * 8 * 1024 as size " +
		"from sys.tables as t " +
		"join sys.partitions as p on p.object_id = t.object_id " +
		"join sys.allocation_units as u on u.container_id = p.hobt_id " +
		"where t.is_ms_shipped = 0 and t.name <> 'sysdiagrams'" +
		ssSchemaWhereClause(schema, "schema_name(t.schema_id)") +
		" group by t.name, t.schema_id " +
		"order by [schema], t.name", nil
}

// CompileViews 对应 SqlServerGrammar::compileViews（:89-96）：
// sys.views inner join sys.sql_modules 取视图定义。
func (g *SqlServerGrammar) CompileViews(schema string) (string, []any) {
	return "select name, schema_name(v.schema_id) as [schema], definition from sys.views as v " +
		"inner join sys.sql_modules as m on v.object_id = m.object_id " +
		"where v.is_ms_shipped = 0" +
		ssSchemaWhereClause(schema, "schema_name(v.schema_id)") +
		" order by [schema], name", nil
}

// CompileColumns 对应 SqlServerGrammar::compileColumns（:121-142）：
// sys.columns 全元数据（default 约束、identity、collation、生成列表达式、注释）。
func (g *SqlServerGrammar) CompileColumns(schema, table string) (string, []any) {
	schemaFilter := "schema_name()"
	if schema != "" {
		schemaFilter = g.ssQuoteString(schema)
	}
	return fmt.Sprintf(
		"select col.name, type.name as type_name, "+
			"col.max_length as length, col.precision as precision, col.scale as places, "+
			"col.is_nullable as nullable, def.definition as [default], "+
			"col.is_identity as autoincrement, col.collation_name as collation, "+
			"com.definition as [expression], is_persisted as [persisted], "+
			"cast(prop.value as nvarchar(max)) as comment "+
			"from sys.columns as col "+
			"join sys.types as type on col.user_type_id = type.user_type_id "+
			"join sys.objects as obj on col.object_id = obj.object_id "+
			"join sys.schemas as scm on obj.schema_id = scm.schema_id "+
			"left join sys.default_constraints def on col.default_object_id = def.object_id and col.object_id = def.parent_object_id "+
			"left join sys.extended_properties as prop on obj.object_id = prop.major_id and col.column_id = prop.minor_id and prop.name = 'MS_Description' "+
			"left join sys.computed_columns as com on col.column_id = com.column_id and col.object_id = com.object_id "+
			"where obj.type in ('U', 'V') and obj.name = %s and scm.name = %s "+
			"order by col.column_id",
		g.ssQuoteString(table), schemaFilter), nil
}

// CompileIndexes 对应 SqlServerGrammar::compileIndexes（:151-166）：
// string_agg within group（order by key_ordinal）聚合索引列。
func (g *SqlServerGrammar) CompileIndexes(schema, table string) (string, []any) {
	schemaFilter := "schema_name()"
	if schema != "" {
		schemaFilter = g.ssQuoteString(schema)
	}
	return fmt.Sprintf(
		"select idx.name as name, string_agg(col.name, ',') within group (order by idxcol.key_ordinal) as columns, "+
			"idx.type_desc as [type], idx.is_unique as [unique], idx.is_primary_key as [primary] "+
			"from sys.indexes as idx "+
			"join sys.tables as tbl on idx.object_id = tbl.object_id "+
			"join sys.schemas as scm on tbl.schema_id = scm.schema_id "+
			"join sys.index_columns as idxcol on idx.object_id = idxcol.object_id and idx.index_id = idxcol.index_id "+
			"join sys.columns as col on idxcol.object_id = col.object_id and idxcol.column_id = col.column_id "+
			"where tbl.name = %s and scm.name = %s "+
			"group by idx.name, idx.type_desc, idx.is_unique, idx.is_primary_key",
		g.ssQuoteString(table), schemaFilter), nil
}

// CompileForeignKeys 对应 SqlServerGrammar::compileForeignKeys（:175-197）：
// 动作值直接输出 *_referential_action_desc（NO_ACTION/CASCADE 形态，不映射）。
func (g *SqlServerGrammar) CompileForeignKeys(schema, table string) (string, []any) {
	schemaFilter := "schema_name()"
	if schema != "" {
		schemaFilter = g.ssQuoteString(schema)
	}
	return fmt.Sprintf(
		"select fk.name as name, "+
			"string_agg(lc.name, ',') within group (order by fkc.constraint_column_id) as columns, "+
			"fs.name as foreign_schema, ft.name as foreign_table, "+
			"string_agg(fc.name, ',') within group (order by fkc.constraint_column_id) as foreign_columns, "+
			"fk.update_referential_action_desc as on_update, "+
			"fk.delete_referential_action_desc as on_delete "+
			"from sys.foreign_keys as fk "+
			"join sys.foreign_key_columns as fkc on fkc.constraint_object_id = fk.object_id "+
			"join sys.tables as lt on lt.object_id = fk.parent_object_id "+
			"join sys.schemas as ls on lt.schema_id = ls.schema_id "+
			"join sys.columns as lc on fkc.parent_object_id = lc.object_id and fkc.parent_column_id = lc.column_id "+
			"join sys.tables as ft on ft.object_id = fk.referenced_object_id "+
			"join sys.schemas as fs on ft.schema_id = fs.schema_id "+
			"join sys.columns as fc on fkc.referenced_object_id = fc.object_id and fkc.referenced_column_id = fc.column_id "+
			"where lt.name = %s and ls.name = %s "+
			"group by fk.name, fs.name, ft.name, fk.update_referential_action_desc, fk.delete_referential_action_desc",
		g.ssQuoteString(table), schemaFilter), nil
}

// CompileDropAllTables 对应 SqlServerGrammar::compileDropAllTables（:366-369）。
func (g *SqlServerGrammar) CompileDropAllTables() string {
	return "EXEC sp_msforeachtable 'DROP TABLE ?'"
}

// CompileDropAllForeignKeys 对应 SqlServerGrammar::compileDropAllForeignKeys（:535-548）：
// DECLARE @sql 动态拼 ALTER TABLE ... DROP CONSTRAINT 后 sp_executesql。
func (g *SqlServerGrammar) CompileDropAllForeignKeys() string {
	return "DECLARE @sql NVARCHAR(MAX) = N'';\n" +
		"            SELECT @sql += 'ALTER TABLE '\n" +
		"                + QUOTENAME(OBJECT_SCHEMA_NAME(parent_object_id)) + '.' + + QUOTENAME(OBJECT_NAME(parent_object_id))\n" +
		"                + ' DROP CONSTRAINT ' + QUOTENAME(name) + ';'\n" +
		"            FROM sys.foreign_keys;\n" +
		"\n" +
		"            EXEC sp_executesql @sql;"
}

// CompileDropAllViews 对应 SqlServerGrammar::compileDropAllViews（:551-559）。
func (g *SqlServerGrammar) CompileDropAllViews() string {
	return "DECLARE @sql NVARCHAR(MAX) = N'';\n" +
		"            SELECT @sql += 'DROP VIEW ' + QUOTENAME(OBJECT_SCHEMA_NAME(object_id)) + '.' + QUOTENAME(name) + ';'\n" +
		"            FROM sys.views;\n" +
		"\n" +
		"            EXEC sp_executesql @sql;"
}
