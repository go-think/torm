package grammar

// MariaDbGrammar 为 MariaDB 方言查询编译器。
// 与 MySQL 的差异点：JSON 选择器走 json_value、JSON 值 cast 走 json_query、
// lateral join 不支持；其余（straight join、timeout 提示等）继承 MySQL。
type MariaDbGrammar struct {
	MySqlGrammar
}

func NewMariaDbGrammar() *MariaDbGrammar {
	g := &MariaDbGrammar{}
	inner := NewMySqlGrammar()
	g.MySqlGrammar = *inner

	// MariaDB 覆盖项
	g.BaseGrammar.wrapJsonSelectorFn = g.wrapJsonSelector
	g.compileJsonValueCastFn = g.compileJsonValueCast
	// lateral join 不支持（对应 MariaDbGrammar::compileJoinLateral 抛错）
	g.compileJoinLateralFn = nil

	return g
}

// wrapJsonSelector 对应 MariaDbGrammar::wrapJsonSelector：json_value。
func (g *MariaDbGrammar) wrapJsonSelector(value string) string {
	field, path := g.wrapJsonFieldAndPath(value)
	return "json_value(" + field + path + ")"
}

// compileJsonValueCast 对应 MariaDbGrammar::compileJsonValueCast：json_query。
func (g *MariaDbGrammar) compileJsonValueCast(value string) string {
	return "json_query(" + value + ", '$')"
}
