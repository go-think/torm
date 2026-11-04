package grammar

import (
	"fmt"
	"strings"
)

// ---- MySQL Schema 编译（对齐 Illuminate\Database\Schema\Grammars\MySqlGrammar 输出） ----

func (g *MySqlGrammar) getColumnType(c *ColumnDefinition) string {
	switch c.Type {
	case "boolean":
		return "tinyint(1)"
	case "decimal":
		return fmt.Sprintf("decimal(%d, %d)", c.Precision, c.Scale)
	case "uuid":
		return "char(36)"
	case "varchar", "char":
		return fmt.Sprintf("%s(%d)", c.Type, c.Length)
	default:
		return c.Type
	}
}

func (g *MySqlGrammar) columnDefinition(c *ColumnDefinition) string {
	parts := []string{g.Wrap(c.Name), g.getColumnType(c)}
	if c.Unsigned {
		parts = append(parts, "unsigned")
	}
	if c.NotNull || !c.IsNullable {
		parts = append(parts, "not null")
	} else {
		parts = append(parts, "null")
	}
	if c.DefaultRaw != "" {
		parts = append(parts, "default "+c.DefaultRaw)
	} else if c.DefaultValue != nil {
		parts = append(parts, "default "+g.QuoteString(c.DefaultValue))
	}
	if c.AutoIncrement {
		parts = append(parts, "auto_increment")
		if c.PrimaryKey {
			parts = append(parts, "primary key")
		}
	}
	return strings.Join(parts, " ")
}

// CompileCreate：create table ... （含 charset/collation/engine 与尾部 primary key 段）
func (g *MySqlGrammar) CompileCreate(b *Blueprint) []string {
	cols := make([]string, 0, len(b.Columns)+1)
	var pkCols []string
	for _, c := range b.Columns {
		cols = append(cols, g.columnDefinition(c))
		if !c.AutoIncrement && c.PrimaryKey {
			pkCols = append(pkCols, g.Wrap(c.Name))
		}
	}
	for _, idx := range b.Indexes {
		if idx.Type == "primary" {
			for _, col := range idx.Columns {
				pkCols = append(pkCols, g.Wrap(col))
			}
		} else if idx.Type != "index" && idx.Type != "unique" && idx.Type != "foreign" {
			cols = append(cols, g.columnDefinition(&ColumnDefinition{Name: idx.Name}))
		}
	}
	if len(pkCols) > 0 {
		cols = append(cols, "primary key ("+strings.Join(pkCols, ", ")+")")
	}

	sql := "create table " + g.WrapTable(b.Table) + " (" + strings.Join(cols, ", ") + ")"
	if b.Charset != "" {
		sql += " default character set " + b.Charset
	}
	if b.Collation != "" {
		sql += " collate '" + b.Collation + "'"
	}
	if b.Engine != "" {
		sql += " engine = " + b.Engine
	}

	// MySQL：index/unique/foreign 在 create 场景下也是独立 alter 语句（对齐 Laravel）
	out := []string{sql}
	out = append(out, g.compileSchemaIndexesNonPrimary(b)...)
	return out
}

// CompileAdd：alter 逐列 add；自增起始值单独语句
func (g *MySqlGrammar) CompileAdd(b *Blueprint) []string {
	table := g.WrapTable(b.Table)
	out := make([]string, 0, len(b.Columns)+len(b.Indexes)+1)
	for _, c := range b.Columns {
		out = append(out, "alter table "+table+" add "+g.columnDefinition(c))
	}
	out = append(out, g.compileSchemaIndexes(b)...)
	for _, c := range b.Columns {
		if c.From > 0 {
			out = append(out, "alter table "+table+" auto_increment = "+fmt.Sprint(c.From))
		}
	}
	return out
}

func (g *MySqlGrammar) compileSchemaIndexes(b *Blueprint) []string {
	return g.compileSchemaIndexesFiltered(b, "index", "unique", "primary", "foreign")
}

func (g *MySqlGrammar) compileSchemaIndexesNonPrimary(b *Blueprint) []string {
	return g.compileSchemaIndexesFiltered(b, "index", "unique", "foreign")
}

func (g *MySqlGrammar) compileSchemaIndexesFiltered(b *Blueprint, kinds ...string) []string {
	out := make([]string, 0, len(b.Indexes))
	table := g.WrapTable(b.Table)
	for _, idx := range b.Indexes {
		kind := idx.Type
		allowed := false
		for _, k := range kinds {
			if kind == k {
				allowed = true
				break
			}
		}
		if !allowed {
			continue
		}
		switch kind {
		case "index":
			out = append(out, "alter table "+table+" add index "+g.Wrap(idx.Name)+"("+g.columnizeNames(idx.Columns)+")")
		case "unique":
			out = append(out, "alter table "+table+" add unique "+g.Wrap(idx.Name)+"("+g.columnizeNames(idx.Columns)+")")
		case "primary":
			out = append(out, "alter table "+table+" add primary key ("+g.columnizeNames(idx.Columns)+")")
		case "foreign":
			sql := "alter table " + table + " add constraint " + g.Wrap(idx.Name) +
				" foreign key (" + g.columnizeNames(idx.Columns) + ") references " +
				g.WrapTable(idx.OnTable) + " (" + g.columnizeNames([]string{idx.ReferenceColumn}) + ")"
			if idx.OnDelete != "" {
				sql += " on delete " + idx.OnDelete
			}
			if idx.OnUpdate != "" {
				sql += " on update " + idx.OnUpdate
			}
			out = append(out, sql)
		}
	}
	return out
}

func (g *MySqlGrammar) columnizeNames(cols []string) string {
	wrapped := make([]string, len(cols))
	for i, c := range cols {
		wrapped[i] = g.Wrap(c)
	}
	return strings.Join(wrapped, ", ")
}

func (g *MySqlGrammar) CompileDrop(table string) string {
	return "drop table " + g.WrapTable(table)
}

func (g *MySqlGrammar) CompileDropIfExists(table string) string {
	return "drop table if exists " + g.WrapTable(table)
}

func (g *MySqlGrammar) CompileIndex(b *Blueprint, idx *IndexDefinition) string {
	return g.compileSchemaIndexes(b)[0]
}

func (g *MySqlGrammar) CompileUnique(b *Blueprint, idx *IndexDefinition) string {
	return g.compileSchemaIndexes(b)[0]
}

func (g *MySqlGrammar) CompilePrimary(b *Blueprint, idx *IndexDefinition) string {
	return g.compileSchemaIndexes(b)[0]
}

func (g *MySqlGrammar) CompileForeign(b *Blueprint, idx *IndexDefinition) string {
	return g.compileSchemaIndexes(b)[0]
}

func (g *MySqlGrammar) CompileTableExists(schema, table string) (string, []any) {
	return "select * from information_schema.tables where table_schema = ? and table_name = ? and table_type = 'BASE TABLE'",
		[]any{schema, table}
}

func (g *MySqlGrammar) CompileColumnListing(schema, table string) (string, []any) {
	return "select `column_name` as `column_name` from information_schema.columns where `table_schema` = ? and `table_name` = ?",
		[]any{schema, table}
}

func (g *MySqlGrammar) WrapTableForSchema(v any) string {
	return g.WrapTable(v)
}

// 编译期断言：实现 SchemaGrammar
var _ SchemaGrammar = (*MySqlGrammar)(nil)
var _ SchemaGrammar = (*PostgresGrammar)(nil)
