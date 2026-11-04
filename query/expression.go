package query

// Expression 对应 Laravel 的 Query\Expression（DB::raw）。
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
