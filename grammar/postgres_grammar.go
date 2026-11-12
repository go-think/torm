package grammar

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/thinkoner/torm/query"
)

// PostgresGrammar 对应 Illuminate\Database\Query\Grammars\PostgresGrammar。
type PostgresGrammar struct {
	QueryGrammar
}

func NewPostgresGrammar() *PostgresGrammar {
	g := &PostgresGrammar{}

	g.BaseGrammar.wrapJsonSelectorFn = g.wrapJsonSelector
	g.BaseGrammar.wrapJsonBoolSelFn = g.wrapJsonBooleanSelector
	g.BaseGrammar.wrapJsonBoolValueFn = g.wrapJsonBooleanValue
	g.BaseGrammar.jsonFieldAndPathFn = g.wrapJsonFieldAndPath
	g.BaseGrammar.wrapJsonPathSegFn = g.wrapJsonPathSegment

	g.compileColumnsFn = g.compileColumns
	g.compileLockFn = g.compileLock

	g.compileJsonContainsFn = g.compileJsonContains
	g.compileJsonContainsKeyFn = g.compileJsonContainsKey
	g.compileJsonLengthFn = g.compileJsonLength
	g.compileJsonUpdateColFn = g.compileJsonUpdateColumn

	g.whereBasicFn = g.whereBasic
	g.whereLikeFn = g.whereLike
	g.whereDateFn = g.whereDate
	g.whereTimeFn = g.whereTime
	g.dateBasedWhereFn = g.dateBasedWhere
	g.whereFullTextFn = g.whereFullText
	g.whereBitwiseFn = g.whereBitwise

	g.compileInsertOrIgnoreFn = g.compileInsertOrIgnore
	g.compileInsertOrIgnoreUsingFn = g.compileInsertOrIgnoreUsing
	g.compileInsertGetIdFn = g.compileInsertGetId
	g.compileUpsertFn = g.compileUpsert
	g.compileUpdateFn = g.compileUpdate
	g.compileUpdateColumnsFn = g.compileUpdateColumns
	g.compileTruncateFn = g.compileTruncate
	g.compileHavingBitwiseFn = g.compileHavingBitwise
	g.compileJoinLateralFn = g.compileJoinLateral

	return g
}

// whereBitwise 对应 PostgresGrammar::whereBitwise：(col op ?)::bool。
func (g *PostgresGrammar) whereBitwise(q *query.Query, w *query.Where) string {
	value := g.Parameter(w.Value)
	operator := escapeOperator(w.Operator)
	return "(" + g.Wrap(w.Column) + " " + operator + " " + value + ")::bool"
}

// compileHavingBitwise 对应 PostgresGrammar::compileHavingBitwise。
func (g *PostgresGrammar) compileHavingBitwise(h *query.Where) string {
	return "(" + g.Wrap(h.Column) + " " + h.Operator + " " + g.Parameter(h.Value) + ")::bool"
}

// compileJoinLateral 对应 PostgresGrammar::compileJoinLateral。
func (g *PostgresGrammar) compileJoinLateral(expr, joinType string) string {
	return strings.TrimSpace(joinType + " join lateral " + expr + " on true")
}

// BitwiseOperators 对应 PostgresGrammar::$bitwiseOperators（builder 侧识别用）。
func (g *PostgresGrammar) BitwiseOperators() []string {
	return []string{"~", "&", "|", "#", "<<", ">>", "<<=", ">>="}
}

// pgValidFullTextLanguages 对应 validFullTextLanguages 白名单。
var pgValidFullTextLanguages = []string{
	"simple", "arabic", "danish", "dutch", "english", "finnish", "french",
	"german", "hungarian", "indonesian", "irish", "italian", "lithuanian",
	"nepali", "norwegian", "portuguese", "romanian", "russian", "spanish",
	"swedish", "tamil", "turkish",
}

// ---- where 覆盖 ----

func (g *PostgresGrammar) whereBasic(q *query.Query, w *query.Where) string {
	if strings.Contains(strings.ToLower(w.Operator), "like") {
		return fmt.Sprintf("%s::text %s %s", g.Wrap(w.Column), w.Operator, g.Parameter(w.Value))
	}
	return g.QueryGrammar.whereBasic(q, w)
}

func (g *PostgresGrammar) whereLike(q *query.Query, w *query.Where) string {
	operator := ""
	if w.Not {
		operator = "not "
	}
	if w.CaseSensitive {
		operator += "like"
	} else {
		operator += "ilike"
	}
	return g.Wrap(w.Column) + "::text " + operator + " " + g.Parameter(w.Value)
}

func (g *PostgresGrammar) whereDate(q *query.Query, w *query.Where) string {
	column := g.Wrap(w.Column)
	value := g.Parameter(w.Value)
	if g.isJsonSelector(column) {
		column = "(" + column + ")"
	}
	return column + "::date " + w.Operator + " " + value
}

func (g *PostgresGrammar) whereTime(q *query.Query, w *query.Where) string {
	column := g.Wrap(w.Column)
	value := g.Parameter(w.Value)
	if g.isJsonSelector(column) {
		column = "(" + column + ")"
	}
	return column + "::time " + w.Operator + " " + value
}

func (g *PostgresGrammar) dateBasedWhere(kind string, q *query.Query, w *query.Where) string {
	value := g.Parameter(w.Value)
	return "extract(" + kind + " from " + g.Wrap(w.Column) + ") " + w.Operator + " " + value
}

func (g *PostgresGrammar) whereFullText(q *query.Query, w *query.Where) string {
	language := "english"
	if l, ok := w.Options["language"].(string); ok {
		language = l
	}

	// 语言白名单：非法语言回退 english（对应 validFullTextLanguages）
	valid := false
	for _, l := range pgValidFullTextLanguages {
		if language == l {
			valid = true
			break
		}
	}
	if !valid {
		language = "english"
	}

	parts := make([]string, 0, len(w.Columns))
	for _, column := range w.Columns {
		parts = append(parts, fmt.Sprintf("to_tsvector('%s', %s)", language, g.Wrap(column)))
	}

	mode := "plainto_tsquery"
	if m, ok := w.Options["mode"].(string); ok {
		switch m {
		case "phrase":
			mode = "phraseto_tsquery"
		case "websearch":
			mode = "websearch_to_tsquery"
		case "raw":
			mode = "to_tsquery"
		}
	}

	return "(" + strings.Join(parts, " || ") + ") @@ " + mode + "('" + language + "', " + g.Parameter(w.Value) + ")"
}

// ---- columns ----

func (g *PostgresGrammar) compileColumns(q *query.Query, columns []any) string {
	if q.Aggregate != nil {
		return ""
	}

	var sel string
	if distinctColumns, ok := q.Distinct.([]any); ok {
		sel = "select distinct on (" + g.Columnize(distinctColumns) + ") "
	} else if isTrueDistinct(q.Distinct) {
		sel = "select distinct "
	} else {
		sel = "select "
	}

	return sel + g.Columnize(columns)
}

// ---- JSON ----

func (g *PostgresGrammar) compileJsonContains(column, value string) string {
	column = strings.ReplaceAll(g.Wrap(column), "->>", "->")
	return "(" + column + ")::jsonb @> " + value
}

func (g *PostgresGrammar) compileJsonContainsKey(column string) string {
	segments := strings.Split(column, "->")
	lastSegment := segments[len(segments)-1]
	segments = segments[:len(segments)-1]

	i := ""
	if isIntString(lastSegment) {
		i = lastSegment
	} else if m := jsonTrailingIndex.FindStringSubmatch(lastSegment); m != nil {
		segments = append(segments, strings.TrimSuffix(lastSegment, m[0]))
		i = m[1]
	}

	col := strings.ReplaceAll(g.Wrap(strings.Join(segments, "->")), "->>", "->")

	if i != "" {
		n := atoi(i)
		bound := n
		if n < 0 {
			bound = -n
		} else {
			bound = n + 1
		}
		return fmt.Sprintf("case when jsonb_typeof((%s)::jsonb) = 'array' then jsonb_array_length((%s)::jsonb) >= %s else false end", col, col, itoa(bound))
	}

	key := "'" + strings.ReplaceAll(lastSegment, "'", "''") + "'"
	return "coalesce((" + col + ")::jsonb ?? " + key + ", false)"
}

func (g *PostgresGrammar) compileJsonLength(column, operator, value string) string {
	column = strings.ReplaceAll(g.Wrap(column), "->>", "->")
	return "jsonb_array_length((" + column + ")::jsonb) " + operator + " " + value
}

func (g *PostgresGrammar) wrapJsonSelector(value string) string {
	path := strings.Split(value, "->")
	field := g.wrapSegments(strings.Split(path[0], "."))
	path = path[1:]

	wrappedPath := g.wrapJsonPathAttributes(path, "'")

	attribute := wrappedPath[len(wrappedPath)-1]
	wrappedPath = wrappedPath[:len(wrappedPath)-1]

	if len(wrappedPath) > 0 {
		return field + "->" + strings.Join(wrappedPath, "->") + "->>" + attribute
	}
	return field + "->>" + attribute
}

func (g *PostgresGrammar) wrapJsonBooleanSelector(value string) string {
	selector := strings.ReplaceAll(g.wrapJsonSelector(value), "->>", "->")
	return "(" + selector + ")::jsonb"
}

func (g *PostgresGrammar) wrapJsonBooleanValue(value string) string {
	return "'" + value + "'::jsonb"
}

// parseJsonPathArrayKeys 对应 Laravel PostgresGrammar::parseJsonPathArrayKeys：
// 把 "attr[0][1]" 拆为 ["attr", "0", "1"]（取括号内内容，不带方括号）。
// 返回原始片段，引号由 wrapJsonPathAttributes 按 quote 参数统一添加。
func (g *PostgresGrammar) parseJsonPathArrayKeys(segment string) []string {
	m := jsonTrailingIndex.FindStringSubmatch(segment)
	if m == nil {
		return []string{segment}
	}
	// m[0] 为结尾的完整括号组（如 "[0][1]"），key 为其前的属性名
	key := strings.TrimSuffix(segment, m[0])
	keys := jsonArrayKeyPattern.FindAllStringSubmatch(m[0], -1)

	out := make([]string, 0, len(keys)+1)
	if key != "" {
		out = append(out, key)
	}
	for _, k := range keys {
		// 对应 Laravel 的 ->diff('')：剔除空片段
		if k[1] != "" {
			out = append(out, k[1])
		}
	}
	return out
}

// jsonArrayKeyPattern 对应 Laravel 的 preg_match_all('/\[([^\]]+)\]/', ...)。
var jsonArrayKeyPattern = regexp.MustCompile(`\[([^\]]+)\]`)

// wrapJsonPathAttributes 对应 Laravel PostgresGrammar::wrapJsonPathAttributes：
// 展开数组下标后逐段加引号；整数下标保持裸数字（filter_var FILTER_VALIDATE_INT）。
func (g *PostgresGrammar) wrapJsonPathAttributes(path []string, quote string) []string {
	var attrs []string
	for _, attribute := range path {
		attrs = append(attrs, g.parseJsonPathArrayKeys(attribute)...)
	}
	out := make([]string, len(attrs))
	for i, attr := range attrs {
		out[i] = pgQuotePath(attr, quote)
	}
	return out
}

func pgQuotePath(attr, quote string) string {
	if isIntString(attr) {
		return attr
	}
	attr = strings.ReplaceAll(attr, "'", "''")
	if quote != "'" {
		attr = strings.ReplaceAll(attr, quote, quote+quote)
	}
	return quote + attr + quote
}

func (g *PostgresGrammar) wrapJsonPathSegment(segment string) string {
	if isIntString(segment) {
		return segment
	}
	return `"` + segment + `"`
}

// compileJsonUpdateColumn 对应 PG 的 jsonb_set 更新。
func (g *PostgresGrammar) compileJsonUpdateColumn(key string, value any) string {
	segments := strings.Split(key, "->")
	field := g.Wrap(segments[0])
	segments = segments[1:]

	attrs := g.wrapJsonPathAttributes(segments, `"`)
	path := "'{" + strings.Join(attrs, ",") + "}'"

	return fmt.Sprintf("%s = jsonb_set(%s::jsonb, %s, %s)", field, field, path, g.Parameter(value))
}

// ---- lock / truncate ----

func (g *PostgresGrammar) compileLock(q *query.Query, value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	if bv, ok := value.(bool); ok {
		if bv {
			return "for update"
		}
		return "for share"
	}
	return ""
}

func (g *PostgresGrammar) compileTruncate(q *query.Query) []TruncateStatement {
	return []TruncateStatement{{Sql: "truncate " + g.WrapTable(q.From) + " restart identity cascade"}}
}

// ---- 写入族 ----

// Operators 对应 PostgresGrammar::$operators（PostgresGrammar.php:18-24）。
func (g *PostgresGrammar) Operators() []string {
	return []string{
		"=", "<", ">", "<=", ">=", "<>", "!=",
		"like", "not like", "between", "ilike", "not ilike",
		"~", "&", "|", "#", "<<", ">>", "<<=", ">>=",
		"&&", "@>", "<@", "?", "?|", "?&", "||", "-", "@?", "@@", "#-",
		"is distinct from", "is not distinct from",
	}
}

func (g *PostgresGrammar) compileInsertOrIgnore(q *query.Query, rows []query.Row) (string, []any) {
	sql, bindings := g.QueryGrammar.baseCompileInsert(q, rows)
	return sql + " on conflict do nothing", bindings
}

// CompileInsertOrIgnoreReturning 对应 PostgresGrammar.php:406-414：
// insert or ignore + returning（uniqueBy 非空时限定 conflict 列）。
func (g *PostgresGrammar) CompileInsertOrIgnoreReturning(q *query.Query, rows []query.Row, returning, uniqueBy []string) (string, []any) {
	sql, bindings := g.QueryGrammar.baseCompileInsert(q, rows)
	if len(uniqueBy) > 0 {
		cols := make([]string, 0, len(uniqueBy))
		for _, c := range uniqueBy {
			cols = append(cols, g.Wrap(c))
		}
		sql += " on conflict (" + strings.Join(cols, ", ") + ") do nothing"
	} else {
		sql += " on conflict do nothing"
	}
	returns := make([]string, 0, len(returning))
	for _, c := range returning {
		returns = append(returns, g.Wrap(c))
	}
	sql += " returning " + strings.Join(returns, ", ")
	return sql, bindings
}

func (g *PostgresGrammar) compileInsertOrIgnoreUsing(q *query.Query, columns []any, sql string) string {
	return g.QueryGrammar.CompileInsertUsing(q, columns, sql) + " on conflict do nothing"
}

func (g *PostgresGrammar) compileInsertGetId(q *query.Query, rows []query.Row, sequence string) (string, []any) {
	sql, bindings := g.QueryGrammar.baseCompileInsert(q, rows)
	if sequence == "" {
		sequence = "id"
	}
	return sql + " returning " + g.Wrap(sequence), bindings
}

func (g *PostgresGrammar) compileUpsert(q *query.Query, rows []query.Row, uniqueBy, update []string) (string, []any) {
	sql, bindings := g.QueryGrammar.baseCompileInsert(q, rows)

	sql += " on conflict (" + g.Columnize(toAnySlice(uniqueBy)) + ") do update set "

	cols := make([]string, 0, len(update))
	for _, column := range update {
		cols = append(cols, g.Wrap(column)+" = "+g.wrapValue("excluded")+"."+g.Wrap(column))
	}

	return sql + strings.Join(cols, ", "), bindings
}

// CompileDelete 对应 Laravel PostgresGrammar::compileDelete：
// 带 joins 或 limit 时改写为 delete ... where ctid in (select ctid ...)。
func (g *PostgresGrammar) CompileDelete(q *query.Query) string {
	if len(q.Joins) > 0 || q.Limit != nil {
		return g.compileDeleteWithJoinsOrLimit(q)
	}
	return g.QueryGrammar.CompileDelete(q)
}

// compileDeleteWithJoinsOrLimit 对应 Laravel PostgresGrammar::compileDeleteWithJoinsOrLimit：
// $query->select($alias.'.ctid') 后 compileSelect（joins/wheres/limit 保留在子查询内）。
func (g *PostgresGrammar) compileDeleteWithJoinsOrLimit(q *query.Query) string {
	table := g.WrapTable(q.From)

	alias := fmtString(q.From)
	if segs := splitAs(alias); len(segs) > 0 {
		alias = segs[len(segs)-1]
	}

	sub := *q
	sub.Columns = []any{alias + ".ctid"}
	selectSql := g.CompileSelect(&sub)

	return fmt.Sprintf("delete from %s where %s in (%s)", table, g.Wrap("ctid"), selectSql)
}

func (g *PostgresGrammar) compileUpdate(q *query.Query, values map[string]any) string {
	// PG：带 join 或 limit 的 update 走 from/ctid 形式
	if len(q.Joins) > 0 || q.Limit != nil {
		return g.compileUpdateWithJoinsOrLimit(q, values)
	}
	return g.QueryGrammar.baseCompileUpdate(q, values)
}

// CompileUpdateFrom 对应 PostgresGrammar::compileUpdateFrom：
// update table set ... from (join 表列表) where [wheres + join wheres]。
func (g *PostgresGrammar) CompileUpdateFrom(q *query.Query, values map[string]any) string {
	table := g.WrapTable(q.From)
	columns := g.compileUpdateColumns(q, values)

	from := ""
	if len(q.Joins) > 0 {
		froms := make([]string, 0, len(q.Joins))
		for _, join := range q.Joins {
			froms = append(froms, g.WrapTable(join.Table))
		}
		if len(froms) > 0 {
			from = " from " + strings.Join(froms, ", ")
		}
	}

	where := g.compileUpdateWheres(q)

	return strings.TrimSpace("update " + table + " set " + columns + from + " " + where)
}

// compileUpdateWheres 对应 compileUpdateWheres：joins 存在时把 join 的 on
// 条件并入 where（基础 where 为空时去掉首布尔词）。
func (g *PostgresGrammar) compileUpdateWheres(q *query.Query) string {
	baseWheres := g.QueryGrammar.compileWheres(q)

	if len(q.Joins) == 0 {
		return baseWheres
	}

	joinWheres := g.compileUpdateJoinWheres(q)

	if strings.TrimSpace(baseWheres) == "" {
		return "where " + g.removeLeadingBoolean(joinWheres)
	}
	return baseWheres + " " + joinWheres
}

// compileUpdateJoinWheres 对应 compileUpdateJoinWheres。
func (g *PostgresGrammar) compileUpdateJoinWheres(q *query.Query) string {
	sqls := make([]string, 0, len(q.Joins))
	for _, join := range q.Joins {
		for _, w := range join.Wheres {
			sqls = append(sqls, w.Boolean+" "+g.compileWhereByType(q, w))
		}
	}
	return strings.Join(sqls, " ")
}

// PrepareBindingsForUpdateFrom 对应 PostgresGrammar::prepareBindingsForUpdateFrom：
// 数组/JSON 选择器值 json_encode；顺序为 values、where 绑定、其余段。
func (g *PostgresGrammar) PrepareBindingsForUpdateFrom(bindings map[string][]any, values map[string]any) []any {
	vals := make([]any, 0, len(values))
	for _, key := range sortedKeys(values) {
		v := values[key]
		isExpr := g.IsExpression(v)
		if _, ok := v.([]any); ok || (g.isJsonSelector(key) && !isExpr) {
			if data, err := json.Marshal(v); err == nil {
				v = string(data)
			}
		}
		if !isExpr {
			vals = append(vals, flattenBinding(v))
		}
	}

	out := make([]any, 0, len(vals)+8)
	out = append(out, vals...)
	out = append(out, bindings["where"]...)
	for _, segment := range BindingSegments {
		if segment == "select" || segment == "where" {
			continue
		}
		for _, v := range bindings[segment] {
			out = append(out, flattenBinding(v))
		}
	}
	return out
}

// PrepareBindingsForUpdate 对应 PostgresGrammar::prepareBindingsForUpdate：
// 数组/JSON 选择器值 json_encode；顺序为 values、除 select 外全部绑定。
func (g *PostgresGrammar) PrepareBindingsForUpdate(bindings map[string][]any, values map[string]any) []any {
	vals := make([]any, 0, len(values))
	for _, key := range sortedKeys(values) {
		v := values[key]
		isExpr := g.IsExpression(v)
		if _, ok := v.([]any); ok || (g.isJsonSelector(key) && !isExpr) {
			if data, err := json.Marshal(v); err == nil {
				v = string(data)
			}
		}
		if !isExpr {
			vals = append(vals, flattenBinding(v))
		}
	}

	out := make([]any, 0, len(vals)+8)
	out = append(out, vals...)
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

// SubstituteBindingsIntoRawSql 对应 PostgresGrammar::substituteBindingsIntoRawSql：
// 基础替换后把转义的 PG 运算符还原（?? → ?、??| → ?|、??& → ?&）。
func (g *PostgresGrammar) SubstituteBindingsIntoRawSql(sql string, bindings []any) string {
	queryStr := g.QueryGrammar.SubstituteBindingsIntoRawSql(sql, bindings)
	return strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(
		queryStr,
		"??|", "?|"),
		"??&", "?&"),
		"??", "?")
}

func (g *PostgresGrammar) compileUpdateColumns(q *query.Query, values map[string]any) string {
	// PG：set 列不带表限定（取最后一段）
	keys := sortedKeys(values)
	cols := make([]string, 0, len(keys))
	for _, key := range keys {
		column := key
		if i := strings.LastIndex(key, "."); i >= 0 {
			column = key[i+1:]
		}
		if g.isJsonSelector(key) {
			cols = append(cols, g.compileJsonUpdateColumn(key, values[key]))
			continue
		}
		cols = append(cols, g.Wrap(column)+" = "+g.Parameter(values[key]))
	}
	return strings.Join(cols, ", ")
}

func (g *PostgresGrammar) compileUpdateWithJoinsOrLimit(q *query.Query, values map[string]any) string {
	table := g.WrapTable(q.From)
	columns := g.compileUpdateColumns(q, values)

	alias := fmtString(q.From)
	if segs := splitAs(alias); len(segs) == 2 {
		alias = segs[1]
	}
	if i := strings.LastIndex(alias, "."); i >= 0 {
		alias = alias[i+1:]
	}

	// ctid 子查询（对应 Laravel compileUpdateWithJoinsOrLimit:649-660）：
	// 复用原 query 仅替换列为 ctid，orders/limit/offset 保留在子查询内
	// （与同文件 delete ctid 路径语义一致）
	sub := *q
	sub.Aggregate = nil
	sub.Columns = []any{alias + ".ctid"}
	subLock := sub
	subLock.Lock = nil
	selectSql := g.CompileSelect(&subLock)

	return fmt.Sprintf("update %s set %s where %s in (%s)", table, columns, g.Wrap("ctid"), selectSql)
}

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

var intPattern = regexp.MustCompile(`^-?\d+$`)

func isIntString(s string) bool { return intPattern.MatchString(s) }

func atoi(s string) int {
	n := 0
	neg := false
	for i, ch := range s {
		if i == 0 && ch == '-' {
			neg = true
			continue
		}
		n = n*10 + int(ch-'0')
	}
	if neg {
		return -n
	}
	return n
}

func itoa(n int) string { return strconv.Itoa(n) }
