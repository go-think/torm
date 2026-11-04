package query

// Query 是 Laravel Query Builder 状态的 Go 映射。
// 字段语义与 Illuminate\Database\Query\Builder 的属性一一对应；
// nil/零值表示 Laravel 中的 null（未设置）。

type Query struct {
	Aggregate *Aggregate

	// Columns: 元素为 string 或 Expression（含子查询 Expression）
	Columns []any

	// Distinct: bool 或 []any（Postgres distinct on）
	Distinct any

	// From: string 或 Expression
	From any

	IndexHint *IndexHint

	Joins []*Join

	Wheres []*Where

	// Groups: 元素为 string 或 Expression
	Groups []any

	// Havings 与 Where 同构（Laravel havings 即 where 数组）
	Havings []*Where

	Orders []*Order

	// Limit/Offset: nil 表示未设置（对应 Laravel 的 null）
	Limit  *int64
	Offset *int64

	Unions      []*Union
	UnionOrders []*Order
	UnionLimit  *int64
	UnionOffset *int64

	// Lock: nil / bool / string
	Lock any

	// CTE（Laravel 10+ with/withRecursive）
	Withes        []*With
	WithRecursive bool

	// GroupLimit：Eloquent 预加载分组 limit（compileGroupLimit）
	GroupLimit *GroupLimit

	// 标记当前 Query 是 join 子句（编译 on 而非 where）
	JoinClause bool
}

type Aggregate struct {
	Function string
	Columns  []any
}

// IndexHint 对应 Laravel 的 Query\IndexHint：type ∈ hint/force/ignore
type IndexHint struct {
	Type  string
	Index string
}

type Join struct {
	// Type: inner / left / right / cross / straight_join
	Type string
	// Table: string 或 Expression（joinSub 场景）
	Table any
	// 嵌套 join（Laravel JoinClause->joins）
	Joins  []*Join
	Wheres []*Where
}

type Where struct {
	Type     string
	Column   any
	Operator string
	Value    any
	Values   []any
	// whereColumn 的两列比较 / whereRowValues 等
	First  any
	Second any
	// 括号分组字段（Laravel whereBetweenColumns / RowValues 的多列）
	Columns []any
	// Raw 的 SQL 片段（string 或 Expression）
	Sql any
	// 嵌套/子查询
	Query *Query
	// In/Null/Between/Nested 的 not 标记
	Not bool
	// whereLike 大小写敏感
	CaseSensitive bool
	// whereFullText 的选项（mode/expanded）
	Options map[string]any

	Boolean string
}

type Order struct {
	Column    any
	Direction string
	// orderByRaw：Expression 或 string
	Sql any
}

type Union struct {
	All   bool
	Query *Query
}

// GroupLimit 对应 Laravel 的 groupLimit（预加载分组限额）。
type GroupLimit struct {
	Column string
	Value  int64
}

// With 对应 CTE：with "name" (cols) as (sub)
type With struct {
	Name    string
	Columns []string
	Query   *Query
}
