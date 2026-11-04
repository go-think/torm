package grammar

import (
	"fmt"
	"strings"
)

// BaseGrammar 对应 Illuminate\Database\Grammar：标识符 wrap、参数占位、表达式求值。
// 默认双引号（SQL 标准方言）；方言构造器通过 wrapValueFn / wrapJsonSelectorFn 覆盖。
type BaseGrammar struct {
	tablePrefix string

	// 方言钩子（nil = 基础实现）
	wrapValueFn         func(value string) string
	wrapJsonSelectorFn  func(value string) string
	wrapJsonBoolSelFn   func(value string) string
	wrapJsonBoolValueFn func(value string) string
	wrapJsonPathFn      func(value, delimiter string) string
	wrapJsonPathSegFn   func(segment string) string
	jsonFieldAndPathFn  func(column string) (string, string)
}

func (g *BaseGrammar) SetTablePrefix(prefix string) { g.tablePrefix = prefix }

func (g *BaseGrammar) GetTablePrefix() string { return g.tablePrefix }

func (g *BaseGrammar) wrapValue(value string) string {
	if g.wrapValueFn != nil {
		return g.wrapValueFn(value)
	}
	if value != "*" {
		return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
	}
	return value
}

// WrapTable wrap 一张表名；支持 Expression 与 "table as alias"。
func (g *BaseGrammar) WrapTable(value any) string {
	if g.IsExpression(value) {
		return fmt.Sprint(g.GetValue(value))
	}
	prefix := g.tablePrefix

	table, ok := value.(string)
	if !ok {
		return fmt.Sprint(value)
	}

	if strings.Contains(strings.ToLower(table), " as ") {
		return g.wrapAliasedTable(table, prefix)
	}

	if strings.Contains(table, ".") {
		// 自定义 schema：前缀只作用于最后一段
		idx := strings.LastIndex(table, ".")
		table = table[:idx] + "." + prefix + table[idx+1:]
		segments := strings.Split(table, ".")
		for i, seg := range segments {
			segments[i] = g.wrapValue(seg)
		}
		return strings.Join(segments, ".")
	}

	return g.wrapValue(prefix + table)
}

// Wrap wrap 一个标识符（列名/表达式）；支持 Expression、alias、JSON 选择器。
func (g *BaseGrammar) Wrap(value any) string {
	if g.IsExpression(value) {
		return fmt.Sprint(g.GetValue(value))
	}

	v, ok := value.(string)
	if !ok {
		return fmt.Sprint(value)
	}

	if strings.Contains(strings.ToLower(v), " as ") {
		return g.wrapAliasedValue(v)
	}

	if g.isJsonSelector(v) {
		return g.wrapJsonSelector(v)
	}

	return g.wrapSegments(strings.Split(v, "."))
}

func (g *BaseGrammar) wrapAliasedValue(value string) string {
	segments := splitAs(value)
	return g.Wrap(segments[0]) + " as " + g.wrapValue(segments[1])
}

func (g *BaseGrammar) wrapAliasedTable(value string, prefix string) string {
	segments := splitAs(value)
	return g.WrapTable(segments[0]) + " as " + g.wrapValue(prefix+segments[1])
}

func (g *BaseGrammar) wrapSegments(segments []string) string {
	parts := make([]string, len(segments))
	for i, segment := range segments {
		if i == 0 && len(segments) > 1 {
			parts[i] = g.WrapTable(segment)
		} else {
			parts[i] = g.wrapValue(segment)
		}
	}
	return strings.Join(parts, ".")
}

func (g *BaseGrammar) isJsonSelector(value string) bool {
	return strings.Contains(value, "->")
}

func (g *BaseGrammar) wrapJsonSelector(value string) string {
	if g.wrapJsonSelectorFn != nil {
		return g.wrapJsonSelectorFn(value)
	}
	panic(ErrUnsupported{"This database engine does not support JSON operations."})
}

func (g *BaseGrammar) wrapJsonBooleanSelector(value string) string {
	if g.wrapJsonBoolSelFn != nil {
		return g.wrapJsonBoolSelFn(value)
	}
	return g.wrapJsonSelector(value)
}

func (g *BaseGrammar) wrapJsonBooleanValue(value string) string {
	if g.wrapJsonBoolValueFn != nil {
		return g.wrapJsonBoolValueFn(value)
	}
	return value
}

// wrapJsonFieldAndPath 对应 CompilesJsonPaths trait 的 wrapJsonFieldAndPath。
func (g *BaseGrammar) wrapJsonFieldAndPath(column string) (string, string) {
	if g.jsonFieldAndPathFn != nil {
		return g.jsonFieldAndPathFn(column)
	}
	parts := strings.SplitN(column, "->", 2)
	field := g.Wrap(parts[0])
	path := ""
	if len(parts) > 1 {
		path = ", " + g.wrapJsonPath(parts[1], "->")
	}
	return field, path
}

// wrapJsonPath 对应 CompilesJsonPaths trait 的 wrapJsonPath（方言共享，默认实现）。
func (g *BaseGrammar) wrapJsonPath(value, delimiter string) string {
	if g.wrapJsonPathFn != nil {
		return g.wrapJsonPathFn(value, delimiter)
	}
	return defaultWrapJsonPath(value, delimiter, g.wrapJsonPathSegment)
}

func (g *BaseGrammar) wrapJsonPathSegment(segment string) string {
	if g.wrapJsonPathSegFn != nil {
		return g.wrapJsonPathSegFn(segment)
	}
	return defaultWrapJsonPathSegment(segment)
}

// Columnize 把列名数组转为逗号分隔的 wrapped 列表。
func (g *BaseGrammar) Columnize(columns []any) string {
	parts := make([]string, len(columns))
	for i, c := range columns {
		parts[i] = g.Wrap(c)
	}
	return strings.Join(parts, ", ")
}

// Parameterize 把一组绑定值转为占位符列表。
func (g *BaseGrammar) Parameterize(values []any) string {
	parts := make([]string, len(values))
	for i := range values {
		parts[i] = g.Parameter(values[i])
	}
	return strings.Join(parts, ", ")
}

// Parameter 求出单个值的占位符：Expression 原样展开，否则 "?"。
func (g *BaseGrammar) Parameter(value any) string {
	if g.IsExpression(value) {
		return fmt.Sprint(g.GetValue(value))
	}
	return "?"
}

// QuoteString 引用一个字符串字面量。
func (g *BaseGrammar) QuoteString(value any) string {
	switch v := value.(type) {
	case []string:
		parts := make([]string, len(v))
		for i, s := range v {
			parts[i] = g.QuoteString(s)
		}
		return strings.Join(parts, ", ")
	case []any:
		parts := make([]string, len(v))
		for i, s := range v {
			parts[i] = g.QuoteString(s)
		}
		return strings.Join(parts, ", ")
	default:
		return "'" + fmt.Sprint(v) + "'"
	}
}

func (g *BaseGrammar) IsExpression(value any) bool {
	_, ok := value.(Expression)
	return ok
}

// GetValue 递归解出 Expression 的标量值。
func (g *BaseGrammar) GetValue(expression any) any {
	if g.IsExpression(expression) {
		return g.GetValue(expression.(Expression).GetValue())
	}
	return expression
}

// GetDateFormat 数据库存日期格式。
func (g *BaseGrammar) GetDateFormat() string {
	return "Y-m-d H:i:s"
}

// CompileSavepoint 定义保存点。
func (g *BaseGrammar) CompileSavepoint(name string) string {
	return "SAVEPOINT " + name
}

// CompileSavepointRollBack 回滚到保存点。
func (g *BaseGrammar) CompileSavepointRollBack(name string) string {
	return "ROLLBACK TO SAVEPOINT " + name
}

// ErrUnsupported 对应 Laravel 各 grammar 方法的 RuntimeException。
type ErrUnsupported struct{ Msg string }

func (e ErrUnsupported) Error() string { return e.Msg }

// splitAs 按 " as "（任意大小写、任意空白）切分。
func splitAs(value string) []string {
	lower := strings.ToLower(value)
	idx := strings.Index(lower, " as ")
	if idx < 0 {
		return []string{value}
	}
	left := strings.TrimRight(value[:idx], " \t")
	right := strings.TrimLeft(value[idx+4:], " \t")
	return []string{left, right}
}
