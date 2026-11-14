package grammar

import (
	"regexp"
	"strings"

	"github.com/go-think/torm/query"
)

// SqlServerGrammar 为 SQL Server（T-SQL）方言查询编译器的常用子集。
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
	g.wrapUnionFn = g.wrapUnion
	g.compileRowNumberFn = g.compileRowNumber
	g.whereNullSafeEqualsFn = g.whereNullSafeEquals
	g.whereBitwiseFn = g.whereBitwise
	g.compileHavingBitwiseFn = g.compileHavingBitwise
	g.whereDateFn = g.whereDate
	g.whereTimeFn = g.whereTime
	g.compileJsonContainsFn = g.compileJsonContains
	g.compileJsonContainsKeyFn = g.compileJsonContainsKey
	g.compileJsonLengthFn = g.compileJsonLength
	g.compileJoinLateralFn = g.compileJoinLateral
	g.compileUpsertFn = g.compileUpsert
	g.compileUpdateWithoutJoinsFn = g.compileUpdateWithoutJoins
	g.compileUpdateWithJoinsFn = g.compileUpdateWithJoins
	g.compileDeleteWithoutJoinsFn = g.compileDeleteWithoutJoins
	g.compileJsonValueCastFn = g.compileJsonValueCast

	// JSON 选择器（wrapJsonSelector 的 json_query 实现；
	// 此前未注册 → 任何 JSON 选择器列都会 panic）
	g.BaseGrammar.wrapJsonSelectorFn = g.wrapJsonSelector
	g.BaseGrammar.wrapJsonBoolSelFn = g.wrapJsonBooleanSelector
	g.BaseGrammar.wrapJsonBoolValueFn = g.wrapJsonBooleanValue

	return g
}

// wrapJsonSelector 对应 SqlServerGrammar::wrapJsonSelector：
// JSON_PATH 函数（json_query(field, 'lax $.a."b"')）。
func (g *SqlServerGrammar) wrapJsonSelector(value string) string {
	field, path := g.wrapJsonFieldAndPath(value)
	return "json_query(" + field + path + ")"
}

// wrapJsonBooleanSelector 对应 SqlServerGrammar::wrapJsonBooleanSelector：
// coalesce(json_query(...), 'false')——布尔上下文缺省 false。
func (g *SqlServerGrammar) wrapJsonBooleanSelector(value string) string {
	field, path := g.wrapJsonFieldAndPath(value)
	return "coalesce(json_query(" + field + path + "), 'false')"
}

// wrapJsonBooleanValue 对应 SqlServerGrammar::wrapJsonBooleanValue。
func (g *SqlServerGrammar) wrapJsonBooleanValue(value string) string {
	return "'" + value + "'::nvarchar(10)"
}

// wrapJsonPathSegment 对应 SqlServerGrammar::wrapJsonPathSegment：
// '."segment"' 片段（路径段双引号包裹，整数下标同样加引号处理）。
func (g *SqlServerGrammar) wrapJsonPathSegment(segment string) string {
	return `'."` + segment + `"'.'`
}

// CompileSelect 编译 select 语句：
// 有 offset 而无 order by 时追加 order by (SELECT 0) 兜底（SQL Server 的
// offset 必须 order by）。
func (g *SqlServerGrammar) CompileSelect(q *query.Query) string {
	if q.Offset != nil && *q.Offset > 0 && len(q.Orders) == 0 {
		q.Orders = append(q.Orders, &query.Order{Sql: NewExpression("(SELECT 0)")})
	}
	return g.QueryGrammar.CompileSelect(q)
}

// CompileExists 编译 exists 检查：
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

// compileFrom 编译 from 段：
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

// compileLock 返回空串：
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

// whereDate/whereTime：T-SQL 无 date()/time() 函数，使用 cast(col as date/time)。
func (g *SqlServerGrammar) whereDate(q *query.Query, w *query.Where) string {
	return "cast(" + g.Wrap(w.Column) + " as date) " + w.Operator + " " + g.Parameter(w.Value)
}

func (g *SqlServerGrammar) whereTime(q *query.Query, w *query.Where) string {
	return "cast(" + g.Wrap(w.Column) + " as time) " + w.Operator + " " + g.Parameter(w.Value)
}

// ---- JSON 编译（openjson，对应 SqlServerGrammar.php:195-260） ----

// Operators 对应 SqlServerGrammar::$operators（SqlServerGrammar.php:18-23）。
func (g *SqlServerGrammar) Operators() []string {
	return []string{
		"=", "<", ">", "<=", ">=", "!<", "!>", "<>", "!=",
		"like", "not like", "ilike",
		"&", "&=", "|", "|=", "^", "^=",
	}
}

func (g *SqlServerGrammar) compileJsonContains(column, value string) string {
	field, path := g.wrapJsonFieldAndPath(column)
	return value + " in (select [value] from openjson(" + field + path + "))"
}

func (g *SqlServerGrammar) compileJsonContainsKey(column string) string {
	segments := strings.Split(column, "->")
	lastSegment := segments[len(segments)-1]
	segments = segments[:len(segments)-1]

	key := "'" + strings.ReplaceAll(lastSegment, "'", "''") + "'"
	if m := sqlserverArrayKeyPattern.FindStringSubmatch(lastSegment); m != nil {
		segments = append(segments, strings.TrimSuffix(lastSegment, m[0]))
		key = m[1]
	}

	field, path := g.wrapJsonFieldAndPath(strings.Join(segments, "->"))
	return key + " in (select [key] from openjson(" + field + path + "))"
}

func (g *SqlServerGrammar) compileJsonLength(column, operator, value string) string {
	field, path := g.wrapJsonFieldAndPath(column)
	return "(select count(*) from openjson(" + field + path + ")) " + operator + " " + value
}

var sqlserverArrayKeyPattern = regexp.MustCompile(`\[([0-9]+)\]$`)

// PrepareBindingForJsonContains 对应 SqlServerGrammar.php:208：bool → json 字面量。
func (g *SqlServerGrammar) PrepareBindingForJsonContains(binding any) any {
	if b, ok := binding.(bool); ok {
		if b {
			return "true"
		}
		return "false"
	}
	return binding
}

// compileColumns 有 limit 且无 offset 时输出 select top(n)。
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

// ---- R3-A5 方言残余 ----

// GetDateFormat 对应 SqlServerGrammar::getDateFormat（毫秒精度）。
func (g *SqlServerGrammar) GetDateFormat() string {
	return "Y-m-d H:i:s.v"
}

// wrapUnion 对应 SqlServerGrammar::wrapUnion。
func (g *SqlServerGrammar) wrapUnion(sql string) string {
	return "select * from (" + sql + ") as " + g.WrapTable("temp_table")
}

// compileRowNumber 对应 SqlServerGrammar::compileRowNumber：
// 空 orders 兜底 order by (select 0)。
func (g *SqlServerGrammar) compileRowNumber(partition, orders string) string {
	if strings.TrimSpace(orders) == "" {
		orders = "order by (select 0)"
	}
	over := strings.TrimSpace("partition by " + g.Wrap(partition) + " " + orders)
	return ", row_number() over (" + over + ") as " + g.Wrap("group_row")
}

// whereNullSafeEquals 对应 SqlServerGrammar::whereNullSafeEquals：
// exists (select col intersect select ?)。
func (g *SqlServerGrammar) whereNullSafeEquals(q *query.Query, w *query.Where) string {
	return "exists (select " + g.Wrap(w.Column) + " intersect select " + g.Parameter(w.Value) + ")"
}

// whereBitwise 对应 SqlServerGrammar::whereBitwise：(col op ?) != 0。
func (g *SqlServerGrammar) whereBitwise(q *query.Query, w *query.Where) string {
	operator := escapeOperator(w.Operator)
	return "(" + g.Wrap(w.Column) + " " + operator + " " + g.Parameter(w.Value) + ") != 0"
}

// compileHavingBitwise 对应 SqlServerGrammar::compileHavingBitwise。
func (g *SqlServerGrammar) compileHavingBitwise(h *query.Where) string {
	return "(" + g.Wrap(h.Column) + " " + h.Operator + " " + g.Parameter(h.Value) + ") != 0"
}

// BitwiseOperators 对应 SqlServerGrammar::$operators 中的位运算符。
func (g *SqlServerGrammar) BitwiseOperators() []string {
	return []string{"&", "&=", "|", "|=", "^", "^="}
}

// compileJsonValueCast 对应 SqlServerGrammar::compileJsonValueCast。
func (g *SqlServerGrammar) compileJsonValueCast(value string) string {
	return "json_query(" + value + ")"
}

// compileUpdateWithoutJoins 对应 SqlServerGrammar::compileUpdateWithoutJoins：
// 仅 limit（无 offset）时改写为 update top(n)。
func (g *SqlServerGrammar) compileUpdateWithoutJoins(q *query.Query, table, columns, where string) string {
	sql := "update " + table + " set " + columns + " " + where
	if q.Limit != nil && *q.Limit > 0 && (q.Offset == nil || *q.Offset <= 0) {
		sql = replaceFirst("update", "update top ("+intToString(*q.Limit)+")", sql)
	}
	return strings.TrimSpace(sql)
}

// compileUpdateWithJoins 对应 SqlServerGrammar::compileUpdateWithJoins：
// update alias set ... from table joins where。
func (g *SqlServerGrammar) compileUpdateWithJoins(q *query.Query, table, columns, where string) string {
	alias := table
	if i := strings.LastIndex(strings.ToLower(table), " as "); i >= 0 {
		alias = table[i+4:]
	}
	joins := g.compileJoins(q, q.Joins)
	return strings.TrimSpace("update " + alias + " set " + columns + " from " + table + " " + joins + " " + where)
}

// compileDeleteWithoutJoins 对应 SqlServerGrammar::compileDeleteWithoutJoins：
// 仅 limit（无 offset）时改写为 delete top(n)。
func (g *SqlServerGrammar) compileDeleteWithoutJoins(q *query.Query, table, where string) string {
	sql := "delete from " + table + " " + where
	if q.Limit != nil && *q.Limit > 0 && (q.Offset == nil || *q.Offset <= 0) {
		sql = replaceFirst("delete", "delete top ("+intToString(*q.Limit)+")", sql)
	}
	return strings.TrimSpace(sql)
}

// compileUpsert 对应 SqlServerGrammar::compileUpsert：MERGE 语句。
func (g *SqlServerGrammar) compileUpsert(q *query.Query, rows []query.Row, uniqueBy, update []string) (string, []any) {
	if len(rows) == 0 {
		rows = []query.Row{{}}
	}
	columns := rows[0].Columns()
	wrapped := make([]string, 0, len(columns))
	for _, col := range columns {
		wrapped = append(wrapped, g.Wrap(col))
	}
	columnList := strings.Join(wrapped, ", ")

	sql := "merge " + g.WrapTable(q.From) + " "

	var bindings []any
	parameters := make([]string, 0, len(rows))
	for _, row := range rows {
		rowParts := make([]string, 0, len(columns))
		for _, col := range columns {
			v, _ := row.Value(col)
			if g.IsExpression(v) {
				rowParts = append(rowParts, g.Parameter(v))
				continue
			}
			bindings = append(bindings, v)
			rowParts = append(rowParts, g.Parameter(v))
		}
		parameters = append(parameters, "("+strings.Join(rowParts, ", ")+")")
	}

	sql += "using (values " + strings.Join(parameters, ", ") + ") " + g.WrapTable("merge_source") + " (" + columnList + ") "

	on := make([]string, 0, len(uniqueBy))
	for _, column := range uniqueBy {
		on = append(on, g.Wrap("merge_source."+column)+" = "+g.Wrap(fmtString(q.From)+"."+column))
	}
	sql += "on " + strings.Join(on, " and ") + " "

	if len(update) > 0 {
		updates := make([]string, 0, len(update))
		for _, column := range update {
			updates = append(updates, g.Wrap(column)+" = "+g.Wrap("merge_source."+column))
		}
		sql += "when matched then update set " + strings.Join(updates, ", ") + " "
	}

	sql += "when not matched then insert (" + columnList + ") values (" + columnList + ");"

	return sql, bindings
}

// PrepareBindingsForUpdate 对应 SqlServerGrammar::prepareBindingsForUpdate：
// values 在前，除 select 外全部绑定在后。
func (g *SqlServerGrammar) PrepareBindingsForUpdate(bindings map[string][]any, values map[string]any) []any {
	out := make([]any, 0, len(values)+8)
	for _, key := range sortedKeys(values) {
		v := values[key]
		if g.IsExpression(v) {
			continue
		}
		out = append(out, flattenBinding(v))
	}
	for _, segment := range BindingSegments {
		if segment == "select" {
			continue
		}
		for _, v := range bindings[segment] {
			out = append(out, flattenBinding(v))
		}
	}
	return out
}

// compileJoinLateral 对应 SqlServerGrammar::compileJoinLateral：
// left → outer apply，其余 → cross apply。
func (g *SqlServerGrammar) compileJoinLateral(expr, joinType string) string {
	if joinType == "left" {
		return strings.TrimSpace("outer apply " + expr)
	}
	return strings.TrimSpace("cross apply " + expr)
}

// CompileSavepoint 对应 SqlServerGrammar::compileSavepoint。
func (g *SqlServerGrammar) CompileSavepoint(name string) string {
	return "SAVE TRANSACTION " + name
}

// CompileSavepointRollBack 对应 SqlServerGrammar::compileSavepointRollBack。
func (g *SqlServerGrammar) CompileSavepointRollBack(name string) string {
	return "ROLLBACK TRANSACTION " + name
}
