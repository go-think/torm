package grammar

import "github.com/go-think/torm/query"

// Expression 与 NewExpression 为兼容别名：Expression 的实现位于 query 包，
// 此处保留原引用路径。
type Expression = query.Expression

var NewExpression = query.NewExpression
