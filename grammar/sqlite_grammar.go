package grammar

import (
	"encoding/json"
	"strings"

	"github.com/thinkoner/torm/query"
)

// SQLiteGrammar 对应 Illuminate\Database\Query\Grammars\SQLiteGrammar。
// 标识符为双引号（继承 BaseGrammar 默认实现）；JSON 走 json_extract/json_each。
// 必须使用 NewSqliteQueryGrammar 构造。
type SQLiteGrammar struct {
	QueryGrammar

	// useLegacyGroupLimit：SQLite < 3.25（无窗口函数）时置 true，
	// groupLimit 降级为普通 select（对应 Laravel 的版本分支）。
	useLegacyGroupLimit bool
}

// NewSqliteQueryGrammar 构造 SQLite 方言 grammar。
// 连接侧接线：connection.GetQueryGrammar 按 driver=="sqlite" 返回本实例
// （connection.go 在禁改清单中，故此处仅提供工厂函数）。
func NewSqliteQueryGrammar() *SQLiteGrammar {
	g := &SQLiteGrammar{}

	g.BaseGrammar.wrapJsonSelectorFn = g.wrapJsonSelector
	g.BaseGrammar.jsonFieldAndPathFn = g.wrapJsonFieldAndPath

	g.compileIndexHintFn = g.compileIndexHint
	g.compileLockFn = g.compileLock
	g.wrapUnionFn = g.wrapUnion
	g.whereLikeFn = g.whereLike
	g.whereNullSafeEqualsFn = g.whereNullSafeEquals
	g.dateBasedWhereFn = g.dateBasedWhere

	g.compileJsonContainsFn = g.compileJsonContains
	g.compileJsonContainsKeyFn = g.compileJsonContainsKey
	g.compileJsonLengthFn = g.compileJsonLength

	g.compileInsertOrIgnoreFn = g.compileInsertOrIgnore
	g.compileInsertOrIgnoreUsingFn = g.compileInsertOrIgnoreUsing
	g.compileUpsertFn = g.compileUpsert
	g.compileUpdateFn = g.compileUpdate
	g.compileUpdateColumnsFn = g.compileUpdateColumns
	g.compileTruncateFn = g.compileTruncate

	return g
}

// SetUseLegacyGroupLimit 切换 groupLimit 的 legacy 分支（SQLite < 3.25）。
func (g *SQLiteGrammar) SetUseLegacyGroupLimit(v bool) {
	g.useLegacyGroupLimit = v
}

// CompileSelect 对应 SQLiteGrammar::compileGroupLimit 的版本分支：
// legacy（< 3.25）时丢弃 groupLimit 直接按普通 select 编译。
func (g *SQLiteGrammar) CompileSelect(q *query.Query) string {
	if q.GroupLimit != nil && g.useLegacyGroupLimit {
		q.GroupLimit = nil
	}
	return g.QueryGrammar.CompileSelect(q)
}

// ---- lock / union ----

func (g *SQLiteGrammar) compileLock(q *query.Query, value any) string {
	return ""
}

func (g *SQLiteGrammar) wrapUnion(sql string) string {
	return "select * from (" + sql + ")"
}

// ---- where 覆盖 ----

func (g *SQLiteGrammar) whereLike(q *query.Query, w *query.Where) string {
	if !w.CaseSensitive {
		return g.QueryGrammar.baseWhereLike(q, w)
	}
	operator := "glob"
	if w.Not {
		operator = "not glob"
	}
	return g.whereBasic(q, &query.Where{Column: w.Column, Operator: operator, Value: w.Value})
}

// PrepareWhereLikeBinding 对应 SQLiteGrammar::prepareWhereLikeBinding：
// 大小写敏感时把 like 模式改写为 glob 模式（* → [*]、? → [?]、% → *、_ → ?）。
func (g *SQLiteGrammar) PrepareWhereLikeBinding(value any, caseSensitive bool) any {
	if !caseSensitive {
		return value
	}
	s, ok := value.(string)
	if !ok {
		return value
	}
	s = strings.ReplaceAll(s, "*", "[*]")
	s = strings.ReplaceAll(s, "?", "[?]")
	s = strings.ReplaceAll(s, "%", "*")
	s = strings.ReplaceAll(s, "_", "?")
	return s
}

func (g *SQLiteGrammar) whereNullSafeEquals(q *query.Query, w *query.Where) string {
	return g.Wrap(w.Column) + " is " + g.Parameter(w.Value)
}

// dateBasedWhere 对应 SQLite 的 strftime 日期族：
// strftime('%Y-%m-%d', col) op cast(? as text)。
func (g *SQLiteGrammar) dateBasedWhere(kind string, q *query.Query, w *query.Where) string {
	var format string
	switch kind {
	case "date":
		format = "%Y-%m-%d"
	case "time":
		format = "%H:%M:%S"
	case "day":
		format = "%d"
	case "month":
		format = "%m"
	case "year":
		format = "%Y"
	default:
		format = "%Y-%m-%d"
	}
	value := g.Parameter(w.Value)
	return "strftime('" + format + "', " + g.Wrap(w.Column) + ") " + w.Operator + " cast(" + value + " as text)"
}

func (g *SQLiteGrammar) compileIndexHint(q *query.Query, hint *query.IndexHint) string {
	if hint.Type != "force" {
		return ""
	}
	index := hint.Index
	if !indexNamePattern.MatchString(index) {
		panic(ErrUnsupported{"Index name contains invalid characters."})
	}
	return "indexed by " + index
}

// ---- JSON ----

func (g *SQLiteGrammar) wrapJsonSelector(value string) string {
	field, path := g.wrapJsonFieldAndPath(value)
	return "json_extract(" + field + path + ")"
}

func (g *SQLiteGrammar) wrapJsonFieldAndPath(column string) (string, string) {
	parts := strings.SplitN(column, "->", 2)
	field := g.Wrap(parts[0])
	path := ""
	if len(parts) > 1 {
		path = ", " + g.wrapJsonPath(parts[1], "->")
	}
	return field, path
}

func (g *SQLiteGrammar) compileJsonContains(column, value string) string {
	field, path := g.wrapJsonFieldAndPath(column)
	return "exists (select 1 from json_each(" + field + path + ") where " + g.Wrap("json_each.value") + " is " + value + ")"
}

func (g *SQLiteGrammar) compileJsonContainsKey(column string) string {
	field, path := g.wrapJsonFieldAndPath(column)
	return "json_type(" + field + path + ") is not null"
}

func (g *SQLiteGrammar) compileJsonLength(column, operator, value string) string {
	field, path := g.wrapJsonFieldAndPath(column)
	return "json_array_length(" + field + path + ") " + operator + " " + value
}

// PrepareBindingForJsonContains 对应 SQLiteGrammar::prepareBindingForJsonContains：
// 原样返回（不 json_encode 数组）。
func (g *SQLiteGrammar) PrepareBindingForJsonContains(binding any) any {
	return binding
}

// ---- 写入族 ----

func (g *SQLiteGrammar) compileInsertOrIgnore(q *query.Query, rows []query.Row) (string, []any) {
	sql, bindings := g.QueryGrammar.baseCompileInsert(q, rows)
	return replaceFirst("insert", "insert or ignore", sql), bindings
}
// CompileInsertOrIgnoreReturning 对应 SQLiteGrammar.php:300-309：
// insert or ignore + returning（SQLite 3.35+）。
func (g *SQLiteGrammar) CompileInsertOrIgnoreReturning(q *query.Query, rows []query.Row, returning, uniqueBy []string) (string, []any) {
	sql, bindings := g.compileInsertOrIgnore(q, rows)
	returns := make([]string, 0, len(returning))
	for _, c := range returning {
		returns = append(returns, g.Wrap(c))
	}
	sql += " returning " + strings.Join(returns, ", ")
	return sql, bindings
}


func (g *SQLiteGrammar) compileInsertOrIgnoreUsing(q *query.Query, columns []any, sql string) string {
	return replaceFirst("insert", "insert or ignore", g.QueryGrammar.CompileInsertUsing(q, columns, sql))
}

func (g *SQLiteGrammar) compileUpsert(q *query.Query, rows []query.Row, uniqueBy, update []string) (string, []any) {
	sql, bindings := g.QueryGrammar.baseCompileInsert(q, rows)

	sql += " on conflict (" + g.Columnize(toAnySlice(uniqueBy)) + ") do update set "

	cols := make([]string, 0, len(update))
	for _, column := range update {
		cols = append(cols, g.Wrap(column)+" = "+g.wrapValue("excluded")+"."+g.Wrap(column))
	}

	return sql + strings.Join(cols, ", "), bindings
}

// compileUpdate：带 joins/limit 时走 rowid 重写。
func (g *SQLiteGrammar) compileUpdate(q *query.Query, values map[string]any) string {
	if len(q.Joins) > 0 || q.Limit != nil {
		return g.compileUpdateWithJoinsOrLimit(q, values)
	}
	return g.QueryGrammar.baseCompileUpdate(q, values)
}

// compileUpdateColumns 对应 SQLiteGrammar::compileUpdateColumns：
// JSON 选择器按首段列分组，编译为 json_patch。
func (g *SQLiteGrammar) compileUpdateColumns(q *query.Query, values map[string]any) string {
	groups := groupJsonColumnsForUpdate(values)

	cols := make([]string, 0, len(values))
	for _, key := range sortedKeys(values) {
		if g.isJsonSelector(key) {
			continue
		}
		cols = append(cols, g.wrapLastSegment(key)+" = "+g.Parameter(values[key]))
	}
	for _, key := range sortedKeys(groups) {
		cols = append(cols, g.wrapLastSegment(key)+" = "+g.compileJsonPatch(key, groups[key]))
	}
	return strings.Join(cols, ", ")
}

// groupJsonColumnsForUpdate 对应 SQLiteGrammar::groupJsonColumnsForUpdate：
// 把 "col->path" 的值按首段列聚合为嵌套 map（col -> path 段 -> value）。
// 对应 Arr::set($groups, str_replace('->', '.', Str::after($key, '.')), $value)。
func groupJsonColumnsForUpdate(values map[string]any) map[string]any {
	groups := map[string]any{}
	for _, key := range sortedKeys(values) {
		if !strings.Contains(key, "->") {
			continue
		}
		after := key
		if i := strings.Index(key, "."); i >= 0 {
			after = key[i+1:]
		}
		segments := strings.Split(strings.ReplaceAll(after, "->", "."), ".")
		node := groups
		for i, seg := range segments {
			if i == len(segments)-1 {
				node[seg] = values[key]
				break
			}
			if _, ok := node[seg].(map[string]any); !ok {
				node[seg] = map[string]any{}
			}
			node = node[seg].(map[string]any)
		}
	}
	return groups
}

// compileJsonPatch 对应 SQLiteGrammar::compileJsonPatch。
func (g *SQLiteGrammar) compileJsonPatch(column string, value any) string {
	return "json_patch(ifnull(" + g.Wrap(column) + ", json('{}')), json(" + g.Parameter(value) + "))"
}

func (g *SQLiteGrammar) compileUpdateWithJoinsOrLimit(q *query.Query, values map[string]any) string {
	table := g.WrapTable(q.From)
	columns := g.compileUpdateColumns(q, values)

	alias := lastAsSegment(fmtString(q.From))

	sub := *q
	sub.Columns = []any{alias + ".rowid"}
	selectSql := g.CompileSelect(&sub)

	return "update " + table + " set " + columns + " where " + g.Wrap("rowid") + " in (" + selectSql + ")"
}

// PrepareBindingsForUpdate 对应 SQLiteGrammar::prepareBindingsForUpdate：
// JSON 选择器剔除，分组值 json_encode 后置于最前，随后拼接除 select 外全部绑定。
func (g *SQLiteGrammar) PrepareBindingsForUpdate(bindings map[string][]any, values map[string]any) []any {
	groups := groupJsonColumnsForUpdate(values)

	vals := make([]any, 0, len(values))
	for _, key := range sortedKeys(values) {
		if g.isJsonSelector(key) {
			continue
		}
		vals = append(vals, jsonEncodeValue(values[key]))
	}
	for _, key := range sortedKeys(groups) {
		vals = append(vals, jsonEncodeValue(groups[key]))
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

// jsonEncodeValue 对应 prepareBindingsForUpdate 的 is_array → json_encode。
func jsonEncodeValue(v any) any {
	switch v.(type) {
	case []any, map[string]any:
		data, err := json.Marshal(v)
		if err != nil {
			return v
		}
		return string(data)
	default:
		return v
	}
}

// CompileDelete：带 joins/limit 时走 rowid 重写。
func (g *SQLiteGrammar) CompileDelete(q *query.Query) string {
	if len(q.Joins) > 0 || q.Limit != nil {
		return g.compileDeleteWithJoinsOrLimit(q)
	}
	return g.QueryGrammar.CompileDelete(q)
}

func (g *SQLiteGrammar) compileDeleteWithJoinsOrLimit(q *query.Query) string {
	table := g.WrapTable(q.From)
	alias := lastAsSegment(fmtString(q.From))

	sub := *q
	sub.Columns = []any{alias + ".rowid"}
	selectSql := g.CompileSelect(&sub)

	return "delete from " + table + " where " + g.Wrap("rowid") + " in (" + selectSql + ")"
}

// compileTruncate 对应 SQLiteGrammar::compileTruncate：
// 先清 sqlite_sequence（绑定表名），再 delete from 表。
func (g *SQLiteGrammar) compileTruncate(q *query.Query) []TruncateStatement {
	table := fmtString(q.From)
	// Laravel 先 delete from 表，再清 sqlite_sequence（SQLiteGrammar.php:487-497）
	return []TruncateStatement{
		{Sql: "delete from " + g.WrapTable(q.From)},
		{
			Sql:      "delete from " + g.wrapValue("sqlite_sequence") + " where name = ?",
			Bindings: []any{g.GetTablePrefix() + table},
		},
	}
}

// lastAsSegment 对应 last(preg_split('/\s+as\s+/i', $from))。
func lastAsSegment(from string) string {
	if segs := splitAs(from); len(segs) > 1 {
		return segs[len(segs)-1]
	}
	if i := strings.LastIndex(from, "."); i >= 0 {
		return from[i+1:]
	}
	return from
}

// wrapLastSegment 对应 compileUpdateColumns 的 last(explode('.', $key)) 后 wrap。
func (g *SQLiteGrammar) wrapLastSegment(key string) string {
	column := key
	if i := strings.LastIndex(key, "."); i >= 0 {
		column = key[i+1:]
	}
	return g.Wrap(column)
}
