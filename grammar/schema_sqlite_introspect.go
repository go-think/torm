package grammar

import (
	"fmt"
	"strings"
)

// ---- SQLite Schema 内省编译 ----
//
// 方言说明：
//   - schema 为空时回落 'main'（SQLite 默认 attached schema 为 main）；
//   - CompileTables 不做 withSize 的 dbstat 版本（需先探针
//     compileDbstatExists 再决定是否 join dbstat；size 为可选信息，Go 侧
//     SchemaTable.Size 恒为 0，如需可后续按 compileDbstatExists + dbstat 补齐）；
//   - 3.37 以下版本走 compileLegacyTables（pragma_table_list 不可用）——版本
//     分支依赖可注入的 serverVersion（TODO 接线点：SQLiteGrammar SetServerVersion）。

// sqliteQuoteString 对应 Grammar::quoteString（SQLite 内省 SQL 内联字面量）；
// 内部单引号双写转义，schema/table 名含 `'` 时不再破坏内省 SQL。
func sqliteQuoteString(v string) string {
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}

// CompileTables 对应 SQLiteGrammar::compileTables（:105-119）：
// pragma_table_list（SQLite 3.37+），type in ('table', 'virtual') 且排除
// sqlite_% 内部表；size 段（dbstat join）简化省略。
func (g *SQLiteGrammar) CompileTables(schema string) (string, []any) {
	clause := ""
	if schema != "" {
		clause = " tl.schema = " + sqliteQuoteString(schema) + " and"
	}
	sql := "select tl.name as name, tl.schema as schema from pragma_table_list as tl where" +
		clause +
		" tl.type in ('table', 'virtual') and tl.name not like 'sqlite\\_%' escape '\\' " +
		"order by tl.schema, tl.name"
	return sql, nil
}

// CompileViews 对应 SQLiteGrammar::compileViews（:155-162）：
// sqlite_master type='view'，sql 列作为视图定义。
func (g *SQLiteGrammar) CompileViews(schema string) (string, []any) {
	s := orMain(schema)
	return fmt.Sprintf(
		"select name, %s as schema, sql as definition from %s.sqlite_master where type = 'view' order by name",
		sqliteQuoteString(s), g.wrapValue(s)), nil
}

// CompileColumns 对应 SQLiteGrammar::compileColumns（:171-179）：
// pragma_table_xinfo 输出 nullable/default/primary/hidden（hidden 即 extra，
// 2/3 为生成列；生成列表达式提取依赖建表 SQL 正则，见 SQLiteProcessor
// processColumns 的 collation/generation 段——TODO 接线点）。
func (g *SQLiteGrammar) CompileColumns(schema, table string) (string, []any) {
	return fmt.Sprintf(
		"select name, type, not \"notnull\" as \"nullable\", dflt_value as \"default\", pk as \"primary\", hidden as \"extra\" "+
			"from pragma_table_xinfo(%s, %s) order by cid asc",
		sqliteQuoteString(table), sqliteQuoteString(orMain(schema))), nil
}

// CompileIndexes 对应 SQLiteGrammar::compileIndexes（:188-202）：
// 主键段（pragma_table_xinfo pk > 0）union pragma_index_list ×
// pragma_index_info（origin = 'pk' 标记主键形态索引）。
func (g *SQLiteGrammar) CompileIndexes(schema, table string) (string, []any) {
	t := sqliteQuoteString(table)
	s := sqliteQuoteString(orMain(schema))
	return fmt.Sprintf(
		"select 'primary' as name, group_concat(col) as columns, 1 as \"unique\", 1 as \"primary\" "+
			"from (select name as col from pragma_table_xinfo(%s, %s) where pk > 0 order by pk, cid) group by name "+
			"union select name, group_concat(col) as columns, \"unique\", origin = 'pk' as \"primary\" "+
			"from (select il.*, ii.name as col from pragma_index_list(%s, %s) il, pragma_index_info(il.name, %s) ii order by il.seq, ii.seqno) "+
			"group by name, \"unique\", \"primary\"",
		t, s, t, s, s), nil
}

// CompileForeignKeys 对应 SQLiteGrammar::compileForeignKeys（:211-222）：
// pragma_foreign_key_list 按 id 分组（group_concat from/to 列），id desc 排序
// 按 id desc 排序保证复合外键列序正确。
func (g *SQLiteGrammar) CompileForeignKeys(schema, table string) (string, []any) {
	s := sqliteQuoteString(orMain(schema))
	return fmt.Sprintf(
		"select group_concat(\"from\") as columns, %s as foreign_schema, \"table\" as foreign_table, "+
			"group_concat(\"to\") as foreign_columns, on_update, on_delete "+
			"from (select * from pragma_foreign_key_list(%s, %s) order by id desc, seq) "+
			"group by id, \"table\", on_update, on_delete",
		s, sqliteQuoteString(table), s), nil
}

// CompileDropAllTables 对应 SQLiteGrammar::compileDropAllTables（:485-492）：
// writable_schema 打开后直接删 sqlite_master 行（调用方需先
// PRAGMA writable_schema = 1、后 PRAGMA writable_schema = 0 + rebuild）。
func (g *SQLiteGrammar) CompileDropAllTables(schema string) string {
	return fmt.Sprintf("delete from %s.sqlite_master where type in ('table', 'index', 'trigger')",
		g.wrapValue(orMain(schema)))
}

// CompileDropAllViews 对应 SQLiteGrammar::compileDropAllViews（:494-501）。
func (g *SQLiteGrammar) CompileDropAllViews(schema string) string {
	return fmt.Sprintf("delete from %s.sqlite_master where type in ('view')",
		g.wrapValue(orMain(schema)))
}

// CompileRebuild 对应 SQLiteGrammar::compileRebuild（:503-512）：
// 删完 schema 行后 vacuum 重建。
func (g *SQLiteGrammar) CompileRebuild(schema string) string {
	return fmt.Sprintf("vacuum %s", g.wrapValue(orMain(schema)))
}
