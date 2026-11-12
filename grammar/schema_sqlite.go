package grammar

import "fmt"

// ---- SQLite Schema 编译（对齐 Illuminate\Database\Schema\Grammars\SQLiteGrammar） ----
//
// 方言要点：
//   - 无 unsigned 关键字（整数列一概 integer）；
//   - 无 serial：自增为 integer primary key autoincrement；
//   - varchar/char 一律 varchar（无长度）、文本族一概 text、json/jsonb 一概 text；
//   - enum 为 varchar check ("col" in (...))；
//   - timestamp/datetime 一概 datetime，date 为 date，time 为 time，year 为 integer；
//   - create table 时 foreign key 内联（SQLite 不支持独立 add constraint）；
//   - dropPrimary/dropForeign 需重建表，Laravel 走 compileAlter 重建，此处返回空串
//     跳过（调用方 ToSql 忽略空语句），dropForeign 按名不支持（对齐 Laravel 抛错语义）。

func (g *SQLiteGrammar) getColumnTypeSQLite(c *ColumnDefinition) string {
	switch c.Type {
	case "char", "varchar":
		// 对齐 typeChar/typeString：SQLite 无长度
		return "varchar"
	case "text", "tinytext", "mediumtext", "longtext":
		return "text"
	case "int", "bigint", "tinyint", "smallint", "mediumint", "year":
		return "integer"
	case "boolean":
		return "tinyint(1)"
	case "decimal":
		return "numeric"
	case "float":
		return "float"
	case "double":
		return "double"
	case "enum", "set":
		return "varchar check (" + g.wrapValue(c.Name) + " in (" + g.QuoteString(c.Allowed) + "))"
	case "json", "jsonb":
		// 对齐 typeJson/typeJsonb：默认 text（use_native_json 配置走 json）
		return "text"
	case "date":
		return "date"
	case "datetime", "datetimetz", "timestamp", "timestamptz":
		return "datetime"
	case "time", "timetz":
		return "time"
	case "binary":
		return "blob"
	case "uuid", "ulid", "ipaddress", "macaddress":
		return "varchar"
	case "geometry", "geography":
		return "geometry"
	case "vector", "tsvector":
		panic(ErrUnsupported{"This database driver does not support vector columns."})
	case "computed":
		panic(ErrUnsupported{"This database driver requires a type, see the virtualAs / storedAs modifiers."})
	default:
		return c.Type
	}
}

func (g *SQLiteGrammar) columnDefinitionSQLite(c *ColumnDefinition) string {
	if c.UseCurrent && (c.Type == "timestamp" || c.Type == "timestamptz" || c.Type == "datetime" || c.Type == "datetimetz") {
		c = cloneColumn(c)
		if c.DefaultRaw == "" && c.DefaultValue == nil {
			c.DefaultRaw = "CURRENT_TIMESTAMP"
		}
	}

	parts := []string{g.Wrap(c.Name), g.getColumnTypeSQLite(c)}

	// 生成列
	if c.VirtualAs != "" {
		parts = append(parts, "as ("+c.VirtualAs+")")
		return joinSpace(parts)
	}
	if c.StoredAs != "" {
		parts = append(parts, "as ("+c.StoredAs+") stored")
		return joinSpace(parts)
	}

	// SQLite：无 unsigned；自增 = integer primary key autoincrement
	if c.IsAutoIncrement {
		parts = append(parts, "primary key autoincrement")
	} else if c.PrimaryKey {
		parts = append(parts, "primary key")
	}
	if c.NotNull || !c.IsNullable {
		parts = append(parts, "not null")
	}
	// SQLite 对可空列不输出 "null"（对齐 Laravel SQLiteGrammar::modifyNullable）
	if c.DefaultRaw != "" {
		parts = append(parts, "default "+c.DefaultRaw)
	} else if c.DefaultValue != nil {
		parts = append(parts, "default "+g.QuoteString(c.DefaultValue))
	}
	if c.Collation != "" {
		parts = append(parts, "collate "+g.wrapValue(c.Collation))
	}
	return joinSpace(parts)
}

func joinSpace(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " "
		}
		out += p
	}
	return out
}

// CompileCreate 对应 compileCreate：foreign key 内联 + primary key 内联；
// index/unique 为独立语句。
func (g *SQLiteGrammar) CompileCreate(b *Blueprint) []string {
	cols := make([]string, 0, len(b.Columns))
	var fkSQL []string
	var pkCols []string
	for _, c := range b.Columns {
		cols = append(cols, g.columnDefinitionSQLite(c))
	}
	for _, idx := range b.Indexes {
		switch idx.Type {
		case "primary":
			pkCols = append(pkCols, idx.Columns...)
		case "foreign":
			fkSQL = append(fkSQL, g.sqliteForeignKeyInline(idx))
		}
	}
	table := g.WrapTable(b.Table)
	prefix := "create table "
	if b.Temporary {
		// Laravel SQLiteGrammar.php:233-234：create temporary table
		prefix = "create temporary table "
	}
	sql := prefix + table + " (" + joinComma(cols)
	sql += joinComma(fkSQL)
	if len(pkCols) > 0 {
		sql += ", primary key (" + g.columnizeNamesSQLite(pkCols) + ")"
	}
	sql += ")"

	out := []string{sql}
	// index/unique/fulltext 为独立语句
	for _, idx := range b.Indexes {
		switch idx.Type {
		case "index":
			out = append(out, "create index "+g.Wrap(idx.Name)+" on "+table+" ("+g.columnizeNamesSQLite(idx.Columns)+")")
		case "unique":
			out = append(out, "create unique index "+g.Wrap(idx.Name)+" on "+table+" ("+g.columnizeNamesSQLite(idx.Columns)+")")
		}
	}
	return out
}

// sqliteForeignKeyInline 内联外键：, foreign key(...) references ...(...)。
func (g *SQLiteGrammar) sqliteForeignKeyInline(idx *IndexDefinition) string {
	sql := ", foreign key(" + g.columnizeNamesSQLite(idx.Columns) + ") references " +
		g.WrapTable(idx.OnTable) + " (" + g.columnizeNamesSQLite(idx.foreignRefColumns()) + ")"
	if idx.OnDelete != "" {
		sql += " on delete " + idx.OnDelete
	}
	if idx.OnUpdate != "" {
		sql += " on update " + idx.OnUpdate
	}
	return sql
}

// CompileAdd 对应 compileAdd：alter table add column（逐列，一条一列）。
func (g *SQLiteGrammar) CompileAdd(b *Blueprint) []string {
	table := g.WrapTable(b.Table)
	out := make([]string, 0, len(b.Columns)+len(b.Indexes))
	for _, c := range b.Columns {
		if c.IsChange {
			// SQLite 列修改需重建整表（compileAlter 流程），超出本实现范围
			panic(ErrUnsupported{"SQLite requires the table to be rebuilt for column changes."})
		}
		out = append(out, "alter table "+table+" add column "+g.columnDefinitionSQLite(c))
	}
	for _, idx := range b.Indexes {
		switch idx.Type {
		case "index":
			out = append(out, "create index "+g.Wrap(idx.Name)+" on "+table+" ("+g.columnizeNamesSQLite(idx.Columns)+")")
		case "unique":
			out = append(out, "create unique index "+g.Wrap(idx.Name)+" on "+table+" ("+g.columnizeNamesSQLite(idx.Columns)+")")
		case "foreign":
			// SQLite 仅支持建表时内联外键（对齐 Laravel 运行时错误）
			panic(ErrUnsupported{"This database driver does not support adding foreign keys after table creation."})
		case "spatialindex":
			panic(ErrUnsupported{"The database driver in use does not support spatial indexes."})
		case "fulltext":
			panic(ErrUnsupported{"The database driver in use does not support fulltext index creation."})
		case "vectorindex":
			panic(ErrUnsupported{"The database driver in use does not support vector indexes."})
		}
	}
	return out
}

// CompileChange：SQLite 修改列需重建整表（Laravel compileAlter 的
// __temp__ 重建流程依赖运行期内省），返回不支持错误。
func (g *SQLiteGrammar) CompileChange(b *Blueprint, c *ColumnDefinition) []string {
	panic(ErrUnsupported{"SQLite requires the table to be rebuilt for column changes."})
}

func (g *SQLiteGrammar) CompileDrop(table string) string {
	return "drop table " + g.WrapTable(table)
}

func (g *SQLiteGrammar) CompileDropIfExists(table string) string {
	return "drop table if exists " + g.WrapTable(table)
}

// CompileDropColumn 对应 compileDropColumn（SQLite >= 3.35）：逐列语句。
func (g *SQLiteGrammar) CompileDropColumn(b *Blueprint, columns []string) []string {
	table := g.WrapTable(b.Table)
	out := make([]string, len(columns))
	for i, c := range columns {
		out[i] = "alter table " + table + " drop column " + g.Wrap(c)
	}
	return out
}

func (g *SQLiteGrammar) CompileRenameColumn(b *Blueprint, from, to string) string {
	return "alter table " + g.WrapTable(b.Table) + " rename column " + g.Wrap(from) + " to " + g.Wrap(to)
}

// CompileRename 对应 compileRename：alter table x rename to y。
func (g *SQLiteGrammar) CompileRename(b *Blueprint, to string) string {
	return "alter table " + g.WrapTable(b.Table) + " rename to " + g.WrapTable(to)
}

func (g *SQLiteGrammar) CompileRenameIndex(b *Blueprint, from, to string) string {
	panic(ErrUnsupported{"SQLite requires the table to be rebuilt for index renames."})
}

// CompileDropPrimary：SQLite 不支持修改主键，对齐 Laravel 抛出运行时错误
// （SQLiteGrammar.php:645-647）。
func (g *SQLiteGrammar) CompileDropPrimary(b *Blueprint, index string) string {
	panic("RuntimeException: SQLite does not support altering primary keys")
}

// CompileDropUnique / CompileDropIndex 对应 compileDropIndex：drop index name。
func (g *SQLiteGrammar) CompileDropUnique(b *Blueprint, index string) string {
	return g.CompileDropIndex(b, index)
}

func (g *SQLiteGrammar) CompileDropIndex(b *Blueprint, index string) string {
	return "drop index " + g.Wrap(index)
}

// CompileDropFullText / CompileDropSpatialIndex / CompileDropVectorIndex。
func (g *SQLiteGrammar) CompileDropFullText(b *Blueprint, index string) string {
	return g.CompileDropIndex(b, index)
}

func (g *SQLiteGrammar) CompileDropSpatialIndex(b *Blueprint, index string) string {
	panic(ErrUnsupported{"The database driver in use does not support spatial indexes."})
}

func (g *SQLiteGrammar) CompileDropVectorIndex(b *Blueprint, index string) string {
	panic(ErrUnsupported{"The database driver in use does not support vector indexes."})
}

// CompileDropForeign：SQLite 不支持按名删除外键（Laravel 走建表重建）。
func (g *SQLiteGrammar) CompileDropForeign(b *Blueprint, index string) string {
	panic(ErrUnsupported{"This database driver does not support dropping foreign keys by name."})
}

// ---- 索引命令 ----

func (g *SQLiteGrammar) CompileIndex(b *Blueprint, idx *IndexDefinition) string {
	return "create index " + g.Wrap(idx.Name) + " on " + g.WrapTable(b.Table) + " (" + g.columnizeNamesSQLite(idx.Columns) + ")"
}

func (g *SQLiteGrammar) CompileUnique(b *Blueprint, idx *IndexDefinition) string {
	return "create unique index " + g.Wrap(idx.Name) + " on " + g.WrapTable(b.Table) + " (" + g.columnizeNamesSQLite(idx.Columns) + ")"
}

func (g *SQLiteGrammar) CompilePrimary(b *Blueprint, idx *IndexDefinition) string {
	return ""
}

func (g *SQLiteGrammar) CompileForeign(b *Blueprint, idx *IndexDefinition) string {
	// SQLite 仅支持建表时内联外键
	panic(ErrUnsupported{"This database driver does not support adding foreign keys after table creation."})
}

func (g *SQLiteGrammar) CompileFullText(b *Blueprint, idx *IndexDefinition) string {
	panic(ErrUnsupported{"The database driver in use does not support fulltext index creation."})
}

func (g *SQLiteGrammar) CompileSpatialIndex(b *Blueprint, idx *IndexDefinition) string {
	panic(ErrUnsupported{"The database driver in use does not support spatial indexes."})
}

func (g *SQLiteGrammar) CompileVectorIndex(b *Blueprint, idx *IndexDefinition) string {
	panic(ErrUnsupported{"The database driver in use does not support vector indexes."})
}

// ---- 内省 ----

func (g *SQLiteGrammar) CompileTableExists(schema, table string) (string, []any) {
	return fmt.Sprintf("select exists (select 1 from %s.sqlite_master where name = ? and type = 'table') as \"exists\"",
		g.wrapValue(orMain(schema))), []any{table}
}

func (g *SQLiteGrammar) CompileColumnListing(schema, table string) (string, []any) {
	return "select name as \"column_name\" from pragma_table_info(?) order by cid", []any{table}
}

func (g *SQLiteGrammar) WrapTableForSchema(v any) string {
	return g.WrapTable(v)
}

func orMain(s string) string {
	if s == "" {
		return "main"
	}
	return s
}

func (g *SQLiteGrammar) columnizeNamesSQLite(cols []string) string {
	return joinComma(wrapAllSQLite(g, cols))
}

func wrapAllSQLite(g *SQLiteGrammar, cols []string) []string {
	wrapped := make([]string, len(cols))
	for i, c := range cols {
		wrapped[i] = g.Wrap(c)
	}
	return wrapped
}
