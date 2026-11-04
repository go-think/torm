package torm

import (
	"strings"

	"github.com/thinkoner/torm/grammar"
)

// SchemaBuilder 对应 Laravel 的 Schema 门面（挂在具名连接上）。
type SchemaBuilder struct {
	conn   *Connection
	prefix string
}

// Schema 返回连接的 Schema 构建器。
func (c *Connection) Schema() *SchemaBuilder {
	return &SchemaBuilder{conn: c, prefix: c.tablePrefix}
}

// GetSchemaGrammar 按驱动返回 DDL 编译器（未知驱动按 MySQL 处理）。
func (c *Connection) GetSchemaGrammar() grammar.SchemaGrammar {
	switch strings.ToLower(c.driver) {
	case "postgres", "postgresql", "pgsql":
		return grammar.NewPostgresGrammar()
	default:
		return grammar.NewMySqlGrammar()
	}
}

// Create 建表：CreateTable("users", func(t *grammar.Blueprint) {...})。
func (s *SchemaBuilder) CreateTable(table string, fn func(t *grammar.Blueprint)) error {
	g := s.conn.GetSchemaGrammar()
	g.SetTablePrefix(s.prefix)
	b := grammar.NewBlueprint(table)
	b.Create = true
	fn(b)
	for _, stmt := range b.ToSql(g) {
		if err := s.conn.Statement(stmt); err != nil {
			return err
		}
	}
	return nil
}

// Table 修改表。
func (s *SchemaBuilder) AlterTable(table string, fn func(t *grammar.Blueprint)) error {
	g := s.conn.GetSchemaGrammar()
	g.SetTablePrefix(s.prefix)
	b := grammar.NewBlueprint(table)
	fn(b)
	for _, stmt := range b.ToSql(g) {
		if err := s.conn.Statement(stmt); err != nil {
			return err
		}
	}
	return nil
}

// Drop 删表。
func (s *SchemaBuilder) Drop(table string) error {
	g := s.conn.GetSchemaGrammar()
	g.SetTablePrefix(s.prefix)
	return s.conn.Statement(g.CompileDrop(table))
}

// DropIfExists 条件删表。
func (s *SchemaBuilder) DropIfExists(table string) error {
	g := s.conn.GetSchemaGrammar()
	g.SetTablePrefix(s.prefix)
	return s.conn.Statement(g.CompileDropIfExists(table))
}

// HasTable 表是否存在。
func (s *SchemaBuilder) HasTable(table string) (bool, error) {
	g := s.conn.GetSchemaGrammar()
	sql, bindings := g.CompileTableExists(s.databaseName(), s.prefix+table)
	rows, err := s.conn.SelectMaps(sql, bindings)
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

// GetColumnListing 列名列表。
func (s *SchemaBuilder) GetColumnListing(table string) ([]string, error) {
	g := s.conn.GetSchemaGrammar()
	sql, bindings := g.CompileColumnListing(s.databaseName(), s.prefix+table)
	rows, err := s.conn.SelectMaps(sql, bindings)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if v, ok := row["column_name"]; ok {
			if str, ok := v.(string); ok {
				out = append(out, str)
			}
		}
	}
	return out, nil
}

// databaseName 从 DSN 解析库名（MySQL DSN：/dbname?params；PG：dbname= 或 /dbname）。
func (s *SchemaBuilder) databaseName() string {
	dsn := s.conn.Dsn()
	if i := strings.Index(dsn, "?"); i >= 0 {
		dsn = dsn[:i]
	}
	if i := strings.LastIndex(dsn, "/"); i >= 0 {
		dsn = dsn[i+1:]
	}
	if dsn == "" {
		return "public"
	}
	return dsn
}

// SetDsn 内部接线（Open 后由 Connection 注入）。
func (c *Connection) Dsn() string { return c.dsn }
