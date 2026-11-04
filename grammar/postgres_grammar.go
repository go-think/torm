package grammar

import (
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

	g.compileInsertOrIgnoreFn = g.compileInsertOrIgnore
	g.compileInsertOrIgnoreUsingFn = g.compileInsertOrIgnoreUsing
	g.compileInsertGetIdFn = g.compileInsertGetId
	g.compileUpsertFn = g.compileUpsert
	g.compileUpdateFn = g.compileUpdate
	g.compileUpdateColumnsFn = g.compileUpdateColumns
	g.compileTruncateFn = g.compileTruncate

	return g
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

	wrappedPath := make([]string, 0, len(path))
	for _, p := range path {
		wrappedPath = append(wrappedPath, g.parseJsonPathArrayKeys(p)...)
	}

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

// parseJsonPathArrayKeys 把 "attr[0][1]" 拆为 ["attr", "[0]", "[1]"]；
// 字符串属性加单引号，数组下标保持裸数字（对齐 Laravel wrapJsonPathAttributes）。
func (g *PostgresGrammar) parseJsonPathArrayKeys(segment string) []string {
	parts := jsonTrailingIndex.FindAllString(segment, -1)
	if parts == nil {
		return []string{pgQuotePath(segment)}
	}
	key := strings.TrimSuffix(segment, parts[len(parts)-1])
	if key == "" {
		return parts
	}
	return append([]string{pgQuotePath(key)}, parts...)
}

func pgQuotePath(attr string) string {
	if isIntString(attr) {
		return attr
	}
	return "'" + strings.ReplaceAll(attr, "'", "''") + "'"
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

	attrs := make([]string, 0, len(segments))
	for _, s := range segments {
		attrs = append(attrs, g.parseJsonPathArrayKeys(s)...)
	}
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
	return []TruncateStatement{{Sql: "truncate " + g.WrapTable(q.From) + " restart identity"}}
}

// ---- 写入族 ----

func (g *PostgresGrammar) compileInsertOrIgnore(q *query.Query, rows []query.Row) (string, []any) {
	sql, bindings := g.QueryGrammar.baseCompileInsert(q, rows)
	return sql + " on conflict do nothing", bindings
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

func (g *PostgresGrammar) compileUpdate(q *query.Query, values map[string]any) string {
	// PG：带 join 或 limit 的 update 走 from/ctid 形式
	if len(q.Joins) > 0 || q.Limit != nil {
		return g.compileUpdateWithJoinsOrLimit(q, values)
	}
	return g.QueryGrammar.CompileUpdate(q, values)
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

	// ctid 子查询（对应 Laravel compileUpdateWithJoinsOrLimit）
	sub := *q
	sub.Aggregate = nil
	sub.Columns = []any{alias + ".ctid"}
	sub.Orders = nil
	sub.Limit = nil
	sub.Offset = nil
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
