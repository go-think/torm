package query

// Expression 表示原生 SQL 表达式片段（等价于 raw 表达式，值直接嵌入语句）。
type Expression interface {
	GetValue() interface{}
}

// RawExpression 是 Expression 的默认实现。
type RawExpression struct {
	value interface{}
}

func NewExpression(value interface{}) *RawExpression {
	return &RawExpression{value: value}
}

func (e *RawExpression) GetValue() interface{} {
	return e.value
}
