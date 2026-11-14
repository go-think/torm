package query

// Query 是查询构建器的状态结构。
// 字段语义与常见查询构建器（query builder）的属性一一对应；
// nil/零值表示未设置（null）。

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

	// Havings 与 Where 同构（havings 即 where 数组）
	Havings []*Where

	Orders []*Order

	// Limit/Offset: nil 表示未设置（null）
	Limit  *int64
	Offset *int64

	Unions      []*Union
	UnionOrders []*Order
	UnionLimit  *int64
	UnionOffset *int64

	// Lock: nil / bool / string
	Lock any

	// CTE（with/withRecursive）
	Withes        []*With
	WithRecursive bool

	// GroupLimit：预加载分组 limit（compileGroupLimit）
	GroupLimit *GroupLimit

	// 标记当前 Query 是 join 子句（编译 on 而非 where）
	JoinClause bool

	// Timeout 查询执行超时（秒），MySQL 编译为 MAX_EXECUTION_TIME 提示。
	Timeout *int64
}

type Aggregate struct {
	Function string
	Columns  []any
}

// IndexHint 索引提示：type ∈ hint/force/ignore
type IndexHint struct {
	Type  string
	Index string
}

type Join struct {
	// Type: inner / left / right / cross / straight_join
	Type string
	// Table: string 或 Expression（joinSub / joinLateral 场景）
	Table any
	// Lateral 标记 lateral join（joinLateral 子句）
	Lateral bool
	// 嵌套 join（JoinClause->joins）
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
	// 括号分组字段（whereBetweenColumns / RowValues 的多列）
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
	// Type：InOrderOf（inOrderOf 顺序排序）；空 = 普通列排序
	Type string
	// InOrderOf 的取值序列（Type == "InOrderOf" 时使用）
	Values []any
}

type Union struct {
	All   bool
	Query *Query
}

// GroupLimit 表示 groupLimit（预加载分组限额）。
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
