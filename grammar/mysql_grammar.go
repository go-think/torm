package grammar

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-think/torm/query"
)

// MySqlGrammar 对应 Illuminate\Database\Query\Grammars\MySqlGrammar。
// 方言差异通过构造器注入的钩子实现；必须使用 NewMySqlGrammar 构造。
type MySqlGrammar struct {
	QueryGrammar

	// useUpsertAlias：upsert 编译走 as laravel_upsert_alias 形式
	// （对应连接配置 use_upsert_alias）。
	useUpsertAlias bool

	// D7 版本分支注入参数（对应 Laravel connection->getServerVersion()/isMaria()，
	// 经 SetServerVersion/SetIsMaria 注入，避免 grammar 反向依赖 connection）。
	serverVersion string
	isMaria       bool
}

// SetServerVersion 注入服务器版本（如 "8.0.36" / "10.5.2-MariaDB"）；
// 空串表示未知，各方言版本分支保守取现代语法。
func (g *MySqlGrammar) SetServerVersion(v string) { g.serverVersion = v }

// SetIsMaria 标记 MariaDB 方言（对应 connection->isMaria()，按 driver 传入）。
func (g *MySqlGrammar) SetIsMaria(v bool) { g.isMaria = v }

func NewMySqlGrammar() *MySqlGrammar {
	g := &MySqlGrammar{}

	g.BaseGrammar.wrapValueFn = mysqlWrapValue
	g.BaseGrammar.wrapJsonSelectorFn = g.wrapJsonSelector
	g.BaseGrammar.wrapJsonBoolSelFn = g.wrapJsonBooleanSelector
	g.BaseGrammar.jsonFieldAndPathFn = g.wrapJsonFieldAndPath

	g.compileIndexHintFn = g.compileIndexHint
	g.whereNullFn = g.whereNull
	g.whereNotNullFn = g.whereNotNull
	g.whereLikeFn = g.whereLike
	g.whereNullSafeEqualsFn = g.whereNullSafeEquals
	g.whereFullTextFn = g.whereFullText

	g.compileJsonContainsFn = g.compileJsonContains
	g.compileJsonOverlapsFn = g.compileJsonOverlaps
	g.compileJsonContainsKeyFn = g.compileJsonContainsKey
	g.compileJsonLengthFn = g.compileJsonLength
	g.compileJsonUpdateColFn = g.compileJsonUpdateColumn

	g.compileLimitFn = g.compileLimit
	g.compileLockFn = g.compileLock
	g.compileRandomFn = g.compileRandom
	g.compileTimeoutFn = g.compileTimeout
	g.compileJoinLateralFn = g.compileJoinLateral
	g.supportsStraightJoinsFn = func() bool { return true }
	g.compileJsonValueCastFn = g.compileJsonValueCast

	g.compileInsertFn = g.compileInsert
	g.compileInsertOrIgnoreFn = g.compileInsertOrIgnore
	g.compileInsertOrIgnoreUsingFn = g.compileInsertOrIgnoreUsing
	g.compileUpsertFn = g.compileUpsert
	g.compileUpdateWithoutJoinsFn = g.compileUpdateWithoutJoins
	g.compileDeleteWithoutJoinsFn = g.compileDeleteWithoutJoins
	g.compileDeleteWithJoinsFn = g.compileDeleteWithJoins

	return g
}

// SetUseUpsertAlias 切换 upsert 的别名形式（对应 use_upsert_alias 配置）。
func (g *MySqlGrammar) SetUseUpsertAlias(v bool) {
	g.useUpsertAlias = v
}

// compileTimeout 对应 MySqlGrammar::compileSelect 的超时改写：
// 把开头的 select 替换为 select /*+ MAX_EXECUTION_TIME(ms) */。
func (g *MySqlGrammar) compileTimeout(sql string, seconds int64) string {
	milliseconds := seconds * 1000
	return selectPrefix.ReplaceAllString(sql, "select /*+ MAX_EXECUTION_TIME("+intToString(milliseconds)+") */")
}

var selectPrefix = regexp.MustCompile(`^select\b`)

// compileJoinLateral 对应 MySqlGrammar::compileJoinLateral。
func (g *MySqlGrammar) compileJoinLateral(expr, joinType string) string {
	return strings.TrimSpace(joinType + " join lateral " + expr + " on true")
}

func mysqlWrapValue(value string) string {
	if value == "*" {
		return value
	}
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}

// ---- JSON ----

func (g *MySqlGrammar) wrapJsonSelector(value string) string {
	field, path := g.wrapJsonFieldAndPath(value)
	return "json_unquote(json_extract(" + field + path + "))"
}

func (g *MySqlGrammar) wrapJsonBooleanSelector(value string) string {
	field, path := g.wrapJsonFieldAndPath(value)
	return "json_extract(" + field + path + ")"
}

func (g *MySqlGrammar) compileJsonContains(column, value string) string {
	field, path := g.wrapJsonFieldAndPath(column)
	return "json_contains(" + field + ", " + value + path + ")"
}

func (g *MySqlGrammar) compileJsonOverlaps(column, value string) string {
	field, path := g.wrapJsonFieldAndPath(column)
	return "json_overlaps(" + field + ", " + value + path + ")"
}

func (g *MySqlGrammar) compileJsonContainsKey(column string) string {
	field, path := g.wrapJsonFieldAndPath(column)
	return "ifnull(json_contains_path(" + field + ", 'one'" + path + "), 0)"
}

func (g *MySqlGrammar) compileJsonLength(column, operator, value string) string {
	field, path := g.wrapJsonFieldAndPath(column)
	return "json_length(" + field + path + ") " + operator + " " + value
}

// compileJsonValueCast 把值 cast 为 json。
func (g *MySqlGrammar) compileJsonValueCast(value string) string {
	return "cast(" + value + " as json)"
}

func (g *MySqlGrammar) wrapJsonFieldAndPath(column string) (string, string) {
	parts := strings.SplitN(column, "->", 2)
	field := g.Wrap(parts[0])
	path := ""
	if len(parts) > 1 {
		path = ", " + g.wrapJsonPath(parts[1], "->")
	}
	return field, path
}

// ---- where 覆盖 ----

func (g *MySqlGrammar) whereNull(q *query.Query, w *query.Where) string {
	columnValue := fmtString(g.GetValue(w.Column))
	if g.isJsonSelector(columnValue) {
		field, path := g.wrapJsonFieldAndPath(columnValue)
		return "(json_extract(" + field + path + ") is null OR json_type(json_extract(" + field + path + ")) = 'NULL')"
	}
	return g.QueryGrammar.whereNull(q, w)
}

func (g *MySqlGrammar) whereNotNull(q *query.Query, w *query.Where) string {
	columnValue := fmtString(g.GetValue(w.Column))
	if g.isJsonSelector(columnValue) {
		field, path := g.wrapJsonFieldAndPath(columnValue)
		return "(json_extract(" + field + path + ") is not null AND json_type(json_extract(" + field + path + ")) != 'NULL')"
	}
	return g.QueryGrammar.whereNotNull(q, w)
}

func (g *MySqlGrammar) whereLike(q *query.Query, w *query.Where) string {
	operator := ""
	if w.Not {
		operator = "not "
	}
	if w.CaseSensitive {
		operator += "like binary"
	} else {
		operator += "like"
	}
	return g.Wrap(w.Column) + " " + operator + " " + g.Parameter(w.Value)
}

func (g *MySqlGrammar) whereNullSafeEquals(q *query.Query, w *query.Where) string {
	return g.Wrap(w.Column) + " <=> " + g.Parameter(w.Value)
}

func (g *MySqlGrammar) whereFullText(q *query.Query, w *query.Where) string {
	columns := g.Columnize(w.Columns)
	value := g.Parameter(w.Value)

	mode := " in natural language mode"
	if m, ok := w.Options["mode"].(string); ok && m == "boolean" {
		mode = " in boolean mode"
	}

	expanded := ""
	if e, ok := w.Options["expanded"].(bool); ok && e && mode != " in boolean mode" {
		expanded = " with query expansion"
	}

	return "match (" + columns + ") against (" + value + mode + expanded + ")"
}

// ---- index hint / random / lock ----

var indexNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_$]+$`)

func (g *MySqlGrammar) compileIndexHint(q *query.Query, hint *query.IndexHint) string {
	index := hint.Index
	for _, i := range strings.Split(index, ",") {
		if !indexNamePattern.MatchString(strings.TrimSpace(i)) {
			panic(ErrUnsupported{"Index name contains invalid characters."})
		}
	}
	switch hint.Type {
	case "hint":
		return "use index (" + index + ")"
	case "force":
		return "force index (" + index + ")"
	default:
		return "ignore index (" + index + ")"
	}
}

func (g *MySqlGrammar) compileRandom(seed any) string {
	if seed == nil || seed == "" {
		return "RAND()"
	}
	if s, ok := seed.(string); ok {
		n, err := strconv.Atoi(s)
		if err != nil {
			panic(ErrUnsupported{"The seed value must be numeric."})
		}
		return "RAND(" + strconv.Itoa(n) + ")"
	}
	switch v := seed.(type) {
	case int:
		return "RAND(" + strconv.Itoa(v) + ")"
	case int64:
		return "RAND(" + strconv.FormatInt(v, 10) + ")"
	default:
		panic(ErrUnsupported{"The seed value must be numeric."})
	}
}

func (g *MySqlGrammar) compileLock(q *query.Query, value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	if bv, ok := value.(bool); ok {
		if bv {
			return "for update"
		}
		return "lock in share mode"
	}
	return ""
}

// compileLimit MySQL 支持负值输出（与 Laravel 一致，limit 为 int）。
func (g *MySqlGrammar) compileLimit(q *query.Query, limit int64) string {
	return "limit " + intToString(limit)
}

// ---- insert / upsert ----

func (g *MySqlGrammar) compileInsert(q *query.Query, rows []query.Row) (string, []any) {
	if len(rows) == 0 {
		rows = []query.Row{{}}
	}
	return g.QueryGrammar.baseCompileInsert(q, rows)
}

func (g *MySqlGrammar) compileInsertOrIgnore(q *query.Query, rows []query.Row) (string, []any) {
	sql, bindings := g.QueryGrammar.baseCompileInsert(q, rows)
	return replaceFirst("insert", "insert ignore", sql), bindings
}

func (g *MySqlGrammar) compileInsertOrIgnoreUsing(q *query.Query, columns []any, sql string) string {
	return replaceFirst("insert", "insert ignore", g.QueryGrammar.CompileInsertUsing(q, columns, sql))
}

func (g *MySqlGrammar) compileUpsert(q *query.Query, rows []query.Row, uniqueBy, update []string) (string, []any) {
	sql, bindings := g.QueryGrammar.baseCompileInsert(q, rows)

	if g.useUpsertAlias {
		sql += " as laravel_upsert_alias"
	}

	sql += " on duplicate key update "

	cols := make([]string, 0, len(update))
	for _, column := range update {
		if g.useUpsertAlias {
			cols = append(cols, g.Wrap(column)+" = "+g.Wrap("laravel_upsert_alias")+"."+g.Wrap(column))
			continue
		}
		cols = append(cols, g.Wrap(column)+" = values("+g.Wrap(column)+")")
	}

	return sql + strings.Join(cols, ", "), bindings
}

// PrepareBindingsForUpdate 对应 MySqlGrammar::prepareBindingsForUpdate：
// JSON 选择器上的 bool 值剔除（编译为 raw true/false），数组值 json_encode。
func (g *MySqlGrammar) PrepareBindingsForUpdate(bindings map[string][]any, values map[string]any) []any {
	vals := make(map[string]any, len(values))
	for _, key := range sortedKeys(values) {
		v := values[key]
		if g.isJsonSelector(key) {
			if _, ok := v.(bool); ok {
				continue
			}
		}
		if arr, ok := v.([]any); ok {
			data, err := json.Marshal(arr)
			if err == nil {
				v = string(data)
			}
		}
		vals[key] = v
	}
	return g.QueryGrammar.PrepareBindingsForUpdate(bindings, vals)
}

func (g *MySqlGrammar) compileJsonUpdateColumn(key string, value any) string {
	var v string
	if bv, ok := value.(bool); ok {
		if bv {
			v = "true"
		} else {
			v = "false"
		}
	} else if _, ok := value.([]any); ok {
		v = "cast(? as json)"
	} else {
		v = g.Parameter(value)
	}

	field, path := g.wrapJsonFieldAndPath(key)
	return field + " = json_set(" + field + path + ", " + v + ")"
}

// ---- update / delete 追加 order/limit ----

func (g *MySqlGrammar) compileUpdateWithoutJoins(q *query.Query, table, columns, where string) string {
	sql := "update " + table + " set " + columns + " " + where

	if len(q.Orders) > 0 {
		sql += " " + g.compileOrders(q, q.Orders)
	}
	if q.Limit != nil {
		sql += " " + g.compileLimit(q, *q.Limit)
	}
	return sql
}

func (g *MySqlGrammar) compileDeleteWithoutJoins(q *query.Query, table, where string) string {
	sql := "delete from " + table + " " + where

	if len(q.Orders) > 0 {
		sql += " " + g.compileOrders(q, q.Orders)
	}
	if q.Limit != nil {
		sql += " " + g.compileLimit(q, *q.Limit)
	}
	return sql
}

func (g *MySqlGrammar) compileDeleteWithJoins(q *query.Query, table, where string) string {
	sql := g.QueryGrammar.compileDeleteWithJoins(q, table, where)

	if len(q.Orders) > 0 {
		sql += " " + g.compileOrders(q, q.Orders)
	}
	if q.Limit != nil {
		sql += " " + g.compileLimit(q, *q.Limit)
	}
	return sql
}
