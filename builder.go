package torm

import (
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"reflect"
	"strconv"
	"strings"

	"github.com/thinkoner/torm/grammar"
	"github.com/thinkoner/torm/query"
)

// Builder 对应 Illuminate\Database\Query\Builder。
type Builder struct {
	Connection *Connection
	grammar    grammar.Grammar
	Query      *query.Query
	Bindings   map[string][]interface{}

	// modelInfo：Model() 查询所关联的模型元数据（软删除 scope 等）
	modelInfo     *modelInfo
	scopesApplied bool

	// useWritePdo：select 强制走写连接（对应 Laravel useWritePdo）
	useWritePdo bool

	// relations：待预加载的关系（With）
	relations []pendingRelation
}

// applyModelScopes 在执行前追加模型全局 scope（对齐 Laravel 全局 scope 后置语义）。
func (b *Builder) applyModelScopes() {
	if b.modelInfo != nil && b.modelInfo.softDeletes && !b.scopesApplied {
		b.WhereNull(b.modelInfo.deletedAtField)
		b.scopesApplied = true
	}
}

func NewBuilder(connection *Connection, g grammar.Grammar) *Builder {
	return &Builder{
		Connection: connection,
		grammar:    g,
		Query:      &query.Query{},
		Bindings:   getDefaultBindings(),
	}
}

func (b *Builder) GetConnection() *Connection { return b.Connection }

func (b *Builder) GetGrammar() grammar.Grammar { return b.grammar }

func (b *Builder) isQueryable(v interface{}) bool {
	switch v.(type) {
	case *Builder, func(*Builder), func(*Builder) *Builder, *query.Query:
		return true
	}
	return false
}

// applySubQuery 执行子查询构造回调（兼容两种闭包签名）。
func applySubQuery(sub *Builder, q interface{}) *Builder {
	switch cb := q.(type) {
	case func(*Builder):
		cb(sub)
	case func(*Builder) *Builder:
		if out := cb(sub); out != nil {
			return out
		}
	}
	return sub
}

// createSub 把 *Builder / func(*Builder) / Expression 归一为 (sql, bindings)。
func (b *Builder) createSub(q interface{}) (string, []interface{}) {
	switch v := q.(type) {
	case *Builder:
		return v.ToSql(), v.GetBindings()
	case *query.Query:
		return b.grammar.CompileSelect(v), nil
	case func(*Builder), func(*Builder) *Builder:
		sub := NewBuilder(b.Connection, b.grammar)
		sub = applySubQuery(sub, v)
		return sub.ToSql(), sub.GetBindings()
	case grammar.Expression:
		return fmt.Sprint(b.grammar.GetValue(v)), nil
	default:
		return "", nil
	}
}

// ---- Select ----

// Select 设置查询列；元素为列名字符串或 grammar.Expression。
func (b *Builder) Select(columns ...interface{}) *Builder {
	if len(columns) == 0 {
		columns = []interface{}{"*"}
	}
	b.Query.Columns = columns
	return b
}

// AddSelect 追加查询列；支持列表展开并跳过重复列（Laravel 13 语义）。
func (b *Builder) AddSelect(columns ...interface{}) *Builder {
	for _, column := range columns {
		switch v := column.(type) {
		case []string:
			for _, c := range v {
				b.AddSelect(c)
			}
			continue
		case []interface{}:
			for _, c := range v {
				b.AddSelect(c)
			}
			continue
		}
		if b.hasColumn(column) {
			continue
		}
		b.Query.Columns = append(b.Query.Columns, column)
	}
	return b
}

func (b *Builder) hasColumn(column interface{}) bool {
	for _, c := range b.Query.Columns {
		if fmtString(c) == fmtString(column) {
			return true
		}
	}
	return false
}

// SelectRaw 追加原始表达式列。
func (b *Builder) SelectRaw(sql string, bindings ...interface{}) *Builder {
	b.AddSelect(grammar.NewExpression(sql))
	if len(bindings) != 0 {
		b.AddBinding(bindings, "select")
	}
	return b
}

// SelectSub 以子查询作为列：select (select ...) as "as"。
func (b *Builder) SelectSub(q interface{}, as string) *Builder {
	sql, bindings := b.createSub(q)
	b.Query.Columns = append(b.Query.Columns, grammar.NewExpression("("+sql+") as "+b.grammar.Wrap(as)))
	b.AddBinding(bindings, "select")
	return b
}

// Distinct 无参时 distinct；传列名时对应 Postgres 的 distinct on。
func (b *Builder) Distinct(args ...interface{}) *Builder {
	if len(args) == 0 {
		b.Query.Distinct = true
		return b
	}
	if cols, ok := args[0].([]string); ok && len(args) == 1 {
		vals := make([]interface{}, len(cols))
		for i, c := range cols {
			vals[i] = c
		}
		b.Query.Distinct = vals
		return b
	}
	b.Query.Distinct = args
	return b
}

// ---- From ----

func (b *Builder) From(table interface{}) *Builder {
	b.Query.From = table
	return b
}

// FromRaw 设置原始 from 表达式。
func (b *Builder) FromRaw(sql interface{}) *Builder {
	b.Query.From = sql
	return b
}

// FromSub 以子查询作为表：from (select ...) as "as"。
func (b *Builder) FromSub(q interface{}, as string) *Builder {
	sql, bindings := b.createSub(q)
	b.Query.From = grammar.NewExpression("(" + sql + ") as " + b.grammar.WrapTable(as))
	b.AddBinding(bindings, "from")
	return b
}

// ---- Join ----

// Join 添加 join 子句：Join(table, first, operator, second, type, where)，
// 对应 Laravel join($table,$first,$operator=null,$second=null,$type='inner',$where=false)。
func (b *Builder) Join(table string, first string, args ...interface{}) *Builder {
	var (
		operator string
		second   interface{}
		typeStr  = "inner"
		where    bool
	)
	if len(args) >= 1 {
		operator, _ = args[0].(string)
	}
	if len(args) >= 2 {
		second = args[1]
	}
	if len(args) >= 3 {
		typeStr, _ = args[2].(string)
	}
	if len(args) >= 4 {
		where, _ = args[3].(bool)
	}
	if operator == "" {
		operator = "="
	}
	return b.addJoin(typeStr, table, first, operator, second, where)
}

func (b *Builder) LeftJoin(table string, first string, args ...interface{}) *Builder {
	return b.Join(table, first, append(args, "left")...)
}

func (b *Builder) RightJoin(table string, first string, args ...interface{}) *Builder {
	return b.Join(table, first, append(args, "right")...)
}

func (b *Builder) CrossJoin(table string, first string, args ...interface{}) *Builder {
	return b.Join(table, first, append(args, "cross")...)
}

// JoinWhere 对应 join(..., $where=true)：把第二操作数作为绑定值。
func (b *Builder) JoinWhere(table string, first string, operator string, second interface{}) *Builder {
	return b.Join(table, first, operator, second, "inner", true)
}

func (b *Builder) LeftJoinWhere(table string, first string, operator string, second interface{}) *Builder {
	return b.Join(table, first, operator, second, "left", true)
}

func (b *Builder) RightJoinWhere(table string, first string, operator string, second interface{}) *Builder {
	return b.Join(table, first, operator, second, "right", true)
}

func (b *Builder) addJoin(typeStr string, table interface{}, first, operator string, second interface{}, where bool) *Builder {
	wheres := make([]*query.Where, 0, 1)
	if where {
		wheres = append(wheres, &query.Where{
			Type:     "Basic",
			Column:   first,
			Operator: "=",
			Value:    second,
			Boolean:  "and",
		})
		b.AddBinding(second, "join")
	} else {
		wheres = append(wheres, &query.Where{
			Type:     "Column",
			First:    first,
			Operator: operator,
			Second:   second,
			Boolean:  "and",
		})
	}
	b.Query.Joins = append(b.Query.Joins, &query.Join{
		Type:   typeStr,
		Table:  table,
		Wheres: wheres,
	})
	return b
}

// JoinSub 以子查询作为 join 表：inner join (select ...) as "as" on ...
func (b *Builder) JoinSub(q interface{}, as string, first, operator, second interface{}) *Builder {
	return b.addJoinOfSub("inner", q, as, first, operator, second)
}

func (b *Builder) LeftJoinSub(q interface{}, as string, first, operator, second interface{}) *Builder {
	return b.addJoinOfSub("left", q, as, first, operator, second)
}

func (b *Builder) RightJoinSub(q interface{}, as string, first, operator, second interface{}) *Builder {
	return b.addJoinOfSub("right", q, as, first, operator, second)
}

func (b *Builder) addJoinOfSub(typeStr string, q interface{}, as string, first, operator, second interface{}) *Builder {
	sql, _ := b.createSub(q)
	return b.addJoin(typeStr, grammar.NewExpression("("+sql+") as "+b.grammar.WrapTable(as)), fmtString(first), fmtString(operator), second, false)
}

// ---- Where ----

// queryOperators 对应 Laravel Query\Builder 的 $operators。
var queryOperators = []string{
	"=", "<", ">", "<=", ">=", "<>", "!=", "<=>",
	"like", "like binary", "not like", "ilike", "not ilike",
	"&", "|", "^", "<<", ">>",
	"rlike", "not rlike", "regexp", "not regexp",
	"~", "~*", "!~", "!~*", "similar to", "not similar to",
	"sounds like",
}

func invalidOperator(operator string) bool {
	if operator == "" {
		return true
	}
	for _, op := range queryOperators {
		if strings.ToLower(operator) == op {
			return false
		}
	}
	return true
}

// Where 基础 where：Where(column, value) / Where(column, operator, value, boolean...)。
// column 可为字符串、grammar.Expression、map（批量 and）、func(*Builder)（括号分组）或 *Builder（子查询比较）。
func (b *Builder) Where(column interface{}, args ...interface{}) *Builder {
	return b.whereImpl(column, args, "and")
}

func (b *Builder) OrWhere(column interface{}, args ...interface{}) *Builder {
	return b.whereImpl(column, args, "or")
}

func (b *Builder) WhereNot(column interface{}, args ...interface{}) *Builder {
	return b.whereImpl(column, args, "and not")
}

func (b *Builder) OrWhereNot(column interface{}, args ...interface{}) *Builder {
	return b.whereImpl(column, args, "or not")
}

func (b *Builder) whereImpl(column interface{}, args []interface{}, boolean string) *Builder {
	if m, ok := column.(map[string]interface{}); ok {
		return b.addArrayOfWheres(m, boolean)
	}

	operator, value := parseWhereArgs(args...)

	// func(*Builder) 列 → 括号分组
	switch column.(type) {
	case func(*Builder), func(*Builder) *Builder:
		if operator == "" {
			sub := b.forNestedWhere()
			applySubQuery(sub, column)
			return b.addNestedWhereQuery(sub, boolean)
		}
	}

	// 子查询列：where(sub, op, value) → (sub) op ?
	if b.isQueryable(column) && operator != "" {
		sql, bindings := b.createSub(column)
		return b.addBinding(bindings, "where").addWhere("Basic", grammar.NewExpression("("+sql+")"), operator, value, boolean, false)
	}

	// 操作符回退：非法操作符视为值，操作符取 "="
	if invalidOperator(operator) {
		operator, value = "=", operator
	}

	// 子查询值：column op (sub)
	if b.isQueryable(value) {
		var sub *query.Query
		var bindings []interface{}
		switch v := value.(type) {
		case *Builder:
			sub, bindings = v.Query, v.GetBindings()
		case *query.Query:
			sub = v
		default:
			nb := NewBuilder(b.Connection, b.grammar)
			nb = applySubQuery(nb, value)
			sub, bindings = nb.Query, nb.GetBindings()
		}
		b.AddBinding(bindings, "where")
		b.Query.Wheres = append(b.Query.Wheres, &query.Where{
			Type:     "Sub",
			Column:   column,
			Operator: operator,
			Query:    sub,
			Boolean:  boolean,
		})
		return b
	}

	// null 值 → whereNull / whereNotNull
	if value == nil {
		not := operator != "=" && operator != "<=>"
		return b.whereNullImpl(column, boolean, not)
	}

	typ := "Basic"

	// JSON 列上的布尔值：作为 raw true/false
	if colStr, ok := column.(string); ok && strings.Contains(colStr, "->") {
		if bv, ok := value.(bool); ok {
			if bv {
				value = grammar.NewExpression("true")
			} else {
				value = grammar.NewExpression("false")
			}
			typ = "JsonBoolean"
		}
	}

	if operator == "<=>" {
		typ = "NullSafeEquals"
	}

	w := &query.Where{
		Type:     typ,
		Column:   column,
		Operator: operator,
		Value:    value,
		Boolean:  boolean,
	}
	b.Query.Wheres = append(b.Query.Wheres, w)

	if !b.grammar.IsExpression(value) {
		b.AddBinding(flattenValue(value), "where")
	}

	return b
}

// addArrayOfWheres 对应 Laravel 的 where([col => val, ...])（括号分组）。
func (b *Builder) addArrayOfWheres(m map[string]interface{}, boolean string) *Builder {
	return b.WhereNested(func(nq *Builder) {
		for _, key := range sortedColumnNames(m) {
			nq.whereImpl(key, []interface{}{"=", m[key]}, boolean)
		}
	}, boolean)
}

// addWhere 添加指定类型的 where。
func (b *Builder) addWhere(typ string, column, operator, value, boolean interface{}, not bool) *Builder {
	w := &query.Where{
		Type:     typ,
		Column:   column,
		Operator: fmtString(operator),
		Value:    value,
		Boolean:  fmtString(boolean),
		Not:      not,
	}
	b.Query.Wheres = append(b.Query.Wheres, w)
	if !b.grammar.IsExpression(value) {
		b.AddBinding(flattenValue(value), "where")
	}
	return b
}

// addBinding 链式便捷封装。
func (b *Builder) addBinding(value interface{}, segment string) *Builder {
	b.BuilderAddBinding(value, segment)
	return b
}

// flattenValue 对应 Laravel 的 flattenValue：数组值取扁平化后的首元素。
func flattenValue(value interface{}) interface{} {
	if arr, ok := value.([]interface{}); ok {
		if len(arr) == 0 {
			return nil
		}
		return flattenValue(arr[0])
	}
	return value
}

// WhereColumn 比较两列：WhereColumn(first, second) / (first, operator, second, boolean...)。
func (b *Builder) WhereColumn(first interface{}, args ...interface{}) *Builder {
	operator, second, boolean := parseColumnArgs(args...)
	w := &query.Where{
		Type:     "Column",
		First:    first,
		Operator: operator,
		Second:   second,
		Boolean:  boolean,
	}
	b.Query.Wheres = append(b.Query.Wheres, w)
	return b
}

func (b *Builder) OrWhereColumn(first interface{}, args ...interface{}) *Builder {
	operator, second, _ := parseColumnArgs(args...)
	return b.WhereColumn(first, operator, second, "or")
}

// WhereRaw / OrWhereRaw
// 对应 Laravel whereRaw($sql, $bindings=[], $boolean='and')：可选末位 string 参数
// 为 boolean 关键字（and / or / and not / or not）。
func (b *Builder) WhereRaw(sql interface{}, bindings ...interface{}) *Builder {
	boolean, bindings := splitRawBoolean("and", bindings)
	b.Query.Wheres = append(b.Query.Wheres, &query.Where{
		Type:    "Raw",
		Sql:     sql,
		Boolean: boolean,
	})
	if len(bindings) != 0 {
		b.AddBinding(bindings, "where")
	}
	return b
}

func (b *Builder) OrWhereRaw(sql interface{}, bindings ...interface{}) *Builder {
	boolean, bindings := splitRawBoolean("or", bindings)
	b.Query.Wheres = append(b.Query.Wheres, &query.Where{
		Type:    "Raw",
		Sql:     sql,
		Boolean: boolean,
	})
	if len(bindings) != 0 {
		b.AddBinding(bindings, "where")
	}
	return b
}

// splitRawBoolean 识别变参末位的 boolean 关键字（对应 Laravel 的显式 $boolean 形参）。
func splitRawBoolean(defaultBoolean string, bindings []interface{}) (string, []interface{}) {
	if n := len(bindings); n > 0 {
		if s, ok := bindings[n-1].(string); ok {
			switch s {
			case "and", "or", "and not", "or not":
				return s, bindings[:n-1]
			}
		}
	}
	return defaultBoolean, bindings
}

// WhereIn / OrWhereIn / WhereNotIn / OrWhereNotIn
func (b *Builder) WhereIn(column interface{}, values interface{}, args ...interface{}) *Builder {
	return b.whereInImpl(column, values, "In", args, false)
}

func (b *Builder) OrWhereIn(column interface{}, values interface{}) *Builder {
	return b.WhereIn(column, values, "or")
}

func (b *Builder) WhereNotIn(column interface{}, values interface{}, args ...interface{}) *Builder {
	return b.whereInImpl(column, values, "NotIn", args, false)
}

func (b *Builder) OrWhereNotIn(column interface{}, values interface{}) *Builder {
	return b.WhereNotIn(column, values, "or")
}

func (b *Builder) whereInImpl(column interface{}, values interface{}, typ string, args []interface{}, not bool) *Builder {
	boolean := "and"
	if len(args) > 0 {
		if s, ok := args[0].(string); ok {
			boolean = s
		}
	}

	// 子查询值：in (select ...)
	if b.isQueryable(values) {
		sql, bindings := b.createSub(values)
		b.AddBinding(bindings, "where")
		values = []interface{}{grammar.NewExpression(sql)}
	}

	list, ok := values.([]interface{})
	if !ok {
		list = []interface{}{values}
	}
	values = list

	w := &query.Where{
		Type:    typ,
		Column:  column,
		Values:  list,
		Boolean: boolean,
		Not:     not,
	}

	// 嵌套数组校验（对应 Laravel InvalidArgumentException）
	flat := make([]interface{}, 0, len(list))
	for _, v := range list {
		switch av := v.(type) {
		case []interface{}:
			if len(av) != 1 {
				panic(ErrInvalid{"Nested arrays may not be passed to whereIn method."})
			}
			flat = append(flat, av[0])
		case map[string]interface{}:
			if len(av) != 1 {
				panic(ErrInvalid{"Nested arrays may not be passed to whereIn method."})
			}
			for _, k := range sortedColumnNames(av) {
				flat = append(flat, av[k])
			}
		default:
			flat = append(flat, v)
		}
	}

	b.Query.Wheres = append(b.Query.Wheres, w)
	b.AddBinding(cleanBindings(flat), "where")
	return b
}

// WhereIntegerInRaw / OrWhereIntegerInRaw / WhereIntegerNotInRaw / OrWhereIntegerNotInRaw
// 对应 Laravel Builder.php:1560-1604：整型值以 raw 形式内联进 SQL，不产生绑定。
func (b *Builder) WhereIntegerInRaw(column interface{}, values []interface{}) *Builder {
	return b.whereIntegerInRawImpl(column, values, "and", false)
}

func (b *Builder) OrWhereIntegerInRaw(column interface{}, values []interface{}) *Builder {
	return b.whereIntegerInRawImpl(column, values, "or", false)
}

func (b *Builder) WhereIntegerNotInRaw(column interface{}, values []interface{}) *Builder {
	return b.whereIntegerInRawImpl(column, values, "and", true)
}

func (b *Builder) OrWhereIntegerNotInRaw(column interface{}, values []interface{}) *Builder {
	return b.whereIntegerInRawImpl(column, values, "or", true)
}

func (b *Builder) whereIntegerInRawImpl(column interface{}, values []interface{}, boolean string, not bool) *Builder {
	typ := "InRaw"
	if not {
		typ = "NotInRaw"
	}
	ints := make([]interface{}, 0, len(values))
	for _, v := range flattenBindings(values) {
		ints = append(ints, toIntValue(v))
	}
	b.Query.Wheres = append(b.Query.Wheres, &query.Where{
		Type:    typ,
		Column:  column,
		Values:  ints,
		Boolean: boolean,
	})
	return b
}

// toIntValue 对应 Laravel 的 (int) enum_value($value) 强转。
func toIntValue(v interface{}) interface{} {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int8:
		return int64(n)
	case int16:
		return int64(n)
	case int32:
		return int64(n)
	case int64:
		return n
	case uint:
		return int64(n)
	case uint8:
		return int64(n)
	case uint16:
		return int64(n)
	case uint32:
		return int64(n)
	case uint64:
		return int64(n)
	case float32:
		return int64(n)
	case float64:
		return int64(n)
	case string:
		if parsed, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil {
			return parsed
		}
		return int64(0)
	default:
		return int64(0)
	}
}

// WhereLike / OrWhereLike / WhereNotLike / OrWhereNotLike
// 对应 Laravel Builder.php:1379-1432。
func (b *Builder) WhereLike(column interface{}, value interface{}, caseSensitive bool) *Builder {
	return b.addWhereLike(column, value, caseSensitive, "and", false)
}

func (b *Builder) OrWhereLike(column interface{}, value interface{}, caseSensitive bool) *Builder {
	return b.addWhereLike(column, value, caseSensitive, "or", false)
}

func (b *Builder) WhereNotLike(column interface{}, value interface{}, caseSensitive bool) *Builder {
	return b.addWhereLike(column, value, caseSensitive, "and", true)
}

func (b *Builder) OrWhereNotLike(column interface{}, value interface{}, caseSensitive bool) *Builder {
	return b.addWhereLike(column, value, caseSensitive, "or", true)
}

func (b *Builder) addWhereLike(column interface{}, value interface{}, caseSensitive bool, boolean string, not bool) *Builder {
	b.Query.Wheres = append(b.Query.Wheres, &query.Where{
		Type:          "Like",
		Column:        column,
		Value:         value,
		CaseSensitive: caseSensitive,
		Boolean:       boolean,
		Not:           not,
	})
	b.AddBinding(value, "where")
	return b
}

// WhereNull / OrWhereNull / WhereNotNull / OrWhereNotNull
// 对应 Laravel whereNull($columns, $boolean='and', $not=false)：columns 支持单列、
// []string / []interface{} 多列（每列一条 where），第二参数为 boolean。
func (b *Builder) WhereNull(columns interface{}, args ...interface{}) *Builder {
	boolean := "and"
	if len(args) > 0 {
		if s, ok := args[0].(string); ok {
			boolean = s
		}
	}
	return b.whereNullImpl(columns, boolean, false)
}

func (b *Builder) OrWhereNull(columns interface{}) *Builder {
	return b.whereNullImpl(columns, "or", false)
}

func (b *Builder) WhereNotNull(columns interface{}, args ...interface{}) *Builder {
	boolean := "and"
	if len(args) > 0 {
		if s, ok := args[0].(string); ok {
			boolean = s
		}
	}
	return b.whereNullImpl(columns, boolean, true)
}

func (b *Builder) OrWhereNotNull(columns interface{}) *Builder {
	return b.whereNullImpl(columns, "or", true)
}

func (b *Builder) whereNullImpl(columns interface{}, boolean string, not bool) *Builder {
	typ := "Null"
	if not {
		typ = "NotNull"
	}
	for _, column := range wrapColumns(columns) {
		b.Query.Wheres = append(b.Query.Wheres, &query.Where{
			Type:    typ,
			Column:  column,
			Boolean: boolean,
		})
	}
	return b
}

// wrapColumns 把 string / []string / []interface{} 归一为 []interface{}（Arr::wrap）。
func wrapColumns(columns interface{}) []interface{} {
	switch c := columns.(type) {
	case []string:
		out := make([]interface{}, len(c))
		for i, v := range c {
			out[i] = v
		}
		return out
	case []interface{}:
		return c
	default:
		return []interface{}{columns}
	}
}

// WhereBetween / OrWhereBetween / WhereNotBetween / OrWhereNotBetween
func (b *Builder) WhereBetween(column interface{}, values []interface{}, args ...interface{}) *Builder {
	return b.whereBetweenImpl(column, values, "and", false)
}

func (b *Builder) OrWhereBetween(column interface{}, values []interface{}) *Builder {
	return b.whereBetweenImpl(column, values, "or", false)
}

func (b *Builder) WhereNotBetween(column interface{}, values []interface{}, args ...interface{}) *Builder {
	return b.whereBetweenImpl(column, values, "and", true)
}

func (b *Builder) OrWhereNotBetween(column interface{}, values []interface{}) *Builder {
	return b.whereBetweenImpl(column, values, "or", true)
}

func (b *Builder) whereBetweenImpl(column interface{}, values []interface{}, boolean string, not bool) *Builder {
	// 子查询列：whereBetween(sub, values)
	if b.isQueryable(column) {
		sql, bindings := b.createSub(column)
		b.AddBinding(bindings, "where")
		return b.whereBetweenImpl(grammar.NewExpression("("+sql+")"), values, boolean, not)
	}

	b.Query.Wheres = append(b.Query.Wheres, &query.Where{
		Type:    "between",
		Column:  column,
		Values:  values,
		Boolean: boolean,
		Not:     not,
	})

	// 绑定：扁平化后取前两个（对应 Laravel 的 array_slice(cleanBindings(flatten), 0, 2)）
	var flat []interface{}
	var flatten func(items []interface{})
	flatten = func(items []interface{}) {
		for _, v := range items {
			if inner, ok := v.([]interface{}); ok {
				flatten(inner)
				continue
			}
			flat = append(flat, v)
		}
	}
	flatten(values)
	bound := flat
	if len(bound) > 2 {
		bound = bound[:2]
	}
	b.AddBinding(cleanBindings(bound), "where")
	return b
}

// WhereBetweenColumns / OrWhereBetweenColumns / WhereNotBetweenColumns / OrWhereNotBetweenColumns
// 对应 Laravel Builder.php:1696-1714：两列比较的 between，values 为列名，不产生绑定。
func (b *Builder) WhereBetweenColumns(column interface{}, values []interface{}) *Builder {
	return b.whereBetweenColumnsImpl(column, values, "and", false)
}

func (b *Builder) OrWhereBetweenColumns(column interface{}, values []interface{}) *Builder {
	return b.whereBetweenColumnsImpl(column, values, "or", false)
}

func (b *Builder) WhereNotBetweenColumns(column interface{}, values []interface{}) *Builder {
	return b.whereBetweenColumnsImpl(column, values, "and", true)
}

func (b *Builder) OrWhereNotBetweenColumns(column interface{}, values []interface{}) *Builder {
	return b.whereBetweenColumnsImpl(column, values, "or", true)
}

func (b *Builder) whereBetweenColumnsImpl(column interface{}, values []interface{}, boolean string, not bool) *Builder {
	// 子查询列：whereBetweenColumns(sub, values)
	if b.isQueryable(column) {
		sql, bindings := b.createSub(column)
		b.AddBinding(bindings, "where")
		return b.whereBetweenColumnsImpl(grammar.NewExpression("("+sql+")"), values, boolean, not)
	}
	b.Query.Wheres = append(b.Query.Wheres, &query.Where{
		Type:    "betweenColumns",
		Column:  column,
		Values:  values,
		Boolean: boolean,
		Not:     not,
	})
	return b
}

// When / Unless 条件执行（对应 Laravel 的 when/unless；Go 侧条件为 bool）。
func (b *Builder) When(condition bool, callback func(*Builder), defaultCallback ...func(*Builder)) *Builder {
	if condition {
		callback(b)
	} else if len(defaultCallback) > 0 {
		defaultCallback[0](b)
	}
	return b
}

func (b *Builder) Unless(condition bool, callback func(*Builder), defaultCallback ...func(*Builder)) *Builder {
	return b.When(!condition, callback, defaultCallback...)
}

// WhereNested 括号分组：WhereNested(func(nb *Builder){ nb.Where(...) })。
func (b *Builder) WhereNested(callback func(*Builder), boolean ...string) *Builder {
	bol := "and"
	if len(boolean) > 0 && boolean[0] != "" {
		bol = boolean[0]
	}
	sub := b.forNestedWhere()
	callback(sub)
	return b.addNestedWhereQuery(sub, bol)
}

func (b *Builder) OrWhereNested(callback func(*Builder)) *Builder {
	return b.WhereNested(callback, "or")
}

func (b *Builder) forNestedWhere() *Builder {
	sub := NewBuilder(b.Connection, b.grammar)
	sub.Query.JoinClause = b.Query.JoinClause
	sub.Query.From = b.Query.From
	return sub
}

func (b *Builder) addNestedWhereQuery(sub *Builder, boolean string) *Builder {
	if len(sub.Query.Wheres) > 0 {
		b.Query.Wheres = append(b.Query.Wheres, &query.Where{
			Type:    "Nested",
			Query:   sub.Query,
			Boolean: boolean,
		})
		// 嵌套子查询的 where 绑定并入父查询（对应 Laravel 的 addBinding 合并）
		b.AddBinding(sub.GetRawBindings()["where"], "where")
	}
	return b
}

// WhereExists / OrWhereExists / WhereNotExists / OrWhereNotExists
// 对应 Laravel Builder.php:2199-2253；q 可为 func(*Builder)/func(*Builder) *Builder/
// *Builder/*query.Query 闭包或子查询。
func (b *Builder) WhereExists(q interface{}) *Builder {
	return b.whereExistsImpl(q, "and", false)
}

func (b *Builder) OrWhereExists(q interface{}) *Builder {
	return b.whereExistsImpl(q, "or", false)
}

func (b *Builder) WhereNotExists(q interface{}) *Builder {
	return b.whereExistsImpl(q, "and", true)
}

func (b *Builder) OrWhereNotExists(q interface{}) *Builder {
	return b.whereExistsImpl(q, "or", true)
}

func (b *Builder) whereExistsImpl(q interface{}, boolean string, not bool) *Builder {
	// 闭包：在全新的子 builder 上构造（对应 Laravel 的 forSubQuery）
	switch q.(type) {
	case func(*Builder), func(*Builder) *Builder:
		sub := NewBuilder(b.Connection, b.grammar)
		sub = applySubQuery(sub, q)
		return b.AddWhereExistsQuery(sub, boolean, not)
	default:
		sub, ok := q.(*Builder)
		if !ok {
			nb := NewBuilder(b.Connection, b.grammar)
			nb = applySubQuery(nb, q)
			sub = nb
		}
		return b.AddWhereExistsQuery(sub, boolean, not)
	}
}

// AddWhereExistsQuery 对应 Laravel Builder.php:2260-2269。
func (b *Builder) AddWhereExistsQuery(sub *Builder, boolean string, not bool) *Builder {
	typ := "Exists"
	if not {
		typ = "NotExists"
	}
	b.Query.Wheres = append(b.Query.Wheres, &query.Where{
		Type:    typ,
		Query:   sub.Query,
		Boolean: boolean,
	})
	b.AddBinding(sub.GetBindings(), "where")
	return b
}

// WhereRowValues / OrWhereRowValues 对应 Laravel Builder.php:2279-2313：
// 行值比较 (a, b) = (?, ?)；列数与值数必须一致。
func (b *Builder) WhereRowValues(columns []interface{}, operator string, values []interface{}) *Builder {
	return b.whereRowValuesImpl(columns, operator, values, "and")
}

func (b *Builder) OrWhereRowValues(columns []interface{}, operator string, values []interface{}) *Builder {
	return b.whereRowValuesImpl(columns, operator, values, "or")
}

func (b *Builder) whereRowValuesImpl(columns []interface{}, operator string, values []interface{}, boolean string) *Builder {
	if len(columns) != len(values) {
		panic(ErrInvalid{"The number of columns must match the number of values"})
	}
	b.Query.Wheres = append(b.Query.Wheres, &query.Where{
		Type:     "RowValues",
		Columns:  columns,
		Operator: operator,
		Values:   values,
		Boolean:  boolean,
	})
	b.AddBinding(cleanBindings(values), "where")
	return b
}

// ---- GroupBy / Having ----

func (b *Builder) GroupBy(groups ...interface{}) *Builder {
	if len(groups) == 1 {
		if gs, ok := groups[0].([]string); ok {
			for _, g := range gs {
				b.Query.Groups = append(b.Query.Groups, g)
			}
			return b
		}
	}
	for _, group := range groups {
		b.Query.Groups = append(b.Query.Groups, group)
	}
	return b
}

func (b *Builder) GroupByRaw(sql string, bindings ...interface{}) *Builder {
	b.Query.Groups = append(b.Query.Groups, grammar.NewExpression(sql))
	if len(bindings) != 0 {
		b.AddBinding(bindings, "groupBy")
	}
	return b
}

// Having 基础 having。
func (b *Builder) Having(column interface{}, args ...interface{}) *Builder {
	operator, value := parseWhereArgs(args...)
	if invalidOperator(operator) {
		operator, value = "=", operator
	}
	b.Query.Havings = append(b.Query.Havings, &query.Where{
		Type:     "Basic",
		Column:   column,
		Operator: operator,
		Value:    value,
		Boolean:  "and",
	})
	if !b.grammar.IsExpression(value) {
		b.AddBinding(flattenValue(value), "having")
	}
	return b
}

func (b *Builder) OrHaving(column interface{}, args ...interface{}) *Builder {
	operator, value := parseWhereArgs(args...)
	if invalidOperator(operator) {
		operator, value = "=", operator
	}
	b.Query.Havings = append(b.Query.Havings, &query.Where{
		Type:     "Basic",
		Column:   column,
		Operator: operator,
		Value:    value,
		Boolean:  "or",
	})
	if !b.grammar.IsExpression(value) {
		b.AddBinding(flattenValue(value), "having")
	}
	return b
}

// HavingRaw / OrHavingRaw（对应 Laravel havingRaw($sql, $bindings=[], $boolean='and)：
// 可选末位 string 参数为 boolean 关键字。
func (b *Builder) HavingRaw(sql interface{}, bindings ...interface{}) *Builder {
	boolean, bindings := splitRawBoolean("and", bindings)
	b.Query.Havings = append(b.Query.Havings, &query.Where{
		Type:    "Raw",
		Sql:     sql,
		Boolean: boolean,
	})
	if len(bindings) != 0 {
		b.AddBinding(bindings, "having")
	}
	return b
}

func (b *Builder) OrHavingRaw(sql interface{}, bindings ...interface{}) *Builder {
	boolean, bindings := splitRawBoolean("or", bindings)
	b.Query.Havings = append(b.Query.Havings, &query.Where{
		Type:    "Raw",
		Sql:     sql,
		Boolean: boolean,
	})
	if len(bindings) != 0 {
		b.AddBinding(bindings, "having")
	}
	return b
}

// HavingNested / OrHavingNested / addNestedHavingQuery 对应 Laravel Builder.php:2830-2858。
func (b *Builder) HavingNested(callback func(*Builder), boolean ...string) *Builder {
	bol := "and"
	if len(boolean) > 0 && boolean[0] != "" {
		bol = boolean[0]
	}
	sub := b.forNestedWhere()
	callback(sub)
	return b.addNestedHavingQuery(sub, bol)
}

func (b *Builder) OrHavingNested(callback func(*Builder)) *Builder {
	return b.HavingNested(callback, "or")
}

func (b *Builder) addNestedHavingQuery(sub *Builder, boolean string) *Builder {
	if len(sub.Query.Havings) > 0 {
		b.Query.Havings = append(b.Query.Havings, &query.Where{
			Type:    "Nested",
			Query:   sub.Query,
			Boolean: boolean,
		})
		b.AddBinding(sub.GetRawBindings()["having"], "having")
	}
	return b
}

// HavingNull / OrHavingNull / HavingNotNull / OrHavingNotNull 对应 Laravel
// Builder.php:2867-2913：columns 支持多列（每列一条 having）。
func (b *Builder) HavingNull(columns interface{}, args ...interface{}) *Builder {
	boolean := "and"
	if len(args) > 0 {
		if s, ok := args[0].(string); ok {
			boolean = s
		}
	}
	return b.havingNullImpl(columns, boolean, false)
}

func (b *Builder) OrHavingNull(columns interface{}) *Builder {
	return b.havingNullImpl(columns, "or", false)
}

func (b *Builder) HavingNotNull(columns interface{}, args ...interface{}) *Builder {
	boolean := "and"
	if len(args) > 0 {
		if s, ok := args[0].(string); ok {
			boolean = s
		}
	}
	return b.havingNullImpl(columns, boolean, true)
}

func (b *Builder) OrHavingNotNull(columns interface{}) *Builder {
	return b.havingNullImpl(columns, "or", true)
}

func (b *Builder) havingNullImpl(columns interface{}, boolean string, not bool) *Builder {
	typ := "Null"
	if not {
		typ = "NotNull"
	}
	for _, column := range wrapColumns(columns) {
		b.Query.Havings = append(b.Query.Havings, &query.Where{
			Type:    typ,
			Column:  column,
			Boolean: boolean,
		})
	}
	return b
}

// HavingBetween / OrHavingBetween / HavingNotBetween / OrHavingNotBetween
// 对应 Laravel Builder.php:2921-2950：绑定取 flatten 后前两个。
func (b *Builder) HavingBetween(column interface{}, values []interface{}) *Builder {
	return b.havingBetweenImpl(column, values, "and", false)
}

func (b *Builder) OrHavingBetween(column interface{}, values []interface{}) *Builder {
	return b.havingBetweenImpl(column, values, "or", false)
}

func (b *Builder) HavingNotBetween(column interface{}, values []interface{}) *Builder {
	return b.havingBetweenImpl(column, values, "and", true)
}

func (b *Builder) OrHavingNotBetween(column interface{}, values []interface{}) *Builder {
	return b.havingBetweenImpl(column, values, "or", true)
}

func (b *Builder) havingBetweenImpl(column interface{}, values []interface{}, boolean string, not bool) *Builder {
	b.Query.Havings = append(b.Query.Havings, &query.Where{
		Type:    "between",
		Column:  column,
		Values:  values,
		Boolean: boolean,
		Not:     not,
	})
	flat := flattenBindings(values)
	if len(flat) > 2 {
		flat = flat[:2]
	}
	b.AddBinding(cleanBindings(flat), "having")
	return b
}

// flattenBindings 对应 Laravel 的 Arr::flatten（一层层展平嵌套数组）。
func flattenBindings(values []interface{}) []interface{} {
	var out []interface{}
	var flatten func(items []interface{})
	flatten = func(items []interface{}) {
		for _, v := range items {
			if inner, ok := v.([]interface{}); ok {
				flatten(inner)
				continue
			}
			out = append(out, v)
		}
	}
	flatten(values)
	return out
}

// ---- Order ----

// OrderBy 排序；direction 只接受 asc/desc（大小写不敏感），非法值 panic（对应 Laravel InvalidArgumentException）。
func (b *Builder) OrderBy(column interface{}, direction ...string) *Builder {
	dir := "asc"
	if len(direction) > 0 {
		dir = strings.ToLower(direction[0])
		if dir != "asc" && dir != "desc" {
			panic(ErrInvalid{"Order direction must be \"asc\" or \"desc\""})
		}
	}
	order := &query.Order{Column: column, Direction: dir}
	if len(b.Query.Unions) > 0 {
		b.Query.UnionOrders = append(b.Query.UnionOrders, order)
	} else {
		b.Query.Orders = append(b.Query.Orders, order)
	}
	return b
}

func (b *Builder) OrderByDesc(column interface{}) *Builder {
	return b.OrderBy(column, "desc")
}

func (b *Builder) OrderByRaw(sql string, bindings ...interface{}) *Builder {
	order := &query.Order{Sql: grammar.NewExpression(sql)}
	if len(b.Query.Unions) > 0 {
		b.Query.UnionOrders = append(b.Query.UnionOrders, order)
	} else {
		b.Query.Orders = append(b.Query.Orders, order)
	}
	if len(bindings) != 0 {
		b.AddBinding(bindings, "order")
	}
	return b
}

// Oldest / Latest / InRandomOrder / Reorder
func (b *Builder) Oldest(column ...string) *Builder {
	col := "created_at"
	if len(column) > 0 {
		col = column[0]
	}
	return b.OrderBy(col, "asc")
}

func (b *Builder) Latest(column ...string) *Builder {
	col := "created_at"
	if len(column) > 0 {
		col = column[0]
	}
	return b.OrderBy(col, "desc")
}

func (b *Builder) InRandomOrder(seed ...interface{}) *Builder {
	var s interface{}
	if len(seed) > 0 {
		s = seed[0]
	}
	b.Query.Orders = append(b.Query.Orders, &query.Order{
		Sql: grammar.NewExpression(b.grammar.CompileRandom(s)),
	})
	return b
}

// Reorder 清除或重设排序（对应 Laravel Builder.php:3319-3331）：
// 清空 orders、unionOrders 及 order/unionOrder 段绑定；Reorder(col, dir...) 重设。
func (b *Builder) Reorder(column ...interface{}) *Builder {
	b.Query.Orders = nil
	b.Query.UnionOrders = nil
	b.Bindings["order"] = nil
	b.Bindings["unionOrder"] = nil
	if len(column) == 0 {
		return b
	}
	if len(column) == 1 {
		return b.OrderBy(column[0])
	}
	dir, _ := column[1].(string)
	return b.OrderBy(column[0], dir)
}

// ---- Limit / Offset ----

func (b *Builder) Limit(value uint64) *Builder {
	v := int64(value)
	if len(b.Query.Unions) > 0 {
		b.Query.UnionLimit = &v
	} else {
		b.Query.Limit = &v
	}
	return b
}

func (b *Builder) Take(value uint64) *Builder { return b.Limit(value) }

func (b *Builder) Offset(value uint64) *Builder {
	v := int64(value)
	if len(b.Query.Unions) > 0 {
		b.Query.UnionOffset = &v
	} else {
		b.Query.Offset = &v
	}
	return b
}

func (b *Builder) Skip(value uint64) *Builder { return b.Offset(value) }

// ForPage 分页：page 从 1 开始。
func (b *Builder) ForPage(page, perPage uint64) *Builder {
	return b.Offset((page - 1) * perPage).Limit(perPage)
}

// ---- Lock ----

func (b *Builder) Lock(value interface{}) *Builder {
	b.Query.Lock = value
	return b
}

func (b *Builder) LockForUpdate() *Builder { return b.Lock(true) }

func (b *Builder) SharedLock() *Builder { return b.Lock(false) }

// ---- Union ----

func (b *Builder) Union(q interface{}) *Builder {
	return b.addUnion(q, false)
}

func (b *Builder) UnionAll(q interface{}) *Builder {
	return b.addUnion(q, true)
}

func (b *Builder) addUnion(q interface{}, all bool) *Builder {
	sub := b.resolveUnionQuery(q)
	b.Query.Unions = append(b.Query.Unions, &query.Union{All: all, Query: sub.Query})
	b.AddBinding(sub.GetBindings(), "union")
	return b
}

func (b *Builder) resolveUnionQuery(q interface{}) *Builder {
	switch v := q.(type) {
	case *Builder:
		return v
	case func(*Builder), func(*Builder) *Builder:
		sub := NewBuilder(b.Connection, b.grammar)
		return applySubQuery(sub, v)
	default:
		return NewBuilder(b.Connection, b.grammar).FromRaw(q)
	}
}

// ---- 聚合 ----

// SetAggregate 设置聚合组件（对应 Laravel 公开的 setAggregate）：
// groups 为空时清除 orders 与 order 段绑定（Laravel Builder.php:4166-4176）。
func (b *Builder) SetAggregate(function string, columns []interface{}) *Builder {
	if len(columns) == 0 {
		columns = []interface{}{"*"}
	}
	b.Query.Aggregate = &query.Aggregate{Function: function, Columns: columns}
	if len(b.Query.Groups) == 0 {
		b.Query.Orders = nil
		b.Bindings["order"] = nil
	}
	return b
}

func isWildcardColumns(columns []interface{}) bool {
	return len(columns) == 1 && fmtString(columns[0]) == "*"
}

// Aggregate 在克隆上执行聚合，不污染原查询（对应 Laravel Builder.php:4124-4134）：
// 无 unions/havings 时 cloneWithout(['columns']) + cloneWithoutBindings(['select'])，
// 再由 setAggregate 在 groups 为空时清除 orders 与 order 段绑定。
func (b *Builder) Aggregate(function string, columns []interface{}, dest ...interface{}) error {
	nb := b.Clone()
	if len(nb.Query.Unions) == 0 && len(nb.Query.Havings) == 0 {
		nb.Query.Columns = nil
		nb.Bindings["select"] = nil
	}
	nb.SetAggregate(function, columns)
	return nb.Scan(dest...)
}

// Count 检索 count 结果，写入 dest 指针。
func (b *Builder) Count(dest ...interface{}) error {
	return b.Aggregate("count", []interface{}{"*"}, dest...)
}

func (b *Builder) Min(columns string, dest ...interface{}) error {
	return b.Aggregate("min", []interface{}{columns}, dest...)
}

func (b *Builder) Max(columns string, dest ...interface{}) error {
	return b.Aggregate("max", []interface{}{columns}, dest...)
}

func (b *Builder) Sum(columns string, dest ...interface{}) error {
	return b.Aggregate("sum", []interface{}{columns}, dest...)
}

func (b *Builder) Avg(columns string, dest ...interface{}) error {
	return b.Aggregate("avg", []interface{}{columns}, dest...)
}

// ---- 获取结果 ----

// Get 执行查询并扫描进 dest；columns 可为 string / []string / []interface{}。
func (b *Builder) Get(dest interface{}, columns ...interface{}) error {
	b.applyModelScopes()
	original := b.Query.Columns

	if len(columns) > 0 {
		cols, err := normalizeColumns(columns...)
		if err != nil {
			return err
		}
		if original == nil {
			b.Query.Columns = cols
		}
	}

	err := b.runSelect(dest)
	b.Query.Columns = original
	if err == nil {
		b.Connection.fireRetrieved(dest)
		b.loadRelations(dest)
	}
	return err
}

// First 取第一行。
func (b *Builder) First(dest interface{}, columns ...interface{}) error {
	return b.Take(1).Get(dest, columns...)
}

// Scan 直接以当前查询执行扫描（聚合等场景）。
func (b *Builder) Scan(dest ...interface{}) error {
	b.applyModelScopes()
	return b.runScan(dest...)
}

// Exists 对应 Laravel exists()：select exists(...) as "exists"。
func (b *Builder) Exists() (bool, error) {
	sql := b.grammar.CompileExists(b.Query)
	var v bool
	if err := b.Connection.Scan(sql, b.GetBindings(), &v); err != nil {
		return false, err
	}
	return v, nil
}

func (b *Builder) DoesntExist() (bool, error) {
	v, err := b.Exists()
	if err != nil {
		return false, err
	}
	return !v, nil
}

// ---- 写入 ----

// normalizeRows 把 map / query.Row 混合输入归一为 []query.Row。
// map 无序 → 按 key 排序；Row 保序（对应 PHP 有序数组）。
func normalizeRows(values []interface{}) []query.Row {
	rows := make([]query.Row, 0, len(values))
	for _, v := range values {
		switch r := v.(type) {
		case query.Row:
			rows = append(rows, r)
		case map[string]interface{}:
			rows = append(rows, query.ToRow(r))
		default:
			panic(ErrInvalid{"unsupported insert row type"})
		}
	}
	return rows
}

func toRow(v interface{}) query.Row {
	switch r := v.(type) {
	case query.Row:
		return r
	case map[string]interface{}:
		return query.ToRow(r)
	default:
		panic(ErrInvalid{"unsupported insert row type"})
	}
}

// Insert 插入单行：map（确定性排序）或 query.Row（保序）。
func (b *Builder) Insert(value interface{}) (int64, int64, error) {
	return b.Inserts([]interface{}{value})
}

// Inserts 批量插入：元素为 map 或 query.Row。
func (b *Builder) Inserts(values []interface{}) (int64, int64, error) {
	sql, bindings := b.grammar.CompileInsert(b.Query, normalizeRows(values))
	return b.Connection.Insert(sql, bindings...)
}

// InsertGetId 插入并返回自增 id。
func (b *Builder) InsertGetId(value interface{}, sequence ...string) (int64, error) {
	seq := ""
	if len(sequence) > 0 {
		seq = sequence[0]
	}
	sql, bindings := b.grammar.CompileInsertGetId(b.Query, []query.Row{toRow(value)}, seq)
	return b.Connection.InsertGetId(sql, bindings...)
}

// InsertOrIgnore 插入并忽略冲突行。
func (b *Builder) InsertOrIgnore(values []interface{}) (int64, error) {
	sql, bindings := b.grammar.CompileInsertOrIgnore(b.Query, normalizeRows(values))
	n, _, err := b.Connection.Insert(sql, bindings...)
	return n, err
}

// InsertUsing insert ... select（对应 Laravel Builder.php:4322-4332）：
// 只使用子查询的 cleanBindings 执行，不并入 builder 的绑定段。
func (b *Builder) InsertUsing(columns []string, q interface{}) (int64, error) {
	sql, bindings := b.createSub(q)
	cols := make([]interface{}, len(columns))
	for i, c := range columns {
		cols[i] = c
	}
	stmt := b.grammar.CompileInsertUsing(b.Query, cols, sql)
	n, _, err := b.Connection.Insert(stmt, cleanBindings(bindings)...)
	return n, err
}

// InsertOrIgnoreUsing insert ignore ... select（对应 Laravel Builder.php:4340-4350）。
func (b *Builder) InsertOrIgnoreUsing(columns []string, q interface{}) (int64, error) {
	sql, bindings := b.createSub(q)
	cols := make([]interface{}, len(columns))
	for i, c := range columns {
		cols[i] = c
	}
	stmt := b.grammar.CompileInsertOrIgnoreUsing(b.Query, cols, sql)
	n, _, err := b.Connection.Insert(stmt, cleanBindings(bindings)...)
	return n, err
}

// Upsert upsert（对应 Laravel Builder.php:4434-4473）：uniqueBy 为唯一键列，update 为冲突时更新的列。
// update 为空切片 → 普通 insert；update 为 nil → 全列更新（取首行的列）；
// uniqueBy 为空抛错；values 为空返回 0。
// map 行按 key 排序（对应 Laravel 的 ksort）；query.Row 保序。
func (b *Builder) Upsert(values []interface{}, uniqueBy, update []string) (int64, error) {
	if len(uniqueBy) == 0 {
		return 0, ErrInvalid{"The unique columns must not be empty."}
	}
	if len(values) == 0 {
		return 0, nil
	}
	if update != nil && len(update) == 0 {
		n, _, err := b.Inserts(values)
		return n, err
	}
	if update == nil {
		// 对应 Laravel 的 $update = array_keys(array_first($values))
		first := normalizeRows(values[:1])[0]
		update = first.Columns()
	}
	sql, bindings := b.grammar.CompileUpsert(b.Query, normalizeRows(values), uniqueBy, update)
	n, _, err := b.Connection.Insert(sql, bindings...)
	return n, err
}

// Update 更新记录。
func (b *Builder) Update(value map[string]interface{}) (int64, error) {
	sql := b.grammar.CompileUpdate(b.Query, value)
	cleanBindings := cleanBindings(b.grammar.PrepareBindingsForUpdate(b.Bindings, value))
	return b.Connection.Update(sql, cleanBindings...)
}

// UpdateOrInsert 存在则更新，否则插入（对应 Laravel updateOrInsert）。
func (b *Builder) UpdateOrInsert(attributes, value map[string]interface{}) (bool, error) {
	q := b.Clone()
	for _, col := range sortedColumnNames(attributes) {
		q.Where(col, attributes[col])
	}
	exists, err := q.Exists()
	if err != nil {
		return false, err
	}
	if !exists {
		if _, _, err := b.Clone().Insert(mergeMaps(attributes, value)); err != nil {
			return false, err
		}
		return true, nil
	}
	if len(value) == 0 {
		return true, nil
	}
	if _, err := q.Take(1).Update(value); err != nil {
		return false, err
	}
	return true, nil
}

// Increment / Decrement 数值增减（对应 Laravel increment：列包裹后拼算式，负数转减法）。
func (b *Builder) Increment(column string, amount ...interface{}) (int64, error) {
	amountInt := int64(1)
	extra := map[string]interface{}{}
	if len(amount) > 0 {
		switch v := amount[0].(type) {
		case int:
			amountInt = int64(v)
		case int64:
			amountInt = v
		case float64:
			amountInt = int64(v)
		}
		if len(amount) > 1 {
			if m, ok := amount[1].(map[string]interface{}); ok {
				extra = m
			}
		}
	}

	wrapped := b.grammar.Wrap(column)
	var expr string
	if amountInt < 0 {
		expr = wrapped + " - " + fmtInt(-amountInt)
	} else {
		expr = wrapped + " + " + fmtInt(amountInt)
	}

	value := map[string]interface{}{column: grammar.NewExpression(expr)}
	for k, v := range extra {
		value[k] = v
	}

	nb := b.Clone()
	return nb.Update(value)
}

func (b *Builder) Decrement(column string, amount ...interface{}) (int64, error) {
	amountInt := int64(1)
	if len(amount) > 0 {
		switch v := amount[0].(type) {
		case int:
			amountInt = int64(v)
		case int64:
			amountInt = v
		case float64:
			amountInt = int64(v)
		}
	}
	return b.Increment(column, -amountInt)
}

// Truncate 清空表。
func (b *Builder) Truncate() error {
	for _, stmt := range b.grammar.CompileTruncate(b.Query) {
		if err := b.Connection.Statement(stmt.Sql, stmt.Bindings...); err != nil {
			return err
		}
	}
	return nil
}

// Delete 删除；可传主键值（对应 delete($id)）。
func (b *Builder) Delete(args ...interface{}) (int64, error) {
	if len(args) > 0 && args[0] != nil {
		from := fmtString(b.Query.From)
		b.Where(from+".id", "=", args[0])
	}
	sql := b.grammar.CompileDelete(b.Query)
	return b.Connection.Delete(sql, b.grammar.PrepareBindingsForDelete(b.Bindings)...)
}

// ---- SQL 与绑定 ----

func (b *Builder) ToSql() string {
	return b.grammar.CompileSelect(b.Query)
}

// BuilderAddBinding 添加绑定；未知段 panic（对应 Laravel InvalidArgumentException）。
func (b *Builder) BuilderAddBinding(value interface{}, segment string) {
	if _, ok := b.Bindings[segment]; !ok {
		panic(ErrInvalid{"Invalid binding type: " + segment})
	}
	switch v := value.(type) {
	case grammar.Expression:
		return
	case []interface{}:
		for _, item := range v {
			if b.grammar.IsExpression(item) {
				continue
			}
			b.Bindings[segment] = append(b.Bindings[segment], item)
		}
	default:
		b.Bindings[segment] = append(b.Bindings[segment], value)
	}
}

// AddBinding 添加绑定（导出入口）。
func (b *Builder) AddBinding(value interface{}, segment string) {
	b.BuilderAddBinding(value, segment)
}

// GetBindings 按固定段顺序（select→from→join→where→groupBy→having→order→union→unionOrder）合并绑定。
func (b *Builder) GetBindings() []interface{} {
	bindings := []interface{}{}
	for _, segment := range grammar.BindingSegments {
		for _, v := range b.Bindings[segment] {
			bindings = append(bindings, v)
		}
	}
	return bindings
}

func fmtInt(n int64) string {
	return strconv.FormatInt(n, 10)
}

// GetRawBindings 返回按段分组的原始绑定。
func (b *Builder) GetRawBindings() map[string][]interface{} {
	return b.Bindings
}

// SetBindings 覆盖某段的全部绑定。
func (b *Builder) SetBindings(bindings []interface{}, segment string) {
	if _, ok := b.Bindings[segment]; !ok {
		panic(ErrInvalid{"Invalid binding segment: " + segment})
	}
	b.Bindings[segment] = cleanBindings(bindings)
}

// MergeBindings 合并另一 builder 的绑定。
func (b *Builder) MergeBindings(q *Builder) {
	for _, segment := range grammar.BindingSegments {
		b.Bindings[segment] = append(b.Bindings[segment], q.Bindings[segment]...)
	}
}

// cleanBindings 移除 Expression 绑定。
func cleanBindings(bindings []interface{}) []interface{} {
	var result []interface{}
	for _, v := range bindings {
		if _, ok := v.(grammar.Expression); !ok {
			result = append(result, v)
		}
	}
	return result
}

// ---- 克隆 ----

// Clone 深拷贝查询状态（切片复制，嵌套 Query/Join 引用共享，与 PHP clone 语义一致）。
func (b *Builder) Clone() *Builder {
	nb := *b
	nb.scopesApplied = false
	nb.Query = cloneQuery(b.Query)
	nb.Bindings = cloneBindings(b.Bindings)
	return &nb
}

func cloneQuery(q *query.Query) *query.Query {
	nq := *q
	if q.Columns != nil {
		nq.Columns = append([]interface{}(nil), q.Columns...)
	}
	if q.Groups != nil {
		nq.Groups = append([]interface{}(nil), q.Groups...)
	}
	if q.Wheres != nil {
		nq.Wheres = append([]*query.Where(nil), q.Wheres...)
	}
	if q.Havings != nil {
		nq.Havings = append([]*query.Where(nil), q.Havings...)
	}
	if q.Orders != nil {
		nq.Orders = append([]*query.Order(nil), q.Orders...)
	}
	if q.UnionOrders != nil {
		nq.UnionOrders = append([]*query.Order(nil), q.UnionOrders...)
	}
	if q.Joins != nil {
		nq.Joins = append([]*query.Join(nil), q.Joins...)
	}
	if q.Unions != nil {
		nq.Unions = append([]*query.Union(nil), q.Unions...)
	}
	if q.Withes != nil {
		nq.Withes = append([]*query.With(nil), q.Withes...)
	}
	return &nq
}

func cloneBindings(bindings map[string][]interface{}) map[string][]interface{} {
	nb := make(map[string][]interface{}, len(bindings))
	for k, v := range bindings {
		nb[k] = append([]interface{}(nil), v...)
	}
	return nb
}

// CloneWithout 克隆并清除指定属性（columns/orders/limit/offset/aggregate/distinct/unions）。
func (b *Builder) CloneWithout(properties ...string) *Builder {
	nb := b.Clone()
	for _, property := range properties {
		switch property {
		case "columns":
			nb.Query.Columns = nil
		case "orders":
			nb.Query.Orders = nil
		case "limit":
			nb.Query.Limit = nil
		case "offset":
			nb.Query.Offset = nil
		case "aggregate":
			nb.Query.Aggregate = nil
		case "distinct":
			nb.Query.Distinct = nil
		case "unions":
			nb.Query.Unions = nil
		}
	}
	return nb
}

// CloneWithoutBindings 克隆并移除指定段的绑定。
func (b *Builder) CloneWithoutBindings(except ...string) *Builder {
	nb := b.Clone()
	for _, segment := range except {
		delete(nb.Bindings, segment)
	}
	return nb
}

// ---- 执行 ----

func (b *Builder) runSelect(dest interface{}) error {
	if b.useWritePdo {
		return b.Connection.SelectFromWrite(
			b.ToSql(),
			b.GetBindings(),
			dest,
		)
	}
	return b.Connection.Select(
		b.ToSql(),
		b.GetBindings(),
		dest,
	)
}

func (b *Builder) runScan(dest ...interface{}) error {
	return b.Connection.Scan(
		b.ToSql(),
		b.GetBindings(),
		dest...,
	)
}

// ---- 辅助 ----

func getDefaultBindings() map[string][]interface{} {
	b := make(map[string][]interface{}, len(grammar.BindingSegments))
	for _, segment := range grammar.BindingSegments {
		b[segment] = make([]interface{}, 0)
	}
	return b
}

// parseWhereArgs 解析 (value) / (operator, value)。
func parseWhereArgs(args ...interface{}) (operator string, value interface{}) {
	switch len(args) {
	case 0:
		return "=", nil
	case 1:
		return "=", args[0]
	default:
		return fmtString(args[0]), args[1]
	}
}

// parseColumnArgs 解析 (second) / (operator, second) / (operator, second, boolean)。
func parseColumnArgs(args ...interface{}) (operator string, second interface{}, boolean string) {
	switch len(args) {
	case 0:
		return "=", nil, "and"
	case 1:
		return "=", args[0], "and"
	default:
		operator = fmtString(args[0])
		second = args[1]
		if len(args) > 2 {
			boolean = fmtString(args[2])
		}
		return operator, second, boolean
	}
}

func fmtString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func sortedColumnNames(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}

func mergeMaps(a, b map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func extraValueBindings(extra map[string]interface{}) []interface{} {
	var out []interface{}
	for _, col := range sortedColumnNames(extra) {
		out = append(out, extra[col])
	}
	return out
}

// normalizeColumns 把 Get/First 的 columns 参数归一为 []interface{}。
func normalizeColumns(columns ...interface{}) ([]interface{}, error) {
	if len(columns) == 1 {
		switch v := columns[0].(type) {
		case []string:
			out := make([]interface{}, len(v))
			for i, c := range v {
				out[i] = c
			}
			return out, nil
		case []interface{}:
			return v, nil
		case string:
			return []interface{}{v}, nil
		}
	}
	return columns, nil
}

// ---- 日期时间 where 家族（Laravel whereDate/Time/Day/Month/Year）----

func (b *Builder) addDateBasedWhere(typ string, column interface{}, operator string, value interface{}, boolean string) *Builder {
	b.Query.Wheres = append(b.Query.Wheres, &query.Where{
		Type:     typ,
		Column:   column,
		Operator: operator,
		Value:    value,
		Boolean:  boolean,
	})
	if !b.grammar.IsExpression(value) {
		b.AddBinding(flattenValue(value), "where")
	}
	return b
}

func (b *Builder) dateWhereImpl(typ string, column interface{}, args []interface{}, boolean string) *Builder {
	operator, value := parseWhereArgs(args...)
	if invalidOperator(operator) {
		operator, value = "=", operator
	}
	if value == nil {
		return b.whereNullImpl(column, boolean, operator != "=")
	}
	return b.addDateBasedWhere(typ, column, operator, value, boolean)
}

func (b *Builder) WhereDate(column interface{}, args ...interface{}) *Builder {
	return b.dateWhereImpl("Date", column, args, "and")
}

func (b *Builder) OrWhereDate(column interface{}, args ...interface{}) *Builder {
	return b.dateWhereImpl("Date", column, args, "or")
}

func (b *Builder) WhereTime(column interface{}, args ...interface{}) *Builder {
	return b.dateWhereImpl("Time", column, args, "and")
}

func (b *Builder) OrWhereTime(column interface{}, args ...interface{}) *Builder {
	return b.dateWhereImpl("Time", column, args, "or")
}

func (b *Builder) WhereDay(column interface{}, args ...interface{}) *Builder {
	return b.dateWhereImpl("Day", column, args, "and")
}

func (b *Builder) OrWhereDay(column interface{}, args ...interface{}) *Builder {
	return b.dateWhereImpl("Day", column, args, "or")
}

func (b *Builder) WhereMonth(column interface{}, args ...interface{}) *Builder {
	return b.dateWhereImpl("Month", column, args, "and")
}

func (b *Builder) OrWhereMonth(column interface{}, args ...interface{}) *Builder {
	return b.dateWhereImpl("Month", column, args, "or")
}

func (b *Builder) WhereYear(column interface{}, args ...interface{}) *Builder {
	return b.dateWhereImpl("Year", column, args, "and")
}

func (b *Builder) OrWhereYear(column interface{}, args ...interface{}) *Builder {
	return b.dateWhereImpl("Year", column, args, "or")
}

// ---- JSON where 家族 ----

// prepareJsonBinding 对应 Laravel 的 prepareBindingForJsonContains：数组/对象 → JSON 字符串。
func prepareJsonBinding(value interface{}) interface{} {
	switch value.(type) {
	case []interface{}, map[string]interface{}:
		data, err := json.Marshal(value)
		if err != nil {
			return value
		}
		return string(data)
	default:
		return value
	}
}

func (b *Builder) jsonWhereImpl(typ string, column interface{}, value interface{}, boolean string, not bool) *Builder {
	w := &query.Where{
		Type:    typ,
		Column:  column,
		Value:   value,
		Boolean: boolean,
		Not:     not,
	}
	b.Query.Wheres = append(b.Query.Wheres, w)
	if !b.grammar.IsExpression(value) {
		b.AddBinding(prepareJsonBinding(value), "where")
	}
	return b
}

func (b *Builder) WhereJsonContains(column interface{}, value interface{}) *Builder {
	return b.jsonWhereImpl("JsonContains", column, value, "and", false)
}

func (b *Builder) OrWhereJsonContains(column interface{}, value interface{}) *Builder {
	return b.jsonWhereImpl("JsonContains", column, value, "or", false)
}

func (b *Builder) WhereJsonDoesntContain(column interface{}, value interface{}) *Builder {
	return b.jsonWhereImpl("JsonContains", column, value, "and", true)
}

func (b *Builder) OrWhereJsonDoesntContain(column interface{}, value interface{}) *Builder {
	return b.jsonWhereImpl("JsonContains", column, value, "or", true)
}

func (b *Builder) WhereJsonOverlaps(column interface{}, value interface{}) *Builder {
	return b.jsonWhereImpl("JsonOverlaps", column, value, "and", false)
}

func (b *Builder) OrWhereJsonOverlaps(column interface{}, value interface{}) *Builder {
	return b.jsonWhereImpl("JsonOverlaps", column, value, "or", false)
}

func (b *Builder) WhereJsonDoesntOverlap(column interface{}, value interface{}) *Builder {
	return b.jsonWhereImpl("JsonOverlaps", column, value, "and", true)
}

func (b *Builder) WhereJsonContainsKey(column interface{}) *Builder {
	return b.jsonKeyImpl(column, "and", false)
}

func (b *Builder) OrWhereJsonContainsKey(column interface{}) *Builder {
	return b.jsonKeyImpl(column, "or", false)
}

func (b *Builder) WhereJsonDoesntContainKey(column interface{}) *Builder {
	return b.jsonKeyImpl(column, "and", true)
}

func (b *Builder) OrWhereJsonDoesntContainKey(column interface{}) *Builder {
	return b.jsonKeyImpl(column, "or", true)
}

func (b *Builder) jsonKeyImpl(column interface{}, boolean string, not bool) *Builder {
	b.Query.Wheres = append(b.Query.Wheres, &query.Where{
		Type:    "JsonContainsKey",
		Column:  column,
		Boolean: boolean,
		Not:     not,
	})
	return b
}

func (b *Builder) jsonLengthImpl(column interface{}, args []interface{}, boolean string, not bool) *Builder {
	operator, value := parseWhereArgs(args...)
	if invalidOperator(operator) {
		operator, value = "=", operator
	}
	b.Query.Wheres = append(b.Query.Wheres, &query.Where{
		Type:     "JsonLength",
		Column:   column,
		Operator: operator,
		Value:    value,
		Boolean:  boolean,
		Not:      not,
	})
	if !b.grammar.IsExpression(value) {
		b.AddBinding(value, "where")
	}
	return b
}

func (b *Builder) WhereJsonLength(column interface{}, args ...interface{}) *Builder {
	return b.jsonLengthImpl(column, args, "and", false)
}

func (b *Builder) OrWhereJsonLength(column interface{}, args ...interface{}) *Builder {
	return b.jsonLengthImpl(column, args, "or", false)
}

func (b *Builder) WhereJsonDoesntHaveLength(column interface{}, args ...interface{}) *Builder {
	return b.jsonLengthImpl(column, args, "and", true)
}

func (b *Builder) OrWhereJsonDoesntHaveLength(column interface{}, args ...interface{}) *Builder {
	return b.jsonLengthImpl(column, args, "or", true)
}

// ---- 全文检索 ----

// WhereFullText 对应 Laravel whereFullText($columns, $value, $options, $boolean)。
func (b *Builder) WhereFullText(columns interface{}, value interface{}, options ...map[string]interface{}) *Builder {
	opts := map[string]interface{}{}
	if len(options) > 0 {
		opts = options[0]
	}
	return b.fullTextImpl(columns, value, opts, "and")
}

func (b *Builder) OrWhereFullText(columns interface{}, value interface{}, options ...map[string]interface{}) *Builder {
	opts := map[string]interface{}{}
	if len(options) > 0 {
		opts = options[0]
	}
	return b.fullTextImpl(columns, value, opts, "or")
}

func (b *Builder) fullTextImpl(columns interface{}, value interface{}, options map[string]interface{}, boolean string) *Builder {
	var cols []interface{}
	switch v := columns.(type) {
	case []string:
		for _, c := range v {
			cols = append(cols, c)
		}
	case []interface{}:
		cols = v
	default:
		cols = []interface{}{columns}
	}
	b.Query.Wheres = append(b.Query.Wheres, &query.Where{
		Type:    "FullText",
		Columns: cols,
		Value:   value,
		Options: options,
		Boolean: boolean,
	})
	b.AddBinding(value, "where")
	return b
}

// ---- 读取族补全（value / pluck / sole / firstOr / chunk）----

// Value 取单个值（select col limit 1）。
func (b *Builder) Value(column string) (any, error) {
	nb := b.Clone()
	nb.Query.Columns = []interface{}{column}
	nb.Take(1)
	var dest any
	if err := nb.Scan(&dest); err != nil {
		return nil, err
	}
	return dest, nil
}

// RawValue 取原始表达式的单值。
func (b *Builder) RawValue(expression string) (any, error) {
	nb := b.Clone()
	nb.Query.Columns = []interface{}{grammar.NewExpression(expression)}
	nb.Take(1)
	var dest any
	if err := nb.Scan(&dest); err != nil {
		return nil, err
	}
	return dest, nil
}

// Pluck 取某列的值集合，扫入 dest（*[]T / *[]any / *[]string 等）。
func (b *Builder) Pluck(column string, dest interface{}) error {
	nb := b.Clone()
	nb.Query.Columns = []interface{}{column}
	return nb.runSelect(dest)
}

// Implode 取某列的值并连接为字符串。
func (b *Builder) Implode(column string, glue string) (string, error) {
	var values []any
	if err := b.Pluck(column, &values); err != nil {
		return "", err
	}
	parts := make([]string, 0, len(values))
	for _, v := range values {
		parts = append(parts, fmtString(v))
	}
	return strings.Join(parts, glue), nil
}

// FirstOrFail 未命中返回 ErrNotFound（对应 Laravel firstOrFail）。
func (b *Builder) FirstOrFail(dest interface{}, columns ...interface{}) error {
	if err := b.First(dest, columns...); err != nil {
		return err
	}
	if isEmptyDest(dest) {
		return ErrNotFound{Msg: "No query results for record."}
	}
	return nil
}

// Sole 要求恰好一行；零行 ErrNotFound，多行 ErrMultiple（对应 Laravel sole）。
func (b *Builder) Sole(dest interface{}, columns ...interface{}) error {
	rv := reflect.Indirect(reflect.ValueOf(dest))
	slicePtr := dest
	if rv.Kind() != reflect.Slice {
		slicePtr = reflect.New(reflect.SliceOf(rv.Type())).Interface()
	}

	if err := b.Get(slicePtr, columns...); err != nil {
		return err
	}

	sv := reflect.Indirect(reflect.ValueOf(slicePtr))
	switch sv.Len() {
	case 0:
		return ErrNotFound{Msg: "No query results for record."}
	case 1:
		if rv.Kind() != reflect.Slice {
			rv.Set(sv.Index(0))
		}
		return nil
	default:
		return ErrMultiple{Msg: fmt.Sprintf("Multiple records found (%d).", sv.Len())}
	}
}

// FirstOr 未命中时执行 fallback（对应 Laravel firstOr）。
func (b *Builder) FirstOr(dest interface{}, fallback func() error, columns ...interface{}) error {
	if err := b.First(dest, columns...); err != nil {
		return err
	}
	if isEmptyDest(dest) {
		return fallback()
	}
	return nil
}

// Chunk 分块处理：每块通过 makeDest 创建扫描目标（如 func() any { return &[]User{} }），
// 回调返回 false 提前终止（对应 Laravel chunk）。
func (b *Builder) Chunk(perPage uint64, makeDest func() any, callback func(page any) (bool, error)) error {
	page := uint64(1)
	for {
		var count int
		dest := makeDest()
		nb := b.Clone().ForPage(page, perPage)
		if err := nb.Get(dest); err != nil {
			return err
		}
		count = reflectSliceLen(dest)
		cont, err := callback(dest)
		if err != nil {
			return err
		}
		if !cont || count < int(perPage) {
			return nil
		}
		page++
	}
}

// Each 为 Chunk 的逐行封装：dest 为单个元素模板指针。
func (b *Builder) Each(makeDest func() any, callback func(item any) (bool, error)) error {
	return b.Chunk(1000,
		func() any { return makeDest() },
		func(page any) (bool, error) {
			return iterEachRow(page, callback)
		})
}

func isEmptyDest(dest interface{}) bool {
	rv := reflect.Indirect(reflect.ValueOf(dest))
	switch rv.Kind() {
	case reflect.Slice:
		return rv.Len() == 0
	case reflect.Struct:
		// 全零结构体视为未命中
		return rv.IsZero()
	case reflect.Ptr:
		return rv.IsNil()
	}
	return false
}

func reflectSliceLen(dest interface{}) int {
	rv := reflect.Indirect(reflect.ValueOf(dest))
	if rv.Kind() != reflect.Slice {
		return 0
	}
	return rv.Len()
}

// ---- 内省与提示 ----

// ToRawSql 以字面值内联绑定输出 SQL（对应 Laravel toRawSql）。
func (b *Builder) ToRawSql() string {
	return b.grammar.SubstituteBindingsIntoRawSql(b.ToSql(), b.GetBindings())
}

// Dump 输出当前 SQL 到 stderr。
func (b *Builder) Dump() *Builder {
	fmt.Fprintln(os.Stderr, b.ToSql())
	return b
}

// DumpRawSql 输出内联绑定后的 SQL。
func (b *Builder) DumpRawSql() *Builder {
	fmt.Fprintln(os.Stderr, b.ToRawSql())
	return b
}

// DD 输出 SQL 后终止进程（对应 Laravel dd）。
func (b *Builder) DD() {
	fmt.Fprintln(os.Stderr, b.ToSql())
	os.Exit(1)
}

// Explain 返回执行计划（explain + SQL）。
func (b *Builder) Explain() ([]map[string]any, error) {
	return b.Connection.SelectMaps("explain "+b.ToSql(), b.GetBindings())
}

// UseIndex 对应 Laravel useIndex。
func (b *Builder) UseIndex(index string) *Builder {
	b.Query.IndexHint = &query.IndexHint{Type: "hint", Index: index}
	return b
}

// ForceIndex 对应 Laravel forceIndex。
func (b *Builder) ForceIndex(index string) *Builder {
	b.Query.IndexHint = &query.IndexHint{Type: "force", Index: index}
	return b
}

// IgnoreIndex 对应 Laravel ignoreIndex。
func (b *Builder) IgnoreIndex(index string) *Builder {
	b.Query.IndexHint = &query.IndexHint{Type: "ignore", Index: index}
	return b
}

// ---- 游标与懒加载（ChunkById / Lazy / Cursor）----

// UseWritePdo 强制 select 走写连接（对应 Laravel useWritePdo）。
func (b *Builder) UseWritePdo() *Builder {
	b.useWritePdo = true
	return b
}

// ForPageAfterId 基于列游标的分页：where column > lastId + orderBy column asc + limit
// （对应 Laravel forPageAfterId；不使用 offset）。
func (b *Builder) ForPageAfterId(perPage uint64, lastId interface{}, column ...string) *Builder {
	col := "id"
	if len(column) > 0 && column[0] != "" {
		col = column[0]
	}
	// 移除已有针对该列的排序（对齐 Laravel 的 orders 过滤）
	orders := make([]*query.Order, 0, len(b.Query.Orders))
	for _, o := range b.Query.Orders {
		if fmtString(o.Column) != col {
			orders = append(orders, o)
		}
	}
	b.Query.Orders = orders
	b.Where(col, ">", lastId)
	b.OrderBy(col, "asc")
	b.Limit(perPage)
	return b
}

// ChunkById 基于列游标的分块处理，避免大 offset（对应 Laravel chunkById）。
// makeDest 每页创建扫描目标（如 func() any { return &[]User{} }）；回调返回 false 提前终止。
func (b *Builder) ChunkById(perPage uint64, makeDest func() any, callback func(page any) (bool, error), column ...string) error {
	col := "id"
	if len(column) > 0 && column[0] != "" {
		col = column[0]
	}
	lastId := any(int64(0))
	for {
		nb := b.Clone().ForPageAfterId(perPage, lastId, col)
		dest := makeDest()
		if err := nb.Get(dest); err != nil {
			return err
		}
		n := reflectSliceLen(dest)
		cont, err := callback(dest)
		if err != nil {
			return err
		}
		if !cont || n < int(perPage) {
			return nil
		}
		lastId, err = lastColumnValue(dest, col)
		if err != nil {
			return err
		}
	}
}

// EachById 为 ChunkById 的逐行封装。
func (b *Builder) EachById(makeDest func() any, callback func(item any) (bool, error), column ...string) error {
	return b.ChunkById(1000,
		makeDest,
		func(page any) (bool, error) {
			return iterEachRow(page, callback)
		},
		column...)
}

// Cursor 全表流式读取：逐行产出 map（对应 Laravel cursor；错误经第二通道传出）。
func (b *Builder) Cursor() (iter.Seq2[map[string]any, error], error) {
	b.applyModelScopes()
	return b.Connection.CursorQuery(b.ToSql(), b.GetBindings(), b.useWritePdo)
}

// Lazy 分页懒加载：内部按 limit/offset 翻页，逐行产出 map（对应 Laravel lazy）。
func (b *Builder) Lazy(perPage uint64) (iter.Seq2[map[string]any, error], error) {
	return b.lazyImpl(perPage, "", nil)
}

// LazyById 基于 id 游标的懒加载（对应 Laravel lazyById，无大 offset 问题）。
func (b *Builder) LazyById(perPage uint64, column ...string) (iter.Seq2[map[string]any, error], error) {
	col := "id"
	if len(column) > 0 && column[0] != "" {
		col = column[0]
	}
	return b.lazyImpl(perPage, col, nil)
}

func (b *Builder) lazyImpl(perPage uint64, idColumn string, _ any) (iter.Seq2[map[string]any, error], error) {
	if perPage == 0 {
		perPage = 1000
	}
	b.applyModelScopes()
	lastId := any(int64(0))
	return func(yield func(map[string]any, error) bool) {
		page := uint64(1)
		for {
			var nb *Builder
			if idColumn != "" {
				nb = b.Clone().ForPageAfterId(perPage, lastId, idColumn)
			} else {
				nb = b.Clone().ForPage(page, perPage)
			}
			rows, err := nb.Connection.CursorQuery(nb.ToSql(), nb.GetBindings(), nb.useWritePdo)
			if err != nil {
				yield(nil, err)
				return
			}
			count := 0
			cont := true
			rows(func(row map[string]any, rowErr error) bool {
				if rowErr != nil {
					yield(nil, rowErr)
					return false
				}
				count++
				if idColumn != "" {
					if v, ok := row[idColumn]; ok {
						lastId = v
					}
				}
				cont = yield(row, nil)
				return cont
			})
			if !cont {
				return
			}
			if count < int(perPage) {
				return
			}
			page++
		}
	}, nil
}

// lastColumnValue 取扫描结果最后一行中指定列的值（ChunkById 游标推进）。
func lastColumnValue(dest any, col string) (any, error) {
	rv := reflect.Indirect(reflect.ValueOf(dest))
	if rv.Kind() != reflect.Slice || rv.Len() == 0 {
		return int64(0), nil
	}
	last := rv.Index(rv.Len() - 1)
	if last.Kind() == reflect.Ptr {
		last = last.Elem()
	}
	schema, err := NewSchema(last.Addr().Interface())
	if err != nil {
		return int64(0), err
	}
	for _, f := range schema.Fields {
		if f.Name == col {
			return f.Value.Addr().Interface(), nil
		}
	}
	return int64(0), nil
}

// iterEachRow 把分页结果逐行回调；返回 false 时外层分页终止。
func iterEachRow(page any, callback func(item any) (bool, error)) (bool, error) {
	rv := reflect.Indirect(reflect.ValueOf(page))
	for i := 0; i < rv.Len(); i++ {
		item := rv.Index(i)
		if item.Kind() == reflect.Ptr {
			cont, err := callback(item.Interface())
			if err != nil || !cont {
				return false, err
			}
			continue
		}
		ptr := reflect.New(item.Type())
		ptr.Elem().Set(item)
		cont, err := callback(ptr.Interface())
		if err != nil || !cont {
			return false, err
		}
	}
	return true, nil
}
