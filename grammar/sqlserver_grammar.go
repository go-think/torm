package grammar

import (
	"strings"

	"github.com/thinkoner/torm/query"
)

// SqlServerGrammar 对应 Illuminate\Database\Query\Grammars\SqlServerGrammar 的常用子集。
// 覆盖：[标识符] wrap、insertGetId（output inserted）、随机 NEWID()、日期函数、
// insert 空值 default values、truncate、lock（rowlock/updlock/holdlock）。
// 未覆盖：top(n) limit/offset 重写、groupLimit。

type SqlServerGrammar struct {
	QueryGrammar
}

func NewSqlServerGrammar() *SqlServerGrammar {
	g := &SqlServerGrammar{}
	g.BaseGrammar.wrapValueFn = sqlserverWrapValue

	// SQL Server：offset 先于 limit（offset N rows fetch next M rows only）
	g.SetSelectComponents([]string{
		"aggregate",
		"columns",
		"from",
		"indexHint",
		"joins",
		"wheres",
		"groups",
		"havings",
		"orders",
		"offset",
		"limit",
		"lock",
	})

	g.compileRandomFn = g.compileRandom
	g.compileLockFn = g.compileLock
	g.compileInsertFn = g.compileInsert
	g.compileInsertGetIdFn = g.compileInsertGetId
	g.compileColumnsFn = g.compileColumns
	g.compileLimitFn = g.compileLimit
	g.compileOffsetFn = g.compileOffset

	return g
}

func sqlserverWrapValue(value string) string {
	if value == "*" {
		return value
	}
	return "[" + strings.ReplaceAll(value, "]", "]]") + "]"
}

func (g *SqlServerGrammar) compileRandom(seed any) string {
	return "NEWID()"
}

func (g *SqlServerGrammar) compileLock(q *query.Query, value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	if bv, ok := value.(bool); ok {
		if bv {
			return "with (rowlock,updlock,holdlock)"
		}
		return "with (rowlock,holdlock)"
	}
	return ""
}

// compileInsert SQL Server 空值 insert 用 default values。
func (g *SqlServerGrammar) compileInsert(q *query.Query, rows []query.Row) (string, []any) {
	if len(rows) == 0 {
		return "insert into " + g.WrapTable(q.From) + " default values", nil
	}
	return g.QueryGrammar.baseCompileInsert(q, rows)
}

// compileInsertGetId 对应 SQL Server 的 output inserted。
func (g *SqlServerGrammar) compileInsertGetId(q *query.Query, rows []query.Row, sequence string) (string, []any) {
	if sequence == "" {
		sequence = "id"
	}
	base, bindings := g.QueryGrammar.baseCompileInsert(q, rows)
	// insert into [x] (...) output inserted.[id] values (...)
	parts := strings.SplitN(base, " values ", 2)
	if len(parts) != 2 {
		return base, bindings
	}
	return parts[0] + " output inserted." + g.wrapValue(sequence) + " values " + parts[1], bindings
}

func (g *SqlServerGrammar) dateBasedWhere(kind string, q *query.Query, w *query.Where) string {
	value := g.Parameter(w.Value)
	return kind + "(" + g.Wrap(w.Column) + ") " + w.Operator + " " + value
}

// compileColumns 有 limit 且无 offset 时输出 select top(n)（对齐 Laravel SqlServerGrammar）。
func (g *SqlServerGrammar) compileColumns(q *query.Query, columns []any) string {
	if q.Aggregate != nil {
		return ""
	}
	sel := "select "
	if isTrueDistinct(q.Distinct) {
		sel = "select distinct "
	}
	if q.Limit != nil && *q.Limit > 0 && (q.Offset == nil || *q.Offset <= 0) {
		sel = sel + "top " + intToString(*q.Limit) + " "
	}
	return sel + g.Columnize(columns)
}

// compileLimit：带 offset 时输出 fetch next。
func (g *SqlServerGrammar) compileLimit(q *query.Query, limit int64) string {
	if limit > 0 && q.Offset != nil && *q.Offset > 0 {
		return "fetch next " + intToString(limit) + " rows only"
	}
	return ""
}

// compileOffset：offset N rows。
func (g *SqlServerGrammar) compileOffset(q *query.Query, offset int64) string {
	if offset > 0 {
		return "offset " + intToString(offset) + " rows"
	}
	return ""
}
