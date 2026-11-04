package grammar

import "github.com/thinkoner/torm/query"

// Expression 与 NewExpression 为兼容别名：Laravel 中 Expression 位于 Query 命名空间，
// Go 侧实现移至 query 包，此处保留原引用路径。
type Expression = query.Expression

var NewExpression = query.NewExpression
