package grammar

import "github.com/thinkoner/torm/query"

// Grammar 是方言 grammar 必须实现的公开编译接口。
type Grammar interface {
	// Select
	CompileSelect(q *query.Query) string
	CompileExists(q *query.Query) string

	// Insert
	CompileInsert(q *query.Query, rows []query.Row) (string, []any)
	CompileInsertGetId(q *query.Query, rows []query.Row, sequence string) (string, []any)
	CompileInsertOrIgnore(q *query.Query, rows []query.Row) (string, []any)
	CompileInsertUsing(q *query.Query, columns []any, sql string) string
	CompileInsertOrIgnoreUsing(q *query.Query, columns []any, sql string) string
	CompileUpsert(q *query.Query, rows []query.Row, uniqueBy, update []string) (string, []any)

	// Update / Delete / Truncate
	CompileUpdate(q *query.Query, values map[string]any) string
	CompileDelete(q *query.Query) string
	CompileTruncate(q *query.Query) []TruncateStatement

	// Bindings
	PrepareBindingsForUpdate(bindings map[string][]any, values map[string]any) []any
	PrepareBindingsForDelete(bindings map[string][]any) []any

	// 标识符与表达式
	Wrap(value any) string
	WrapTable(value any) string
	Columnize(columns []any) string
	Parameterize(values []any) string
	Parameter(value any) string
	QuoteString(value any) string
	IsExpression(value any) bool
	GetValue(expression any) any

	// 方言杂项
	CompileRandom(seed any) string
	GetDateFormat() string
	SetTablePrefix(prefix string)
	GetTablePrefix() string

	// Savepoint（事务）
	CompileSavepoint(name string) string
	CompileSavepointRollBack(name string) string

	// 内省
	SubstituteBindingsIntoRawSql(sql string, bindings []any) string
}
