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
	g.compileFromFn = g.compileFrom
	g.compileInsertFn = g.compileInsert
	g.compileInsertGetIdFn = g.compileInsertGetId
	g.compileColumnsFn = g.compileColumns
	g.compileLimitFn = g.compileLimit
	g.compileOffsetFn = g.compileOffset

	return g
}

// CompileSelect 对应 Laravel SqlServerGrammar::compileSelect：
// 有 offset 而无 order by 时追加 order by (SELECT 0) 兜底（SQL Server 的
// offset 必须 order by）。
func (g *SqlServerGrammar) CompileSelect(q *query.Query) string {
	if q.Offset != nil && *q.Offset > 0 && len(q.Orders) == 0 {
		q.Orders = append(q.Orders, &query.Order{Sql: NewExpression("(SELECT 0)")})
	}
	return g.QueryGrammar.CompileSelect(q)
}

// CompileExists 对应 Laravel SqlServerGrammar::compileExists：
// clone 后 selectRaw('1 [exists]')->limit(1) → select top 1 1 [exists]。
func (g *SqlServerGrammar) CompileExists(q *query.Query) string {
	exists := *q
	exists.Columns = []any{NewExpression("1 [exists]")}
	if len(exists.Unions) > 0 {
		one := int64(1)
		exists.UnionLimit = &one
	} else {
		one := int64(1)
		exists.Limit = &one
	}
	return g.CompileSelect(&exists)
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

// compileFrom 对应 Laravel SqlServerGrammar::compileFrom：
// lock 拼接在 from 之后（with(rowlock,updlock,holdlock) 形式）。
func (g *SqlServerGrammar) compileFrom(q *query.Query, table any) string {
	// 直接内联基础实现（compileFromFn 钩子会再次路由回本方法）
	from := "from " + g.WrapTable(table)

	if s, ok := q.Lock.(string); ok {
		return from + " " + s
	}
	if q.Lock != nil {
		if bv, ok := q.Lock.(bool); ok && bv {
			return from + " with(rowlock,updlock,holdlock)"
		}
		return from + " with(rowlock,holdlock)"
	}
	return from
}

// compileLock 对应 Laravel SqlServerGrammar::compileLock：返回空串
// （锁已由 compileFrom 拼进 from，语句末尾不再出现 lock）。
func (g *SqlServerGrammar) compileLock(q *query.Query, value any) string {
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
