package grammar

import (
	"fmt"
	"strings"
)

// ---- Postgres Schema 编译 ----

func (g *PostgresGrammar) getColumnTypePG(c *ColumnDefinition) string {
	switch c.Type {
	case "boolean":
		return "boolean"
	case "decimal":
		return fmt.Sprintf("decimal(%d, %d)", c.Precision, c.Scale)
	case "float":
		if c.Precision > 0 {
			return fmt.Sprintf("float(%d)", c.Precision)
		}
		return "float"
	case "double":
		return "double precision"
	case "enum":
		// 对齐 typeEnum：varchar(255) check + 引用列名
		return fmt.Sprintf("varchar(255) check (%s in (%s))",
			g.wrapValue(c.Name), g.QuoteString(c.Allowed))
	case "uuid":
		return "uuid"
	case "ulid":
		return fmt.Sprintf("char(%d)", c.Length)
	case "json":
		// PG 区分 json/jsonb：json 不再一律映射 jsonb
		return "json"
	case "jsonb":
		return "jsonb"
	case "datetime":
		// 对齐 typeDateTime → typeTimestamp
		return g.pgTimestampType(c.Precision, false)
	case "datetimetz":
		return g.pgTimestampType(c.Precision, true)
	case "timestamp":
		return g.pgTimestampType(c.Precision, false)
	case "timestamptz":
		return g.pgTimestampType(c.Precision, true)
	case "time":
		return g.pgTimeType(c.Precision, false)
	case "timetz":
		return g.pgTimeType(c.Precision, true)
	case "year":
		// 对齐 typeYear：int + useCurrent 由默认值注入
		return "integer"
	case "binary":
		return "bytea"
	case "ipaddress":
		return "inet"
	case "macaddress":
		return "macaddr"
	case "geometry":
		return g.pgGeometryType("geometry", c)
	case "geography":
		return g.pgGeometryType("geography", c)
	case "vector":
		if c.Dimensions > 0 {
			return fmt.Sprintf("vector(%d)", c.Dimensions)
		}
		return "vector"
	case "tsvector":
		return "tsvector"
	case "tinytext":
		return "varchar(255)"
	case "mediumtext", "longtext":
		return "text"
	case "computed":
		// PG 计算列经由 GeneratedAs（generated always as ... stored），
		// 类型按 text 处理（对齐 typeComputed → typeText）
		return "text"
	case "varchar", "char":
		return fmt.Sprintf("%s(%d)", c.Type, c.Length)
	default:
		return c.Type
	}
}

// pgTimestampType：未指定精度按 (0)
// （项目既有 golden 也基于此约定）。
func (g *PostgresGrammar) pgTimestampType(precision int, tz bool) string {
	suffix := " without time zone"
	if tz {
		suffix = " with time zone"
	}
	if precision > 0 {
		return fmt.Sprintf("timestamp(%d)%s", precision, suffix)
	}
	return "timestamp(0)" + suffix
}

func (g *PostgresGrammar) pgTimeType(precision int, tz bool) string {
	suffix := " without time zone"
	if tz {
		suffix = " with time zone"
	}
	if precision > 0 {
		return fmt.Sprintf("time(%d)%s", precision, suffix)
	}
	return "time(0)" + suffix
}

func (g *PostgresGrammar) pgGeometryType(base string, c *ColumnDefinition) string {
	if c.Subtype != "" {
		if c.Srid > 0 {
			return fmt.Sprintf("%s(%s,%d)", base, strings.ToLower(c.Subtype), c.Srid)
		}
		return fmt.Sprintf("%s(%s)", base, strings.ToLower(c.Subtype))
	}
	return base
}

func (g *PostgresGrammar) columnDefinitionPG(c *ColumnDefinition) string {
	// useCurrent 在 timestamp 家族 / date 上注入默认值（对齐 PG type* 注入语义）
	if c.UseCurrent {
		c = cloneColumn(c)
		switch c.Type {
		case "timestamp", "timestamptz", "datetime", "datetimetz":
			if c.DefaultRaw == "" && c.DefaultValue == nil {
				c.DefaultRaw = "CURRENT_TIMESTAMP"
			}
		case "date":
			if c.DefaultRaw == "" && c.DefaultValue == nil {
				c.DefaultRaw = "CURRENT_DATE"
			}
		}
	}

	name := g.Wrap(c.Name)

	// 自增类型映射为 serial/bigserial
	if c.IsAutoIncrement {
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
	if c.Collation != "" {
		parts = append(parts, "collate "+g.wrapValue(c.Collation))
	}
	// 生成列（generated always as ... stored）
	if c.GeneratedAs != "" || c.StoredAs != "" || c.VirtualAs != "" {
		expr := c.GeneratedAs
		if expr == "" {
			expr = c.StoredAs
			if expr == "" {
				expr = c.VirtualAs
			}
		}
		parts = append(parts, "generated always as ("+expr+") stored")
		if c.NotNull || !c.IsNullable {
			parts = append(parts, "not null")
		}
		return strings.Join(parts, " ")
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
	return strings.Join(parts, " ")
}

func (g *PostgresGrammar) CompileCreate(b *Blueprint) []string {
	cols := make([]string, 0, len(b.Columns))
	var pkCols []string
	for _, c := range b.Columns {
		cols = append(cols, g.columnDefinitionPG(c))
		if !c.IsAutoIncrement && c.PrimaryKey {
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

	createPrefix := "create table "
	if b.Temporary {
		// 临时表使用 create temporary table
		createPrefix = "create temporary table "
	}
	out := []string{createPrefix + g.WrapTable(b.Table) + " (" + strings.Join(cols, ", ") + ")"}
	// PG：index/unique/fulltext/spatialindex/vectorindex/foreign 为独立语句
	//（primary 内联进 create）
	out = append(out, g.compileSchemaIndexesPGFiltered(b, "index", "unique", "fulltext",
		"spatialindex", "vectorindex", "foreign")...)
	return out
}

// compileSchemaIndexesPGFiltered 按 kind 过滤编译索引命令。
func (g *PostgresGrammar) compileSchemaIndexesPGFiltered(b *Blueprint, kinds ...string) []string {
	all := b.Indexes
	b.Indexes = nil
	for _, idx := range all {
		allowed := false
		for _, k := range kinds {
			if idx.Type == k {
				allowed = true
				break
			}
		}
		if allowed {
			b.Indexes = append(b.Indexes, idx)
		}
	}
	out := g.compileSchemaIndexesPG(b)
	b.Indexes = all
	return out
}

func (g *PostgresGrammar) CompileAdd(b *Blueprint) []string {
	table := g.WrapTable(b.Table)
	out := make([]string, 0, len(b.Columns)+len(b.Indexes))
	for _, c := range b.Columns {
		if c.IsChange {
			out = append(out, g.CompileChange(b, c)...)
			continue
		}
		out = append(out, "alter table "+table+" add column "+g.columnDefinitionPG(c))
	}
	out = append(out, g.compileSchemaIndexesPG(b)...)
	return out
}

// CompileChange 对齐 PostgresGrammar::compileChange：alter column ... type /
// set|drop not null / drop default / drop identity if exists。
func (g *PostgresGrammar) CompileChange(b *Blueprint, c *ColumnDefinition) []string {
	table := g.WrapTable(b.Table)
	col := g.Wrap(c.Name)
	parts := []string{
		"alter column " + col + " type " + g.getColumnTypePG(c),
	}
	if c.IsNullable {
		parts = append(parts, "alter column "+col+" drop not null")
	} else {
		parts = append(parts, "alter column "+col+" set not null")
	}
	if c.DefaultRaw != "" {
		parts = append(parts, "alter column "+col+" set default "+c.DefaultRaw)
	} else {
		parts = append(parts, "alter column "+col+" drop default")
	}
	parts = append(parts, "alter column "+col+" drop identity if exists")
	return []string{"alter table " + table + " " + strings.Join(parts, ", ")}
}

func (g *PostgresGrammar) compileSchemaIndexesPG(b *Blueprint) []string {
	out := make([]string, 0, len(b.Indexes))
	table := g.WrapTable(b.Table)
	for _, idx := range b.Indexes {
		switch idx.Type {
		case "index":
			out = append(out, g.pgCreateIndex(table, idx, "", ""))
		case "unique":
			out = append(out, g.pgCreateIndex(table, idx, "unique ", ""))
		case "primary":
			out = append(out, "alter table "+table+" add primary key ("+g.columnizeNames(idx.Columns)+")")
		case "fulltext":
			out = append(out, g.pgFullTextIndex(table, idx))
		case "spatialindex":
			algorithm := idx.Algorithm
			if algorithm == "" {
				algorithm = "gist"
			}
			out = append(out, g.pgCreateIndex(table, idx, "", " using "+algorithm))
		case "vectorindex":
			algorithm := idx.Algorithm
			if algorithm == "" {
				algorithm = "hnsw"
			}
			out = append(out, g.pgCreateIndexWithOperatorClass(table, idx, algorithm))
		case "foreign":
			out = append(out, g.pgForeignKeySQL(table, idx))
		}
	}
	return out
}

// pgCreateIndex create [unique] index [name] on table [using alg] (cols)。
func (g *PostgresGrammar) pgCreateIndex(table string, idx *IndexDefinition, unique, using string) string {
	cols := g.columnizeNames(idx.Columns)
	if idx.Expression != "" {
		cols = idx.Expression
	}
	return "create " + unique + "index " + g.Wrap(idx.Name) + " on " + table + using + " (" + cols + ")"
}

// pgCreateIndexWithOperatorClass create index ... using hnsw (col operator_class)。
func (g *PostgresGrammar) pgCreateIndexWithOperatorClass(table string, idx *IndexDefinition, algorithm string) string {
	cols := make([]string, len(idx.Columns))
	for i, c := range idx.Columns {
		if idx.OperatorClass != "" {
			cols[i] = g.Wrap(c) + " " + idx.OperatorClass
		} else {
			cols[i] = g.Wrap(c)
		}
	}
	return "create index " + g.Wrap(idx.Name) + " on " + table + " using " + algorithm + " (" + strings.Join(cols, ", ") + ")"
}

// pgFullTextIndex 对应 compileFulltext：gin + to_tsvector。
func (g *PostgresGrammar) pgFullTextIndex(table string, idx *IndexDefinition) string {
	language := idx.Language
	if language == "" {
		language = "english"
	}
	cols := make([]string, len(idx.Columns))
	for i, c := range idx.Columns {
		cols[i] = fmt.Sprintf("to_tsvector(%s, %s)", g.QuoteString(language), g.Wrap(c))
	}
	return "create index " + g.Wrap(idx.Name) + " on " + table + " using gin ((" + strings.Join(cols, " || ") + "))"
}

// pgForeignKeySQL 编译外键（复合列 + on delete/on update）。
func (g *PostgresGrammar) pgForeignKeySQL(table string, idx *IndexDefinition) string {
	sql := "alter table " + table + " add constraint " + g.Wrap(idx.Name) +
		" foreign key (" + g.columnizeNames(idx.Columns) + ") references " +
		g.WrapTable(idx.OnTable) + " (" + g.columnizeNames(idx.foreignRefColumns()) + ")"
	if idx.OnDelete != "" {
		sql += " on delete " + idx.OnDelete
	}
	if idx.OnUpdate != "" {
		sql += " on update " + idx.OnUpdate
	}
	return sql
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

// CompileDropColumn 对应 compileDropColumn：drop column "a", drop column "b"。
func (g *PostgresGrammar) CompileDropColumn(b *Blueprint, columns []string) []string {
	parts := make([]string, len(columns))
	for i, c := range columns {
		parts[i] = "drop column " + g.Wrap(c)
	}
	return []string{"alter table " + g.WrapTable(b.Table) + " " + strings.Join(parts, ", ")}
}

func (g *PostgresGrammar) CompileRenameColumn(b *Blueprint, from, to string) string {
	return "alter table " + g.WrapTable(b.Table) + " rename column " + g.Wrap(from) + " to " + g.Wrap(to)
}

func (g *PostgresGrammar) CompileRename(b *Blueprint, to string) string {
	return "alter table " + g.WrapTable(b.Table) + " rename to " + g.WrapTable(to)
}

// CompileRenameIndex 对应 compileRenameIndex：alter index a rename to b。
func (g *PostgresGrammar) CompileRenameIndex(b *Blueprint, from, to string) string {
	return "alter index " + g.Wrap(from) + " rename to " + g.Wrap(to)
}

// CompileDropPrimary 对齐 compileDropPrimary：约束名为 {table}_pkey。
func (g *PostgresGrammar) CompileDropPrimary(b *Blueprint, index string) string {
	table := b.Table
	if i := strings.LastIndex(table, "."); i >= 0 {
		table = table[i+1:]
	}
	return "alter table " + g.WrapTable(b.Table) + " drop constraint " + g.Wrap(table+"_pkey")
}

func (g *PostgresGrammar) CompileDropUnique(b *Blueprint, index string) string {
	return "alter table " + g.WrapTable(b.Table) + " drop constraint " + g.Wrap(index)
}

func (g *PostgresGrammar) CompileDropIndex(b *Blueprint, index string) string {
	return "drop index " + g.Wrap(index)
}

func (g *PostgresGrammar) CompileDropFullText(b *Blueprint, index string) string {
	return g.CompileDropIndex(b, index)
}

func (g *PostgresGrammar) CompileDropSpatialIndex(b *Blueprint, index string) string {
	return g.CompileDropIndex(b, index)
}

func (g *PostgresGrammar) CompileDropVectorIndex(b *Blueprint, index string) string {
	return g.CompileDropIndex(b, index)
}

func (g *PostgresGrammar) CompileDropForeign(b *Blueprint, index string) string {
	return "alter table " + g.WrapTable(b.Table) + " drop constraint " + g.Wrap(index)
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

func (g *PostgresGrammar) CompileFullText(b *Blueprint, idx *IndexDefinition) string {
	return g.compileSchemaIndexesPG(b)[0]
}

func (g *PostgresGrammar) CompileSpatialIndex(b *Blueprint, idx *IndexDefinition) string {
	return g.compileSchemaIndexesPG(b)[0]
}

func (g *PostgresGrammar) CompileVectorIndex(b *Blueprint, idx *IndexDefinition) string {
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
