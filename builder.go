package torm

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/go-think/torm/grammar"
	"github.com/go-think/torm/query"
)

// Builder 查询构建器：封装 query.Query，经 grammar 编译为方言 SQL。
type Builder struct {
	Connection *Connection
	grammar    grammar.Grammar
	Query      *query.Query
	Bindings   map[string][]interface{}

	// modelInfo：Model() 查询所关联的模型元数据（软删除 scope 等）
	modelInfo          *modelInfo
	modelInternalQuery bool
	scopesApplied      bool
	softDelete         softDeleteMode

	// useWritePdo：select 强制走写连接
	useWritePdo bool

	// relations：待预加载的关系（With）
	relations []pendingRelation

	// joinClause：join 闭包使用的 JoinClause 变体——
	// 携带自身 join 类型与表，编译为 on 子句。
	joinType  string
	joinTable interface{}

	// beforeQueryCallbacks / afterQueryCallbacks：查询执行前/后的回调。
	beforeQueryCallbacks []func(*Builder)
	afterQueryCallbacks  []func(any) any
}

// softDeleteMode 软删全局 scope 的状态（对应 SoftDeletingScope 的宏语义）。
type softDeleteMode uint8

const (
	sdApply          softDeleteMode = iota // 默认：deleted_at IS NULL
	sdWithTrashed                          // 去掉软删约束
	sdOnlyTrashed                          // 仅已删除：deleted_at IS NOT NULL
	sdWithoutTrashed                       // 仅未删除：deleted_at IS NULL
)

// applyModelScopes 在执行前追加模型全局 scope（执行时统一应用，而非构建期一次性生效）。
// 软删约束的列名带表限定（"users"."deleted_at"），
// 与 getQualifiedDeletedAtColumn 口径一致；scope 状态随 Builder 克隆一并继承。
func (b *Builder) applyModelScopes() {
	if b.modelInfo == nil || !b.modelInfo.softDeletes || b.scopesApplied {
		return
	}
	// 模型内部路径（Save/软删/Restore 的 UPDATE）：
	// 不注册全局 scope（Model.php:1859-1865），软删行也参与更新
	if b.modelInternalQuery {
		return
	}
	column := b.modelInfo.table + "." + b.modelInfo.deletedAtField
	switch b.softDelete {
	case sdWithTrashed:
		// 无约束
	case sdOnlyTrashed:
		b.WhereNotNull(column)
	default:
		b.WhereNull(column)
	}
	b.scopesApplied = true
}

// WithTrashed 去掉软删约束（对应 SoftDeletingScope 的 withTrashed 宏）。
func (b *Builder) WithTrashed() *Builder {
	return b.setSoftDeleteMode(sdWithTrashed)
}

// OnlyTrashed 只查已软删的行：deleted_at IS NOT NULL（对应 onlyTrashed 宏）。
func (b *Builder) OnlyTrashed() *Builder {
	return b.setSoftDeleteMode(sdOnlyTrashed)
}

// WithoutTrashed 只查未软删的行：deleted_at IS NULL（对应 withoutTrashed 宏，默认态）。
func (b *Builder) WithoutTrashed() *Builder {
	return b.setSoftDeleteMode(sdWithoutTrashed)
}

// setSoftDeleteMode 切换软删 scope 状态并剔除已物化的软删约束，
// 下次执行按新模式重新应用（scope 每次执行都重新生效）。
func (b *Builder) setSoftDeleteMode(m softDeleteMode) *Builder {
	if b.modelInfo == nil || !b.modelInfo.softDeletes {
		return b
	}
	b.softDelete = m
	b.removeSoftDeleteScopeWheres()
	b.scopesApplied = false
	return b
}

// removeSoftDeleteScopeWheres 剔除先前由软删 scope 物化的 Null/NotNull 约束。
func (b *Builder) removeSoftDeleteScopeWheres() {
	column := b.modelInfo.table + "." + b.modelInfo.deletedAtField
	wheres := b.Query.Wheres
	out := make([]*query.Where, 0, len(wheres))
	for _, w := range wheres {
		if (w.Type == "Null" || w.Type == "NotNull") && w.Column == column {
			continue
		}
		out = append(out, w)
	}
	b.Query.Wheres = out
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

// AddSelect 追加查询列；支持列表展开并跳过重复列。
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
// operator/second 可省略，type 缺省 "inner"，where 缺省 false。
// first 亦可为 func(*Builder)（复杂 on 子句，JoinClause 闭包形式）。
func (b *Builder) Join(table interface{}, first interface{}, args ...interface{}) *Builder {
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
	// 闭包形式：复杂 on 子句（first 为回调时使用）
	switch first.(type) {
	case func(*Builder), func(*Builder) *Builder:
		return b.addJoinWithClosure(typeStr, table, first)
	}
	if operator == "" {
		operator = "="
	}
	return b.addJoin(typeStr, table, fmtString(first), operator, second, where)
}

func (b *Builder) LeftJoin(table interface{}, first interface{}, args ...interface{}) *Builder {
	return b.Join(table, first, append(args, "left")...)
}

func (b *Builder) RightJoin(table interface{}, first interface{}, args ...interface{}) *Builder {
	return b.Join(table, first, append(args, "right")...)
}

func (b *Builder) CrossJoin(table interface{}, first interface{}, args ...interface{}) *Builder {
	return b.Join(table, first, append(args, "cross")...)
}

// StraightJoin 编译为 straight_join（仅 MySQL 支持，其它方言编译时抛错）。
func (b *Builder) StraightJoin(table interface{}, first string, operator string, second interface{}) *Builder {
	return b.Join(table, first, operator, second, "straight_join", false)
}

// StraightJoinWhere straight_join 且 where=true（第二操作数作为绑定值）。
func (b *Builder) StraightJoinWhere(table interface{}, first string, operator string, second interface{}) *Builder {
	return b.Join(table, first, operator, second, "straight_join", true)
}

// JoinWhere 对应 join(..., $where=true)：把第二操作数作为绑定值。
func (b *Builder) JoinWhere(table interface{}, first string, operator string, second interface{}) *Builder {
	return b.Join(table, first, operator, second, "inner", true)
}

func (b *Builder) LeftJoinWhere(table interface{}, first string, operator string, second interface{}) *Builder {
	return b.Join(table, first, operator, second, "left", true)
}

func (b *Builder) RightJoinWhere(table interface{}, first string, operator string, second interface{}) *Builder {
	return b.Join(table, first, operator, second, "right", true)
}

// newJoinClause 构造 JoinClause 变体（JoinClause 标记的 Builder，携带类型与表）。
func (b *Builder) newJoinClause(typeStr string, table interface{}) *Builder {
	jc := &Builder{
		Connection: b.Connection,
		grammar:    b.grammar,
		Query:      &query.Query{JoinClause: true, From: table},
		Bindings:   getDefaultBindings(),
		joinType:   typeStr,
		joinTable:  table,
	}
	return jc
}

// addJoinWithClosure 处理闭包形式的 join：先构造 JoinClause，
// 由回调在其上构造 on 条件，随后把 clause 追加到父查询。
func (b *Builder) addJoinWithClosure(typeStr string, table interface{}, fn interface{}) *Builder {
	jc := b.newJoinClause(typeStr, table)
	applySubQuery(jc, fn)
	return b.appendJoinClause(jc)
}

// appendJoinClause 把 JoinClause 变体的 wheres 与绑定并入父查询。
func (b *Builder) appendJoinClause(jc *Builder) *Builder {
	b.Query.Joins = append(b.Query.Joins, &query.Join{
		Type:   jc.joinType,
		Table:  jc.joinTable,
		Wheres: jc.Query.Wheres,
	})
	b.AddBinding(jc.GetBindings(), "join")
	return b
}

func (b *Builder) addJoin(typeStr string, table interface{}, first, operator string, second interface{}, where bool) *Builder {
	jc := b.newJoinClause(typeStr, table)
	if where {
		// where=true：第二操作数作为绑定值，操作符必须保留
		// 与三参 where(first, operator, second) 等价
		jc.Where(first, operator, second)
	} else {
		jc.On(first, operator, second)
	}
	return b.appendJoinClause(jc)
}

// On 在 JoinClause 上添加 on 条件：列比较或嵌套闭包（对应 JoinClause::on）。
func (b *Builder) On(first interface{}, args ...interface{}) *Builder {
	operator, second, boolean := parseColumnArgs(args...)
	if boolean == "" {
		boolean = "and"
	}
	switch first.(type) {
	case func(*Builder), func(*Builder) *Builder:
		if operator == "=" && second == nil {
			return b.WhereNested(func(nq *Builder) { applySubQuery(nq, first) }, boolean)
		}
	}
	return b.WhereColumn(first, operator, second, boolean)
}

// OrOn 在 JoinClause 上添加 or on 条件（对应 JoinClause::orOn）。
func (b *Builder) OrOn(first interface{}, args ...interface{}) *Builder {
	operator, second, _ := parseColumnArgs(args...)
	switch first.(type) {
	case func(*Builder), func(*Builder) *Builder:
		return b.WhereNested(func(nq *Builder) { applySubQuery(nq, first) }, "or")
	}
	return b.On(first, operator, second, "or")
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

// StraightJoinSub 以子查询作为 straight join 表
// （仅 MySQL 支持，其它方言编译时抛错）。
func (b *Builder) StraightJoinSub(q interface{}, as string, first, operator, second interface{}) *Builder {
	return b.addJoinOfSub("straight_join", q, as, first, operator, second)
}

func (b *Builder) addJoinOfSub(typeStr string, q interface{}, as string, first, operator, second interface{}) *Builder {
	sql, bindings := b.createSub(q)
	b.AddBinding(bindings, "join")
	expr := grammar.NewExpression("(" + sql + ") as " + b.grammar.WrapTable(as))
	jc := b.newJoinClause(typeStr, expr)
	jc.On(fmtString(first), fmtString(operator), second)
	return b.appendJoinClause(jc)
}

// CrossJoinSub 交叉 join 子查询（无 on 条件，对应 crossJoinSub）。
func (b *Builder) CrossJoinSub(q interface{}, as string) *Builder {
	sql, bindings := b.createSub(q)
	b.AddBinding(bindings, "join")
	jc := b.newJoinClause("cross", grammar.NewExpression("("+sql+") as "+b.grammar.WrapTable(as)))
	return b.appendJoinClause(jc)
}

// JoinLateral 添加 lateral join（MySQL/PG 支持，对应 joinLateral）。
func (b *Builder) JoinLateral(q interface{}, as string, typeStr ...string) *Builder {
	t := "inner"
	if len(typeStr) > 0 && typeStr[0] != "" {
		t = typeStr[0]
	}
	sql, bindings := b.createSub(q)
	b.AddBinding(bindings, "join")
	b.Query.Joins = append(b.Query.Joins, &query.Join{
		Type:    t,
		Table:   grammar.NewExpression("(" + sql + ") as " + b.grammar.WrapTable(as)),
		Lateral: true,
	})
	return b
}

// LeftJoinLateral 添加 left lateral join。
func (b *Builder) LeftJoinLateral(q interface{}, as string) *Builder {
	return b.JoinLateral(q, as, "left")
}

// ---- Where ----

// queryOperators 基础操作符表（不含方言扩展）。
var queryOperators = []string{
	"=", "<", ">", "<=", ">=", "<>", "!=", "<=>",
	"like", "like binary", "not like", "not like binary", "ilike", "not ilike",
	"&", "|", "^", "<<", ">>", "&~", "is", "is not",
	"rlike", "not rlike", "regexp", "not regexp",
	"~", "~*", "!~", "!~*", "similar to", "not similar to",
	"~~", "!~~", "~~*", "!~~*",
}

// invalidBaseOperator 基础操作符表判定：
// 仅查 queryOperators，不含方言扩展。
func invalidBaseOperator(operator string) bool {
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

// invalidOperator：基础操作符表 ∪ grammar 方言表联合判定。
// 方言经 Grammar 的 Operators() []string 钩子提供，
// PG/SqlServer 方言表待接线，见待接线清单。
func (b *Builder) invalidOperator(operator string) bool {
	if invalidBaseOperator(operator) {
		if ops, ok := b.grammar.(interface{ Operators() []string }); ok {
			lower := strings.ToLower(operator)
			for _, op := range ops.Operators() {
				if lower == op {
					return false
				}
			}
		}
		return true
	}
	return false
}

// builderBitwiseOperators 基础位运算符集合。
var builderBitwiseOperators = []string{"&", "|", "^", "<<", ">>", "&~"}

// isBitwiseOperator：基础位运算符集合
// ∪ 方言集合（方言通过 BitwiseOperators() 方法提供）。
func (b *Builder) isBitwiseOperator(operator string) bool {
	lower := strings.ToLower(operator)
	for _, op := range builderBitwiseOperators {
		if lower == op {
			return true
		}
	}
	if bo, ok := b.grammar.(interface{ BitwiseOperators() []string }); ok {
		for _, op := range bo.BitwiseOperators() {
			if lower == op {
				return true
			}
		}
	}
	return false
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
	if b.invalidOperator(operator) {
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

	// null 值 → whereNull / whereNotNull（仅 "=" 视为 NULL，
	// 其余操作符——含 "<=>"——都走 IS NOT NULL）
	if value == nil {
		not := operator != "="
		return b.whereNullImpl(column, boolean, not)
	}

	typ := "Basic"

	// 条件表达式：无参 where(Expression) 视为完整条件（对应 ConditionExpression）
	if len(args) == 0 {
		if _, ok := column.(grammar.Expression); ok {
			b.Query.Wheres = append(b.Query.Wheres, &query.Where{
				Type:    "Expression",
				Column:  column,
				Boolean: boolean,
			})
			return b
		}
	}

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

	// 位运算符：Bitwise 类型（PG ::bool / SqlServer != 0 等方言编译）
	if b.isBitwiseOperator(operator) {
		typ = "Bitwise"
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

// addArrayOfWheres 处理 map 形式条件（col => val），整体括号分组。
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

// flattenValue 递归取数组扁平化后的首元素。
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
// 非法操作符时回退为两参等值比较（first = second）。
func (b *Builder) WhereColumn(first interface{}, args ...interface{}) *Builder {
	operator, second, boolean := parseColumnArgs(args...)
	if second != nil && b.invalidOperator(operator) {
		first, second = second, first
		operator = "="
	}
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

// WhereExpression 添加条件表达式：
// 条件原样进入 where，不追加操作符与绑定。
func (b *Builder) WhereExpression(condition interface{}, boolean ...string) *Builder {
	col := condition
	if s, ok := condition.(string); ok {
		col = grammar.NewExpression(s)
	}
	b.Query.Wheres = append(b.Query.Wheres, &query.Where{
		Type:    "Expression",
		Column:  col,
		Boolean: boolArg(boolean, "and"),
	})
	return b
}

// OrWhereExpression 以 or 连接条件表达式。
func (b *Builder) OrWhereExpression(condition interface{}) *Builder {
	return b.WhereExpression(condition, "or")
}

// WhereRaw / OrWhereRaw
// 签名等价 whereRaw(sql, bindings, boolean='and')：可选末位 string 参数
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

// splitRawBoolean 识别变参末位的 boolean 关键字（等价显式 boolean 形参）。
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

	// 嵌套数组校验（非法嵌套报错）
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
// 整型值以 raw 形式内联进 SQL，不产生绑定。
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
		parsed, ok := toIntValue(v)
		// 值会以内联 raw 形式进入 SQL：非整型值直接拒绝，
		// 此库对编程期错误的既有约定是 panic ErrInvalid
		// （见 OrderBy 方向校验）。
		if !ok {
			panic(ErrInvalid{Msg: fmt.Sprintf("integer value expected, %T given (%v)", v, v)})
		}
		ints = append(ints, parsed)
	}
	b.Query.Wheres = append(b.Query.Wheres, &query.Where{
		Type:    typ,
		Column:  column,
		Values:  ints,
		Boolean: boolean,
	})
	return b
}

// toIntValue 将值强转为整型；
// 非数值返回 ok=false，调用方转为 panic。
func toIntValue(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int8:
		return int64(n), true
	case int16:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case uint:
		return int64(n), true
	case uint8:
		return int64(n), true
	case uint16:
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint64:
		return int64(n), true
	case float32:
		return int64(n), true
	case float64:
		return int64(n), true
	case string:
		if parsed, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil {
			return parsed, true
		}
		return 0, false
	default:
		return 0, false
	}
}

// WhereLike / OrWhereLike / WhereNotLike / OrWhereNotLike
// 大小写敏感性由 caseSensitive 参数控制。
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
	// 方言改写 like 绑定（SQLite 大小写敏感 → glob 模式转换）
	if !b.grammar.IsExpression(value) {
		if prep, ok := b.grammar.(interface {
			PrepareWhereLikeBinding(any, bool) any
		}); ok {
			value = prep.PrepareWhereLikeBinding(value, caseSensitive)
		}
	}
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

// ---- WhereNullSafeEquals ----

// WhereNullSafeEquals 空安全等值比较（对应 whereNullSafeEquals）：
// 编译为 is not distinct from（MySQL/SqlServer/SQLite 有方言实现，
// 基础方言走 is not distinct from）。
func (b *Builder) WhereNullSafeEquals(column interface{}, value interface{}) *Builder {
	return b.addNullSafeEquals(column, value, "and")
}

// OrWhereNullSafeEquals 以 or 连接的空安全等值（对应 orWhereNullSafeEquals）。
func (b *Builder) OrWhereNullSafeEquals(column interface{}, value interface{}) *Builder {
	return b.addNullSafeEquals(column, value, "or")
}

func (b *Builder) addNullSafeEquals(column, value interface{}, boolean string) *Builder {
	b.Query.Wheres = append(b.Query.Wheres, &query.Where{
		Type:    "NullSafeEquals",
		Column:  column,
		Value:   value,
		Boolean: boolean,
	})
	if !b.grammar.IsExpression(value) {
		b.AddBinding(flattenValue(value), "where")
	}
	return b
}

// WhereNull / OrWhereNull / WhereNotNull / OrWhereNotNull
// 签名等价 whereNull(columns, boolean='and', not=false)：columns 支持单列、
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

	// 绑定：扁平化并清洗后取前两个
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
// 两列比较的 between：values 为列名，不产生绑定。
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

// ---- WhereValueBetween 族 ----

// WhereValueBetween 值介于两列之间：where ? between min and max
// （第一参数为值，
// 第二参数为 [下界列, 上界列] 两元素切片）。
func (b *Builder) WhereValueBetween(value interface{}, columns []interface{}) *Builder {
	return b.whereValueBetweenImpl(value, columns, "and", false)
}

// OrWhereValueBetween 以 or 连接（对应 orWhereValueBetween）。
func (b *Builder) OrWhereValueBetween(value interface{}, columns []interface{}) *Builder {
	return b.whereValueBetweenImpl(value, columns, "or", false)
}

// WhereValueNotBetween 值不介于两列之间（对应 whereValueNotBetween）。
func (b *Builder) WhereValueNotBetween(value interface{}, columns []interface{}) *Builder {
	return b.whereValueBetweenImpl(value, columns, "and", true)
}

// OrWhereValueNotBetween 以 or 连接（对应 orWhereValueNotBetween）。
func (b *Builder) OrWhereValueNotBetween(value interface{}, columns []interface{}) *Builder {
	return b.whereValueBetweenImpl(value, columns, "or", true)
}

func (b *Builder) whereValueBetweenImpl(value interface{}, columns []interface{}, boolean string, not bool) *Builder {
	b.Query.Wheres = append(b.Query.Wheres, &query.Where{
		Type:    "ValueBetween",
		Value:   value,
		Columns: columns,
		Boolean: boolean,
		Not:     not,
	})
	// 值本身为绑定（Expression 自动跳过）
	b.AddBinding(value, "where")
	return b
}

// When / Unless 条件执行：Go 侧条件为 bool。
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
		// 嵌套子查询的 where 绑定并入父查询
		b.AddBinding(sub.GetRawBindings()["where"], "where")
	}
	return b
}

// WhereExists / OrWhereExists / WhereNotExists / OrWhereNotExists
// q 可为 func(*Builder)/func(*Builder) *Builder/
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
	// 闭包：在全新的子 builder 上构造
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

// AddWhereExistsQuery 将 exists 子查询挂为 where 条件。
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

// WhereRowValues / OrWhereRowValues：
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
	if b.invalidOperator(operator) {
		operator, value = "=", operator
	}
	typ := "Basic"
	if b.isBitwiseOperator(operator) {
		typ = "Bitwise"
	}
	b.Query.Havings = append(b.Query.Havings, &query.Where{
		Type:     typ,
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
	if b.invalidOperator(operator) {
		operator, value = "=", operator
	}
	typ := "Basic"
	if b.isBitwiseOperator(operator) {
		typ = "Bitwise"
	}
	b.Query.Havings = append(b.Query.Havings, &query.Where{
		Type:     typ,
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

// MergeWheres 合并外部 where 数组与绑定。
func (b *Builder) MergeWheres(wheres []*query.Where, bindings []interface{}) *Builder {
	b.Query.Wheres = append(b.Query.Wheres, wheres...)
	b.Bindings["where"] = append(b.Bindings["where"], bindings...)
	return b
}

// WhereAll 多列 and 组内条件：
// where (a = ? and b = ?)。
func (b *Builder) WhereAll(columns []interface{}, operator string, value interface{}, boolean ...string) *Builder {
	return b.whereAllAnyImpl(columns, operator, value, boolArg(boolean, "and"), "and")
}

// OrWhereAll 对应 orWhereAll。
func (b *Builder) OrWhereAll(columns []interface{}, operator string, value interface{}) *Builder {
	return b.whereAllAnyImpl(columns, operator, value, "or", "and")
}

// WhereAny 多列 or 组内条件：where (a = ? or b = ?)。
func (b *Builder) WhereAny(columns []interface{}, operator string, value interface{}, boolean ...string) *Builder {
	return b.whereAllAnyImpl(columns, operator, value, boolArg(boolean, "and"), "or")
}

// OrWhereAny 对应 orWhereAny。
func (b *Builder) OrWhereAny(columns []interface{}, operator string, value interface{}) *Builder {
	return b.whereAllAnyImpl(columns, operator, value, "or", "or")
}

// WhereNone 多列均不满足（or 组内条件整体取反）。
func (b *Builder) WhereNone(columns []interface{}, operator string, value interface{}, boolean ...string) *Builder {
	return b.whereAllAnyImpl(columns, operator, value, boolArg(boolean, "and")+" not", "or")
}

// OrWhereNone 对应 orWhereNone。
func (b *Builder) OrWhereNone(columns []interface{}, operator string, value interface{}) *Builder {
	return b.whereAllAnyImpl(columns, operator, value, "or not", "or")
}

func (b *Builder) whereAllAnyImpl(columns []interface{}, operator string, value interface{}, boolean, inner string) *Builder {
	if b.invalidOperator(operator) {
		operator, value = "=", operator
	}
	return b.WhereNested(func(nq *Builder) {
		for _, column := range columns {
			nq.whereImpl(column, []interface{}{operator, value}, inner)
		}
	}, boolean)
}

func boolArg(args []string, def string) string {
	if len(args) > 0 && args[0] != "" {
		return args[0]
	}
	return def
}

// HavingRaw / OrHavingRaw（sql + bindings，boolean 缺省 and）：
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

// HavingNested / OrHavingNested / addNestedHavingQuery：嵌套 having 条件组。
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

// HavingNull / OrHavingNull / HavingNotNull / OrHavingNotNull：
// columns 支持多列（每列一条 having）。
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
// 绑定取 flatten 后前两个。
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

// flattenBindings 一层层展平嵌套数组。
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

// ---- CTE（with / withRecursive）----

// WithExpression 注册公用表表达式：with "name" (columns...) as (sub)
// q 可为 *Builder / *query.Query / 闭包。
func (b *Builder) WithExpression(name string, q interface{}, columns ...string) *Builder {
	w := b.cteSubQuery(q)
	b.Query.Withes = append(b.Query.Withes, &query.With{Name: name, Columns: columns, Query: w.Query})
	// CTE 编译在最终 SQL 最前，全部子查询绑定（where/union/order 等各段）
	// 依 GetBindings 序入 "with" 段——只拷 where 段会让递归 CTE（union）等
	// 形态占位符无绑定
	b.AddBinding(w.GetBindings(), "with")
	return b
}

// WithRecursiveExpression 注册递归 CTE（with recursive）。
func (b *Builder) WithRecursiveExpression(name string, q interface{}, columns ...string) *Builder {
	w := b.cteSubQuery(q)
	b.Query.WithRecursive = true
	b.Query.Withes = append(b.Query.Withes, &query.With{Name: name, Columns: columns, Query: w.Query})
	b.AddBinding(w.GetBindings(), "with")
	return b
}

// cteSubQuery 把任意可查询对象解析为独立子查询（复用 union 的解析语义）。
func (b *Builder) cteSubQuery(q interface{}) *Builder {
	nb := NewBuilder(b.Connection, b.grammar)
	applySubQuery(nb, q)
	return nb
}

// OrderBy 排序；direction 只接受 asc/desc（大小写不敏感），非法值 panic。
// column 可为 *Builder / 闭包（编译为子查询列：order by (select ...)）。
func (b *Builder) OrderBy(column interface{}, direction ...string) *Builder {
	dir := "asc"
	if len(direction) > 0 {
		dir = strings.ToLower(direction[0])
		if dir != "asc" && dir != "desc" {
			panic(ErrInvalid{"Order direction must be \"asc\" or \"desc\""})
		}
	}
	// 子查询列：order by (select ...)
	if b.isQueryable(column) {
		sql, bindings := b.createSub(column)
		column = grammar.NewExpression("(" + sql + ")")
		segment := "order"
		if len(b.Query.Unions) > 0 {
			segment = "unionOrder"
		}
		b.AddBinding(bindings, segment)
	}
	order := &query.Order{Column: column, Direction: dir}
	if len(b.Query.Unions) > 0 {
		b.Query.UnionOrders = append(b.Query.UnionOrders, order)
	} else {
		b.Query.Orders = append(b.Query.Orders, order)
	}
	return b
}

// InOrderOf 按取值序列排序：
// order by case "col" = ? then 0 ... else N end。
func (b *Builder) InOrderOf(column interface{}, values []interface{}) *Builder {
	if len(values) == 0 {
		return b
	}
	segment := "order"
	order := &query.Order{Type: "InOrderOf", Column: column, Values: values}
	if len(b.Query.Unions) > 0 {
		segment = "unionOrder"
		b.Query.UnionOrders = append(b.Query.UnionOrders, order)
	} else {
		b.Query.Orders = append(b.Query.Orders, order)
	}
	b.AddBinding(cleanBindings(values), segment)
	return b
}

func (b *Builder) OrderByDesc(column interface{}) *Builder {
	return b.OrderBy(column, "desc")
}

func (b *Builder) OrderByRaw(sql string, bindings ...interface{}) *Builder {
	order := &query.Order{Sql: grammar.NewExpression(sql)}
	// 绑定段与 order 归属保持一致（有 union 时归 unionOrder 段，否则 order 段），
	// 否则 union 场景下占位符在 union 子查询之后而绑定在之前，绑定全部错位。
	segment := "order"
	if len(b.Query.Unions) > 0 {
		b.Query.UnionOrders = append(b.Query.UnionOrders, order)
		segment = "unionOrder"
	} else {
		b.Query.Orders = append(b.Query.Orders, order)
	}
	if len(bindings) != 0 {
		b.AddBinding(bindings, segment)
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
	// 随机排序等价 orderByRaw(compileRandom())：union 感知
	order := &query.Order{Sql: grammar.NewExpression(b.grammar.CompileRandom(s))}
	if len(b.Query.Unions) > 0 {
		b.Query.UnionOrders = append(b.Query.UnionOrders, order)
	} else {
		b.Query.Orders = append(b.Query.Orders, order)
	}
	return b
}

// Reorder 清除或重设排序：
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

// ReorderDesc 降序重设排序。
func (b *Builder) ReorderDesc(column interface{}) *Builder {
	return b.Reorder(column, "desc")
}

// Timeout 设置查询执行超时（秒），MySQL 编译为 MAX_EXECUTION_TIME 提示。
func (b *Builder) Timeout(seconds int64) *Builder {
	if seconds <= 0 {
		panic(ErrInvalid{"Timeout must be greater than zero."})
	}
	b.Query.Timeout = &seconds
	return b
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

// GroupLimit 分组限额：
// value >= 0 时设置（编译为 row_number() over (partition by column) <= value，
// 预加载分组限额的基础入口）。
func (b *Builder) GroupLimit(value int, column string) *Builder {
	if value >= 0 {
		b.Query.GroupLimit = &query.GroupLimit{Value: int64(value), Column: column}
	}
	return b
}

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

// SetAggregate 设置聚合组件：
// groups 为空时清除 orders 与 order 段绑定。
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

// Aggregate 在克隆上执行聚合，不污染原查询：
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

// Average Avg 的别名。
func (b *Builder) Average(columns string, dest ...interface{}) error {
	return b.Avg(columns, dest...)
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
		b.applyAfterQueryCallbacks(dest)
		b.Connection.fireRetrieved(dest)
		if rerr := b.loadRelations(dest); rerr != nil {
			return rerr
		}
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

// Exists 存在性检查：select exists(...) as "exists"。
// 必须先应用模型全局 scope（软删等），否则软删模型会把已删行当作存在。
func (b *Builder) Exists() (bool, error) {
	b.applyModelScopes()
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

// ExistsOr 存在返回 true，否则执行回调。
func (b *Builder) ExistsOr(callback func() error) (bool, error) {
	v, err := b.Exists()
	if err != nil {
		return false, err
	}
	if v {
		return true, nil
	}
	return false, callback()
}

// DoesntExistOr 不存在返回 true，否则执行回调。
func (b *Builder) DoesntExistOr(callback func() error) (bool, error) {
	v, err := b.DoesntExist()
	if err != nil {
		return false, err
	}
	if v {
		return true, nil
	}
	return false, callback()
}

// ---- 主键 where 族（whereKey / orWhereKey / whereKeyNot / orWhereKeyNot）----

// keyName 返回主键列名：模型建器取模型主键列，普通表建器缺省 "id"
// （builder 级无模型元数据时的主键映射口径）。
func (b *Builder) keyName() string {
	if b.modelInfo != nil && b.modelInfo.primaryKey != "" {
		return b.modelInfo.primaryKey
	}
	return "id"
}

// WhereKey 按主键添加 where：id 可为主键值、
// 模型实例（取其主键值）或切片（编译为 whereIn）。
func (b *Builder) WhereKey(id interface{}) *Builder {
	return b.whereKeyImpl(id, false, "and")
}

// WhereKeyNot 按主键添加排除 where：
// 切片编译为 whereNotIn，单值编译为 !=。
func (b *Builder) WhereKeyNot(id interface{}) *Builder {
	return b.whereKeyImpl(id, true, "and")
}

// OrWhereKey 以 or 连接主键条件（括号分组 + or）。
func (b *Builder) OrWhereKey(id interface{}) *Builder {
	return b.whereKeyImpl(id, false, "or")
}

// OrWhereKeyNot 以 or 连接主键排除条件。
func (b *Builder) OrWhereKeyNot(id interface{}) *Builder {
	return b.whereKeyImpl(id, true, "or")
}

func (b *Builder) whereKeyImpl(id interface{}, not bool, boolean string) *Builder {
	key := b.keyName()
	// 模型实例 → 取其主键值
	if _, v, ok := modelPrimaryKey(id); ok {
		id = v
	}
	if boolean == "or" {
		// or 连接：先嵌套括号分组，再以 or 连接
		return b.WhereNested(func(nq *Builder) {
			nq.modelInfo = b.modelInfo
			nq.whereKeyImpl(id, not, "and")
		}, "or")
	}
	if ids, ok := interfaceSlice(id); ok {
		if not {
			return b.WhereNotIn(key, ids)
		}
		return b.WhereIn(key, ids)
	}
	if not {
		return b.Where(key, "!=", id)
	}
	return b.Where(key, "=", id)
}

// interfaceSlice 切片值归一为 []interface{}（[]byte 视为单值）。
func interfaceSlice(v interface{}) ([]interface{}, bool) {
	if v == nil {
		return nil, false
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, false
	}
	if rv.Type().Elem().Kind() == reflect.Uint8 {
		return nil, false
	}
	out := make([]interface{}, 0, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		out = append(out, rv.Index(i).Interface())
	}
	return out, true
}

// ---- 关系存在性/计数阈值（对应 QueriesRelationships.php:39-90 has）----

// hasImpl Has 的实现体：无参 → 存在性 exists；带 count 阈值 → 计数子查询比较。
// 优化口径：count==1 且操作符为
// >= 或 < 时退化为 exists / not exists（has(rel, '<', 1) 即 doesntHave 语义）。
// 其余组合编译为 where (select count(*) from target where keys...) op count，
// count 取绝对值、以字面值内联进 SQL。
func (b *Builder) hasImpl(rel Relation, args ...any) *Builder {
	operator := ">="
	count := int64(1)
	switch len(args) {
	case 1:
		if s, ok := args[0].(string); ok {
			operator = s
		} else if n, ok := toIncrementAmount(args[0]); ok {
			count = n
		}
	case 2:
		if s, ok := args[0].(string); ok && s != "" {
			operator = s
		}
		if n, ok := toIncrementAmount(args[1]); ok {
			count = n
		}
	}
	// 存在性检查优化（canUseExistsForExistenceCheck）
	if count == 1 && (operator == ">=" || operator == "<") {
		if operator == "<" {
			return b.addHasWhere(rel, nil, "and-not")
		}
		return b.addHasWhere(rel, nil, "and")
	}
	// 计数子查询形态（addWhereCountQuery）
	sub := b.hasSubQuery(rel)
	if sub == nil {
		// MorphTo：目标类型由行数据决定，无法静态构造子查询（防 panic 静默跳过）
		return b
	}
	switch m := rel.(type) {
	case MorphMany:
		addMorphTypeFilter(sub, m.MorphName, m.TypeColumn, m.MorphKey, fmtString(b.Query.From))
	case MorphOne:
		addMorphTypeFilter(sub, m.MorphName, m.TypeColumn, m.MorphKey, fmtString(b.Query.From))
	}
	sub.SetAggregate("count", []interface{}{"*"})
	return b.Where(sub, operator, grammar.NewExpression(strconv.FormatInt(count, 10)))
}

// ---- Find 族 ----

// Find 按主键取单行（等价 whereKey(id) 后 first）。
func (b *Builder) Find(id interface{}, dest interface{}, columns ...interface{}) error {
	return b.WhereKey(id).First(dest, columns...)
}

// FindMany 按主键批量取行（等价 whereKey(ids) 后 get）；
// ids 为任意切片。空集直接返回空 dest，不发 SQL。
func (b *Builder) FindMany(ids interface{}, dest interface{}, columns ...interface{}) error {
	list, _ := interfaceSlice(ids)
	if len(list) == 0 {
		rv := reflect.Indirect(reflect.ValueOf(dest))
		if rv.Kind() == reflect.Slice {
			rv.Set(reflect.MakeSlice(rv.Type(), 0, 0))
		}
		return nil
	}
	return b.WhereKey(list).Get(dest, columns...)
}

// FindOrFail 按主键取单行，未命中返回 *ErrNotFound
// （即记录不存在错误）。
func (b *Builder) FindOrFail(id interface{}, dest interface{}, columns ...interface{}) error {
	if err := b.Find(id, dest, columns...); err != nil {
		return err
	}
	if isEmptyDest(dest) {
		return ErrNotFound{Msg: "No query results for record [" + fmtString(id) + "]."}
	}
	return nil
}

// FindOrNew 按主键查找；未命中时把 dest 重置为空白实例并仅设主键
// （不落库）。
func (b *Builder) FindOrNew(id interface{}, dest interface{}, columns ...interface{}) error {
	if err := b.Find(id, dest, columns...); err != nil {
		return err
	}
	if isEmptyDest(dest) {
		if _, v, ok := modelPrimaryKey(id); ok {
			id = v
		}
		rv := reflect.ValueOf(dest)
		if rv.Kind() == reflect.Ptr && rv.Elem().Kind() == reflect.Struct {
			rv.Elem().Set(reflect.Zero(rv.Elem().Type()))
			_ = setModelAttribute(dest, b.keyName(), id)
		}
	}
	return nil
}

// FindOr 按主键取单行，未命中执行 fallback。
func (b *Builder) FindOr(id interface{}, dest interface{}, fallback func() error, columns ...interface{}) error {
	if err := b.Find(id, dest, columns...); err != nil {
		return err
	}
	if isEmptyDest(dest) {
		return fallback()
	}
	return nil
}

// ---- firstOrCreate 族（builder 级转发 Connection）----

// FirstOrCreate 按 attributes 查找；未命中则合并 attributes+values 创建
// （转发 Connection.FirstOrCreate，保持两者语义一致）。
func (b *Builder) FirstOrCreate(model interface{}, attributes map[string]interface{}, values ...map[string]interface{}) error {
	return b.Connection.FirstOrCreate(model, attributes, values...)
}

// FirstOrNew 按 attributes 查找；未命中仅构造不落库（转发 Connection.FirstOrNew）。
func (b *Builder) FirstOrNew(model interface{}, attributes map[string]interface{}, values ...map[string]interface{}) error {
	return b.Connection.FirstOrNew(model, attributes, values...)
}

// CreateOrFirst 先尝试创建，唯一约束冲突时回退按 attributes 查询
// （转发 Connection.CreateOrFirst 的并发安全语义）。
func (b *Builder) CreateOrFirst(model interface{}, attributes map[string]interface{}, values ...map[string]interface{}) error {
	return b.Connection.CreateOrFirst(model, attributes, values...)
}

// UpdateOrCreate 按 attributes 查找；命中则更新 values，未命中则创建
// （转发 Connection.UpdateOrCreate）。
func (b *Builder) UpdateOrCreate(model interface{}, attributes, values map[string]interface{}) error {
	return b.Connection.UpdateOrCreate(model, attributes, values)
}

// IncrementOrCreate 创建匹配 attributes 的记录（column 取 default），已存在
// 则对 column 自增 step：
//
//	firstOrCreate(attributes, {column: default}) → !wasRecentlyCreated 时 increment。
//
// rest 依次为 default（缺省 1）、step（缺省 1）、extra map[string]interface{}（附加列）。
func (b *Builder) IncrementOrCreate(model interface{}, attributes map[string]interface{}, column string, rest ...interface{}) error {
	def := int64(1)
	step := int64(1)
	var extra map[string]interface{}
	if len(rest) > 0 {
		if v, ok := toIncrementAmount(rest[0]); ok {
			def = v
		}
	}
	if len(rest) > 1 {
		if v, ok := toIncrementAmount(rest[1]); ok {
			step = v
		}
	}
	if len(rest) > 2 {
		if m, ok := rest[2].(map[string]interface{}); ok {
			extra = m
		}
	}
	if err := b.FirstOrCreate(model, attributes, map[string]interface{}{column: def}); err != nil {
		return err
	}
	if WasRecentlyCreated(model) {
		return nil
	}
	args := []interface{}{step}
	if extra != nil {
		args = append(args, extra)
	}
	return b.Connection.Increment(model, column, args...)
}

// toIncrementAmount 解析 increment 金额（整型/浮点 → int64）。
func toIncrementAmount(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int8:
		return int64(n), true
	case int16:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case uint:
		return int64(n), true
	case uint8:
		return int64(n), true
	case uint16:
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint64:
		return int64(n), true
	case float32:
		return int64(n), true
	case float64:
		return int64(n), true
	}
	return 0, false
}

// ---- Touch ----

// Touch 批量刷新时间戳：把匹配行的时间戳列更新为 now。
// 指定列时仅更新该列（对应 touch($column)）；缺省列刷新 updated_at，
// 且仅对带时间戳列的模型建器生效（无时间戳返回 0、不发 SQL）。
// 直接编译 UPDATE（对应 toBase()->update）：不自动追加 updated_at。
func (b *Builder) Touch(columns ...string) (int64, error) {
	if b.modelInfo != nil && b.modelInfo.softDeletes && !b.modelInternalQuery {
		b.applyModelScopes()
	}
	now := nowFunc()
	if len(columns) == 0 {
		if b.modelInfo == nil || !b.modelInfo.hasTimestamps {
			return 0, nil
		}
		columns = []string{b.modelInfo.updatedAtField}
	}
	value := make(map[string]interface{}, len(columns))
	for _, col := range columns {
		value[col] = now
	}
	b.applyBeforeQueryCallbacks()
	sql := b.grammar.CompileUpdate(b.Query, value)
	bindings := cleanBindings(b.grammar.PrepareBindingsForUpdate(b.Bindings, value))
	return b.Connection.Update(sql, bindings...)
}

// SoleValue 取唯一一行的单列值：零行 ErrNotFound、多行 ErrMultiple
// （sole 的单列形式）。
func (b *Builder) SoleValue(column string) (any, error) {
	nb := b.Clone()
	nb.applyModelScopes()
	nb.Query.Columns = []interface{}{column}
	nb.Take(2)
	rows, err := nb.Connection.SelectMaps(nb.ToSql(), nb.GetBindings())
	if err != nil {
		return nil, err
	}
	switch len(rows) {
	case 0:
		return nil, ErrNotFound{Msg: "No query results for record."}
	case 1:
		return rows[0][column], nil
	default:
		return nil, ErrMultiple{Msg: fmt.Sprintf("Multiple records found (%d).", len(rows))}
	}
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
	if len(values) == 0 {
		// 空值列表等价插入成功：不编译不执行
		return 0, 0, nil
	}
	b.applyBeforeQueryCallbacks()
	sql, bindings := b.grammar.CompileInsert(b.Query, normalizeRows(values))
	return b.Connection.Insert(sql, bindings...)
}

// InsertGetId 插入并返回自增 id。
func (b *Builder) InsertGetId(value interface{}, sequence ...string) (int64, error) {
	b.applyBeforeQueryCallbacks()
	seq := ""
	if len(sequence) > 0 {
		seq = sequence[0]
	}
	sql, bindings := b.grammar.CompileInsertGetId(b.Query, []query.Row{toRow(value)}, seq)
	return b.Connection.InsertGetId(sql, bindings...)
}

// InsertOrIgnore 插入并忽略冲突行。
func (b *Builder) InsertOrIgnore(values []interface{}) (int64, error) {
	if len(values) == 0 {
		return 0, nil
	}
	b.applyBeforeQueryCallbacks()
	sql, bindings := b.grammar.CompileInsertOrIgnore(b.Query, normalizeRows(values))
	n, _, err := b.Connection.Insert(sql, bindings...)
	return n, err
}

// InsertUsing insert ... select：
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

// InsertOrIgnoreUsing insert ignore ... select。
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

// InsertOrIgnoreReturning 插入并忽略冲突行，按 returning 列返回结果行
// （本库扩展 API，上游无对应实现）：
// values 为空返回空结果不发 SQL；returning/uniqueBy 为空切片报错；
// 经写连接执行（对应 selectFromWriteConnection）。
// 编译依赖 grammar 的 CompileInsertOrIgnoreReturning 钩子——PG/SQLite 已实现，
// MySQL/SqlServer/MariaDB 方言未实现时调用 panic ErrUnsupported。
func (b *Builder) InsertOrIgnoreReturning(values []interface{}, returning []string, uniqueBy ...[]string) ([]map[string]any, error) {
	if len(values) == 0 {
		return []map[string]any{}, nil
	}
	if len(returning) == 0 {
		panic(ErrInvalid{"The returning columns must not be empty."})
	}
	var unique []string
	if len(uniqueBy) > 0 {
		if len(uniqueBy[0]) == 0 {
			panic(ErrInvalid{"The unique columns must not be empty."})
		}
		unique = uniqueBy[0]
	}
	g, ok := b.grammar.(interface {
		CompileInsertOrIgnoreReturning(q *query.Query, rows []query.Row, returning, uniqueBy []string) (string, []any)
	})
	if !ok {
		panic(grammar.ErrUnsupported{Msg: "This database engine does not support insert or ignore returning."})
	}
	b.applyBeforeQueryCallbacks()
	sql, bindings := g.CompileInsertOrIgnoreReturning(b.Query, normalizeRows(values), returning, unique)
	var rows []map[string]any
	if err := b.Connection.SelectFromWrite(sql, cleanBindings(bindings), &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// Upsert upsert：uniqueBy 为唯一键列，update 为冲突时更新的列。
// update 为空切片 → 普通 insert；update 为 nil → 全列更新（取首行的列）；
// uniqueBy 为空抛错；values 为空返回 0。
// map 行按 key 排序；query.Row 保序。
func (b *Builder) Upsert(values []interface{}, uniqueBy, update []string) (int64, error) {
	if len(uniqueBy) == 0 {
		return 0, ErrInvalid{"The unique columns must not be empty."}
	}
	// 模型建器 upsert 触碰时间戳（
	// created_at + updated_at 双补，全行共用一个 fresh timestamp；
	// Row 行与 WithoutTimestamps 作用域跳过）
	if b.modelInfo != nil && b.modelInfo.hasTimestamps && !TimestampsIgnored() {
		created, updated := b.modelInfo.createdAtField, b.modelInfo.updatedAtField
		if updated == "" {
			updated = "updated_at"
		}
		if created == "" {
			created = "created_at"
		}
		now := nowFunc()
		for i, row := range values {
			if mm, ok := row.(map[string]interface{}); ok {
				// 不改写调用方传入的 map（副作用）：复制后再补时间戳
				cp := make(map[string]interface{}, len(mm)+2)
				for k, v := range mm {
					cp[k] = v
				}
				if _, exists := cp[updated]; !exists {
					cp[updated] = now
				}
				if _, exists := cp[created]; !exists {
					cp[created] = now
				}
				values[i] = cp
			}
		}
		// addUpdatedAtToUpsertColumns：冲突更新列无条件追加 updated_at
		if update != nil {
			found := false
			for _, u := range update {
				if u == updated {
					found = true
					break
				}
			}
			if !found {
				update = append(append([]string(nil), update...), updated)
			}
		}
	}
	if len(values) == 0 {
		return 0, nil
	}
	if update != nil && len(update) == 0 {
		n, _, err := b.Inserts(values)
		return n, err
	}
	if update == nil {
		// update 为 nil 时取首行的列集合作为更新列
		first := normalizeRows(values[:1])[0]
		update = first.Columns()
	}
	sql, bindings := b.grammar.CompileUpsert(b.Query, normalizeRows(values), uniqueBy, update)
	n, _, err := b.Connection.Insert(sql, bindings...)
	return n, err
}

// addUpdatedAtColumn 模型建器自动补 updated_at（
// update/upsert/increment 系列自动触碰时间戳；
// WithoutTimestamps 作用域内跳过）。
func (b *Builder) addUpdatedAtColumn(value map[string]interface{}) map[string]interface{} {
	if b.modelInfo == nil || !b.modelInfo.hasTimestamps || TimestampsIgnored() {
		return value
	}
	updatedAt := b.modelInfo.updatedAtField
	if updatedAt == "" {
		updatedAt = "updated_at"
	}
	if _, exists := value[updatedAt]; !exists {
		value[updatedAt] = nowFunc()
	}
	return value
}

// addUpdatedAtColumnToRows 对批量行插入补 updated_at（
// 时间戳列已显式给出时不覆盖）。
func (b *Builder) addUpdatedAtColumnToRows(values []interface{}) []interface{} {
	if b.modelInfo == nil || !b.modelInfo.hasTimestamps {
		return values
	}
	updated := b.modelInfo.updatedAtField
	if updated == "" {
		updated = "updated_at"
	}
	for _, row := range values {
		if m, ok := row.(map[string]interface{}); ok {
			if _, exists := m[updated]; !exists {
				m[updated] = nowFunc()
			}
		}
	}
	return values
}

// Update 更新记录。模型建器自动补 updated_at。
func (b *Builder) Update(value map[string]interface{}) (int64, error) {
	if b.modelInfo != nil && b.modelInfo.softDeletes {
		b.applyModelScopes()
	}
	value = b.addUpdatedAtColumn(value)
	b.applyBeforeQueryCallbacks()
	sql := b.grammar.CompileUpdate(b.Query, value)
	cleanBindings := cleanBindings(b.grammar.PrepareBindingsForUpdate(b.Bindings, value))
	return b.Connection.Update(sql, cleanBindings...)
}

// UpdateFrom 对应 Postgres 的 update ... from（compileUpdateFrom）：
// 基础方言不支持（抛 ErrUnsupported）。
func (b *Builder) UpdateFrom(value map[string]interface{}) (int64, error) {
	b.applyBeforeQueryCallbacks()
	g, ok := b.grammar.(interface {
		CompileUpdateFrom(*query.Query, map[string]any) string
		PrepareBindingsForUpdateFrom(map[string][]any, map[string]any) []any
	})
	if !ok {
		panic(grammar.ErrUnsupported{Msg: "This database engine does not support update from."})
	}
	sql := g.CompileUpdateFrom(b.Query, value)
	bindings := cleanBindings(g.PrepareBindingsForUpdateFrom(b.Bindings, value))
	return b.Connection.Update(sql, bindings...)
}

// UpdateOrInsert 存在则更新，否则插入。
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

// Increment / Decrement 数值增减（列包裹后拼算式，负数转减法）。
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

// IncrementEach 按列批量递增：
// 值编译为 raw "col + amount"，extra 为附加 set 值。
func (b *Builder) IncrementEach(columns map[string]float64, extra map[string]interface{}) (int64, error) {
	return b.incrementEachImpl(columns, extra, "+")
}

// DecrementEach 按列批量递减。
func (b *Builder) DecrementEach(columns map[string]float64, extra map[string]interface{}) (int64, error) {
	return b.incrementEachImpl(columns, extra, "-")
}

func (b *Builder) incrementEachImpl(columns map[string]float64, extra map[string]interface{}, op string) (int64, error) {
	values := make(map[string]interface{}, len(columns)+len(extra))
	for _, column := range sortedColumnNames2(columns) {
		amount := columns[column]
		values[column] = grammar.NewExpression(b.grammar.Wrap(column) + " " + op + " " + formatAmount(amount))
	}
	for _, k := range sortedColumnNames(extra) {
		values[k] = extra[k]
	}
	nb := b.Clone()
	return nb.Update(values)
}

func formatAmount(amount float64) string {
	if amount == float64(int64(amount)) {
		return strconv.FormatInt(int64(amount), 10)
	}
	return strconv.FormatFloat(amount, 'f', -1, 64)
}

// sortedColumnNames2 对 map[string]float64 的 key 排序。
func sortedColumnNames2(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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
// 关联软删模型时编译为 update set deleted_at = now（软删宏 onDelete 语义），
// 并分发 deleting/trashed/deleted 事件；
// 无关联模型且无任何 where 条件时返回错误，拒绝整表 DELETE。
func (b *Builder) Delete(args ...interface{}) (int64, error) {
	if len(args) > 0 && args[0] != nil {
		// 主键列名来自模型映射（限定键名口径），
		// 而非硬编码 id
		keyCol := "id"
		if b.modelInfo != nil && b.modelInfo.primaryKey != "" {
			keyCol = b.modelInfo.primaryKey
		}
		// From 为子查询/Expression 时取表名无意义：直接按裸主键列过滤
		// （旧实现 fmtString(Expression) 产出 "table" 前缀垃圾列名；
		// 普通 From 保持表限定，列名交由 grammar 统一 wrap）
		col := keyCol
		if fromStr := fmtString(b.Query.From); fromStr != "" && !b.grammar.IsExpression(b.Query.From) && !b.isQueryable(b.Query.From) {
			col = fromStr + "." + keyCol
		}
		b.Where(col, "=", args[0])
	}
	if b.modelInfo != nil && b.modelInfo.softDeletes {
		return b.softDeleteRows()
	}
	if len(b.Query.Wheres) == 0 {
		return 0, errors.New("torm: delete statement requires a where clause to avoid removing all rows")
	}
	b.applyBeforeQueryCallbacks()
	sql := b.grammar.CompileDelete(b.Query)
	return b.Connection.Delete(sql, b.grammar.PrepareBindingsForDelete(b.Bindings)...)
}

// ForceDelete 硬删除：跳过软删宏，编译为裸 DELETE（绕过模型层
// 直接走底层 delete）。
func (b *Builder) ForceDelete() (int64, error) {
	b.applyBeforeQueryCallbacks()
	sql := b.grammar.CompileDelete(b.Query)
	return b.Connection.Delete(sql, b.grammar.PrepareBindingsForDelete(b.Bindings)...)
}

// softDeleteRows 软删模型的 Builder.Delete（对应 SoftDeletingScope::onDelete
// → performDeleteOnModel：runSoftDelete 写 deleted_at + updated_at）。
// SET 列不带表限定——PG 不允许 update set 后使用限定名；
// 仅在存在 joins 时才限定（与上游行为一致）。
func (b *Builder) softDeleteRows() (int64, error) {
	info := b.modelInfo
	// 软删 onDelete 路径最终走底层 update，会应用 scopes：
	// WHERE 带 deleted_at is null（已软删行不重写）
	b.applyModelScopes()
	now := nowFunc()
	qualify := len(b.Query.Joins) > 0
	setCol := info.deletedAtField
	if qualify {
		setCol = info.table + "." + info.deletedAtField
	}
	columns := map[string]interface{}{setCol: now}
	if info.hasTimestamps {
		upd := info.updatedAtField
		if upd == "" {
			upd = "updated_at"
		}
		if qualify {
			upd = info.table + "." + upd
		}
		columns[upd] = now
	}
	// builder 级 onDelete 不分发任何模型事件（无模型实例）
	n, err := b.Update(columns)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// ---- SQL 与绑定 ----

// BeforeQuery 注册查询执行前回调（toSql 与写入族执行前应用）。
func (b *Builder) BeforeQuery(callback func(*Builder)) *Builder {
	b.beforeQueryCallbacks = append(b.beforeQueryCallbacks, callback)
	return b
}

// AfterQuery 注册查询执行后回调（Get 成功后应用）。
func (b *Builder) AfterQuery(callback func(any) any) *Builder {
	b.afterQueryCallbacks = append(b.afterQueryCallbacks, callback)
	return b
}

// applyBeforeQueryCallbacks 应用并清空执行前回调。
func (b *Builder) applyBeforeQueryCallbacks() {
	for _, cb := range b.beforeQueryCallbacks {
		cb(b)
	}
	b.beforeQueryCallbacks = nil
}

// applyAfterQueryCallbacks 应用执行后回调（回调返回 nil 时保留原结果）。
func (b *Builder) applyAfterQueryCallbacks(result any) any {
	for _, cb := range b.afterQueryCallbacks {
		if out := cb(result); out != nil {
			result = out
		}
	}
	b.afterQueryCallbacks = nil
	return result
}

func (b *Builder) ToSql() string {
	b.applyBeforeQueryCallbacks()
	return b.grammar.CompileSelect(b.Query)
}

// BuilderAddBinding 添加绑定；未知段 panic。
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
// GetBindings 按 grammar.BindingSegments 顺序合并绑定段（"with" 居首：
// CTE 编译在最终 SQL 最前，其占位符最先求值）。
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
// 软删 scope 的物化状态（scopesApplied/softDelete）随 wheres 一并继承，
// 避免克隆链（ChunkById/lazy 翻页）重复追加约束。
func (b *Builder) Clone() *Builder {
	nb := *b
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
		} else {
			boolean = "and"
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

// ---- 日期时间 where 家族（whereDate / whereTime / whereDay / whereMonth / whereYear）----

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
	if b.invalidOperator(operator) {
		operator, value = "=", operator
	}
	// 日期条件不把 nil 转为 whereNull：生成 date(col) = ?
	// 并绑定 null
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

// prepareJsonBinding：JSON contains 绑定的基础准备（
// 基础实现一律 json_encode，含标量——json_contains 的绑定必须是合法 JSON）。
func prepareJsonBinding(value interface{}) interface{} {
	switch value.(type) {
	case []interface{}, map[string]interface{}:
	default:
		// 标量也编码：whereJsonContains("meta->tags", "php") 绑定 "php"（带引号）
		if isJSONScalar(value) {
			data, err := json.Marshal(value)
			if err == nil {
				return string(data)
			}
		}
		return value
	}
	data, err := json.Marshal(value)
	if err != nil {
		return value
	}
	return string(data)
}

// isJSONScalar 判断值是否为可 JSON 编码的标量（bool/数字/字符串；Expression/
// time 等由调用方按原语义透传）。
func isJSONScalar(value interface{}) bool {
	switch value.(type) {
	case bool, string,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return true
	}
	return false
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
		b.AddBinding(b.prepareBindingForJsonContains(value), "where")
	}
	return b
}

// prepareBindingForJsonContains 允许 grammar 层按方言覆盖：
// （SQLite 原样、SqlServer bool → json），默认数组/对象 json_encode。
func (b *Builder) prepareBindingForJsonContains(value interface{}) interface{} {
	// 所有值都交 grammar->prepareBindingForJsonContains（
	// 基础实现一律 json_encode；SQLite 原样、SqlServer bool→json）
	if prep, ok := b.grammar.(interface{ PrepareBindingForJsonContains(any) any }); ok {
		return prep.PrepareBindingForJsonContains(value)
	}
	return prepareJsonBinding(value)
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

// OrWhereJsonDoesntOverlap 以 or 连接的 JSON 不相交（
// 即 whereJsonDoesntOverlap 的 or 版本）。
func (b *Builder) OrWhereJsonDoesntOverlap(column interface{}, value interface{}) *Builder {
	return b.jsonWhereImpl("JsonOverlaps", column, value, "or", true)
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
	if b.invalidOperator(operator) {
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

// WhereFullText 全文检索：columns、value 与可选 options。
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

// FirstOrFail 未命中返回 ErrNotFound。
func (b *Builder) FirstOrFail(dest interface{}, columns ...interface{}) error {
	if err := b.First(dest, columns...); err != nil {
		return err
	}
	if isEmptyDest(dest) {
		return ErrNotFound{Msg: "No query results for record."}
	}
	return nil
}

// Sole 要求恰好一行；零行 ErrNotFound，多行 ErrMultiple。
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

// FirstOr 未命中时执行 fallback。
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
// 回调返回 false 提前终止。
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

// ToRawSql 以字面值内联绑定输出 SQL。
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

// DD 输出 SQL 后终止进程。
func (b *Builder) DD() {
	fmt.Fprintln(os.Stderr, b.ToSql())
	os.Exit(1)
}

// DDRawSql 打印内联绑定后的 SQL。
// 与 dd 的差异：dd 是 die-dump（打印后终止脚本）；
// Go 侧不做 os.Exit（终止宿主进程的职责在调用方），仅打印后返回 builder。
func (b *Builder) DDRawSql() *Builder {
	fmt.Fprintln(os.Stderr, b.ToRawSql())
	return b
}

// Explain 返回执行计划（explain + SQL）。
// Explain 输出执行计划；SQLServer 不支持
// "explain select" 语法，显式报错。
func (b *Builder) Explain() ([]map[string]any, error) {
	switch strings.ToLower(b.Connection.Driver()) {
	case "sqlserver":
		return nil, grammar.ErrUnsupported{Msg: "This database engine does not support explain."}
	}
	return b.Connection.SelectMaps("explain "+b.ToSql(), b.GetBindings())
}

// UseIndex 设置索引提示（use index）。
func (b *Builder) UseIndex(index string) *Builder {
	b.Query.IndexHint = &query.IndexHint{Type: "hint", Index: index}
	return b
}

// ForceIndex 设置索引提示（force index）。
func (b *Builder) ForceIndex(index string) *Builder {
	b.Query.IndexHint = &query.IndexHint{Type: "force", Index: index}
	return b
}

// IgnoreIndex 设置索引提示（ignore index）。
func (b *Builder) IgnoreIndex(index string) *Builder {
	b.Query.IndexHint = &query.IndexHint{Type: "ignore", Index: index}
	return b
}

// ---- 游标与懒加载（ChunkById / Lazy / Cursor）----

// UseWritePdo 强制 select 走写连接。
func (b *Builder) UseWritePdo() *Builder {
	b.useWritePdo = true
	return b
}

// ForPageBeforeId 基于列游标向前翻页：where column < lastId + orderBy column desc + limit
// lastId 为 nil 时约束列非空。
func (b *Builder) ForPageBeforeId(perPage uint64, lastId interface{}, column ...string) *Builder {
	col := "id"
	if len(column) > 0 && column[0] != "" {
		col = column[0]
	}
	b.Query.Orders = b.removeExistingOrdersFor(col)
	if lastId == nil {
		b.WhereNotNull(col)
	} else {
		b.Where(col, "<", lastId)
	}
	return b.OrderBy(col, "desc").Limit(perPage)
}

// ForPageAfterId 基于列游标的分页：where column > lastId + orderBy column asc + limit
// 不使用 offset；lastId 为 nil 时约束列非空。
func (b *Builder) ForPageAfterId(perPage uint64, lastId interface{}, column ...string) *Builder {
	col := "id"
	if len(column) > 0 && column[0] != "" {
		col = column[0]
	}
	// 移除已有针对该列的排序（orders 过滤）
	b.Query.Orders = b.removeExistingOrdersFor(col)
	if lastId == nil {
		b.WhereNotNull(col)
	} else {
		b.Where(col, ">", lastId)
	}
	b.OrderBy(col, "asc")
	b.Limit(perPage)
	return b
}

// removeExistingOrdersFor 移除针对指定列的排序（对应 removeExistingOrdersFor）。
func (b *Builder) removeExistingOrdersFor(col string) []*query.Order {
	orders := make([]*query.Order, 0, len(b.Query.Orders))
	for _, o := range b.Query.Orders {
		if fmtString(o.Column) != col {
			orders = append(orders, o)
		}
	}
	return orders
}

// ChunkById 基于列游标的分块处理，避免大 offset。
// makeDest 每页创建扫描目标（如 func() any { return &[]User{} }）；回调返回 false 提前终止。
// 首块 lastId=nil 走 whereNotNull（起始游标不硬编码 0，
// 字符串主键/负值主键同样适用）。
func (b *Builder) ChunkById(perPage uint64, makeDest func() any, callback func(page any) (bool, error), column ...string) error {
	col := "id"
	if len(column) > 0 && column[0] != "" {
		col = column[0]
	}
	var lastId interface{}
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

// ChunkByIdDesc 基于列游标的降序分块：
// 游标向更小值推进，取块内最后一行的列值（降序排列时最小）。
func (b *Builder) ChunkByIdDesc(perPage uint64, makeDest func() any, callback func(page any) (bool, error), column ...string) error {
	col := "id"
	if len(column) > 0 && column[0] != "" {
		col = column[0]
	}
	var lastId interface{}
	for {
		nb := b.Clone().ForPageBeforeId(perPage, lastId, col)
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

// Cursor 全表流式读取：逐行产出 map（错误经第二通道传出）。
func (b *Builder) Cursor() (iter.Seq2[map[string]any, error], error) {
	b.applyModelScopes()
	return b.Connection.CursorQuery(b.ToSql(), b.GetBindings(), b.useWritePdo)
}

// Lazy 分页懒加载：内部按 limit/offset 翻页，逐行产出 map。
func (b *Builder) Lazy(perPage uint64) (iter.Seq2[map[string]any, error], error) {
	return b.lazyImpl(perPage, "", nil)
}

// LazyById 基于 id 游标的懒加载（无大 offset 问题）。
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
	var lastId interface{} // 首块走 whereNotNull（不硬编码 0）
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

// LazyByIdDesc 基于 id 游标的降序懒加载。
func (b *Builder) LazyByIdDesc(perPage uint64, column ...string) (iter.Seq2[map[string]any, error], error) {
	col := "id"
	if len(column) > 0 && column[0] != "" {
		col = column[0]
	}
	if perPage == 0 {
		perPage = 1000
	}
	b.applyModelScopes()
	var lastId interface{}
	return func(yield func(map[string]any, error) bool) {
		for {
			nb := b.Clone().ForPageBeforeId(perPage, lastId, col)
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
				if v, ok := row[col]; ok {
					lastId = v
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
		}
	}, nil
}

// lastColumnValue 取扫描结果最后一行中指定列的值（ChunkById 游标推进）。
// 列不存在于扫描目标时报错（旧实现静默返回 0 → 同一批数据 where id > 0
// 无限循环）。
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
		return nil, err
	}
	for _, f := range schema.Fields {
		if f.Name == col {
			return f.Value.Addr().Interface(), nil
		}
	}
	return nil, ErrInvalid{Msg: fmt.Sprintf("chunk column %q not present in scan destination", col)}
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

// ---- 分页族（paginate / simplePaginate / cursorPaginate）----

// LengthAwarePaginator 长度感知分页结果结构。
type LengthAwarePaginator struct {
	Items       any
	Total       int64
	PerPage     int64
	CurrentPage int64
	LastPage    int64
}

// SimplePaginator 简单分页结果结构（多取一条判断 hasMore）。
type SimplePaginator struct {
	Items       any
	PerPage     int64
	CurrentPage int64
	HasMore     bool
}

// CursorPaginator 游标分页结果结构。
type CursorPaginator struct {
	Items          any
	PerPage        int64
	HasMore        bool
	Cursor         *Cursor
	NextCursor     *Cursor
	PreviousCursor *Cursor
}

// Cursor 游标：参数集合 + 方向标记，
// 编码为 base64(json)（url 安全：+ → -、/ → _、= 剔除）。
type Cursor struct {
	parameters        map[string]any
	pointsToNextItems bool
}

// NewCursor 构造游标。
func NewCursor(parameters map[string]any, pointsToNextItems bool) *Cursor {
	return &Cursor{parameters: parameters, pointsToNextItems: pointsToNextItems}
}

// Parameter 取游标参数（缺失时 panic）。
func (c *Cursor) Parameter(name string) any {
	v, ok := c.parameters[name]
	if !ok {
		panic(ErrInvalid{"Unable to find parameter [" + name + "] in pagination item."})
	}
	return v
}

// PointsToNextItems 游标是否指向后一页。
func (c *Cursor) PointsToNextItems() bool { return c.pointsToNextItems }

// Encode 编码为 url 安全 base64（对应 Cursor::encode）。
func (c *Cursor) Encode() string {
	payload := make(map[string]any, len(c.parameters)+1)
	for k, v := range c.parameters {
		payload[k] = v
	}
	payload["_pointsToNextItems"] = c.pointsToNextItems
	data, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return base64UrlEncode(data)
}

// CursorFromEncoded 从编码串解码游标（对应 Cursor::fromEncoded；非法返回 nil）。
func CursorFromEncoded(encoded string) *Cursor {
	data, ok := base64UrlDecode(encoded)
	if !ok {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	var payload map[string]any
	if err := dec.Decode(&payload); err != nil {
		return nil
	}
	for k, v := range payload {
		if n, ok := v.(json.Number); ok {
			if i, err := n.Int64(); err == nil {
				payload[k] = i
			}
		}
	}
	raw, exists := payload["_pointsToNextItems"]
	if !exists {
		return nil
	}
	points, _ := raw.(bool)
	delete(payload, "_pointsToNextItems")
	return &Cursor{parameters: payload, pointsToNextItems: points}
}

func base64UrlEncode(data []byte) string {
	r := strings.NewReplacer("+", "-", "/", "_", "=", "")
	return r.Replace(base64.StdEncoding.EncodeToString(data))
}

func base64UrlDecode(encoded string) ([]byte, bool) {
	r := strings.NewReplacer("-", "+", "_", "/")
	padded := r.Replace(encoded)
	if m := len(padded) % 4; m != 0 {
		padded += strings.Repeat("=", 4-m)
	}
	data, err := base64.StdEncoding.DecodeString(padded)
	if err != nil {
		return nil, false
	}
	return data, true
}

// GetCountForPagination 取分页总数：
// 有 group/having 时包裹子查询计数，否则去除 columns/orders/limit/offset 后聚合。
func (b *Builder) GetCountForPagination(columns ...string) (int64, error) {
	cols := make([]interface{}, 0, len(columns))
	if len(columns) == 0 {
		cols = append(cols, "*")
	} else {
		for _, c := range columns {
			cols = append(cols, c)
		}
	}

	var total int64
	var nb *Builder

	if len(b.Query.Groups) > 0 || len(b.Query.Havings) > 0 {
		clone := b.CloneWithout("orders", "limit", "offset").CloneWithoutBindings("order")
		if clone.Query.Columns == nil && len(b.Query.Joins) > 0 {
			clone.Select(fmtString(b.Query.From) + ".*")
		}
		nb = NewBuilder(b.Connection, b.grammar).
			FromRaw(grammar.NewExpression("(" + clone.ToSql() + ") as " + b.grammar.Wrap("aggregate_table")))
		nb.MergeBindings(clone)
		nb.SetAggregate("count", withoutSelectAliases(cols))
	} else {
		var without []string
		var bindingExcept []string
		if len(b.Query.Unions) > 0 {
			without = []string{"unionOrders", "unionLimit", "unionOffset"}
			bindingExcept = []string{"unionOrder"}
		} else {
			without = []string{"columns", "orders", "limit", "offset"}
			bindingExcept = []string{"select", "order"}
		}
		nb = b.CloneWithout(without...).CloneWithoutBindings(bindingExcept...)
		nb.SetAggregate("count", withoutSelectAliases(cols))
	}

	if err := nb.Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

// withoutSelectAliases 去掉列别名（count 查询里别名会破坏聚合）。
func withoutSelectAliases(columns []interface{}) []interface{} {
	out := make([]interface{}, len(columns))
	for i, c := range columns {
		if s, ok := c.(string); ok {
			if pos := strings.Index(strings.ToLower(s), " as "); pos >= 0 {
				out[i] = s[:pos]
				continue
			}
		}
		out[i] = c
	}
	return out
}

// Paginate 长度感知分页：
// 总数为 0 时不执行取数查询。
func (b *Builder) Paginate(dest any, page, perPage int64) (*LengthAwarePaginator, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 15
	}
	total, err := b.GetCountForPagination()
	if err != nil {
		return nil, err
	}
	lastPage := int64(1)
	if total > 0 {
		lastPage = (total + perPage - 1) / perPage
		if lastPage < 1 {
			lastPage = 1
		}
	}
	p := &LengthAwarePaginator{Total: total, PerPage: perPage, CurrentPage: page, LastPage: lastPage}
	if total == 0 {
		return p, nil
	}
	nb := b.Clone().ForPage(uint64(page), uint64(perPage))
	if err := nb.Get(dest); err != nil {
		return nil, err
	}
	p.Items = dest
	return p, nil
}

// SimplePaginate 简单分页：
// 多取一条判断 hasMore，无 total。
func (b *Builder) SimplePaginate(dest any, page, perPage int64) (*SimplePaginator, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 15
	}
	nb := b.Clone().Offset(uint64((page - 1) * perPage)).Limit(uint64(perPage + 1))
	if err := nb.Get(dest); err != nil {
		return nil, err
	}
	p := &SimplePaginator{Items: dest, PerPage: perPage, CurrentPage: page}
	if n := reflectSliceLen(dest); n > int(perPage) {
		p.HasMore = true
		truncateSlice(dest, int(perPage))
	}
	return p, nil
}

// truncateSlice 把切片截断到 n 个元素。
func truncateSlice(dest any, n int) {
	rv := reflect.Indirect(reflect.ValueOf(dest))
	if rv.Kind() == reflect.Slice && rv.Len() > n {
		rv.Set(rv.Slice(0, n))
	}
}

// CursorPaginate 游标分页：
// 依据 order 列生成游标条件（支持多列嵌套 or），cursor 为 "" 或 nil 表示首页。
func (b *Builder) CursorPaginate(dest any, perPage int64, cursor ...*Cursor) (*CursorPaginator, error) {
	if perPage < 1 {
		perPage = 15
	}
	var cur *Cursor
	if len(cursor) > 0 {
		cur = cursor[0]
	}

	orders := b.ensureOrderForCursorPagination(cur != nil && !cur.pointsToNextItems)
	if len(orders) == 0 {
		return nil, ErrInvalid{"No order by defined for the cursor pagination."}
	}

	if cur != nil {
		b.addCursorConditions(b, nil, 0, orders, cur)
	}
	// union 场景：游标条件同样下推到每个 union 子查询，
	// 绑定归入 union 段（占位符位于子查询内部）
	if cur != nil && len(b.Query.Unions) > 0 {
		for _, u := range b.Query.Unions {
			if u == nil || u.Query == nil {
				continue
			}
			ub := NewBuilder(b.Connection, b.grammar)
			ub.Query = u.Query
			ub.Bindings = getDefaultBindings()
			b.addCursorConditions(ub, nil, 0, orders, cur)
			b.AddBinding(ub.Bindings["where"], "union")
		}
	}

	nb := b.Clone().Limit(uint64(perPage + 1))
	if err := nb.Get(dest); err != nil {
		return nil, err
	}

	p := &CursorPaginator{PerPage: perPage, Cursor: cur}
	count := reflectSliceLen(dest)
	if count > int(perPage) {
		p.HasMore = true
	}

	// 组装 next / previous 游标：
	// 先按查询方向截断到 perPage（丢弃多取的 extra 行，其值由下一页的
	// 严格边界条件重新取回），再算游标——NextCursor 取截断后末行（next 页起点
	// 严格大于该行）、PreviousCursor 取首行；反向导航（pointsToNextItems=false）
	// 时查询按反转方向取回，展示前需反转结果集。
	if p.HasMore {
		truncateSlice(dest, int(perPage))
	}
	if cur != nil && !cur.pointsToNextItems {
		reverseSlice(dest)
	}
	count = reflectSliceLen(dest)
	params := make([]string, 0, len(orders))
	for _, o := range orders {
		params = append(params, fmtString(o.Column))
	}
	if count > 0 {
		if p.HasMore {
			last := sliceItem(dest, count-1)
			nextParams := map[string]any{}
			for _, col := range params {
				nextParams[col] = cursorColumnValue(last, col)
			}
			p.NextCursor = NewCursor(nextParams, true)
		}
		// previous 游标：反向导航且无更多数据（已是第一页）时不生成
		if cur != nil && !(cur.pointsToNextItems == false && !p.HasMore) {
			first := sliceItem(dest, 0)
			prevParams := map[string]any{}
			for _, col := range params {
				prevParams[col] = cursorColumnValue(first, col)
			}
			p.PreviousCursor = NewCursor(prevParams, false)
		}
	}
	return p, nil
}

// reverseSlice 原地反转切片元素。
func reverseSlice(dest any) {
	rv := reflect.Indirect(reflect.ValueOf(dest))
	if rv.Kind() != reflect.Slice {
		return
	}
	n := rv.Len()
	for i, j := 0, n-1; i < j; i, j = i+1, j-1 {
		a, b := rv.Index(i), rv.Index(j)
		av, bv := reflect.New(rv.Index(i).Type()).Elem(), reflect.New(rv.Index(j).Type()).Elem()
		av.Set(a)
		bv.Set(b)
		a.Set(bv)
		b.Set(av)
	}
}

// ensureOrderForCursorPagination 对应 ensureOrderForCursorPagination：
// 无排序时抛错；shouldReverse 时反转全部方向；返回带方向的 orders。
func (b *Builder) ensureOrderForCursorPagination(shouldReverse bool) []*query.Order {
	if len(b.Query.Orders) == 0 && len(b.Query.UnionOrders) == 0 {
		panic(ErrInvalid{"No order by column defined for the cursor pagination."})
	}
	if shouldReverse {
		for _, o := range b.Query.Orders {
			if o.Direction == "asc" {
				o.Direction = "desc"
			} else {
				o.Direction = "asc"
			}
		}
		for _, o := range b.Query.UnionOrders {
			if o.Direction == "asc" {
				o.Direction = "desc"
			} else {
				o.Direction = "asc"
			}
		}
	}
	orders := b.Query.Orders
	if len(b.Query.UnionOrders) > 0 {
		orders = b.Query.UnionOrders
	}
	out := make([]*query.Order, 0, len(orders))
	for _, o := range orders {
		if o.Direction != "" {
			out = append(out, o)
		}
	}
	return out
}

// addCursorConditions 递归生成游标条件：
// 前序列相等 + 当前列比较（asc 为 >，desc 为 <），后续列以 or 嵌套。
func (b *Builder) addCursorConditions(bb *Builder, previousColumn interface{}, i int, orders []*query.Order, cur *Cursor) {
	if previousColumn != nil {
		bb.Where(previousColumn, "=", cur.Parameter(fmtString(previousColumn)))
	}
	bb.WhereNested(func(second *Builder) {
		order := orders[i]
		column := fmtString(order.Column)
		operator := ">"
		if order.Direction == "desc" {
			operator = "<"
		}
		second.Where(column, operator, cur.Parameter(column))
		if i < len(orders)-1 {
			second.OrWhereNested(func(third *Builder) {
				b.addCursorConditions(third, column, i+1, orders, cur)
			})
		}
	}, "and")
}

// sliceItem 取切片第 i 个元素（处理 []T 与 []*T）。
func sliceItem(dest any, i int) any {
	rv := reflect.Indirect(reflect.ValueOf(dest))
	if rv.Kind() != reflect.Slice || i >= rv.Len() {
		return nil
	}
	item := rv.Index(i)
	if item.Kind() == reflect.Ptr {
		return item.Interface()
	}
	if !item.CanAddr() {
		cp := reflect.New(item.Type())
		cp.Elem().Set(item)
		return cp.Interface()
	}
	return item.Addr().Interface()
}

// cursorColumnValue 从扫描行提取 order 列值（map 或 struct）。
func cursorColumnValue(item any, col string) any {
	if item == nil {
		return nil
	}
	if m, ok := item.(map[string]any); ok {
		return m[col]
	}
	rv := reflect.Indirect(reflect.ValueOf(item))
	if rv.Kind() != reflect.Struct {
		return nil
	}
	sc, err := NewSchema(item)
	if err != nil {
		return nil
	}
	for _, f := range sc.Fields {
		if f.Name == col {
			return reflect.Indirect(f.Value).Interface()
		}
	}
	return nil
}
