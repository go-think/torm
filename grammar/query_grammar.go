package grammar

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-think/torm/query"
)

// dialectHooks 汇集方言差异点；nil 时使用 QueryGrammar 的基础实现。
// 基础实现与 Laravel Query\Grammars\Grammar 的行为一致。
type dialectHooks struct {
	// 标识符 / JSON（多数已挂在 BaseGrammar 上）
	compileIndexHintFn func(q *query.Query, hint *query.IndexHint) string

	// where 家族差异
	whereBasicFn          func(q *query.Query, w *query.Where) string
	whereDateFn           func(q *query.Query, w *query.Where) string
	whereTimeFn           func(q *query.Query, w *query.Where) string
	whereNullFn           func(q *query.Query, w *query.Where) string
	whereNotNullFn        func(q *query.Query, w *query.Where) string
	whereLikeFn           func(q *query.Query, w *query.Where) string
	whereNullSafeEqualsFn func(q *query.Query, w *query.Where) string
	whereFullTextFn       func(q *query.Query, w *query.Where) string
	whereBitwiseFn        func(q *query.Query, w *query.Where) string

	// JSON 操作差异
	compileJsonContainsFn    func(column, value string) string
	compileJsonOverlapsFn    func(column, value string) string
	compileJsonContainsKeyFn func(column string) string
	compileJsonLengthFn      func(column, operator, value string) string
	compileJsonUpdateColFn   func(key string, value any) string

	// 杂项
	dateBasedWhereFn    func(kind string, q *query.Query, w *query.Where) string
	compileLimitFn      func(q *query.Query, limit int64) string
	compileOffsetFn     func(q *query.Query, offset int64) string
	compileLockFn       func(q *query.Query, value any) string
	compileRandomFn     func(seed any) string
	compileColumnsFn    func(q *query.Query, columns []any) string
	compileFromFn       func(q *query.Query, table any) string
	compileDistinctOn   func(columns []any) string
	wrapUnionFn         func(sql string) string
	compileRowNumberFn  func(partition, orders string) string
	compileJoinLateralFn func(expr, joinType string) string
	// supportsStraightJoinsFn：nil = 不支持（straight_join 抛错）
	supportsStraightJoinsFn func() bool
	// compileTimeoutFn：select 语句超时提示改写（MySQL MAX_EXECUTION_TIME）
	compileTimeoutFn func(sql string, seconds int64) string
	// compileJsonValueCastFn：JSON 值 cast 表达式
	compileJsonValueCastFn func(value string) string

	// 写入族差异
	compileInsertFn              func(q *query.Query, rows []query.Row) (string, []any)
	compileInsertGetIdFn         func(q *query.Query, rows []query.Row, sequence string) (string, []any)
	compileInsertOrIgnoreFn      func(q *query.Query, rows []query.Row) (string, []any)
	compileInsertUsingFn         func(q *query.Query, columns []any, sql string) string
	compileInsertOrIgnoreUsingFn func(q *query.Query, columns []any, sql string) string
	compileUpsertFn              func(q *query.Query, rows []query.Row, uniqueBy, update []string) (string, []any)
	compileUpdateFn              func(q *query.Query, values map[string]any) string
	compileUpdateColumnsFn       func(q *query.Query, values map[string]any) string
	compileUpdateWithoutJoinsFn  func(q *query.Query, table, columns, where string) string
	compileUpdateWithJoinsFn     func(q *query.Query, table, columns, where string) string
	compileDeleteWithoutJoinsFn  func(q *query.Query, table, where string) string
	compileDeleteWithJoinsFn     func(q *query.Query, table, where string) string
	compileTruncateFn            func(q *query.Query) []TruncateStatement
	// having 位运算差异（PG ::bool / SqlServer != 0）
	compileHavingBitwiseFn func(h *query.Where) string
}

// QueryGrammar 对应 Illuminate\Database\Query\Grammars\Grammar（共享编译逻辑）。
type QueryGrammar struct {
	BaseGrammar
	dialectHooks
	// selectComponentsOverride 方言可覆盖的组件顺序（如 SQL Server offset 先于 limit）
	selectComponentsOverride []string
}

// SetSelectComponents 覆盖方言的编译组件顺序。
func (g *QueryGrammar) SetSelectComponents(components []string) {
	g.selectComponentsOverride = components
}

// TruncateStatement：一条 truncate 语句及其绑定（Laravel compileTruncate 返回 map）。
type TruncateStatement struct {
	Sql      string
	Bindings []any
}

var selectComponents = []string{
	"aggregate",
	"columns",
	"from",
	"indexHint",
	"joins",
	"wheres",
	"groups",
	"havings",
	"orders",
	"limit",
	"offset",
	"lock",
}

// NewQueryGrammar 构造基础方言 grammar（双引号，SQL 标准风格）。
func NewQueryGrammar() *QueryGrammar {
	return &QueryGrammar{}
}

// CompileSelect 编译 select 语句。
func (g *QueryGrammar) CompileSelect(q *query.Query) string {
	if (len(q.Unions) > 0 || len(q.Havings) > 0) && q.Aggregate != nil {
		return g.compileUnionAggregate(q)
	}

	if q.GroupLimit != nil {
		if q.Columns == nil {
			q.Columns = []any{"*"}
		}
		return g.compileGroupLimit(q)
	}

	original := q.Columns
	if q.Columns == nil {
		q.Columns = []any{"*"}
	}

	sql := strings.TrimSpace(g.concatenateNamed(g.compileComponents(q)))

	if len(q.Unions) > 0 {
		sql = g.wrapUnion(sql) + " " + g.compileUnions(q)
	}

	q.Columns = original

	// 方言超时提示（MySQL MAX_EXECUTION_TIME）
	if q.Timeout != nil && g.compileTimeoutFn != nil {
		sql = g.compileTimeoutFn(sql, *q.Timeout)
	}

	return sql
}

// namedComponent 带组件名的编译片段（groupLimit 需要定位/剔除特定组件）。
type namedComponent struct {
	name string
	sql  string
}

func (g *QueryGrammar) compileComponents(q *query.Query) []namedComponent {
	components := selectComponents
	if g.selectComponentsOverride != nil {
		components = g.selectComponentsOverride
	}
	sql := make([]namedComponent, 0, len(components))
	for _, component := range components {
		switch component {
		case "aggregate":
			if q.Aggregate != nil {
				sql = append(sql, namedComponent{component, g.compileAggregate(q, q.Aggregate)})
			}
		case "columns":
			if len(q.Columns) > 0 {
				sql = append(sql, namedComponent{component, g.compileColumns(q, q.Columns)})
			}
		case "from":
			if q.From != nil {
				sql = append(sql, namedComponent{component, g.compileFrom(q, q.From)})
			}
		case "indexHint":
			if q.IndexHint != nil {
				sql = append(sql, namedComponent{component, g.compileIndexHint(q, q.IndexHint)})
			}
		case "joins":
			if len(q.Joins) > 0 {
				sql = append(sql, namedComponent{component, g.compileJoins(q, q.Joins)})
			}
		case "wheres":
			if len(q.Wheres) > 0 {
				sql = append(sql, namedComponent{component, g.compileWheres(q)})
			}
		case "groups":
			if len(q.Groups) > 0 {
				sql = append(sql, namedComponent{component, g.compileGroups(q, q.Groups)})
			}
		case "havings":
			if len(q.Havings) > 0 {
				sql = append(sql, namedComponent{component, g.compileHavings(q)})
			}
		case "orders":
			if len(q.Orders) > 0 {
				sql = append(sql, namedComponent{component, g.compileOrders(q, q.Orders)})
			}
		case "limit":
			if q.Limit != nil {
				sql = append(sql, namedComponent{component, g.compileLimit(q, *q.Limit)})
			}
		case "offset":
			if q.Offset != nil {
				sql = append(sql, namedComponent{component, g.compileOffset(q, *q.Offset)})
			}
		case "lock":
			if q.Lock != nil {
				sql = append(sql, namedComponent{component, g.compileLock(q, q.Lock)})
			}
		}
	}
	return sql
}

// concatenateNamed 拼接带名组件的 SQL 部分。
func (g *QueryGrammar) concatenateNamed(components []namedComponent) string {
	sqls := make([]string, 0, len(components))
	for _, c := range components {
		sqls = append(sqls, c.sql)
	}
	return g.concatenate(sqls)
}

// compileGroupLimit 对应 Laravel compileGroupLimit（row_number 分组限额）。
func (g *QueryGrammar) compileGroupLimit(q *query.Query) string {
	limit := q.GroupLimit.Value
	var offsetPtr *int64
	if q.Offset != nil {
		offset := *q.Offset
		limit += offset
		q.Offset = nil
		offsetPtr = &offset
	}

	components := g.compileComponents(q)

	var ordersSQL string
	for _, c := range components {
		if c.name == "orders" {
			ordersSQL = c.sql
		}
	}
	for i := range components {
		if components[i].name == "columns" {
			components[i].sql += g.compileRowNumber(q.GroupLimit.Column, ordersSQL)
		}
	}

	// 剔除 orders 组件
	kept := make([]namedComponent, 0, len(components))
	for _, c := range components {
		if c.name == "orders" {
			continue
		}
		kept = append(kept, c)
	}

	table := g.Wrap("laravel_table")
	row := g.Wrap("laravel_row")

	sql := strings.TrimSpace(g.concatenateNamed(kept))

	sql = "select * from (" + sql + ") as " + table + " where " + row + " <= " + intToString(limit)

	if offsetPtr != nil {
		sql += " and " + row + " > " + intToString(*offsetPtr)
	}

	return sql + " order by " + row
}

// compileRowNumber 对应 Laravel compileRowNumber。
func (g *QueryGrammar) compileRowNumber(partition, orders string) string {
	if g.compileRowNumberFn != nil {
		return g.compileRowNumberFn(partition, orders)
	}
	over := strings.TrimSpace("partition by " + g.Wrap(partition) + " " + orders)
	return ", row_number() over (" + over + ") as " + g.Wrap("laravel_row")
}

func (g *QueryGrammar) compileAggregate(q *query.Query, agg *query.Aggregate) string {
	column := g.Columnize(agg.Columns)

	if distinctColumns, ok := q.Distinct.([]any); ok {
		column = "distinct " + g.Columnize(distinctColumns)
	} else if isTrueDistinct(q.Distinct) && column != "*" {
		column = "distinct " + column
	}

	return "select " + agg.Function + "(" + column + ") as " + g.Wrap("aggregate")
}

func (g *QueryGrammar) compileColumns(q *query.Query, columns []any) string {
	if g.compileColumnsFn != nil {
		return g.compileColumnsFn(q, columns)
	}
	if q.Aggregate != nil {
		return ""
	}

	sel := "select "
	if isTrueDistinct(q.Distinct) {
		sel = "select distinct "
	}

	return sel + g.Columnize(columns)
}

func (g *QueryGrammar) compileFrom(q *query.Query, table any) string {
	if g.compileFromFn != nil {
		return g.compileFromFn(q, table)
	}
	return "from " + g.WrapTable(table)
}

func (g *QueryGrammar) compileJoins(q *query.Query, joins []*query.Join) string {
	segments := make([]string, 0, len(joins))
	for _, join := range joins {
		table := g.WrapTable(join.Table)
		if len(join.Joins) > 0 {
			nested := g.compileJoins(q, join.Joins)
			table = "(" + table + " " + nested + ")"
		}
		// lateral join（Laravel JoinLateralClause）
		if join.Lateral {
			if g.compileJoinLateralFn == nil {
				panic(ErrUnsupported{"This database engine does not support lateral joins."})
			}
			segments = append(segments, g.compileJoinLateralFn(table, join.Type))
			continue
		}
		joinWord := " join"
		if join.Type == "straight_join" {
			if g.supportsStraightJoinsFn == nil || !g.supportsStraightJoinsFn() {
				panic(ErrUnsupported{"This database engine does not support straight joins."})
			}
			joinWord = ""
		}
		segments = append(segments, strings.TrimSpace(join.Type+joinWord+" "+table+" "+g.compileWheres(joinClauseQuery(join))))
	}
	return strings.Join(segments, " ")
}

// joinClauseQuery 把 join 自身的 wheres 包装为标记为 JoinClause 的 Query 以编译 on 子句。
func joinClauseQuery(j *query.Join) *query.Query {
	return &query.Query{
		JoinClause: true,
		Wheres:     j.Wheres,
	}
}

func (g *QueryGrammar) compileWheres(q *query.Query) string {
	if len(q.Wheres) == 0 {
		return ""
	}
	if sql := g.compileWheresToArray(q); len(sql) > 0 {
		return g.concatenateWhereClauses(q, sql)
	}
	return ""
}

func (g *QueryGrammar) compileWheresToArray(q *query.Query) []string {
	sql := make([]string, 0, len(q.Wheres))
	for _, w := range q.Wheres {
		sql = append(sql, w.Boolean+" "+g.compileWhereByType(q, w))
	}
	return sql
}

func (g *QueryGrammar) compileWhereByType(q *query.Query, w *query.Where) string {
	switch w.Type {
	case "Raw":
		return fmtString(g.GetValue(w.Sql))
	case "Basic":
		if g.whereBasicFn != nil {
			return g.whereBasicFn(q, w)
		}
		return g.whereBasic(q, w)
	case "Bitwise":
		if g.whereBitwiseFn != nil {
			return g.whereBitwiseFn(q, w)
		}
		return g.whereBasic(q, w)
	case "Expression":
		return fmtString(g.GetValue(w.Column))
	case "Binary":
		panic(ErrUnsupported{"This database engine does not support binary comparison operations."})
	case "Like":
		if g.whereLikeFn != nil {
			return g.whereLikeFn(q, w)
		}
		// 基础方言对应 Laravel Query\Grammars\Grammar::whereLike：
		// 仅 caseSensitive 时抛错，否则按 like / not like 走基础比较。
		return g.baseWhereLike(q, w)
	case "NullSafeEquals":
		if g.whereNullSafeEqualsFn != nil {
			return g.whereNullSafeEqualsFn(q, w)
		}
		return g.Wrap(w.Column) + " is not distinct from " + g.Parameter(w.Value)
	case "In":
		if w.Not {
			return g.whereNotIn(q, w)
		}
		return g.whereIn(q, w)
	case "NotIn":
		return g.whereNotIn(q, w)
	case "InRaw":
		return g.whereInRaw(q, w)
	case "NotInRaw":
		return g.whereNotInRaw(q, w)
	case "Null":
		if g.whereNullFn != nil {
			return g.whereNullFn(q, w)
		}
		return g.whereNull(q, w)
	case "NotNull":
		if g.whereNotNullFn != nil {
			return g.whereNotNullFn(q, w)
		}
		return g.whereNotNull(q, w)
	case "Between", "between":
		return g.whereBetween(q, w)
	case "BetweenColumns", "betweenColumns":
		return g.whereBetweenColumns(q, w)
	case "ValueBetween":
		return g.whereValueBetween(q, w)
	case "Date":
		if g.whereDateFn != nil {
			return g.whereDateFn(q, w)
		}
		return g.dateBasedWhere("date", q, w)
	case "Time":
		if g.whereTimeFn != nil {
			return g.whereTimeFn(q, w)
		}
		return g.dateBasedWhere("time", q, w)
	case "Day":
		return g.dateBasedWhere("day", q, w)
	case "Month":
		return g.dateBasedWhere("month", q, w)
	case "Year":
		return g.dateBasedWhere("year", q, w)
	case "Column":
		return g.whereColumn(q, w)
	case "Nested":
		return g.whereNested(q, w)
	case "Sub":
		return g.whereSub(q, w)
	case "Exists":
		if w.Not {
			return g.whereNotExists(q, w)
		}
		return g.whereExists(q, w)
	case "NotExists":
		return g.whereNotExists(q, w)
	case "RowValues":
		return g.whereRowValues(q, w)
	case "JsonBoolean":
		return g.whereJsonBoolean(q, w)
	case "JsonContains":
		return g.whereJsonContains(q, w)
	case "JsonOverlaps":
		return g.whereJsonOverlaps(q, w)
	case "JsonContainsKey":
		return g.whereJsonContainsKey(q, w)
	case "JsonLength":
		return g.whereJsonLength(q, w)
	case "FullText":
		if g.whereFullTextFn != nil {
			return g.whereFullTextFn(q, w)
		}
		panic(ErrUnsupported{"This database engine does not support fulltext search."})
	default:
		panic(ErrUnsupported{"Unsupported where type: " + w.Type})
	}
}

func (g *QueryGrammar) concatenateWhereClauses(q *query.Query, sql []string) string {
	conjunction := "where"
	if q.JoinClause {
		conjunction = "on"
	}
	return conjunction + " " + g.removeLeadingBoolean(strings.Join(sql, " "))
}

func (g *QueryGrammar) whereBasic(q *query.Query, w *query.Where) string {
	value := g.Parameter(w.Value)
	operator := escapeOperator(w.Operator)
	return g.Wrap(w.Column) + " " + operator + " " + value
}

// baseWhereLike 对应 Laravel 基础方言的 whereLike（caseSensitive 时抛错）。
func (g *QueryGrammar) baseWhereLike(q *query.Query, w *query.Where) string {
	if w.CaseSensitive {
		panic(ErrUnsupported{"This database engine does not support case sensitive like operations."})
	}
	operator := "like"
	if w.Not {
		operator = "not like"
	}
	return g.whereBasic(q, &query.Where{Column: w.Column, Operator: operator, Value: w.Value})
}

func (g *QueryGrammar) whereColumn(q *query.Query, w *query.Where) string {
	operator := escapeOperator(w.Operator)
	return g.Wrap(w.First) + " " + operator + " " + g.Wrap(w.Second)
}

func (g *QueryGrammar) whereIn(q *query.Query, w *query.Where) string {
	if len(w.Values) > 0 {
		return g.Wrap(w.Column) + " in (" + g.Parameterize(w.Values) + ")"
	}
	return "0 = 1"
}

func (g *QueryGrammar) whereNotIn(q *query.Query, w *query.Where) string {
	if len(w.Values) > 0 {
		return g.Wrap(w.Column) + " not in (" + g.Parameterize(w.Values) + ")"
	}
	return "1 = 1"
}

func (g *QueryGrammar) whereInRaw(q *query.Query, w *query.Where) string {
	if len(w.Values) > 0 {
		return g.Wrap(w.Column) + " in (" + fmtJoin(w.Values, ", ") + ")"
	}
	return "0 = 1"
}

func (g *QueryGrammar) whereNotInRaw(q *query.Query, w *query.Where) string {
	if len(w.Values) > 0 {
		return g.Wrap(w.Column) + " not in (" + fmtJoin(w.Values, ", ") + ")"
	}
	return "1 = 1"
}

func (g *QueryGrammar) whereNull(q *query.Query, w *query.Where) string {
	return g.Wrap(w.Column) + " is null"
}

func (g *QueryGrammar) whereNotNull(q *query.Query, w *query.Where) string {
	return g.Wrap(w.Column) + " is not null"
}

func (g *QueryGrammar) whereBetween(q *query.Query, w *query.Where) string {
	between := "between"
	if w.Not {
		between = "not between"
	}
	min := g.Parameter(w.Values[0])
	max := g.Parameter(w.Values[len(w.Values)-1])
	return g.Wrap(w.Column) + " " + between + " " + min + " and " + max
}

func (g *QueryGrammar) whereBetweenColumns(q *query.Query, w *query.Where) string {
	between := "between"
	if w.Not {
		between = "not between"
	}
	min := g.Wrap(w.Values[0])
	max := g.Wrap(w.Values[len(w.Values)-1])
	return g.Wrap(w.Column) + " " + between + " " + min + " and " + max
}

func (g *QueryGrammar) whereValueBetween(q *query.Query, w *query.Where) string {
	between := "between"
	if w.Not {
		between = "not between"
	}
	min := g.Wrap(w.Columns[0])
	max := g.Wrap(w.Columns[len(w.Columns)-1])
	return g.Parameter(w.Value) + " " + between + " " + min + " and " + max
}

func (g *QueryGrammar) whereNested(q *query.Query, w *query.Where) string {
	offset := 6 // len("where ")
	if w.Query.JoinClause {
		offset = 3 // len("on ")
	}
	inner := g.compileWheres(w.Query)
	if len(inner) >= offset {
		inner = inner[offset:]
	} else {
		inner = ""
	}
	return "(" + inner + ")"
}

func (g *QueryGrammar) whereSub(q *query.Query, w *query.Where) string {
	select_ := g.CompileSelect(w.Query)
	return g.Wrap(w.Column) + " " + w.Operator + " (" + select_ + ")"
}

func (g *QueryGrammar) whereExists(q *query.Query, w *query.Where) string {
	return "exists (" + g.CompileSelect(w.Query) + ")"
}

func (g *QueryGrammar) whereNotExists(q *query.Query, w *query.Where) string {
	return "not exists (" + g.CompileSelect(w.Query) + ")"
}

func (g *QueryGrammar) whereRowValues(q *query.Query, w *query.Where) string {
	columns := g.Columnize(w.Columns)
	values := g.Parameterize(w.Values)
	return "(" + columns + ") " + w.Operator + " (" + values + ")"
}

func (g *QueryGrammar) whereJsonBoolean(q *query.Query, w *query.Where) string {
	column := g.wrapJsonBooleanSelector(fmtString(g.GetValue(w.Column)))
	value := g.wrapJsonBooleanValue(g.Parameter(w.Value))
	return column + " " + w.Operator + " " + value
}

func (g *QueryGrammar) whereJsonContains(q *query.Query, w *query.Where) string {
	not := ""
	if w.Not {
		not = "not "
	}
	if g.compileJsonContainsFn == nil {
		panic(ErrUnsupported{"This database engine does not support JSON contains operations."})
	}
	return not + g.compileJsonContainsFn(fmtString(g.GetValue(w.Column)), g.Parameter(w.Value))
}

func (g *QueryGrammar) whereJsonOverlaps(q *query.Query, w *query.Where) string {
	not := ""
	if w.Not {
		not = "not "
	}
	if g.compileJsonOverlapsFn == nil {
		panic(ErrUnsupported{"This database engine does not support JSON overlaps operations."})
	}
	return not + g.compileJsonOverlapsFn(fmtString(g.GetValue(w.Column)), g.Parameter(w.Value))
}

func (g *QueryGrammar) whereJsonContainsKey(q *query.Query, w *query.Where) string {
	not := ""
	if w.Not {
		not = "not "
	}
	if g.compileJsonContainsKeyFn == nil {
		panic(ErrUnsupported{"This database engine does not support JSON contains key operations."})
	}
	return not + g.compileJsonContainsKeyFn(fmtString(g.GetValue(w.Column)))
}

func (g *QueryGrammar) whereJsonLength(q *query.Query, w *query.Where) string {
	if g.compileJsonLengthFn == nil {
		panic(ErrUnsupported{"This database engine does not support JSON length operations."})
	}
	return g.compileJsonLengthFn(fmtString(g.GetValue(w.Column)), w.Operator, g.Parameter(w.Value))
}

func (g *QueryGrammar) dateBasedWhere(kind string, q *query.Query, w *query.Where) string {
	if g.dateBasedWhereFn != nil {
		return g.dateBasedWhereFn(kind, q, w)
	}
	value := g.Parameter(w.Value)
	return kind + "(" + g.Wrap(w.Column) + ") " + w.Operator + " " + value
}

func (g *QueryGrammar) compileGroups(q *query.Query, groups []any) string {
	return "group by " + g.Columnize(groups)
}

func (g *QueryGrammar) compileHavings(q *query.Query) string {
	sqls := make([]string, 0, len(q.Havings))
	for _, h := range q.Havings {
		sqls = append(sqls, h.Boolean+" "+g.compileHaving(h))
	}
	return "having " + g.removeLeadingBoolean(strings.Join(sqls, " "))
}

func (g *QueryGrammar) compileHaving(h *query.Where) string {
	switch h.Type {
	case "Raw":
		return fmtString(g.GetValue(h.Sql))
	case "between":
		return g.compileHavingBetween(h)
	case "Null":
		return g.Wrap(h.Column) + " is null"
	case "NotNull":
		return g.Wrap(h.Column) + " is not null"
	case "bit":
		return "(" + g.Wrap(h.Column) + " " + h.Operator + " " + g.Parameter(h.Value) + ") != 0"
	case "Bitwise":
		if g.compileHavingBitwiseFn != nil {
			return g.compileHavingBitwiseFn(h)
		}
		return g.compileBasicHaving(h)
	case "Expression":
		return fmtString(g.GetValue(h.Column))
	case "Nested":
		inner := g.compileHavings(h.Query)
		if len(inner) >= 7 {
			inner = inner[7:]
		} else {
			inner = ""
		}
		return "(" + inner + ")"
	default:
		return g.compileBasicHaving(h)
	}
}

func (g *QueryGrammar) compileBasicHaving(h *query.Where) string {
	column := g.Wrap(h.Column)
	parameter := g.Parameter(h.Value)
	return column + " " + h.Operator + " " + parameter
}

func (g *QueryGrammar) compileHavingBetween(h *query.Where) string {
	between := "between"
	if h.Not {
		between = "not between"
	}
	column := g.Wrap(h.Column)
	min := g.Parameter(h.Values[0])
	max := g.Parameter(h.Values[len(h.Values)-1])
	return column + " " + between + " " + min + " and " + max
}

func (g *QueryGrammar) compileOrders(q *query.Query, orders []*query.Order) string {
	if len(orders) == 0 {
		return ""
	}
	sqls := make([]string, 0, len(orders))
	for _, o := range orders {
		if o.Sql != nil {
			sqls = append(sqls, fmtString(g.GetValue(o.Sql)))
			continue
		}
		if o.Type == "InOrderOf" {
			sqls = append(sqls, g.compileInOrderOf(o))
			continue
		}
		sqls = append(sqls, g.Wrap(o.Column)+" "+o.Direction)
	}
	return "order by " + strings.Join(sqls, ", ")
}

// compileInOrderOf 对应 Laravel Grammar::compileInOrderOf：
// case "col" = ? then 0 when ... else N end（按取值序列排序）。
func (g *QueryGrammar) compileInOrderOf(order *query.Order) string {
	column := g.Wrap(order.Column)
	cases := make([]string, 0, len(order.Values))
	for i, value := range order.Values {
		cases = append(cases, "when "+column+" = "+g.Parameter(value)+" then "+intToString(int64(i)))
	}
	return "case " + strings.Join(cases, " ") + " else " + intToString(int64(len(order.Values))) + " end"
}

func (g *QueryGrammar) compileLimit(q *query.Query, limit int64) string {
	if g.compileLimitFn != nil {
		return g.compileLimitFn(q, limit)
	}
	return "limit " + intToString(limit)
}

func (g *QueryGrammar) compileOffset(q *query.Query, offset int64) string {
	if g.compileOffsetFn != nil {
		return g.compileOffsetFn(q, offset)
	}
	return "offset " + intToString(offset)
}

func (g *QueryGrammar) compileUnions(q *query.Query) string {
	sql := ""
	for _, union := range q.Unions {
		sql += g.compileUnion(union)
	}

	if len(q.UnionOrders) > 0 {
		sql += " " + g.compileOrders(q, q.UnionOrders)
	}
	if q.UnionLimit != nil {
		sql += " " + g.compileLimit(q, *q.UnionLimit)
	}
	if q.UnionOffset != nil {
		sql += " " + g.compileOffset(q, *q.UnionOffset)
	}

	return strings.TrimLeft(sql, " ")
}

func (g *QueryGrammar) compileUnion(u *query.Union) string {
	conjunction := " union "
	if u.All {
		conjunction = " union all "
	}
	return conjunction + g.wrapUnion(g.CompileSelect(u.Query))
}

func (g *QueryGrammar) wrapUnion(sql string) string {
	if g.wrapUnionFn != nil {
		return g.wrapUnionFn(sql)
	}
	return "(" + sql + ")"
}

func (g *QueryGrammar) compileUnionAggregate(q *query.Query) string {
	sql := g.compileAggregate(q, q.Aggregate)

	q.Aggregate = nil

	return sql + " from (" + g.CompileSelect(q) + ") as " + g.WrapTable("temp_table")
}

// CompileExists 编译 exists 语句。
func (g *QueryGrammar) CompileExists(q *query.Query) string {
	select_ := g.CompileSelect(q)
	return "select exists(" + select_ + ") as " + g.Wrap("exists")
}

func (g *QueryGrammar) compileIndexHint(q *query.Query, hint *query.IndexHint) string {
	if g.compileIndexHintFn != nil {
		return g.compileIndexHintFn(q, hint)
	}
	panic(ErrUnsupported{"This database engine does not support index hints."})
}

func (g *QueryGrammar) compileLock(q *query.Query, value any) string {
	if g.compileLockFn != nil {
		return g.compileLockFn(q, value)
	}
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func (g *QueryGrammar) compileRandom(seed any) string {
	if g.compileRandomFn != nil {
		return g.compileRandomFn(seed)
	}
	return "RANDOM()"
}

// CompileRandom 导出方言随机表达式（InRandomOrder 使用）。
func (g *QueryGrammar) CompileRandom(seed any) string {
	return g.compileRandom(seed)
}

// CompileJsonValueCast 导出 JSON 值 cast 表达式（不支持时抛错）。
func (g *QueryGrammar) CompileJsonValueCast(value string) string {
	if g.compileJsonValueCastFn == nil {
		panic(ErrUnsupported{"This database engine does not support JSON value casts."})
	}
	return g.compileJsonValueCastFn(value)
}

// CompileInsert 编译 insert（rows 为多行记录，行内列序保留）。
func (g *QueryGrammar) CompileInsert(q *query.Query, rows []query.Row) (string, []any) {
	if g.compileInsertFn != nil {
		return g.compileInsertFn(q, rows)
	}
	return g.baseCompileInsert(q, rows)
}

func (g *QueryGrammar) baseCompileInsert(q *query.Query, rows []query.Row) (string, []any) {
	table := g.WrapTable(q.From)

	if len(rows) == 0 {
		return "insert into " + table + " () values ()", nil
	}

	columns := rows[0].Columns()

	var bindings []any
	parameters := make([]string, 0, len(rows))
	for _, record := range rows {
		row := make([]string, 0, len(columns))
		for _, col := range columns {
			v, _ := record.Value(col)
			if g.IsExpression(v) {
				row = append(row, g.Parameter(v))
				continue
			}
			bindings = append(bindings, v)
			row = append(row, g.Parameter(v))
		}
		parameters = append(parameters, "("+strings.Join(row, ", ")+")")
	}

	wrapped := make([]string, len(columns))
	for i, col := range columns {
		wrapped[i] = g.Wrap(col)
	}

	return "insert into " + table + " (" + strings.Join(wrapped, ", ") + ") values " + strings.Join(parameters, ", "), bindings
}

// CompileInsertGetId 编译 insert ... 并返回 id 的语句。
func (g *QueryGrammar) CompileInsertGetId(q *query.Query, rows []query.Row, sequence string) (string, []any) {
	if g.compileInsertGetIdFn != nil {
		return g.compileInsertGetIdFn(q, rows, sequence)
	}
	return g.CompileInsert(q, rows)
}

// CompileInsertOrIgnore 编译 insert ignore。
func (g *QueryGrammar) CompileInsertOrIgnore(q *query.Query, rows []query.Row) (string, []any) {
	if g.compileInsertOrIgnoreFn != nil {
		return g.compileInsertOrIgnoreFn(q, rows)
	}
	panic(ErrUnsupported{"This database engine does not support inserting while ignoring errors."})
}

// CompileInsertUsing 编译 insert ... select。
func (g *QueryGrammar) CompileInsertUsing(q *query.Query, columns []any, sql string) string {
	if g.compileInsertUsingFn != nil {
		return g.compileInsertUsingFn(q, columns, sql)
	}
	table := g.WrapTable(q.From)
	if len(columns) == 0 {
		return "insert into " + table + " " + sql
	}
	return "insert into " + table + " (" + g.Columnize(columns) + ") " + sql
}

// CompileInsertOrIgnoreUsing 编译 insert ignore ... select。
func (g *QueryGrammar) CompileInsertOrIgnoreUsing(q *query.Query, columns []any, sql string) string {
	if g.compileInsertOrIgnoreUsingFn != nil {
		return g.compileInsertOrIgnoreUsingFn(q, columns, sql)
	}
	panic(ErrUnsupported{"This database engine does not support inserting while ignoring errors."})
}

// CompileUpsert 编译 upsert。
func (g *QueryGrammar) CompileUpsert(q *query.Query, rows []query.Row, uniqueBy, update []string) (string, []any) {
	if g.compileUpsertFn != nil {
		return g.compileUpsertFn(q, rows, uniqueBy, update)
	}
	panic(ErrUnsupported{"This database engine does not support upserts."})
}

// CompileUpdate 编译 update 语句。
func (g *QueryGrammar) CompileUpdate(q *query.Query, values map[string]any) string {
	if g.compileUpdateFn != nil {
		return g.compileUpdateFn(q, values)
	}
	return g.baseCompileUpdate(q, values)
}

// baseCompileUpdate CompileUpdate 的无钩子基础实现（方言回退直接调用，
// 避免 compileUpdateFn 再次路由回方言方法造成无限递归）。
func (g *QueryGrammar) baseCompileUpdate(q *query.Query, values map[string]any) string {
	table := g.WrapTable(q.From)

	columns := g.compileUpdateColumns(q, values)

	where := g.compileWheres(q)

	if len(q.Joins) > 0 {
		return strings.TrimSpace(g.compileUpdateWithJoins(q, table, columns, where))
	}
	return strings.TrimSpace(g.compileUpdateWithoutJoins(q, table, columns, where))
}

func (g *QueryGrammar) compileUpdateColumns(q *query.Query, values map[string]any) string {
	if g.compileUpdateColumnsFn != nil {
		return g.compileUpdateColumnsFn(q, values)
	}
	keys := sortedKeys(values)
	cols := make([]string, 0, len(keys))
	for _, key := range keys {
		if g.isJsonSelector(key) {
			if g.compileJsonUpdateColFn != nil {
				cols = append(cols, g.compileJsonUpdateColFn(key, values[key]))
				continue
			}
			panic(ErrUnsupported{"This database engine does not support JSON update operations."})
		}
		cols = append(cols, g.Wrap(key)+" = "+g.Parameter(values[key]))
	}
	return strings.Join(cols, ", ")
}

func (g *QueryGrammar) compileUpdateWithoutJoins(q *query.Query, table, columns, where string) string {
	if g.compileUpdateWithoutJoinsFn != nil {
		return g.compileUpdateWithoutJoinsFn(q, table, columns, where)
	}
	return "update " + table + " set " + columns + " " + where
}

func (g *QueryGrammar) compileUpdateWithJoins(q *query.Query, table, columns, where string) string {
	if g.compileUpdateWithJoinsFn != nil {
		return g.compileUpdateWithJoinsFn(q, table, columns, where)
	}
	joins := g.compileJoins(q, q.Joins)
	return "update " + table + " " + joins + " set " + columns + " " + where
}

// PrepareBindingsForUpdate 对应 Laravel 的绑定整理：join 绑定 + set 值 + 其余段绑定。
func (g *QueryGrammar) PrepareBindingsForUpdate(bindings map[string][]any, values map[string]any) []any {
	clean := make([]any, 0)
	for _, v := range bindings["join"] {
		clean = append(clean, flattenBinding(v))
	}
	for _, key := range sortedKeys(values) {
		v := values[key]
		if g.IsExpression(v) {
			continue
		}
		clean = append(clean, flattenBinding(v))
	}
	for _, segment := range BindingSegments {
		if segment == "select" || segment == "join" {
			continue
		}
		for _, v := range bindings[segment] {
			clean = append(clean, flattenBinding(v))
		}
	}
	return clean
}

// PrepareBindingsForDelete 对应 Laravel：除 select 外全部段。
func (g *QueryGrammar) PrepareBindingsForDelete(bindings map[string][]any) []any {
	var out []any
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

// CompileDelete 编译 delete 语句。
func (g *QueryGrammar) CompileDelete(q *query.Query) string {
	table := g.WrapTable(q.From)
	where := g.compileWheres(q)

	if len(q.Joins) > 0 {
		return strings.TrimSpace(g.compileDeleteWithJoins(q, table, where))
	}
	return strings.TrimSpace(g.compileDeleteWithoutJoins(q, table, where))
}

func (g *QueryGrammar) compileDeleteWithoutJoins(q *query.Query, table, where string) string {
	if g.compileDeleteWithoutJoinsFn != nil {
		return g.compileDeleteWithoutJoinsFn(q, table, where)
	}
	return "delete from " + table + " " + where
}

func (g *QueryGrammar) compileDeleteWithJoins(q *query.Query, table, where string) string {
	if g.compileDeleteWithJoinsFn != nil {
		return g.compileDeleteWithJoinsFn(q, table, where)
	}
	alias := table
	if i := strings.LastIndex(table, " as "); i >= 0 {
		alias = table[i+4:]
	}
	joins := g.compileJoins(q, q.Joins)
	return "delete " + alias + " from " + table + " " + joins + " " + where
}

// CompileTruncate 编译 truncate 语句（可能多条）。
func (g *QueryGrammar) CompileTruncate(q *query.Query) []TruncateStatement {
	if g.compileTruncateFn != nil {
		return g.compileTruncateFn(q)
	}
	return []TruncateStatement{{Sql: "truncate table " + g.WrapTable(q.From)}}
}

// concatenate 拼接非空片段。
func (g *QueryGrammar) concatenate(segments []string) string {
	var parts []string
	for _, s := range segments {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " ")
}

var leadingBoolean = regexp.MustCompile(`(?i)^(and |or )`)

func (g *QueryGrammar) removeLeadingBoolean(value string) string {
	return leadingBoolean.ReplaceAllString(value, "")
}

// SubstituteBindingsIntoRawSql 把绑定值内联进 SQL（对应 Laravel 同名方法）。
// 字符串字面量内跳过占位符，”/\' 转义成对跳过。
func (g *QueryGrammar) SubstituteBindingsIntoRawSql(sql string, bindings []any) string {
	var out strings.Builder
	bindingIndex := 0
	isStringLiteral := false

	runes := []rune(sql)
	for i := 0; i < len(runes); i++ {
		char := runes[i]
		var next rune
		if i+1 < len(runes) {
			next = runes[i+1]
		}

		pair := string(char) + string(next)
		if pair == "''" || pair == "\\'" || pair == "??" {
			out.WriteString(pair)
			i++
			continue
		}
		if char == '\'' {
			out.WriteRune(char)
			isStringLiteral = !isStringLiteral
			continue
		}
		if char == '?' && !isStringLiteral {
			if bindingIndex < len(bindings) {
				out.WriteString(g.escapeBinding(bindings[bindingIndex]))
			} else {
				out.WriteRune('?')
			}
			bindingIndex++
			continue
		}
		out.WriteRune(char)
	}
	return out.String()
}

// escapeBinding 绑定值的 SQL 字面量转义（对应 Connection::escape 基础实现）。
func (g *QueryGrammar) escapeBinding(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case bool:
		if v {
			return "1"
		}
		return "0"
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case uint64:
		return strconv.FormatUint(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case string:
		return "'" + strings.ReplaceAll(v, "'", "''") + "'"
	default:
		return "'" + strings.ReplaceAll(fmt.Sprint(v), "'", "''") + "'"
	}
}

// escapeOperator 对应 Laravel 的 str_replace('?', '??', $operator)（Postgres 运算符转义）。
func escapeOperator(operator string) string {
	return strings.ReplaceAll(operator, "?", "??")
}

// defaultWrapJsonPath / defaultWrapJsonPathSegment：CompilesJsonPaths trait 的共享实现。
func defaultWrapJsonPath(value, delimiter string, seg func(string) string) string {
	quoteEscape := regexp.MustCompile(`(\\+)?'`)
	value = quoteEscape.ReplaceAllString(value, "''")

	segments := strings.Split(value, delimiter)
	for i, segv := range segments {
		segments[i] = seg(segv)
	}
	jsonPath := strings.Join(segments, ".")

	sep := "."
	if strings.HasPrefix(jsonPath, "[") {
		sep = ""
	}
	return "'$" + sep + jsonPath + "'"
}

var jsonTrailingIndex = regexp.MustCompile(`(\[[^\]]+\])+$`)

func defaultWrapJsonPathSegment(segment string) string {
	if parts := jsonTrailingIndex.FindStringSubmatch(segment); parts != nil {
		index := parts[0]
		key := strings.TrimSuffix(segment, index)
		if key != "" {
			return `"` + key + `"` + index
		}
		return index
	}
	return `"` + segment + `"`
}

// isTrueDistinct 判断 Distinct 是否为 bool true。
func isTrueDistinct(v any) bool {
	bv, ok := v.(bool)
	return ok && bv
}
