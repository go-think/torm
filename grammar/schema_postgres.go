package grammar

import (
	"fmt"
	"strings"
)

// ---- Postgres Schema 编译（对齐 Illuminate\Database\Schema\Grammars\PostgresGrammar 输出） ----

func (g *PostgresGrammar) getColumnTypePG(c *ColumnDefinition) string {
	switch c.Type {
	case "boolean":
		return "boolean"
	case "decimal":
		return fmt.Sprintf("decimal(%d, %d)", c.Precision, c.Scale)
	case "uuid":
		return "uuid"
	case "json":
		return "jsonb"
	case "datetime", "timestamp":
		return "timestamp(0) without time zone"
	case "serial":
		return "serial"
	case "bigserial":
		return "bigserial"
	case "varchar", "char":
		return fmt.Sprintf("%s(%d)", c.Type, c.Length)
	default:
		return c.Type
	}
}

func (g *PostgresGrammar) columnDefinitionPG(c *ColumnDefinition) string {
	name := g.Wrap(c.Name)

	// 自增类型映射为 serial/bigserial
	if c.AutoIncrement {
		serial := "serial"
		if c.Type == "bigint" {
			serial = "bigserial"
		}
		parts := []string{name, serial}
		if c.NotNull || !c.IsNullable {
			parts = append(parts, "not null")
		}
		parts = append(parts, "primary key")
		return strings.Join(parts, " ")
	}

	parts := []string{name, g.getColumnTypePG(c)}
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
	return strings.Join(parts, " ")
}

func (g *PostgresGrammar) CompileCreate(b *Blueprint) []string {
	cols := make([]string, 0, len(b.Columns))
	var pkCols []string
	for _, c := range b.Columns {
		cols = append(cols, g.columnDefinitionPG(c))
		if !c.AutoIncrement && c.PrimaryKey {
			pkCols = append(pkCols, g.Wrap(c.Name))
		}
	}
	for _, idx := range b.Indexes {
		if idx.Type == "primary" {
			for _, col := range idx.Columns {
				pkCols = append(pkCols, g.Wrap(col))
			}
		}
	}
	if len(pkCols) > 0 {
		cols = append(cols, "primary key ("+strings.Join(pkCols, ", ")+")")
	}

	return []string{"create table " + g.WrapTable(b.Table) + " (" + strings.Join(cols, ", ") + ")"}
}

func (g *PostgresGrammar) CompileAdd(b *Blueprint) []string {
	table := g.WrapTable(b.Table)
	out := make([]string, 0, len(b.Columns)+len(b.Indexes))
	for _, c := range b.Columns {
		out = append(out, "alter table "+table+" add column "+g.columnDefinitionPG(c))
	}
	out = append(out, g.compileSchemaIndexesPG(b)...)
	return out
}

func (g *PostgresGrammar) compileSchemaIndexesPG(b *Blueprint) []string {
	out := make([]string, 0, len(b.Indexes))
	table := g.WrapTable(b.Table)
	for _, idx := range b.Indexes {
		switch idx.Type {
		case "index":
			out = append(out, "create index "+g.Wrap(idx.Name)+" on "+table+" ("+g.columnizeNames(idx.Columns)+")")
		case "unique":
			out = append(out, "create unique index "+g.Wrap(idx.Name)+" on "+table+" ("+g.columnizeNames(idx.Columns)+")")
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

func (g *PostgresGrammar) columnizeNames(cols []string) string {
	wrapped := make([]string, len(cols))
	for i, c := range cols {
		wrapped[i] = g.Wrap(c)
	}
	return strings.Join(wrapped, ", ")
}

func (g *PostgresGrammar) CompileDrop(table string) string {
	return "drop table " + g.WrapTable(table)
}

func (g *PostgresGrammar) CompileDropIfExists(table string) string {
	return "drop table if exists " + g.WrapTable(table)
}

func (g *PostgresGrammar) CompileIndex(b *Blueprint, idx *IndexDefinition) string {
	return g.compileSchemaIndexesPG(b)[0]
}

func (g *PostgresGrammar) CompileUnique(b *Blueprint, idx *IndexDefinition) string {
	return g.compileSchemaIndexesPG(b)[0]
}

func (g *PostgresGrammar) CompilePrimary(b *Blueprint, idx *IndexDefinition) string {
	return g.compileSchemaIndexesPG(b)[0]
}

func (g *PostgresGrammar) CompileForeign(b *Blueprint, idx *IndexDefinition) string {
	return g.compileSchemaIndexesPG(b)[0]
}

func (g *PostgresGrammar) CompileTableExists(schema, table string) (string, []any) {
	return "select * from information_schema.tables where table_schema = ? and table_name = ? and table_type = 'BASE TABLE'",
		[]any{schema, table}
}

func (g *PostgresGrammar) CompileColumnListing(schema, table string) (string, []any) {
	return `select column_name as "column_name" from information_schema.columns where table_schema = ? and table_name = ?`,
		[]any{schema, table}
}

func (g *PostgresGrammar) WrapTableForSchema(v any) string {
	return g.WrapTable(v)
}
