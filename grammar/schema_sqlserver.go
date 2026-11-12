package grammar

import (
	"fmt"
	"strings"
)

// ---- SQL Server Schema 编译（对齐 Illuminate\Database\Schema\Grammars\SqlServerGrammar） ----
//
// 方言要点：[标识符]（wrapValueFn 已由 SqlServerGrammar 查询侧注入）、
// nvarchar/nchar/max、identity 自增、datetime2/datetimeoffset、
// drop 约束按名（drop constraint / drop index ... on ...）、sp_rename 改名。

func (g *SqlServerGrammar) getColumnTypeSQLServer(c *ColumnDefinition) string {
	switch c.Type {
	case "char":
		return fmt.Sprintf("nchar(%d)", c.Length)
	case "varchar":
		return fmt.Sprintf("nvarchar(%d)", c.Length)
	case "tinytext":
		return "nvarchar(255)"
	case "text", "mediumtext", "longtext":
		return "nvarchar(max)"
	case "int":
		return "int"
	case "bigint":
		return "bigint"
	case "tinyint":
		return "tinyint"
	case "smallint":
		return "smallint"
	case "mediumint", "year":
		return "int"
	case "boolean":
		return "bit"
	case "decimal":
		return fmt.Sprintf("decimal(%d, %d)", c.Precision, c.Scale)
	case "float":
		if c.Precision > 0 {
			return fmt.Sprintf("float(%d)", c.Precision)
		}
		return "float"
	case "double":
		return "double precision"
	case "enum", "set":
		return fmt.Sprintf("nvarchar(255) check (%s in (%s))",
			g.schemaWrapValue(c.Name), g.QuoteStringN(c.Allowed))
	case "json", "jsonb":
		return "nvarchar(max)"
	case "date":
		return "date"
	case "datetime", "timestamp":
		if c.Precision > 0 {
			return fmt.Sprintf("datetime2(%d)", c.Precision)
		}
		return "datetime"
	case "datetimetz", "timestamptz":
		if c.Precision > 0 {
			return fmt.Sprintf("datetimeoffset(%d)", c.Precision)
		}
		return "datetimeoffset"
	case "time", "timetz":
		if c.Precision > 0 {
			return fmt.Sprintf("time(%d)", c.Precision)
		}
		return "time"
	case "binary":
		if c.Length > 0 {
			if c.Fixed {
				return fmt.Sprintf("binary(%d)", c.Length)
			}
			return fmt.Sprintf("varbinary(%d)", c.Length)
		}
		return "varbinary(max)"
	case "uuid":
		return "uniqueidentifier"
	case "ulid":
		return fmt.Sprintf("nchar(%d)", c.Length)
	case "ipaddress":
		return "nvarchar(45)"
	case "macaddress":
		return "nvarchar(17)"
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

func (g *SqlServerGrammar) columnDefinitionSQLServer(c *ColumnDefinition) string {
	if c.UseCurrent && (c.Type == "timestamp" || c.Type == "timestamptz" || c.Type == "datetime" || c.Type == "datetimetz") {
		c = cloneColumn(c)
		if c.DefaultRaw == "" && c.DefaultValue == nil {
			c.DefaultRaw = "CURRENT_TIMESTAMP"
		}
	}

	parts := []string{g.schemaWrap(c.Name), g.getColumnTypeSQLServer(c)}

	// 生成列（persisted）
	if c.StoredAs != "" || c.VirtualAs != "" {
		expr := c.StoredAs
		if expr == "" {
			expr = c.VirtualAs
		}
		as := " as (" + expr + ")"
		if c.Persisted {
			as += " persisted"
		}
		parts = append(parts, as)
		return joinSpace(parts)
	}

	// 对齐 SqlServerGrammar 修饰符顺序：nullable → default → identity
	if c.NotNull || !c.IsNullable {
		parts = append(parts, "not null")
	} else {
		parts = append(parts, "null")
	}
	// modifyDefault（SqlServerGrammar.php:988-993）：change 列的默认值不内联，
	// 由 compileDefault 以 alter table add default Y for col 形式补挂
	if !c.IsChange {
		if c.DefaultRaw != "" {
			parts = append(parts, "default "+c.DefaultRaw)
		} else if c.DefaultValue != nil {
			parts = append(parts, "default "+g.schemaQuoteString(c.DefaultValue))
		}
	}
	if c.IsAutoIncrement {
		parts = append(parts, "identity")
		if c.PrimaryKey {
			parts = append(parts, "primary key")
		}
	}
	if c.Collation != "" {
		parts = append(parts, "collate "+g.schemaWrapValue(c.Collation))
	}
	return joinSpace(parts)
}

func (g *SqlServerGrammar) CompileCreate(b *Blueprint) []string {
	cols := make([]string, 0, len(b.Columns))
	var pkCols []string
	for _, c := range b.Columns {
		cols = append(cols, g.columnDefinitionSQLServer(c))
		if !c.IsAutoIncrement && c.PrimaryKey {
			pkCols = append(pkCols, g.schemaWrap(c.Name))
		}
	}
	for _, idx := range b.Indexes {
		if idx.Type == "primary" {
			pkCols = append(pkCols, idx.Columns...)
		}
	}
	if len(pkCols) > 0 {
		cols = append(cols, "primary key ("+joinComma(schemaWrapAll(g, pkCols))+")")
	}

	tableName := g.schemaWrapTable(b.Table)
	if b.Temporary {
		// Laravel SqlServerGrammar.php:206-212：临时表用 # 前缀命名
		tableName = "#" + tableName
	}
	out := []string{"create table " + tableName + " (" + joinComma(cols) + ")"}
	// unique/index 为独立语句（primary 已内联）
	table := g.schemaWrapTable(b.Table)
	for _, idx := range b.Indexes {
		switch idx.Type {
		case "index":
			out = append(out, "create index "+g.schemaWrap(idx.Name)+" on "+table+" ("+g.columnizeNamesSS(idx.Columns)+")")
		case "unique":
			out = append(out, "create unique index "+g.schemaWrap(idx.Name)+" on "+table+" ("+g.columnizeNamesSS(idx.Columns)+")")
		case "foreign":
			out = append(out, g.ssForeignKeySQL(table, idx))
		}
	}
	return out
}

// CompileAdd 对应 compileAdd：alter table add（逐列一条）。
func (g *SqlServerGrammar) CompileAdd(b *Blueprint) []string {
	table := g.schemaWrapTable(b.Table)
	out := make([]string, 0, len(b.Columns)+len(b.Indexes))
	for _, c := range b.Columns {
		if c.IsChange {
			out = append(out, g.CompileChange(b, c)...)
			continue
		}
		out = append(out, "alter table "+table+" add "+g.columnDefinitionSQLServer(c))
	}
	for _, idx := range b.Indexes {
		switch idx.Type {
		case "index":
			out = append(out, "create index "+g.schemaWrap(idx.Name)+" on "+table+" ("+g.columnizeNamesSS(idx.Columns)+")")
		case "unique":
			out = append(out, "create unique index "+g.schemaWrap(idx.Name)+" on "+table+" ("+g.columnizeNamesSS(idx.Columns)+")")
		case "primary":
			out = append(out, "alter table "+table+" add constraint "+g.schemaWrap(idx.Name)+" primary key ("+g.columnizeNamesSS(idx.Columns)+")")
		case "spatialindex":
			out = append(out, "create spatial index "+g.schemaWrap(idx.Name)+" on "+table+" ("+g.columnizeNamesSS(idx.Columns)+")")
		case "fulltext":
			panic(ErrUnsupported{"This database driver does not support fulltext index creation."})
		case "vectorindex":
			panic(ErrUnsupported{"The database driver in use does not support vector indexes."})
		case "foreign":
			out = append(out, g.ssForeignKeySQL(table, idx))
		}
	}
	return out
}

// ssForeignKeySQL 编译外键约束（复合列 + on delete/on update）。
func (g *SqlServerGrammar) ssForeignKeySQL(table string, idx *IndexDefinition) string {
	sql := "alter table " + table + " add constraint " + g.schemaWrap(idx.Name) +
		" foreign key (" + g.columnizeNamesSS(idx.Columns) + ") references " +
		g.schemaWrapTable(idx.OnTable) + " (" + g.columnizeNamesSS(idx.foreignRefColumns()) + ")"
	if idx.OnDelete != "" {
		sql += " on delete " + idx.OnDelete
	}
	if idx.OnUpdate != "" {
		sql += " on update " + idx.OnUpdate
	}
	return sql
}

// CompileChange 对应 SqlServerGrammar::compileChange（:250-259）：
// 先编译默认约束删除块（DECLARE @sql ...），再 alter column；
// change 列带 default 时追加 compileDefault 流式命令（:323-329：
// alter table add default Y for col）。
func (g *SqlServerGrammar) CompileChange(b *Blueprint, c *ColumnDefinition) []string {
	out := []string{
		g.CompileDropDefaultConstraint(b, []string{c.Name}),
		"alter table " + g.schemaWrapTable(b.Table) + " alter column " + g.columnDefinitionSQLServer(c),
	}
	// compileDefault（:323-329）：change 列带默认值时以独立命令补挂 default
	if c.DefaultRaw != "" {
		out = append(out, "alter table "+g.schemaWrapTable(b.Table)+" add default "+c.DefaultRaw+" for "+g.schemaWrap(c.Name))
	} else if c.DefaultValue != nil {
		out = append(out, "alter table "+g.schemaWrapTable(b.Table)+" add default "+ssGetDefaultValue(c.DefaultValue)+" for "+g.schemaWrap(c.Name))
	}
	return out
}

func (g *SqlServerGrammar) CompileDrop(table string) string {
	return "drop table " + g.schemaWrapTable(table)
}

func (g *SqlServerGrammar) CompileDropIfExists(table string) string {
	return "if exists (select * from sys.sysobjects where id = object_id(?) and xtype = N'U') drop table " + g.schemaWrapTable(table)
}

// CompileDropColumn 对应 SqlServerGrammar::compileDropColumn（:371-383）：
// 默认约束删除块（; 结尾）与 alter table drop column 同串拼接。
func (g *SqlServerGrammar) CompileDropColumn(b *Blueprint, columns []string) []string {
	wrapped := schemaWrapAll(g, columns)
	block := g.CompileDropDefaultConstraint(b, columns) + ";"
	alter := "alter table " + g.schemaWrapTable(b.Table) + " drop column " + joinComma(wrapped)
	return []string{block + alter}
}

// CompileDropDefaultConstraint 对应 SqlServerGrammar::compileDropDefaultConstraint
// （:394-410）：按 sys.columns 找出目标列的 default 约束并动态拼 DROP CONSTRAINT。
func (g *SqlServerGrammar) CompileDropDefaultConstraint(b *Blueprint, columns []string) string {
	quoted := make([]string, len(columns))
	for i, c := range columns {
		quoted[i] = "'" + strings.ReplaceAll(c, "'", "''") + "'"
	}
	table := g.schemaWrapTable(b.Table)
	tableName := g.ssQuoteString(table)
	sql := "DECLARE @sql NVARCHAR(MAX) = '';"
	sql += "SELECT @sql += 'ALTER TABLE " + table + " DROP CONSTRAINT ' + OBJECT_NAME([default_object_id]) + ';' "
	sql += "FROM sys.columns "
	sql += "WHERE [object_id] = OBJECT_ID(" + tableName + ") AND [name] in (" + joinComma(quoted) + ") AND [default_object_id] <> 0;"
	sql += "EXEC(@sql)"
	return sql
}

// ssGetDefaultValue 对应 Grammar::getDefaultValue（Grammar.php:521-534）：
// bool → '1'/'0'，其余标量单引号包裹（内部单引号双写）。
func ssGetDefaultValue(v any) string {
	switch x := v.(type) {
	case bool:
		if x {
			return "'1'"
		}
		return "'0'"
	case string:
		return "'" + strings.ReplaceAll(x, "'", "''") + "'"
	default:
		return "'" + strings.ReplaceAll(fmt.Sprint(x), "'", "''") + "'"
	}
}

// CompileRenameColumn 对应 sp_rename ... N'COLUMN'。
func (g *SqlServerGrammar) CompileRenameColumn(b *Blueprint, from, to string) string {
	return fmt.Sprintf("sp_rename %s, %s, N'COLUMN'",
		g.schemaQuoteString(g.schemaWrapTable(b.Table)+"."+g.schemaWrap(from)), g.schemaWrap(to))
}

// CompileRename 对应 compileRename：sp_rename。
func (g *SqlServerGrammar) CompileRename(b *Blueprint, to string) string {
	return fmt.Sprintf("sp_rename %s, %s", g.schemaQuoteString(g.schemaWrapTable(b.Table)), g.schemaWrapTable(to))
}

// CompileRenameIndex 对应 sp_rename ... N'INDEX'。
func (g *SqlServerGrammar) CompileRenameIndex(b *Blueprint, from, to string) string {
	return fmt.Sprintf("sp_rename %s, %s, N'INDEX'",
		g.schemaQuoteString(g.schemaWrapTable(b.Table)+"."+g.schemaWrap(from)), g.schemaWrap(to))
}

func (g *SqlServerGrammar) CompileDropPrimary(b *Blueprint, index string) string {
	return "alter table " + g.schemaWrapTable(b.Table) + " drop constraint " + g.schemaWrap(index)
}

func (g *SqlServerGrammar) CompileDropUnique(b *Blueprint, index string) string {
	return "drop index " + g.schemaWrap(index) + " on " + g.schemaWrapTable(b.Table)
}

func (g *SqlServerGrammar) CompileDropIndex(b *Blueprint, index string) string {
	return "drop index " + g.schemaWrap(index) + " on " + g.schemaWrapTable(b.Table)
}

func (g *SqlServerGrammar) CompileDropFullText(b *Blueprint, index string) string {
	return g.CompileDropIndex(b, index)
}

func (g *SqlServerGrammar) CompileDropSpatialIndex(b *Blueprint, index string) string {
	return g.CompileDropIndex(b, index)
}

func (g *SqlServerGrammar) CompileDropVectorIndex(b *Blueprint, index string) string {
	panic(ErrUnsupported{"The database driver in use does not support vector indexes."})
}

func (g *SqlServerGrammar) CompileDropForeign(b *Blueprint, index string) string {
	return "alter table " + g.schemaWrapTable(b.Table) + " drop constraint " + g.schemaWrap(index)
}

// ---- 索引命令 ----

func (g *SqlServerGrammar) CompileIndex(b *Blueprint, idx *IndexDefinition) string {
	return "create index " + g.schemaWrap(idx.Name) + " on " + g.schemaWrapTable(b.Table) + " (" + g.columnizeNamesSS(idx.Columns) + ")"
}

func (g *SqlServerGrammar) CompileUnique(b *Blueprint, idx *IndexDefinition) string {
	return "create unique index " + g.schemaWrap(idx.Name) + " on " + g.schemaWrapTable(b.Table) + " (" + g.columnizeNamesSS(idx.Columns) + ")"
}

func (g *SqlServerGrammar) CompilePrimary(b *Blueprint, idx *IndexDefinition) string {
	return "alter table " + g.schemaWrapTable(b.Table) + " add constraint " + g.schemaWrap(idx.Name) + " primary key (" + g.columnizeNamesSS(idx.Columns) + ")"
}

func (g *SqlServerGrammar) CompileForeign(b *Blueprint, idx *IndexDefinition) string {
	return g.ssForeignKeySQL(g.schemaWrapTable(b.Table), idx)
}

func (g *SqlServerGrammar) CompileFullText(b *Blueprint, idx *IndexDefinition) string {
	panic(ErrUnsupported{"This database driver does not support fulltext index creation."})
}

func (g *SqlServerGrammar) CompileSpatialIndex(b *Blueprint, idx *IndexDefinition) string {
	return "create spatial index " + g.schemaWrap(idx.Name) + " on " + g.schemaWrapTable(b.Table) + " (" + g.columnizeNamesSS(idx.Columns) + ")"
}

func (g *SqlServerGrammar) CompileVectorIndex(b *Blueprint, idx *IndexDefinition) string {
	panic(ErrUnsupported{"The database driver in use does not support vector indexes."})
}

// ---- 内省 ----

func (g *SqlServerGrammar) CompileTableExists(schema, table string) (string, []any) {
	name := table
	if schema != "" {
		name = schema + "." + table
	}
	return fmt.Sprintf("select (case when object_id(%s, 'U') is null then 0 else 1 end) as [exists]",
		g.schemaQuoteString(name)), nil
}

func (g *SqlServerGrammar) CompileColumnListing(schema, table string) (string, []any) {
	return "select col.name from sys.columns as col " +
		"join sys.objects as obj on col.object_id = obj.object_id " +
		"join sys.schemas as scm on obj.schema_id = scm.schema_id " +
		"where obj.type in ('U', 'V') and obj.name = ? and scm.name = ? " +
		"order by col.column_id", []any{table, orMain(schema)}
}

func (g *SqlServerGrammar) WrapTableForSchema(v any) string {
	return g.schemaWrapTable(v)
}

func (g *SqlServerGrammar) columnizeNamesSS(cols []string) string {
	return joinComma(schemaWrapAll(g, cols))
}

func schemaWrapAll(g *SqlServerGrammar, cols []string) []string {
	wrapped := make([]string, len(cols))
	for i, c := range cols {
		wrapped[i] = g.schemaWrap(c)
	}
	return wrapped
}

// schemaQuoteString 字符串字面量（DDL 内省/改名用）。
func (g *SqlServerGrammar) schemaQuoteString(v any) string {
	return "N'" + fmt.Sprint(v) + "'"
}

// schemaWrapValue Laravel 13 Schema\Grammars\SqlServerGrammar 不覆写
// wrapValue：DDL 标识符使用标准双引号（查询侧 [] 包裹不影响 DDL）。
func (g *SqlServerGrammar) schemaWrapValue(v string) string {
	return `"` + strings.ReplaceAll(v, `"`, `""`) + `"`
}

// schemaWrapTable 等价 BaseGrammar.WrapTable（含前缀与 schema.table 段处理），
// 但强制双引号 wrap。
func (g *SqlServerGrammar) schemaWrapTable(value any) string {
	if g.IsExpression(value) {
		return fmt.Sprint(g.GetValue(value))
	}
	table, ok := value.(string)
	if !ok {
		return fmt.Sprint(value)
	}
	prefix := g.GetTablePrefix()
	if strings.Contains(table, ".") {
		idx := strings.LastIndex(table, ".")
		table = table[:idx] + "." + prefix + table[idx+1:]
		segments := strings.Split(table, ".")
		for i, seg := range segments {
			segments[i] = g.schemaWrapValue(seg)
		}
		return strings.Join(segments, ".")
	}
	return g.schemaWrapValue(prefix + table)
}

// schemaWrap 等价 BaseGrammar.Wrap 的 DDL 子集（table.col 段处理）。
func (g *SqlServerGrammar) schemaWrap(value any) string {
	if g.IsExpression(value) {
		return fmt.Sprint(g.GetValue(value))
	}
	v, ok := value.(string)
	if !ok {
		return fmt.Sprint(value)
	}
	if strings.Contains(v, ".") {
		segments := strings.Split(v, ".")
		for i, seg := range segments {
			if i == 0 {
				segments[i] = g.schemaWrapTable(seg)
			} else {
				segments[i] = g.schemaWrapValue(seg)
			}
		}
		return strings.Join(segments, ".")
	}
	return g.schemaWrapValue(v)
}

// QuoteStringN SQL Server N'...' 前缀字符串字面量（enum check 用）。
func (g *BaseGrammar) QuoteStringN(values []string) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = "N'" + v + "'"
	}
	return joinComma(parts)
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
