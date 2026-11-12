package torm

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/thinkoner/torm/grammar"
	"github.com/thinkoner/torm/query"
	"github.com/thinkoner/torm/utils"
)

// Relation 关系定义（对应 Laravel 关系的键约定）。
type Relation interface {
	kind() string
	targetModel() any
	// GetResults 单父查询（对应 Laravel Relation::getResults）：
	// 返回关联模型指针（*T）切片；无关联时返回空切片。
	GetResults(parent any, c *Connection, constraints ...func(*Builder)) ([]any, error)
}

// BelongsTo 反向一对一：本表持有外键。
type BelongsTo struct {
	ForeignKey string // 本表列（默认 *_id）
	OwnerKey   string // 目标表键（默认 id）
	Model      any    // 目标模型零值
	// withDefault（对应 SupportsDefaultModels）：关系未命中时返回空实例而非 nil；
	// DefaultFn 非 nil 时以新实例调用，返回值非 nil 则使用之
	HasDefault bool
	DefaultFn  func(inst any) any
}

// WithDefault 启用默认模型（对应 SupportsDefaultModels::withDefault）：
// 关系未命中（外键为空或 owner 查询无结果）时返回目标模型新实例；
// fn 可选，非 nil 时以新实例调用，返回值非 nil 则使用返回值。
func (r BelongsTo) WithDefault(fn ...func(inst any) any) BelongsTo {
	r.HasDefault = true
	if len(fn) > 0 {
		r.DefaultFn = fn[0]
	}
	return r
}

// defaultResults withDefault 的默认结果（对应 getDefaultFor）。
func (r BelongsTo) defaultResults() []any {
	return relationDefaultResults(r, r.DefaultFn)
}

func (r BelongsTo) kind() string     { return "belongsTo" }
func (r BelongsTo) targetModel() any { return r.Model }

// relationDefaultResults SupportsDefaultModels::getDefaultFor 的共享实现：
// 以目标模型零值实例调用 DefaultFn（非 nil 返回值优先），否则返回新实例。
func relationDefaultResults(rel Relation, fn func(inst any) any) []any {
	inst, err := newRelatedInstance(rel)
	if err != nil {
		return nil
	}
	if fn != nil {
		if out := fn(inst); out != nil {
			return []any{out}
		}
	}
	return []any{inst}
}

// GetResults 单父查询：按 parent 的外键取 owner（对应 BelongsTo::getResults，
// 未命中时 withDefault 生效返回默认实例）。
func (r BelongsTo) GetResults(parent any, c *Connection, constraints ...func(*Builder)) ([]any, error) {
	targetTable := relationTargetTable(r)
	fk := r.ForeignKey
	if fk == "" {
		fk = stripPlural(targetTable) + "_id"
	}
	ownerKey := r.OwnerKey
	if ownerKey == "" {
		ownerKey = "id"
	}
	v := keyValue(reflect.ValueOf(parent), fk)
	if v == nil || utilsIsBlankValue(v) {
		if r.HasDefault {
			return r.defaultResults(), nil
		}
		return nil, nil
	}
	nb := NewBuilder(c, c.GetQueryGrammar()).From(targetTable).Where(ownerKey, "=", v)
	for _, cons := range constraints {
		cons(nb)
	}
	results, err := queryTargetModels(nb, r.Model)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 && r.HasDefault {
		return r.defaultResults(), nil
	}
	return results, nil
}

// HasOne 正向一对一：目标表持有外键。
type HasOne struct {
	ForeignKey string // 目标表列
	LocalKey   string // 本表键（默认 id）
	Model      any
	// withDefault（对应 SupportsDefaultModels，HasOne.php 亦实现）：
	// 关系未命中时返回目标模型新实例而非空；
	// DefaultFn 非 nil 时以新实例调用，返回值非 nil 则使用之
	HasDefault bool
	DefaultFn  func(inst any) any
	// one-of-many 变体（对应 CanBeOneOfMany）：OfManyColumn/OfManyAggregate
	// 非空时 GetResults/预加载按该列取极值一行（latestOfMany → max(created_at)、
	// oldestOfMany → min(created_at)）。
	OfManyColumn    string
	OfManyAggregate string // "max" | "min"；空串 = 关闭
	// Partial one() 显式标记：GetResults/预加载只取单行（对应
	// SupportsPartialRelations::one()；HasOne 本就单行，此标记使查询显式 limit 1）
	Partial bool
}

func (r HasOne) kind() string     { return "hasOne" }
func (r HasOne) targetModel() any { return r.Model }

// LatestOfMany 取该列最新一行（对应 latestOfMany，缺省列 created_at）。
func (r HasOne) LatestOfMany(column ...string) HasOne {
	r.OfManyColumn, r.OfManyAggregate = ofManyColumnArg(column, "created_at"), "max"
	return r
}

// OldestOfMany 取该列最早一行（对应 oldestOfMany，缺省列 created_at）。
func (r HasOne) OldestOfMany(column ...string) HasOne {
	r.OfManyColumn, r.OfManyAggregate = ofManyColumnArg(column, "created_at"), "min"
	return r
}

// OfMany 自定义极值列与聚合（对应 ofMany(column, aggregate)）。
func (r HasOne) OfMany(column, aggregate string) HasOne {
	r.OfManyColumn, r.OfManyAggregate = column, aggregate
	return r
}

// One 标记关系为单行形态（对应 SupportsPartialRelations::one()）。
func (r HasOne) One() HasOne { r.Partial = true; return r }

// WithDefault 启用默认模型（对应 SupportsDefaultModels::withDefault，HasOne.php
// 亦实现）：关系未命中（父键为空或查询无结果）时返回目标模型新实例；
// fn 可选，非 nil 时以新实例调用，返回值非 nil 则使用返回值。
func (r HasOne) WithDefault(fn ...func(inst any) any) HasOne {
	r.HasDefault = true
	if len(fn) > 0 {
		r.DefaultFn = fn[0]
	}
	return r
}

// defaultResults withDefault 的默认结果（对应 getDefaultFor）。
func (r HasOne) defaultResults() []any {
	return relationDefaultResults(r, r.DefaultFn)
}

// ofManyColumnArg 可变列参归一。
func ofManyColumnArg(column []string, def string) string {
	if len(column) > 0 && column[0] != "" {
		return column[0]
	}
	return def
}

// oneOfManyRel one-of-many 关系的排序信息（预加载 GroupLimit 用）：
// max 聚合按列降序、min 聚合按列升序。
type oneOfManyRel interface {
	Relation
	ofManyOrder() (column, direction string, ok bool)
}

func (r HasOne) ofManyOrder() (string, string, bool) {
	if r.OfManyColumn == "" || r.OfManyAggregate == "" {
		return "", "", false
	}
	dir := "desc"
	if r.OfManyAggregate == "min" {
		dir = "asc"
	}
	return r.OfManyColumn, dir, true
}

func (r HasOne) isPartial() bool { return r.Partial }

// GetResults 单父查询：按父键取关联（对应 HasOne::getResults）。
// one-of-many 时按极值列 + 主键 tie-break 排序取一行（对应 CanBeOneOfMany
// 的 row_number 子查询 join 语义）；withDefault 生效时未命中返回默认实例。
func (r HasOne) GetResults(parent any, c *Connection, constraints ...func(*Builder)) ([]any, error) {
	constraints = appendOneOfManyConstraint(r, constraints)
	results, err := hasOneOrManyResults(r, parent, c, constraints...)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 && r.HasDefault {
		return r.defaultResults(), nil
	}
	return results, nil
}

// oneOfManyOrder one-of-many 的单项排序（列 + 方向）。
type oneOfManyOrder struct {
	column    string
	direction string
}

// ofManyOrders one-of-many 的完整排序序列：极值列 + 主键 tie-break
// （对应 Laravel CanBeOneOfMany.php:70-176 ofMany 强制追加 keyName =>
// aggregate——同刻并列以主键同向极值决断，latestOfMany 取 id 更大者）。
func ofManyOrders(rel oneOfManyRel) ([]oneOfManyOrder, bool) {
	col, dir, ok := rel.ofManyOrder()
	if !ok {
		return nil, false
	}
	out := []oneOfManyOrder{{column: col, direction: dir}}
	if pk, err := relationTargetPrimaryKey(rel); err == nil && pk != "" && pk != col {
		out = append(out, oneOfManyOrder{column: pk, direction: dir})
	}
	return out, true
}

// appendOneOfManyConstraint one-of-many / partial 关系的取单行约束。
func appendOneOfManyConstraint(rel oneOfManyRel, constraints []func(*Builder)) []func(*Builder) {
	if orders, ok := ofManyOrders(rel); ok {
		return append(append([]func(*Builder){}, constraints...), func(nb *Builder) {
			for _, o := range orders {
				nb.OrderBy(o.column, o.direction)
			}
			nb.Limit(1)
		})
	}
	if h, ok := rel.(interface{ isPartial() bool }); ok && h.isPartial() {
		return append(append([]func(*Builder){}, constraints...), func(nb *Builder) {
			nb.Limit(1)
		})
	}
	return constraints
}

// HasMany 一对多。
type HasMany struct {
	ForeignKey string
	LocalKey   string
	Model      any
}

func (r HasMany) kind() string     { return "hasMany" }
func (r HasMany) targetModel() any { return r.Model }

// GetResults 单父查询：按父键取关联集合（对应 HasMany::getResults）。
func (r HasMany) GetResults(parent any, c *Connection, constraints ...func(*Builder)) ([]any, error) {
	return hasOneOrManyResults(r, parent, c, constraints...)
}

func hasOneOrManyResults(rel Relation, parent any, c *Connection, constraints ...func(*Builder)) ([]any, error) {
	targetTable := relationTargetTable(rel)
	parentTable := modelTableName(parent)
	fk, parentKey := loadRelationKeys(rel, targetTable, parentTable)
	v := keyValue(reflect.ValueOf(parent), parentKey)
	if v == nil || utilsIsBlankValue(v) {
		return nil, nil
	}
	nb := NewBuilder(c, c.GetQueryGrammar()).From(targetTable).WhereIn(fk, []any{v})
	for _, cons := range constraints {
		cons(nb)
	}
	return queryTargetModels(nb, rel.targetModel())
}

type pendingRelation struct {
	name        string
	rel         Relation
	constraints []func(*Builder)
	limit       *int64
	// morphTo 预加载选项：MorphWith 按类型串的约束闭包 / 软删变体 / 计数
	morphWith      map[string][]func(*Builder)
	morphTrashed   bool   // 旧字段：with 变体（保留兼容，等价 morphTrashMode = "with"）
	morphTrashMode string // "with" | "without" | "only"；空 = 默认排除软删
	// morphWithCount 按类型的计数加载（对应 MorphTo::morphWithCount；
	// Go 侧需显式携带关系描述符）
	morphWithCount map[string][]MorphCountSpec
	// constrainAll 全类型通用约束（对应 MorphTo::constrain）
	constrainAll []func(*Builder)
}

// MorphCountSpec morphTo morphWithCount 的单条计数规格：
// 对应 morphWithCount([类型 => [关系名, ...]]) 的一项；Laravel 按关系名
// 隐式解析，Go 侧无容器需显式给出关系描述符。
type MorphCountSpec struct {
	Name     string   // 计数别名（对应 withCount('comments') → comments_count）
	Relation Relation // 被计数的关系描述符
}

// setPendingRelation 注册待预加载关系；同名覆盖（链式 MorphWith 等选项
// 追加语义下避免重复加载）。
func (b *Builder) setPendingRelation(pr pendingRelation) *Builder {
	for i := range b.relations {
		if b.relations[i].name == pr.name {
			b.relations[i] = pr
			return b
		}
	}
	b.relations = append(b.relations, pr)
	return b
}

// With 预加载关系：Get 时自动执行关联查询并填充结构体字段
// （字段名 = 关系名 StudlyCase；BelongsTo/HasOne 填 *T，HasMany 填 []T）。
func (b *Builder) With(name string, rel Relation) *Builder {
	b.relations = append(b.relations, pendingRelation{name: name, rel: rel})
	return b
}

// WithConstraints 带约束闭包的预加载（对应 with(['name' => fn($q) => ...])）。
func (b *Builder) WithConstraints(name string, rel Relation, constraints ...func(*Builder)) *Builder {
	b.relations = append(b.relations, pendingRelation{name: name, rel: rel, constraints: constraints})
	return b
}

// WithLimit 带限额的预加载：自动经 GroupLimit（row_number 分组）实现每组限额。
func (b *Builder) WithLimit(name string, rel Relation, limit int64, constraints ...func(*Builder)) *Builder {
	b.relations = append(b.relations, pendingRelation{name: name, rel: rel, constraints: constraints, limit: &limit})
	return b
}

// WhereHas 关系存在性约束：where exists (select * from target where keys... and constraints...)
// 支持 HasOne/HasMany/MorphMany/MorphOne/BelongsToMany（经 pivot 中间表）/
// HasManyThrough/MorphToMany；MorphTo 无法静态判定目标类型，静默跳过（防 panic）。
func (b *Builder) WhereHas(rel Relation, constraints func(*Builder)) *Builder {
	return b.addHasWhere(rel, constraints, "and")
}

// OrWhereHas 关系存在性约束（or 布尔）。
func (b *Builder) OrWhereHas(rel Relation, constraints func(*Builder)) *Builder {
	return b.addHasWhere(rel, constraints, "or")
}

// Has 关系存在性/计数阈值（对应 Laravel has($relation, $operator, $count)）：
// Has(rel) 等价 Has(rel, ">=", 1)；Has(rel, 3) 默认操作符 >=；
// Has(rel, ">", 2) 自定义操作符。实现见 builder.go hasImpl。
func (b *Builder) Has(rel Relation, args ...any) *Builder { return b.hasImpl(rel, args...) }

// OrHas 无约束关系存在性（or 布尔，对应 Laravel orHas()）。
func (b *Builder) OrHas(rel Relation) *Builder { return b.OrWhereHas(rel, nil) }

// DoesntHave 关系不存在性约束：where not exists (...)（对应 Laravel doesntHave）。
func (b *Builder) DoesntHave(rel Relation) *Builder { return b.WhereDoesntHave(rel, nil) }

// WhereDoesntHave 关系不存在性约束（对应 Laravel whereDoesntHave）。
func (b *Builder) WhereDoesntHave(rel Relation, constraints func(*Builder)) *Builder {
	return b.addHasWhere(rel, constraints, "and-not")
}

// OrWhereDoesntHave 关系不存在性约束（or 布尔）。
func (b *Builder) OrWhereDoesntHave(rel Relation, constraints func(*Builder)) *Builder {
	return b.addHasWhere(rel, constraints, "or-not")
}

// WhereHasMorph 多态关系存在性约束（对应 Laravel whereHasMorph）：
// 子查询带 type 列 in (types) 过滤，types 为候选 morph key
// （如 []string{"posts", "videos"}）；constraints 应用在目标查询上。
func (b *Builder) WhereHasMorph(rel MorphMany, types []string, constraints ...func(*Builder)) *Builder {
	return b.addHasMorphWhere(rel, types, constraints, "and")
}

// OrWhereHasMorph 多态关系存在性约束（or 布尔）。
func (b *Builder) OrWhereHasMorph(rel MorphMany, types []string, constraints ...func(*Builder)) *Builder {
	return b.addHasMorphWhere(rel, types, constraints, "or")
}

func (b *Builder) addHasMorphWhere(rel MorphMany, types []string, constraints []func(*Builder), boolean string) *Builder {
	sub := b.hasSubQuery(rel)
	if sub == nil {
		return b
	}
	typeColumn, _ := morphColumns(rel.MorphName, rel.TypeColumn, "")
	for _, cons := range constraints {
		if cons != nil {
			cons(sub)
		}
	}
	vals := make([]any, 0, len(types))
	for _, t := range types {
		vals = append(vals, t)
	}
	sub.WhereIn(typeColumn, vals)
	return b.appendExistsWhere(sub, boolean)
}

// WhereMorphedTo 匹配多态列指向特定目标的行（对应 Laravel whereMorphedTo）：
// target 为已持久化模型实例（匹配 type + id）或模型零值（仅匹配 type）。
func (b *Builder) WhereMorphedTo(morphName string, target any) *Builder {
	return b.addMorphedTo(morphName, target, "and")
}

// OrWhereMorphedTo 匹配多态列指向特定目标的行（or 布尔）。
func (b *Builder) OrWhereMorphedTo(morphName string, target any) *Builder {
	return b.addMorphedTo(morphName, target, "or")
}

func (b *Builder) addMorphedTo(morphName string, target any, boolean string) *Builder {
	typeColumn, idColumn := morphColumns(morphName, "", "")
	morphKey, ok := MorphAliasOf(target)
	if !ok {
		morphKey = modelTableName(target)
	}
	add := b.Where
	if boolean == "or" {
		add = b.OrWhere
	}
	add(typeColumn, "=", morphKey)
	if _, pv, ok := modelPrimaryKey(target); ok && !utilsIsBlankValue(pv) {
		add(idColumn, "=", pv)
	}
	return b
}

// WhereBelongsTo 按父模型过滤（对应 Laravel whereBelongsTo）：
// where <singular(parentTable)_id | foreignKey> = parent 主键。
func (b *Builder) WhereBelongsTo(parent any, foreignKey ...string) *Builder {
	fk := ""
	if len(foreignKey) > 0 && foreignKey[0] != "" {
		fk = foreignKey[0]
	} else {
		fk = stripPlural(modelTableName(parent)) + "_id"
	}
	if _, v, ok := modelPrimaryKey(parent); ok {
		return b.Where(fk, "=", v)
	}
	return b.Where(fk, "=", parent)
}

func (b *Builder) addHasWhere(rel Relation, constraints func(*Builder), boolean string) *Builder {
	sub := b.hasSubQuery(rel)
	if sub == nil {
		// MorphTo：目标类型由行数据决定，无法静态构造子查询（防 panic 静默跳过）
		return b
	}
	if constraints != nil {
		constraints(sub)
	}
	// morphMany/morphOne：默认限定类型列 = 父 morphKey（对应 Laravel getMorphTypeFilter）
	switch m := rel.(type) {
	case MorphMany:
		addMorphTypeFilter(sub, m.MorphName, m.TypeColumn, m.MorphKey, fmtString(b.Query.From))
	case MorphOne:
		addMorphTypeFilter(sub, m.MorphName, m.TypeColumn, m.MorphKey, fmtString(b.Query.From))
	}
	return b.appendExistsWhere(sub, boolean)
}

// addMorphTypeFilter 子查询追加多态类型列过滤（morphKey 缺省取父表名）。
func addMorphTypeFilter(sub *Builder, morphName, typeColumn, morphKey, parentTable string) {
	typeColumn, _ = morphColumns(morphName, typeColumn, "")
	if morphKey == "" {
		morphKey = parentTable
	}
	sub.Where(typeColumn, "=", morphKey)
}

// appendExistsWhere 把子查询并入父查询（对应 Laravel whereExists / whereNotExists）。
func (b *Builder) appendExistsWhere(sub *Builder, boolean string) *Builder {
	// 子查询绑定并入父查询（对齐 Laravel whereExists）
	b.AddBinding(sub.GetRawBindings()["where"], "where")
	typ := "Exists"
	if boolean == "and-not" || boolean == "or-not" {
		typ = "NotExists"
	}
	boolWord := "and"
	if boolean == "or" || boolean == "or-not" {
		boolWord = "or"
	}
	b.Query.Wheres = append(b.Query.Wheres, &query.Where{
		Type:    typ,
		Query:   sub.Query,
		Boolean: boolWord,
	})
	return b
}

// hasSubQuery 构造关系存在性子查询（全类型通用，对应 QueriesRelationships）。
func (b *Builder) hasSubQuery(rel Relation) *Builder {
	parentTable := fmtString(b.Query.From)

	switch r := rel.(type) {
	case MorphTo:
		// 反向多态：目标表随行而变，无法构造静态子查询（对应 Laravel 需要
		// whereHasMorph 显式给类型）。先于 relationTargetTable 判定（nil 目标防 panic）。
		_ = r
		return nil
	case MorphMany, MorphOne:
		table := relationTargetTable(rel)
		idColumn := ""
		if m, ok := rel.(MorphMany); ok {
			_, idColumn = morphColumns(m.MorphName, m.TypeColumn, m.IDColumn)
		} else {
			m := rel.(MorphOne)
			_, idColumn = morphColumns(m.MorphName, m.TypeColumn, m.IDColumn)
		}
		// 类型列过滤由调用方追加：WhereHas 加 = morphKey，
		// WhereHasMorph 加 in (types)（避免两者冲突）
		sub := NewBuilder(b.Connection, b.grammar).From(table)
		sub.WhereColumn(parentTable+".id", "=", table+"."+idColumn, "and")
		return sub
	case BelongsToMany:
		table := relationTargetTable(rel)
		pivot, fpk, rpk, rk := btmResolvedKeys(r, parentTable)
		pivotSub := NewBuilder(b.Connection, b.grammar).From(pivot)
		pivotSub.WhereColumn(parentTable+".id", "=", pivot+"."+fpk, "and")
		pivotSub.WhereColumn(pivot+"."+rpk, "=", table+"."+rk, "and")
		sub := NewBuilder(b.Connection, b.grammar).From(table)
		sub.WhereExists(pivotSub)
		return sub
	case MorphToMany:
		table := relationTargetTable(rel)
		pivot, fpk, rpk, rk, typeColumn, morphKey := mtmResolvedKeys(r, parentTable, "")
		pivotSub := NewBuilder(b.Connection, b.grammar).From(pivot)
		pivotSub.WhereColumn(parentTable+".id", "=", pivot+"."+fpk, "and")
		pivotSub.WhereColumn(pivot+"."+rpk, "=", table+"."+rk, "and")
		pivotSub.Where(typeColumn, "=", morphKey)
		sub := NewBuilder(b.Connection, b.grammar).From(table)
		sub.WhereExists(pivotSub)
		return sub
	case HasManyThrough, HasOneThrough:
		hmt, isHmt := rel.(HasManyThrough)
		if !isHmt {
			hmt = rel.(HasOneThrough).asHasManyThrough()
		}
		table := relationTargetTable(hmt)
		throughTable := inspectModel(hmt.ThroughModel).table
		lk := hmt.LocalKey
		if lk == "" {
			lk = "id"
		}
		slk := hmt.SecondLocalKey
		if slk == "" {
			slk = "id"
		}
		firstKey := hmt.FirstKey
		if firstKey == "" {
			firstKey = stripPlural(parentTable) + "_id"
		}
		secondKey := hmt.SecondKey
		if secondKey == "" {
			secondKey = stripPlural(throughTable) + "_id"
		}
		throughSub := NewBuilder(b.Connection, b.grammar).From(throughTable)
		throughSub.WhereColumn(throughTable+"."+slk, "=", table+"."+secondKey, "and")
		throughSub.WhereColumn(parentTable+"."+lk, "=", throughTable+"."+firstKey, "and")
		sub := NewBuilder(b.Connection, b.grammar).From(table)
		sub.WhereExists(throughSub)
		return sub
	}

	table := relationTargetTable(rel)
	sub := NewBuilder(b.Connection, b.grammar).From(table)
	fk, parentKey := relationKeysForHas(rel, parentTable)
	sub.WhereColumn(parentTable+"."+parentKey, "=", table+"."+fk, "and")
	return sub
}

// btmResolvedKeys 归一 BelongsToMany 的 pivot 键：返回 (pivot 表, foreignPivotKey, relatedPivotKey, relatedKey)。
func btmResolvedKeys(r BelongsToMany, parentTable string) (string, string, string, string) {
	return btmResolvedKeysNamed(r, parentTable, nil)
}

func btmResolvedKeysNamed(r BelongsToMany, parentTable string, relName *string) (string, string, string, string) {
	targetTable := relationTargetTable(r)
	rk := r.RelatedKey
	if rk == "" {
		rk = "id"
	}
	fpk := r.ForeignPivotKey
	if fpk == "" {
		fpk = stripPlural(parentTable) + "_id"
	}
	rpk := r.RelatedPivotKey
	if rpk == "" {
		rpk = stripPlural(targetTable) + "_id"
	}
	pivot := r.Table
	if pivot == "" {
		a, c := stripPlural(parentTable), stripPlural(targetTable)
		if a > c {
			a, c = c, a
		}
		pivot = a + "_" + c
	}
	return pivot, fpk, rpk, rk
}

// mtmResolvedKeys 归一 MorphToMany 的 pivot 键：返回 (pivot 表, foreignPivotKey, relatedPivotKey, relatedKey, typeColumn, morphKey)。
func mtmResolvedKeys(r MorphToMany, parentTable, relName string) (string, string, string, string, string, string) {
	targetTable := relationTargetTable(r)
	morphName := r.MorphName
	if morphName == "" {
		morphName = relName
	}
	typeColumn, idColumn := morphColumns(morphName, r.TypeColumn, "")
	rk := r.RelatedKey
	if rk == "" {
		rk = "id"
	}
	morphKey := r.MorphKey
	if morphKey == "" {
		morphKey = parentTable
	}
	fpk := r.ForeignPivotKey
	if fpk == "" {
		fpk = idColumn
	}
	rpk := r.RelatedPivotKey
	if rpk == "" {
		rpk = stripPlural(targetTable) + "_id"
	}
	pivot := r.Table
	if pivot == "" {
		pivot = pluralize(morphName)
	}
	return pivot, fpk, rpk, rk, typeColumn, morphKey
}

// relationKeysForHas WhereHas 用：HasOne/HasMany 形态（目标表持有外键）。
func relationKeysForHas(rel Relation, parentTable string) (string, string) {
	fk, lk := relationKeys(rel, parentTable)
	return fk, lk
}

// WithCount 关系计数子查询：select (select count(*) ...) as "name_count"。
// 增强形态（对应 QueriesRelationships::withCount）：
//   - 约束闭包：WithCount("posts", HasMany{...}, func(nb *Builder){...})
//   - 多关系：链式多次调用
//   - 嵌套点语法：WithCount("posts.comments", postsRel, commentsRel)
//     （后续描述符逐层下钻，编译为嵌套 exists）
func (b *Builder) WithCount(name string, rel Relation, rest ...any) *Builder {
	return b.withAggregate(name, rel, "count", "*", rest...)
}

// WithAggregate 通用关系聚合子查询（对应 Laravel withAggregate）：
// select (select fn(col) from target where keys...) as "name_fn_col | alias"。
func (b *Builder) WithAggregate(name string, rel Relation, function, column string, alias ...string) *Builder {
	return b.withAggregate(name, rel, function, column, anySlice(alias)...)
}

// WithMax 关系最大值聚合（对应 Laravel withMax('posts', 'votes')）。
func (b *Builder) WithMax(name string, rel Relation, column string, alias ...string) *Builder {
	return b.withAggregate(name, rel, "max", column, anySlice(alias)...)
}

// WithMin 关系最小值聚合。
func (b *Builder) WithMin(name string, rel Relation, column string, alias ...string) *Builder {
	return b.withAggregate(name, rel, "min", column, anySlice(alias)...)
}

// WithSum 关系求和聚合。
func (b *Builder) WithSum(name string, rel Relation, column string, alias ...string) *Builder {
	return b.withAggregate(name, rel, "sum", column, anySlice(alias)...)
}

// WithAvg 关系平均值聚合。
func (b *Builder) WithAvg(name string, rel Relation, column string, alias ...string) *Builder {
	return b.withAggregate(name, rel, "avg", column, anySlice(alias)...)
}

// WithExists 关系存在性聚合：select (select exists(...) ...) as "name_exists"。
func (b *Builder) WithExists(name string, rel Relation, alias ...string) *Builder {
	a := name + "_exists"
	if len(alias) > 0 && alias[0] != "" {
		a = alias[0]
	}
	sub, _ := b.aggregateSubQuery(rel, "exists", "*", nil)
	sql := "(" + sub + ") as " + b.grammar.Wrap(a)
	b.Query.Columns = append(b.Query.Columns, grammar.NewExpression(sql))
	return b
}

// anySlice []string → []any。
func anySlice(in []string) []any {
	out := make([]any, 0, len(in))
	for _, v := range in {
		out = append(out, v)
	}
	return out
}

// withAggregate 解析 rest（func(*Builder) 约束 / Relation 嵌套段 / string 别名）
// 并编译聚合子查询。
func (b *Builder) withAggregate(name string, rel Relation, function, column string, rest ...any) *Builder {
	var constraints []func(*Builder)
	var nested []Relation
	alias := ""
	for _, r := range rest {
		switch v := r.(type) {
		case func(*Builder):
			constraints = append(constraints, v)
		case Relation:
			nested = append(nested, v)
		case string:
			alias = v
		}
	}

	segments := strings.Split(name, ".")
	joinName := strings.Join(segments, "_")
	baseAlias := joinName
	switch {
	case function == "count":
		baseAlias = joinName + "_count"
	case column == "*":
		baseAlias = joinName + "_" + function
	default:
		baseAlias = joinName + "_" + function + "_" + column
	}

	// 嵌套段逐层下钻：编译为内层 exists（对齐 Laravel 嵌套 withCount 的
	// 嵌套关系计数语义；这里以 exists 内嵌实现）
	var extraConds []string
	if len(nested) > 0 {
		curRel := rel
		curTable := relationTargetTable(rel)
		for _, nrel := range nested {
			nextTable := relationTargetTable(nrel)
			nfk, npk := relationKeys(nrel, curTable)
			cond := "exists (select * from " + b.grammar.WrapTable(nextTable) +
				" where " + b.grammar.Wrap(nextTable+"."+nfk) +
				" = " + b.grammar.Wrap(curTable+"."+npk) + ")"
			extraConds = append(extraConds, cond)
			curRel, curTable = nrel, nextTable
		}
		_ = curRel
	}

	// 别名（count 无约束无嵌套时保持 name_count 的既有金标准形态）
	_ = segments
	sub, subBindings := b.aggregateSubQuery(rel, function, column, constraints)
	if len(subBindings) > 0 {
		// 约束闭包绑定并入父查询 where 段（简单等值约束场景对齐）
		b.AddBinding(subBindings, "where")
	}
	for _, cond := range extraConds {
		sub += " and " + cond
	}
	finalAlias := alias
	if finalAlias == "" {
		finalAlias = baseAlias
	}
	sql := "(" + sub + ") as " + b.grammar.Wrap(finalAlias)
	b.Query.Columns = append(b.Query.Columns, grammar.NewExpression(sql))
	return b
}

// aggregateSubQuery 编译聚合子查询主体（含 morph type 过滤）。
// TODO(model.go)：BelongsToMany/HasManyThrough 形态的聚合暂未支持，
// 需经 pivot/中间表 join，待 builder 暴露子查询编译入口后补齐。
func (b *Builder) aggregateSubQuery(rel Relation, function, column string, constraints []func(*Builder)) (string, []any) {
	table := relationTargetTable(rel)
	parentTable := fmtString(b.Query.From)
	cond := ""
	constrain := func(extra string) { cond = extra }
	var bindings []any

	switch r := rel.(type) {
	case MorphMany, MorphOne:
		var typeColumn, idColumn string
		morphKey := ""
		if m, ok := rel.(MorphMany); ok {
			typeColumn, idColumn = morphColumns(m.MorphName, m.TypeColumn, m.IDColumn)
			morphKey = m.MorphKey
		} else {
			m := rel.(MorphOne)
			typeColumn, idColumn = morphColumns(m.MorphName, m.TypeColumn, m.IDColumn)
			morphKey = m.MorphKey
		}
		if morphKey == "" {
			morphKey = parentTable
		}
		base := b.grammar.Wrap(table+"."+idColumn) + " = " + b.grammar.Wrap(parentTable+".id") +
			" and " + b.grammar.Wrap(typeColumn) + " = '" + morphKey + "'"
		constrain(base)
		_ = r
	default:
		fk, parentKey := relationKeys(rel, parentTable)
		base := b.grammar.Wrap(table+"."+fk) + " = " + b.grammar.Wrap(parentTable+"."+parentKey)
		constrain(base)
	}

	for _, c := range constraints {
		if c == nil {
			continue
		}
		nb := NewBuilder(b.Connection, b.grammar).From(table)
		c(nb)
		// 约束闭包的 wheres 以原生 SQL 拼进子查询（golden 场景覆盖简单等值条件）
		for _, w := range nb.Query.Wheres {
			if w.Type == "Basic" && w.Column != nil {
				col := fmtString(w.Column)
				op := w.Operator
				if op == "" {
					op = "="
				}
				cond += " and " + b.grammar.Wrap(col) + " " + op + " ?"
				bindings = append(bindings, w.Value)
			}
		}
	}

	if function == "exists" {
		return "select exists(select * from " + b.grammar.WrapTable(table) + " where " + cond + ")", bindings
	}
	colExpr := b.grammar.Wrap(column)
	if column == "*" {
		colExpr = "*"
	}
	return "select " + function + "(" + colExpr + ") from " + b.grammar.WrapTable(table) + " where " + cond, bindings
}

// relationTargetTable 目标模型表名。
func relationTargetTable(rel Relation) string {
	return inspectModel(rel.targetModel()).table
}

// relationKeys 归一关系键：返回 (目标表列, 父表列)。
// BelongsTo：目标列 = OwnerKey(id)，父列 = ForeignKey(目标表单数_id，Laravel 约定)；
// HasOne/HasMany：目标列 = ForeignKey(父表单数_id)，父列 = LocalKey(id)。
func relationKeys(rel Relation, parentTable string) (string, string) {
	switch r := rel.(type) {
	case BelongsTo:
		fk := r.OwnerKey
		if fk == "" {
			fk = "id"
		}
		pk := r.ForeignKey
		if pk == "" {
			pk = stripPlural(relationTargetTable(rel)) + "_id"
		}
		return fk, pk
	case HasOne:
		fk := r.ForeignKey
		if fk == "" {
			fk = stripPlural(parentTable) + "_id"
		}
		lk := r.LocalKey
		if lk == "" {
			lk = "id"
		}
		return fk, lk
	case HasMany:
		fk := r.ForeignKey
		if fk == "" {
			fk = stripPlural(parentTable) + "_id"
		}
		lk := r.LocalKey
		if lk == "" {
			lk = "id"
		}
		return fk, lk
	}
	return "id", "id"
}

// loadRelations 在 Get 之后执行预加载并填充父结构体。
func (b *Builder) loadRelations(dest interface{}) {
	if len(b.relations) == 0 {
		return
	}
	rv := reflect.Indirect(reflect.ValueOf(dest))
	if rv.Kind() != reflect.Slice {
		return
	}

	parentTable := fmtString(b.Query.From)
	for _, pr := range b.relations {
		switch r := pr.rel.(type) {
		case BelongsToMany:
			b.loadBelongsToMany(rv, pr, parentTable)
		case HasManyThrough:
			b.loadHasManyThrough(rv, pr, parentTable)
		case HasOneThrough:
			b.loadHasOneThrough(rv, pr, parentTable)
		case MorphMany:
			b.loadMorph(rv, pr, parentTable, false)
		case MorphOne:
			b.loadMorph(rv, pr, parentTable, true)
		case MorphTo:
			b.loadMorphTo(rv, pr)
		case MorphToMany:
			b.loadMorphToMany(rv, pr, parentTable)
		default:
			_ = r
			b.loadRelation(rv, pr)
		}
	}
}

func (b *Builder) loadRelation(parents reflect.Value, pr pendingRelation) {
	rel := pr.rel
	kind := rel.kind()
	targetType := reflect.TypeOf(rel.targetModel())
	if targetType.Kind() == reflect.Ptr {
		targetType = targetType.Elem()
	}
	targetTable := relationTargetTable(rel)

	// 收集父表键值（外键默认取父查询表名单数 + _id，对齐 Laravel 约定）
	parentTypeName := fmtString(b.Query.From)
	if parentTypeName == "" {
		parentTypeName = "parent"
	}
	fk, parentKey := loadRelationKeys(rel, targetTable, parentTypeName)

	keys := make([]interface{}, 0, parents.Len())
	keySeen := map[string]bool{}
	for i := 0; i < parents.Len(); i++ {
		v := keyValue(parents.Index(i), parentKey)
		if v == nil {
			continue
		}
		k := fmt.Sprint(v)
		if !keySeen[k] {
			keySeen[k] = true
			keys = append(keys, v)
		}
	}
	if len(keys) == 0 {
		return
	}

	// 查询目标表
	nb := NewBuilder(b.Connection, b.grammar).From(targetTable)
	nb.WhereIn(fk, keys)
	for _, c := range pr.constraints {
		c(nb)
	}
	// one-of-many（latestOfMany/oldestOfMany/ofMany）：极值列 + 主键
	// tie-break 排序 + limit 1，经下方 GroupLimit 转换编译为
	// row_number() over (partition by fk order by col, pk) <= 1——与 Laravel
	// CanBeOneOfMany 的 row_number 语义一致
	if om, ok := rel.(oneOfManyRel); ok {
		if orders, ok2 := ofManyOrders(om); ok2 && nb.Query.Limit == nil {
			for _, o := range orders {
				nb.OrderBy(o.column, o.direction)
			}
			nb.Limit(1)
		}
	}
	// 约束/显式设置的 limit 自动接入 GroupLimit（row_number 分组限额，对齐 Laravel 预加载 limit）
	if pr.limit != nil && nb.Query.Limit == nil {
		v := uint64(*pr.limit)
		nb.Limit(v)
	}
	if nb.Query.Limit != nil {
		nb.Query.GroupLimit = &query.GroupLimit{Column: fk, Value: *nb.Query.Limit}
		nb.Query.Limit = nil
	}
	related := reflect.New(reflect.SliceOf(targetType))
	if err := nb.Get(related.Interface()); err != nil {
		return
	}

	// 分组：key → 行集合
	groups := map[string][]reflect.Value{}
	rsv := related.Elem()
	for i := 0; i < rsv.Len(); i++ {
		row := rsv.Index(i)
		v := keyValue(row, fk)
		if v == nil {
			continue
		}
		k := fmt.Sprint(v)
		groups[k] = append(groups[k], row)
	}

	// 填充父字段
	fieldName := utils.StudlyCase(pr.name)
	for i := 0; i < parents.Len(); i++ {
		parent := parents.Index(i)
		if parent.Kind() == reflect.Ptr {
			parent = parent.Elem()
		}
		field := parent.FieldByName(fieldName)
		if !field.IsValid() || !field.CanSet() {
			continue
		}
		v := keyValue(parent, parentKey)
		if v == nil {
			continue
		}
		group := groups[fmt.Sprint(v)]

		switch field.Kind() {
		case reflect.Slice:
			out := reflect.MakeSlice(field.Type(), 0, len(group))
			for _, row := range group {
				if row.Type() != field.Type().Elem() {
					p := reflect.New(field.Type().Elem())
					p.Elem().Set(row)
					out = reflect.Append(out, p)
				} else {
					out = reflect.Append(out, row)
				}
			}
			field.Set(out)
		case reflect.Ptr:
			if len(group) > 0 {
				p := reflect.New(field.Type().Elem())
				p.Elem().Set(group[0])
				field.Set(p)
			}
		case reflect.Struct:
			if len(group) > 0 {
				field.Set(group[0])
			}
		}
	}
	_ = kind
}

// keyValue 取结构体（或其指针）中指定列的值。
func keyValue(structVal reflect.Value, col string) any {
	if structVal.Kind() == reflect.Ptr {
		structVal = structVal.Elem()
	}
	if structVal.Kind() != reflect.Struct {
		return nil
	}
	schema, err := NewSchema(structVal.Addr().Interface())
	if err != nil {
		return nil
	}
	for _, f := range schema.Fields {
		if f.Name == col {
			return f.Value.Interface()
		}
	}
	return nil
}

// loadRelationKeys 预加载用键：返回 (目标表列, 父表列)。
// BelongsTo：目标列 = OwnerKey(id)，父列 = ForeignKey(singular(targetTable)_id)；
// HasOne/HasMany：目标列 = ForeignKey(singular(parentTypeName)_id)，父列 = LocalKey(id)。
func loadRelationKeys(rel Relation, targetTable, parentTypeName string) (string, string) {
	switch r := rel.(type) {
	case BelongsTo:
		fk := r.OwnerKey
		if fk == "" {
			fk = "id"
		}
		pk := r.ForeignKey
		if pk == "" {
			pk = stripPlural(targetTable) + "_id"
		}
		return fk, pk
	case HasOne:
		fk := r.ForeignKey
		if fk == "" {
			fk = stripPlural(parentTypeName) + "_id"
		}
		lk := r.LocalKey
		if lk == "" {
			lk = "id"
		}
		return fk, lk
	case HasMany:
		fk := r.ForeignKey
		if fk == "" {
			fk = stripPlural(parentTypeName) + "_id"
		}
		lk := r.LocalKey
		if lk == "" {
			lk = "id"
		}
		return fk, lk
	}
	return "id", "id"
}

func stripPlural(name string) string {
	if len(name) > 3 && name[len(name)-3:] == "ies" {
		return name[:len(name)-3] + "y"
	}
	if len(name) > 1 && name[len(name)-1] == 's' {
		return name[:len(name)-1]
	}
	return name
}

// BelongsToMany 多对多（经 pivot 表）。
// 默认命名：pivot 表 = 两表单数按字典序拼接（role_user），外键 = 表名_id。
type BelongsToMany struct {
	RelatedModel    any // 目标模型零值（Role{}）
	Table           string
	ForeignPivotKey string // 本表在 pivot 中的键（user_id）
	RelatedPivotKey string // 目标表在 pivot 中的键（role_id）
	RelatedKey      string // 目标表主键（id）

	// pivot 读侧选项（对应 InteractsWithPivotTable / BelongsToMany 链式配置）
	PivotColumns    []string        // WithPivot：select 附加的 pivot 列（pivot_ 前缀别名）
	PivotTimestamps bool            // WithTimestamps：pivot 表 created_at/updated_at
	pivotWheres     []pivotWhere    // WherePivot 族（basic/in/notIn/between/.../null）
	pivotOrders     []pivotOrder    // OrderByPivot
	pivotValues     map[string]any  // WithPivotValue：attach/sync 每行附加的固定值
	pivotValuesSet  bool            // 区分“未设置”与空 map（对应 Laravel null | array）
}

// pivotWhere pivot 表 where 条件（对应 wherePivot* 全族）。
type pivotWhere struct {
	column   string
	operator string
	value    any
	values   []any
	isIn     bool   // 旧字段：WherePivotIn（保留兼容，等价 kind = "in"）
	kind     string // "basic"（空串默认）| "in" | "notIn" | "between" | "notBetween" | "null" | "notNull"
	or       bool
}

type pivotOrder struct {
	column    string
	direction string
}

func (r BelongsToMany) kind() string     { return "belongsToMany" }
func (r BelongsToMany) targetModel() any { return r.RelatedModel }

// WithPivot 附加 pivot 列到 select（别名带 pivot_ 前缀，对应 withPivot）。
func (r BelongsToMany) WithPivot(columns ...string) BelongsToMany {
	r.PivotColumns = append(append([]string{}, r.PivotColumns...), columns...)
	return r
}

// WithTimestamps pivot 表带 created_at/updated_at（对应 withTimestamps）。
func (r BelongsToMany) WithTimestamps() BelongsToMany {
	r.PivotTimestamps = true
	return r
}

// WithPivotValue attach/sync 附加的每行 pivot 固定值（对应
// BelongsToMany.php:494 withPivotValue($column, $value)）：显式传入的
// pivotAttrs 优先于固定值。
func (r BelongsToMany) WithPivotValue(column string, value any) BelongsToMany {
	if r.pivotValues == nil {
		r.pivotValues = map[string]any{}
		r.pivotValuesSet = true
	}
	r.pivotValues[column] = value
	return r
}

// withPivotValueAttrs 归一 attach 行属性：固定值打底，显式 attrs 覆盖。
func (r BelongsToMany) withPivotValueAttrs(attrs map[string]any) map[string]any {
	if !r.pivotValuesSet || len(r.pivotValues) == 0 {
		if attrs == nil {
			return map[string]any{}
		}
		return attrs
	}
	return mergePivotAttrs(r.pivotValues, attrs)
}

// WherePivot 作用于 pivot 表列的 where（对应 wherePivot）。
func (r BelongsToMany) WherePivot(column, operator string, value any) BelongsToMany {
	r.pivotWheres = append(r.pivotWheres, pivotWhere{column: column, operator: operator, value: value})
	return r
}

// WherePivotIn 作用于 pivot 表列的 where in（对应 wherePivotIn）。
func (r BelongsToMany) WherePivotIn(column string, values []any) BelongsToMany {
	r.pivotWheres = append(r.pivotWheres, pivotWhere{column: column, isIn: true, values: values})
	return r
}

// OrWherePivot 作用于 pivot 表列的 or where（对应 orWherePivot）。
func (r BelongsToMany) OrWherePivot(column, operator string, value any) BelongsToMany {
	r.pivotWheres = append(r.pivotWheres, pivotWhere{column: column, operator: operator, value: value, or: true})
	return r
}

// OrderByPivot 按 pivot 表列排序（对应 orderByPivot）。
func (r BelongsToMany) OrderByPivot(column string, direction ...string) BelongsToMany {
	dir := "asc"
	if len(direction) > 0 && direction[0] != "" {
		dir = strings.ToLower(direction[0])
	}
	r.pivotOrders = append(r.pivotOrders, pivotOrder{column: column, direction: dir})
	return r
}

// ---- wherePivot 变体全族（对应 BelongsToMany.php:384-628）----

// WherePivotBetween pivot 列 between（对应 wherePivotBetween，values 取前两个）。
func (r BelongsToMany) WherePivotBetween(column string, values []any) BelongsToMany {
	r.pivotWheres = append(r.pivotWheres, pivotWhere{column: column, kind: "between", values: values})
	return r
}

// OrWherePivotBetween pivot 列 between（or 布尔）。
func (r BelongsToMany) OrWherePivotBetween(column string, values []any) BelongsToMany {
	r.pivotWheres = append(r.pivotWheres, pivotWhere{column: column, kind: "between", values: values, or: true})
	return r
}

// WherePivotNotBetween pivot 列 not between（对应 wherePivotNotBetween）。
func (r BelongsToMany) WherePivotNotBetween(column string, values []any) BelongsToMany {
	r.pivotWheres = append(r.pivotWheres, pivotWhere{column: column, kind: "notBetween", values: values})
	return r
}

// OrWherePivotNotBetween pivot 列 not between（or 布尔）。
func (r BelongsToMany) OrWherePivotNotBetween(column string, values []any) BelongsToMany {
	r.pivotWheres = append(r.pivotWheres, pivotWhere{column: column, kind: "notBetween", values: values, or: true})
	return r
}

// WherePivotNotIn pivot 列 not in（对应 wherePivotNotIn）。
func (r BelongsToMany) WherePivotNotIn(column string, values []any) BelongsToMany {
	r.pivotWheres = append(r.pivotWheres, pivotWhere{column: column, kind: "notIn", values: values})
	return r
}

// OrWherePivotNotIn pivot 列 not in（or 布尔）。
func (r BelongsToMany) OrWherePivotNotIn(column string, values []any) BelongsToMany {
	r.pivotWheres = append(r.pivotWheres, pivotWhere{column: column, kind: "notIn", values: values, or: true})
	return r
}

// OrWherePivotIn pivot 列 in（or 布尔，对应 orWherePivotIn）。
func (r BelongsToMany) OrWherePivotIn(column string, values []any) BelongsToMany {
	r.pivotWheres = append(r.pivotWheres, pivotWhere{column: column, kind: "in", values: values, or: true})
	return r
}

// WherePivotNull pivot 列 is null（对应 wherePivotNull）。
func (r BelongsToMany) WherePivotNull(column string) BelongsToMany {
	r.pivotWheres = append(r.pivotWheres, pivotWhere{column: column, kind: "null"})
	return r
}

// WherePivotNotNull pivot 列 is not null（对应 wherePivotNotNull）。
func (r BelongsToMany) WherePivotNotNull(column string) BelongsToMany {
	r.pivotWheres = append(r.pivotWheres, pivotWhere{column: column, kind: "notNull"})
	return r
}

// OrWherePivotNull pivot 列 is null（or 布尔）。
func (r BelongsToMany) OrWherePivotNull(column string) BelongsToMany {
	r.pivotWheres = append(r.pivotWheres, pivotWhere{column: column, kind: "null", or: true})
	return r
}

// OrWherePivotNotNull pivot 列 is not null（or 布尔）。
func (r BelongsToMany) OrWherePivotNotNull(column string) BelongsToMany {
	r.pivotWheres = append(r.pivotWheres, pivotWhere{column: column, kind: "notNull", or: true})
	return r
}

// GetResults 单父查询：经 pivot 中间表取关联模型（对应 BelongsToMany::getResults）。
func (r BelongsToMany) GetResults(parent any, c *Connection, constraints ...func(*Builder)) ([]any, error) {
	parentTable := modelTableName(parent)
	v := keyValue(reflect.ValueOf(parent), "id")
	if v == nil || utilsIsBlankValue(v) {
		return nil, nil
	}
	q := buildBelongsToManyQuery(c.GetQueryGrammar(), r, parentTable, []any{v})
	rows, err := c.SelectMaps(q.sql, q.bindings)
	if err != nil {
		return nil, err
	}
	return modelAnySlice(rowsToModels(rows, reflect.TypeOf(r.RelatedModel))), nil
}

// btmJoinQuery belongsToMany join 查询构造结果。
type btmJoinQuery struct {
	sql      string
	bindings []any
	pivotCol string // 分组列（pivot_<fpk>）
}

// buildBelongsToManyQuery 构造 belongsToMany join 查询：
// select t.*, p.fp as pivot_fp[, 附加 pivot 列] from t inner join pivot p
// on t.rk = p.rp where [p.fp in (父键) and p.pivotWheres...] [order by ...]。
func buildBelongsToManyQuery(g grammar.Grammar, rel BelongsToMany, parentTable string, keys []any) btmJoinQuery {
	targetTable := relationTargetTable(rel)
	pivot, fpk, rpk, rk := btmResolvedKeys(rel, parentTable)

	wrappedTarget := g.WrapTable(targetTable)
	wrappedPivot := g.WrapTable(pivot)

	// select 列
	selects := []string{wrappedTarget + ".*",
		wrappedPivot + "." + g.Wrap(fpk) + " as " + g.Wrap("pivot_"+fpk)}
	pivotCols := append([]string{}, rel.PivotColumns...)
	if rel.PivotTimestamps {
		pivotCols = append(pivotCols, "created_at", "updated_at")
	}
	for _, col := range pivotCols {
		selects = append(selects, wrappedPivot+"."+g.Wrap(col)+" as "+g.Wrap("pivot_"+col))
	}

	// where：fpk in (父键) + pivotWheres（存在 or 布尔时整组加括号）
	conds := []string{wrappedPivot + "." + g.Wrap(fpk) + " in (" +
		strings.Repeat("?, ", len(keys)-1) + "?)"}
	bindings := append([]any{}, keys...)
	hasOr := false
	for _, w := range rel.pivotWheres {
		if w.or {
			hasOr = true
		}
		kind := w.kind
		if kind == "" && w.isIn {
			kind = "in"
		}
		col := wrappedPivot + "." + g.Wrap(w.column)
		switch kind {
		case "in", "notIn":
			op := "in"
			if kind == "notIn" {
				op = "not in"
			}
			if len(w.values) == 0 {
				// 空集合：in 恒假、not in 恒真（对齐 Laravel whereIn 空集语义）
				conds = append(conds, map[bool]string{true: "1 = 0", false: "1 = 1"}[kind == "in"])
				continue
			}
			conds = append(conds, col+" "+op+" ("+strings.Repeat("?, ", len(w.values)-1)+"?)")
			bindings = append(bindings, w.values...)
		case "between", "notBetween":
			op := "between"
			if kind == "notBetween" {
				op = "not between"
			}
			conds = append(conds, col+" "+op+" ? and ?")
			bindings = append(bindings, w.values...)
		case "null":
			conds = append(conds, col+" is null")
		case "notNull":
			conds = append(conds, col+" is not null")
		default:
			conds = append(conds, col+" "+w.operator+" ?")
			bindings = append(bindings, w.value)
		}
	}
	where := conds[0]
	for i, cond := range conds {
		if i == 0 {
			continue
		}
		boolWord := "and"
		if w := rel.pivotWheres[i-1]; w.or {
			boolWord = "or"
		}
		where += " " + boolWord + " " + cond
	}
	if hasOr {
		where = "(" + where + ")"
	}

	sql := "select " + strings.Join(selects, ", ") +
		" from " + wrappedTarget +
		" inner join " + wrappedPivot + " on " + wrappedTarget + "." + g.Wrap(rk) +
		" = " + wrappedPivot + "." + g.Wrap(rpk) +
		" where " + where

	for _, o := range rel.pivotOrders {
		sql += " order by " + wrappedPivot + "." + g.Wrap(o.column) + " " + o.direction
	}

	return btmJoinQuery{sql: sql, bindings: bindings, pivotCol: "pivot_" + fpk}
}

// loadBelongsToMany 经 pivot 表预加载。
func (b *Builder) loadBelongsToMany(parents reflect.Value, pr pendingRelation, parentTable string) {
	rel, ok := pr.rel.(BelongsToMany)
	if !ok {
		return
	}

	keys := make([]interface{}, 0, parents.Len())
	keySeen := map[string]bool{}
	for i := 0; i < parents.Len(); i++ {
		v := keyValue(parents.Index(i), "id")
		if v == nil {
			continue
		}
		k := fmt.Sprint(v)
		if !keySeen[k] {
			keySeen[k] = true
			keys = append(keys, v)
		}
	}
	if len(keys) == 0 {
		return
	}

	q := buildBelongsToManyQuery(b.grammar, rel, parentTable, keys)
	rows, err := b.Connection.SelectMaps(q.sql, q.bindings)
	if err != nil {
		return
	}

	// 按父键分组并填充目标结构体
	targetType := reflect.TypeOf(rel.RelatedModel)
	if targetType.Kind() == reflect.Ptr {
		targetType = targetType.Elem()
	}
	fieldName := utils.StudlyCase(pr.name)
	pivotCol := q.pivotCol

	for i := 0; i < parents.Len(); i++ {
		parent := parents.Index(i)
		if parent.Kind() == reflect.Ptr {
			parent = parent.Elem()
		}
		v := keyValue(parent, "id")
		if v == nil {
			continue
		}
		want := fmt.Sprint(v)
		field := parent.FieldByName(fieldName)
		if !field.IsValid() || !field.CanSet() {
			continue
		}
		out := reflect.MakeSlice(field.Type(), 0, 4)
		for _, row := range rows {
			pv, has := row[pivotCol]
			if !has || fmt.Sprint(pv) != want {
				continue
			}
			elem := reflect.New(targetType).Elem()
			setSchemaFieldsFromMap(elem.Addr().Interface(), row)
			if field.Type().Elem().Kind() == reflect.Ptr {
				p := reflect.New(field.Type().Elem().Elem())
				p.Elem().Set(elem)
				out = reflect.Append(out, p)
			} else {
				out = reflect.Append(out, elem)
			}
		}
		field.Set(out)
	}
}

// queryTargetModels 执行查询并返回目标模型指针切片。
func queryTargetModels(nb *Builder, targetModel any) ([]any, error) {
	t := reflect.TypeOf(targetModel)
	if t == nil {
		return nil, fmt.Errorf("torm: relation has no target model")
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	slice := reflect.New(reflect.SliceOf(t))
	if err := nb.Get(slice.Interface()); err != nil {
		return nil, err
	}
	out := make([]any, 0, slice.Elem().Len())
	for i := 0; i < slice.Elem().Len(); i++ {
		p := reflect.New(t)
		p.Elem().Set(slice.Elem().Index(i))
		out = append(out, p.Interface())
	}
	return out, nil
}

// rowsToModels 把 SelectMaps 的行映射为目标模型指针切片（[]*T）。
func rowsToModels(rows []map[string]any, targetType reflect.Type) []reflect.Value {
	if targetType.Kind() == reflect.Ptr {
		targetType = targetType.Elem()
	}
	out := make([]reflect.Value, 0, len(rows))
	for _, row := range rows {
		p := reflect.New(targetType)
		setSchemaFieldsFromMap(p.Interface(), row)
		out = append(out, p)
	}
	return out
}

// modelAnySlice 把模型值切片转为 []any。
func modelAnySlice(vals []reflect.Value) []any {
	out := make([]any, 0, len(vals))
	for _, v := range vals {
		out = append(out, v.Interface())
	}
	return out
}

// setSchemaFieldsFromMap 按列名把 map 值写入模型字段。
func setSchemaFieldsFromMap(model any, row map[string]any) {
	schema, err := NewSchema(model)
	if err != nil {
		return
	}
	for _, f := range schema.Fields {
		if rv, ok := row[f.Name]; ok && f.Value.CanSet() {
			_ = f.SetValue(rv)
		}
	}
}

// HasManyThrough 远程一对多（经中间表，Laravel hasManyThrough）。
// 例：User HasManyThrough Comment，经 Post：
//
//	ThroughModel: Post{}; FirstKey: "user_id"(posts 上的父外键);
//	SecondKey: "post_id"(comments 上的中间表键); LocalKey: "id"; SecondLocalKey: "id"
type HasManyThrough struct {
	ThroughModel   any
	FirstKey       string
	SecondKey      string
	LocalKey       string
	SecondLocalKey string
	TargetModel    any
	// WithTrashedParents 跳过中间表软删过滤（对应 Laravel withTrashedParents；
	// 默认：中间模型有 deleted_at 字段时 join 条件追加 deleted_at is null）
	WithTrashedParents bool
}

func (r HasManyThrough) kind() string     { return "hasManyThrough" }
func (r HasManyThrough) targetModel() any { return r.TargetModel }

// WithTrashedParents 跳过中间模型软删过滤（对应 Laravel 同名方法）。
func (r HasManyThrough) WithTrashed() HasManyThrough {
	r.WithTrashedParents = true
	return r
}

// GetResults 单父查询：经中间表取远程关联（对应 HasManyThrough::getResults）。
func (r HasManyThrough) GetResults(parent any, c *Connection, constraints ...func(*Builder)) ([]any, error) {
	parentTable := modelTableName(parent)
	lk := r.LocalKey
	if lk == "" {
		lk = "id"
	}
	v := keyValue(reflect.ValueOf(parent), lk)
	if v == nil || utilsIsBlankValue(v) {
		return nil, nil
	}
	q := buildHasManyThroughQuery(c.GetQueryGrammar(), r, parentTable, []any{v})
	rows, err := c.SelectMaps(q.sql, q.bindings)
	if err != nil {
		return nil, err
	}
	return modelAnySlice(rowsToModels(rows, reflect.TypeOf(r.TargetModel))), nil
}

// hmtQuery hasManyThrough join 查询构造结果。
type hmtQuery struct {
	sql             string
	bindings        []any
	throughKeyAlias string
}

// buildHasManyThroughQuery 构造远程一对多 join 查询；
// 中间模型带软删（deleted_at 字段）且未 WithTrashedParents 时，
// join 条件追加 whereNull(deleted_at)（对应 HasOneOrManyThrough join 条件）。
func buildHasManyThroughQuery(g grammar.Grammar, rel HasManyThrough, parentTable string, keys []any) hmtQuery {
	targetTable := relationTargetTable(rel)
	throughInfo := inspectModel(rel.ThroughModel)
	throughTable := throughInfo.table

	lk := rel.LocalKey
	if lk == "" {
		lk = "id"
	}
	slk := rel.SecondLocalKey
	if slk == "" {
		slk = "id"
	}
	firstKey := rel.FirstKey
	if firstKey == "" {
		firstKey = stripPlural(parentTable) + "_id"
	}
	secondKey := rel.SecondKey
	if secondKey == "" {
		secondKey = stripPlural(throughTable) + "_id"
	}

	wrappedTarget := g.WrapTable(targetTable)
	wrappedThrough := g.WrapTable(throughTable)
	throughKeyAlias := "laravel_through_key"

	sql := "select " + wrappedTarget + ".*, " + wrappedThrough + "." + g.Wrap(firstKey) +
		" as " + g.Wrap(throughKeyAlias) +
		" from " + wrappedTarget +
		" inner join " + wrappedThrough + " on " + wrappedThrough + "." + g.Wrap(slk) +
		" = " + wrappedTarget + "." + g.Wrap(secondKey) +
		" where " + wrappedThrough + "." + g.Wrap(firstKey) + " in (" +
		strings.Repeat("?, ", len(keys)-1) + "?)"
	bindings := append([]any{}, keys...)

	// 中间模型软删过滤（对齐 Laravel HasOneOrManyThrough::performJoin 的
	// SoftDeletes 检测）
	if throughInfo.softDeletes && !rel.WithTrashedParents {
		sql += " and " + wrappedThrough + "." + g.Wrap(throughInfo.deletedAtField) + " is null"
	}
	return hmtQuery{sql: sql, bindings: bindings, throughKeyAlias: throughKeyAlias}
}

// loadHasManyThrough 远程一对多预加载。
func (b *Builder) loadHasManyThrough(parents reflect.Value, pr pendingRelation, parentTable string) {
	rel, ok := pr.rel.(HasManyThrough)
	if !ok {
		return
	}

	lk := rel.LocalKey
	if lk == "" {
		lk = "id"
	}

	keys := make([]interface{}, 0, parents.Len())
	keySeen := map[string]bool{}
	for i := 0; i < parents.Len(); i++ {
		v := keyValue(parents.Index(i), lk)
		if v == nil {
			continue
		}
		k := fmt.Sprint(v)
		if !keySeen[k] {
			keySeen[k] = true
			keys = append(keys, v)
		}
	}
	if len(keys) == 0 {
		return
	}

	q := buildHasManyThroughQuery(b.grammar, rel, parentTable, keys)
	rows, err := b.Connection.SelectMaps(q.sql, q.bindings)
	if err != nil {
		return
	}

	targetType := reflect.TypeOf(rel.TargetModel)
	if targetType.Kind() == reflect.Ptr {
		targetType = targetType.Elem()
	}
	fieldName := utils.StudlyCase(pr.name)
	throughKeyAlias := q.throughKeyAlias

	for i := 0; i < parents.Len(); i++ {
		parent := parents.Index(i)
		if parent.Kind() == reflect.Ptr {
			parent = parent.Elem()
		}
		v := keyValue(parent, lk)
		if v == nil {
			continue
		}
		want := fmt.Sprint(v)
		field := parent.FieldByName(fieldName)
		if !field.IsValid() || !field.CanSet() {
			continue
		}
		out := reflect.MakeSlice(field.Type(), 0, 4)
		for _, row := range rows {
			tv, has := row[throughKeyAlias]
			if !has || fmt.Sprint(tv) != want {
				continue
			}
			elem := reflect.New(targetType).Elem()
			setSchemaFieldsFromMap(elem.Addr().Interface(), row)
			if field.Type().Elem().Kind() == reflect.Ptr {
				p := reflect.New(field.Type().Elem().Elem())
				p.Elem().Set(elem)
				out = reflect.Append(out, p)
			} else {
				out = reflect.Append(out, elem)
			}
		}
		field.Set(out)
	}
}

// HasOneThrough 远程一对一（对应 Laravel HasOneThrough，HasOneOrManyThrough
// 子类）：键约定与 HasManyThrough 相同，仅取首条结果。
// 例：Mechanic HasOneThrough CarOwner（经 Car）：
//
//	ThroughModel: Car{}; FirstKey: "mechanic_id"(cars 上的父外键);
//	SecondKey: "car_id"(owners 上的中间表键); LocalKey: "id"; SecondLocalKey: "id"
type HasOneThrough struct {
	ThroughModel   any
	FirstKey       string
	SecondKey      string
	LocalKey       string
	SecondLocalKey string
	TargetModel    any
	// WithTrashedParents 跳过中间表软删过滤（同 HasManyThrough）
	WithTrashedParents bool
}

func (r HasOneThrough) kind() string     { return "hasOneThrough" }
func (r HasOneThrough) targetModel() any { return r.TargetModel }

// WithTrashed 跳过中间模型软删过滤（对应 Laravel withTrashed）。
func (r HasOneThrough) WithTrashed() HasOneThrough {
	r.WithTrashedParents = true
	return r
}

// asHasManyThrough HasOneThrough 复用 HasManyThrough 的 join 查询构造。
func (r HasOneThrough) asHasManyThrough() HasManyThrough {
	return HasManyThrough{
		ThroughModel:       r.ThroughModel,
		FirstKey:           r.FirstKey,
		SecondKey:          r.SecondKey,
		LocalKey:           r.LocalKey,
		SecondLocalKey:     r.SecondLocalKey,
		TargetModel:        r.TargetModel,
		WithTrashedParents: r.WithTrashedParents,
	}
}

// GetResults 单父查询：经中间表取首条关联（对应 HasOneThrough::getResults
// = first() ?: default；父键为空时无默认实例直接返回空）。
func (r HasOneThrough) GetResults(parent any, c *Connection, constraints ...func(*Builder)) ([]any, error) {
	parentTable := modelTableName(parent)
	lk := r.LocalKey
	if lk == "" {
		lk = "id"
	}
	v := keyValue(reflect.ValueOf(parent), lk)
	if v == nil || utilsIsBlankValue(v) {
		return nil, nil
	}
	q := buildHasManyThroughQuery(c.GetQueryGrammar(), r.asHasManyThrough(), parentTable, []any{v})
	rows, err := c.SelectMaps(q.sql, q.bindings)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return modelAnySlice(rowsToModels(rows[:1], reflect.TypeOf(r.TargetModel))), nil
}

// loadHasOneThrough 远程一对一预加载：复用 buildHasManyThroughQuery 的 join
// 查询，按 throughKey 分组后每组取首行回填单值字段（对应 HasOneThrough::match
// 的 matchOne 语义）。约束闭包经 join 后置 where 形式不支持（与 HasManyThrough
// 预加载一致，join 为手拼 SQL）。
func (b *Builder) loadHasOneThrough(parents reflect.Value, pr pendingRelation, parentTable string) {
	rel, ok := pr.rel.(HasOneThrough)
	if !ok {
		return
	}
	hmt := rel.asHasManyThrough()

	lk := hmt.LocalKey
	if lk == "" {
		lk = "id"
	}

	keys := make([]interface{}, 0, parents.Len())
	keySeen := map[string]bool{}
	for i := 0; i < parents.Len(); i++ {
		v := keyValue(parents.Index(i), lk)
		if v == nil {
			continue
		}
		k := fmt.Sprint(v)
		if !keySeen[k] {
			keySeen[k] = true
			keys = append(keys, v)
		}
	}
	if len(keys) == 0 {
		return
	}

	q := buildHasManyThroughQuery(b.grammar, hmt, parentTable, keys)
	rows, err := b.Connection.SelectMaps(q.sql, q.bindings)
	if err != nil {
		return
	}

	targetType := reflect.TypeOf(hmt.TargetModel)
	if targetType.Kind() == reflect.Ptr {
		targetType = targetType.Elem()
	}
	fieldName := utils.StudlyCase(pr.name)
	throughKeyAlias := q.throughKeyAlias

	for i := 0; i < parents.Len(); i++ {
		parent := parents.Index(i)
		if parent.Kind() == reflect.Ptr {
			parent = parent.Elem()
		}
		v := keyValue(parent, lk)
		if v == nil {
			continue
		}
		want := fmt.Sprint(v)
		field := parent.FieldByName(fieldName)
		if !field.IsValid() || !field.CanSet() {
			continue
		}
		for _, row := range rows {
			tv, has := row[throughKeyAlias]
			if !has || fmt.Sprint(tv) != want {
				continue
			}
			elem := reflect.New(targetType).Elem()
			setSchemaFieldsFromMap(elem.Addr().Interface(), row)
			switch field.Kind() {
			case reflect.Ptr:
				p := reflect.New(field.Type().Elem())
				p.Elem().Set(elem)
				field.Set(p)
			case reflect.Struct:
				field.Set(elem)
			}
			break // 每组只取首条（对应 HasOneThrough 语义）
		}
	}
}

// ---- 惰性加载（对应 Laravel $model->load('relation')）----

var (
	loadedRelationMu sync.RWMutex
	// loadedRelations 以模型指针为键缓存惰性加载的关系结果
	// （Go 无魔法属性，用 SetLoadedRelation/LoadedRelation 访问器替代）。
	loadedRelations = map[any]map[string][]any{}
)

// SetLoadedRelation 把关系结果缓存到模型（对应 Laravel relation 属性赋值）。
func SetLoadedRelation(model any, name string, models []any) {
	loadedRelationMu.Lock()
	defer loadedRelationMu.Unlock()
	m, ok := loadedRelations[model]
	if !ok {
		m = map[string][]any{}
		loadedRelations[model] = m
	}
	m[name] = models
}

// LoadedRelation 取惰性加载的关系结果（不存在时 ok=false）。
func LoadedRelation(model any, name string) ([]any, bool) {
	loadedRelationMu.RLock()
	defer loadedRelationMu.RUnlock()
	m, ok := loadedRelations[model]
	if !ok {
		return nil, false
	}
	models, ok := m[name]
	return models, ok
}

// RelationLoaded 关系是否已加载（对应 Laravel relationLoaded）。
func RelationLoaded(model any, name string) bool {
	_, ok := LoadedRelation(model, name)
	return ok
}

// ForgetLoadedRelations 释放模型的全部关系缓存（测试与长生命周期防泄漏用）。
func ForgetLoadedRelations(model any) {
	loadedRelationMu.Lock()
	defer loadedRelationMu.Unlock()
	delete(loadedRelations, model)
}

// Load 显式触发关系加载并缓存到模型（对应 Laravel $model->load(name)）：
// 结果经 SetLoadedRelation 缓存；模型存在同名关系字段（StudlyCase）时同步回填。
func (c *Connection) Load(model any, name string, rel Relation, constraints ...func(*Builder)) error {
	results, err := rel.GetResults(model, c, constraints...)
	if err != nil {
		return err
	}
	if results == nil {
		results = []any{}
	}
	SetLoadedRelation(model, name, results)
	fillLoadedRelationField(model, name, rel, results)
	return nil
}

// LoadMissing 惰性加载已加载则跳过的关系（对应 Laravel Model.php loadMissing：
// relationLoaded 时直接返回，不发关联查询）。
func (c *Connection) LoadMissing(model any, name string, rel Relation, constraints ...func(*Builder)) error {
	if RelationLoaded(model, name) {
		return nil
	}
	return c.Load(model, name, rel, constraints...)
}

// LoadMany 一次加载多个关系。
func (c *Connection) LoadMany(model any, rels map[string]Relation) error {
	// map 迭代顺序不定，但各关系相互独立，无顺序依赖
	for name, rel := range rels {
		if err := c.Load(model, name, rel); err != nil {
			return err
		}
	}
	return nil
}

// fillLoadedRelationField 把结果回填到模型同名关系字段（与 With 预加载一致）。
func fillLoadedRelationField(model any, name string, rel Relation, results []any) {
	rv := reflect.ValueOf(model)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return
	}
	rv = rv.Elem()
	if rv.Kind() != reflect.Struct {
		return
	}
	field := rv.FieldByName(utils.StudlyCase(name))
	if !field.IsValid() || !field.CanSet() || len(results) == 0 {
		return
	}
	elemType := reflect.TypeOf(rel.targetModel())
	if elemType == nil {
		return
	}
	if elemType.Kind() == reflect.Ptr {
		elemType = elemType.Elem()
	}
	switch field.Kind() {
	case reflect.Slice:
		out := reflect.MakeSlice(field.Type(), 0, len(results))
		for _, r := range results {
			rov := reflect.ValueOf(r)
			if rov.Type() == field.Type().Elem() {
				out = reflect.Append(out, rov)
			} else if rov.Kind() == reflect.Ptr && rov.Type().Elem() == field.Type().Elem() {
				out = reflect.Append(out, rov.Elem())
			}
		}
		field.Set(out)
	case reflect.Ptr:
		rov := reflect.ValueOf(results[0])
		if field.Type() == rov.Type() {
			field.Set(rov)
		} else if rov.Kind() == reflect.Struct && field.Type().Elem() == rov.Type() {
			p := reflect.New(rov.Type())
			p.Elem().Set(rov)
			field.Set(p)
		}
	case reflect.Struct:
		rov := reflect.ValueOf(results[0])
		if field.Type() == rov.Type() {
			field.Set(rov)
		} else if rov.Kind() == reflect.Ptr && rov.Elem().Type() == field.Type() {
			field.Set(rov.Elem())
		}
	}
}

// ---- touches 级联（对应 HasRelationships.php:978-1002 touchOwners）----

// modelTouchesRelations 解析模型 `torm:"touches=rel1,rel2"` tag 声明的所属
// 关系名（对应 Laravel $touches 数组属性；tag 可挂任意字段）。
func modelTouchesRelations(model any) []string {
	schema, err := NewSchema(model)
	if err != nil {
		return nil
	}
	var names []string
	for _, f := range schema.Fields {
		v, ok := f.GetAttr("touches")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		for _, n := range strings.Split(v, ",") {
			if n = strings.TrimSpace(n); n != "" {
				names = append(names, n)
			}
		}
	}
	return names
}

// TouchOwners 触碰模型声明关系（torm:"touches=name" tag）指向的父模型时间戳
// （对应 HasRelationships::touchOwners，最小实现）。关系已惰性加载
// （LoadedRelation）时逐个 touch 已加载的父模型实例并递归其 touches；
// 否则按 belongsTo 键约定反查：父表 = pluralize(关系名)，外键 = 关系名_id，
// 执行 update <parent> set updated_at = now where id = <fk 值>。
// 注意：Save/Delete 生命周期不会自动调用（模型保存入口在 model.go，本迭代
// 不允许改动），需在 save/delete 后显式调用。
func (c *Connection) TouchOwners(model any) error {
	return c.touchOwners(model, map[uintptr]bool{})
}

func (c *Connection) touchOwners(model any, visiting map[uintptr]bool) error {
	rv := reflect.ValueOf(model)
	if model == nil || rv.Kind() != reflect.Ptr || rv.IsNil() {
		return nil
	}
	key := rv.Pointer()
	if visiting[key] {
		return nil
	}
	visiting[key] = true

	names := modelTouchesRelations(model)
	if len(names) == 0 {
		return nil
	}
	g := c.GetQueryGrammar()
	for _, name := range names {
		if loaded, ok := LoadedRelation(model, name); ok && len(loaded) > 0 {
			// 已加载：touch 父模型实例并递归其 touches（对齐 Laravel 递归语义）
			for _, p := range loaded {
				if err := c.touch(p); err != nil {
					return err
				}
				if err := c.touchOwners(p, visiting); err != nil {
					return err
				}
			}
			continue
		}
		// 未加载：belongsTo 键约定反查（父表 = pluralize(关系名)，外键 = 关系名_id）
		table := pluralize(utils.SnakeCase(name))
		fk := utils.SnakeCase(name) + "_id"
		v := keyValue(rv, fk)
		if v == nil || utilsIsBlankValue(v) {
			continue
		}
		sql := "update " + g.WrapTable(table) + " set " + g.Wrap("updated_at") + " = ? where " + g.Wrap("id") + " = ?"
		if _, err := c.Update(sql, nowFunc(), v); err != nil {
			return err
		}
	}
	return nil
}

// Touches 模型是否声明触碰指定关系（对应 Laravel touches($relation)）。
func Touches(model any, relation string) bool {
	return containsString(modelTouchesRelations(model), relation)
}

// ---- 关系级 find / first / sole（对应 Relation.php 与 HasOneOrMany.php:228-485）----

// relationTargetPrimaryKey 目标模型主键列名。
func relationTargetPrimaryKey(rel Relation) (string, error) {
	inst, err := newRelatedInstance(rel)
	if err != nil {
		return "", err
	}
	schema, err := NewSchema(inst)
	if err != nil {
		return "", err
	}
	if schema.PrimaryField == nil {
		return "id", nil
	}
	return schema.PrimaryField.Name, nil
}

// RelationFind 关系内按目标模型主键查找（对应 BelongsToMany::find /
// HasOneOrMany::find）：按主键在关系结果内匹配，无命中返回 (nil, nil)。
// 统一在内存中按主键过滤——对 pivot/中间表 join 形态（BelongsToMany 等
// GetResults 不做约束下推的关系）与普通关系行为一致。
func (c *Connection) RelationFind(parent any, rel Relation, id any) (any, error) {
	pk, err := relationTargetPrimaryKey(rel)
	if err != nil {
		return nil, err
	}
	results, err := rel.GetResults(parent, c)
	if err != nil {
		return nil, err
	}
	for _, r := range results {
		if v, ok := pluckValue(reflect.ValueOf(r), pk); ok && fmt.Sprint(v) == fmt.Sprint(id) {
			return r, nil
		}
	}
	return nil, nil
}

// RelationFirst 关系内取首条（对应 first）：constraints 附加在关系查询上
// （键形态关系做 SQL 下推；BelongsToMany/MorphToMany/through 形态的
// GetResults 不下推约束，为 best-effort）；无命中返回 (nil, nil)。
func (c *Connection) RelationFirst(parent any, rel Relation, constraints ...func(*Builder)) (any, error) {
	results, err := rel.GetResults(parent, c, constraints...)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, nil
	}
	return results[0], nil
}

// RelationFirstOrFail 关系内取首条；无命中或查询出错 panic *FailError
// （对应 firstOrFail 抛 ModelNotFoundException，ErrNotFound 可经 Unwrap 取回）。
func (c *Connection) RelationFirstOrFail(parent any, rel Relation, constraints ...func(*Builder)) any {
	res, err := c.RelationFirst(parent, rel, constraints...)
	if err != nil {
		panic(&FailError{Op: "firstOrFail", Err: err})
	}
	if res == nil {
		panic(&FailError{Op: "firstOrFail", Err: ErrNotFound{Msg: "No query results for model."}})
	}
	return res
}

// RelationFirstOr 关系内取首条，无命中走 fallback（对应 firstOr($callback)）。
func (c *Connection) RelationFirstOr(parent any, rel Relation, fallback func() (any, error), constraints ...func(*Builder)) (any, error) {
	res, err := c.RelationFirst(parent, rel, constraints...)
	if err != nil {
		return nil, err
	}
	if res == nil {
		return fallback()
	}
	return res, nil
}

// RelationSole 关系内取唯一一条（对应 sole）：0 条 → ErrNotFound，
// 多条 → ErrMultiple。
func (c *Connection) RelationSole(parent any, rel Relation, constraints ...func(*Builder)) (any, error) {
	results, err := rel.GetResults(parent, c, constraints...)
	if err != nil {
		return nil, err
	}
	switch len(results) {
	case 1:
		return results[0], nil
	case 0:
		return nil, ErrNotFound{Msg: "No query results for model."}
	default:
		return nil, ErrMultiple{Msg: fmt.Sprintf("torm: %d results were found for the relation, expected exactly one", len(results))}
	}
}

// ---- associate / dissociate（对应 BelongsTo.php:187-235、MorphTo.php:246-280）----
// 均只写模型属性，不自动落库（对齐 Laravel）。

// Associate 把 owner 的主键写入 parent 的外键列
// （外键缺省 = singular(owner 表名)_id；不自动 save）。
func Associate(parent, owner any) error {
	fk := stripPlural(modelTableName(owner)) + "_id"
	if _, v, ok := modelPrimaryKey(owner); ok && !utilsIsBlankValue(v) {
		return setModelAttribute(parent, fk, v)
	}
	return fmt.Errorf("torm: associate requires a persisted owner model")
}

// Dissociate 将 parent 的外键列置空（Go 无方法名魔法，外键列需显式给出；
// 不自动 save）。
func Dissociate(parent any, foreignKey string) error {
	return setModelAttribute(parent, foreignKey, nil)
}

// MorphToAssociate 写外键与类型双列（对应 MorphTo::associate）：
// 类型取值 = morph map 别名，未注册时回退 owner 表名。
func MorphToAssociate(parent, owner any, morphName string) error {
	_, idColumn := morphColumns(morphName, "", "")
	typeColumn, _ := morphColumns(morphName, "", "")
	_, v, ok := modelPrimaryKey(owner)
	if !ok {
		return fmt.Errorf("torm: morphTo associate requires a persisted owner model")
	}
	if err := setModelAttribute(parent, idColumn, v); err != nil {
		return err
	}
	alias, ok := MorphAliasOf(owner)
	if !ok {
		alias = modelTableName(owner)
	}
	return setModelAttribute(parent, typeColumn, alias)
}

// MorphToDissociate 同时清空 id 列与 type 列（对应 MorphTo::dissociate）。
func MorphToDissociate(parent any, morphName string) error {
	typeColumn, idColumn := morphColumns(morphName, "", "")
	if err := setModelAttribute(parent, idColumn, nil); err != nil {
		return err
	}
	return setModelAttribute(parent, typeColumn, nil)
}

// ---- 模型级 insert-or-ignore 保存（对应 Laravel Model.php:1432 saveOrIgnore）----

// SaveOrIgnore 未持久化模型以 insert or ignore 写入（对应 Laravel
// saveOrIgnore → performInsertOrIgnore）：唯一约束冲突时静默跳过并返回
// (false, nil)；已持久化（exists）模型返回错误（Laravel 抛 LogicException）。
// 成功时固时间戳、回填自增主键、置 exists/wasRecentlyCreated 并触发
// creating/created/saved 事件（对齐 performInsertOrIgnore 生命周期）。
// 差异说明：Laravel 的 uniqueBy 形参经 insertOrIgnoreReturning 下推到方言
// 编译，Go 侧编译钩子待接线（见待接线清单），故不提供该参数。
func (c *Connection) SaveOrIgnore(model any) (bool, error) {
	if Exists(model) {
		return false, errors.New("torm: cannot use SaveOrIgnore on an existing model")
	}
	schema, err := NewSchema(model)
	if err != nil {
		return false, err
	}
	ob := observerFor(model)
	if err := c.fireModelEvent(ob, EventSaving, model); err != nil {
		return false, err
	}
	if err := c.fireModelEvent(ob, EventCreating, model); err != nil {
		return false, err
	}
	if err := applyUniqueIDs(schema); err != nil {
		return false, err
	}
	applyCreateTimestamps(model)
	attributes := schema.Attributes()
	if TimestampsIgnored() {
		delete(attributes, "created_at")
		delete(attributes, "updated_at")
	}
	if len(attributes) == 0 {
		// Laravel：空属性视为已保存（不写库、不置 exists）
		return true, nil
	}
	nb := c.newModelQuery(model)
	sql, bindings := nb.grammar.CompileInsertOrIgnore(nb.Query, []query.Row{query.ToRow(attributes)})
	lastID, affected, err := c.Insert(sql, bindings...)
	if err != nil {
		return false, err
	}
	if affected == 0 {
		// 冲突被忽略：不回填、不置 exists（对应 Laravel 返回 false）
		return false, nil
	}
	if schema.PrimaryField == nil || schema.PrimaryField.IsBlank {
		// 自增回填（对应 Laravel incrementing 分支）；冲突场景 affected==0 已提前返回
		_ = schema.SetId(lastID)
	}
	setExists(model, true, true)
	syncOriginal(model)
	if err := c.fireModelEvent(ob, EventCreated, model); err != nil {
		return false, err
	}
	return true, c.fireModelEvent(ob, EventSaved, model)
}

// ---- 关系级写方法（对应 HasOneOrMany.php:228-485、MorphOneOrMany.php:330-370）----
// RelationCreate 创建关联模型并自动填外键（morph 关系同时填类型列），
// 走完整 Create 生命周期；rel 支持 HasOne/HasMany/MorphOne/MorphMany。
func (c *Connection) RelationCreate(parent any, rel Relation, attributes map[string]any) (any, error) {
	inst, err := newRelatedInstance(rel)
	if err != nil {
		return nil, err
	}
	if len(attributes) > 0 {
		if err := Fill(inst, attributes); err != nil {
			return nil, err
		}
	}
	if err := applyRelationForeignKeys(inst, parent, rel); err != nil {
		return nil, err
	}
	if err := c.Create(inst); err != nil {
		return nil, err
	}
	return inst, nil
}

// RelationSave 保存关联模型：先填外键（+morph type）再 Save
// （无主键 → insert，有主键 → update 脏列；对齐 Laravel saveOnRelation）。
func (c *Connection) RelationSave(parent any, rel Relation, related any) error {
	if err := applyRelationForeignKeys(related, parent, rel); err != nil {
		return err
	}
	return c.Save(related)
}

// RelationFirstOrCreate 按外键 + attributes 查找或创建关联模型
// （对应 Laravel firstOrCreate）。
func (c *Connection) RelationFirstOrCreate(parent any, rel Relation, attributes map[string]any, values ...map[string]any) (any, error) {
	inst, err := newRelatedInstance(rel)
	if err != nil {
		return nil, err
	}
	if err := applyRelationForeignKeys(inst, parent, rel); err != nil {
		return nil, err
	}
	merged := map[string]any{}
	for k, v := range attributes {
		merged[k] = v
	}
	// 外键/morph 列并入查找条件（对齐 Laravel 关系 firstOrCreate 语义）
	for k, v := range relationForeignKeyAttributes(parent, rel) {
		merged[k] = v
	}
	if err := c.FirstOrCreate(inst, merged, values...); err != nil {
		return nil, err
	}
	return inst, nil
}

// RelationUpdateOrCreate 按外键 + attributes 查找并更新，或创建新关联模型
// （对应 Laravel updateOrCreate）。
func (c *Connection) RelationUpdateOrCreate(parent any, rel Relation, attributes, values map[string]any) (any, error) {
	inst, err := newRelatedInstance(rel)
	if err != nil {
		return nil, err
	}
	if err := applyRelationForeignKeys(inst, parent, rel); err != nil {
		return nil, err
	}
	merged := map[string]any{}
	for k, v := range attributes {
		merged[k] = v
	}
	for k, v := range relationForeignKeyAttributes(parent, rel) {
		merged[k] = v
	}
	if err := c.UpdateOrCreate(inst, merged, values); err != nil {
		return nil, err
	}
	return inst, nil
}

// newRelatedInstance 从关系描述符创建目标模型实例。
func newRelatedInstance(rel Relation) (any, error) {
	t := reflect.TypeOf(rel.targetModel())
	if t == nil {
		return nil, fmt.Errorf("torm: relation has no target model")
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return reflect.New(t).Interface(), nil
}

// applyRelationForeignKeys 把父键（+morph type）写入关联模型。
func applyRelationForeignKeys(related, parent any, rel Relation) error {
	switch r := rel.(type) {
	case HasOne:
		return setModelAttribute(related, defaultHasForeignKey(r.ForeignKey, modelTableName(parent)), modelKeyValue(parent))
	case HasMany:
		return setModelAttribute(related, defaultHasForeignKey(r.ForeignKey, modelTableName(parent)), modelKeyValue(parent))
	case MorphOne:
		return setMorphForeignKeys(related, parent, r.MorphName, r.MorphKey, r.TypeColumn, r.IDColumn)
	case MorphMany:
		return setMorphForeignKeys(related, parent, r.MorphName, r.MorphKey, r.TypeColumn, r.IDColumn)
	}
	return fmt.Errorf("torm: relation write unsupported for %s", rel.kind())
}

func defaultHasForeignKey(explicit, parentTable string) string {
	if explicit != "" {
		return explicit
	}
	return stripPlural(parentTable) + "_id"
}

func setMorphForeignKeys(related, parent any, morphName, morphKey, typeColumn, idColumn string) error {
	typeColumn, idColumn = morphColumns(morphName, typeColumn, idColumn)
	if morphKey == "" {
		morphKey = morphKeyForParent("", modelTableName(parent), reflect.ValueOf(modelSliceOf(parent)))
	}
	if err := setModelAttribute(related, idColumn, modelKeyValue(parent)); err != nil {
		return err
	}
	return setModelAttribute(related, typeColumn, morphKey)
}

// relationForeignKeyAttributes 计算关系的外键属性（FirstOrCreate/UpdateOrCreate 查找条件用）。
func relationForeignKeyAttributes(parent any, rel Relation) map[string]any {
	out := map[string]any{}
	switch r := rel.(type) {
	case HasOne:
		out[defaultHasForeignKey(r.ForeignKey, modelTableName(parent))] = modelKeyValue(parent)
	case HasMany:
		out[defaultHasForeignKey(r.ForeignKey, modelTableName(parent))] = modelKeyValue(parent)
	case MorphOne:
		typeColumn, idColumn := morphColumns(r.MorphName, r.TypeColumn, r.IDColumn)
		out[idColumn] = modelKeyValue(parent)
		out[typeColumn] = morphKeyForParent(r.MorphKey, modelTableName(parent), reflect.ValueOf(modelSliceOf(parent)))
	case MorphMany:
		typeColumn, idColumn := morphColumns(r.MorphName, r.TypeColumn, r.IDColumn)
		out[idColumn] = modelKeyValue(parent)
		out[typeColumn] = morphKeyForParent(r.MorphKey, modelTableName(parent), reflect.ValueOf(modelSliceOf(parent)))
	}
	return out
}

// modelSliceOf 把单模型包成 1 元素切片（morphKeyForParent 需要 parents 切片）。
func modelSliceOf(model any) any {
	if model == nil {
		return nil
	}
	if t := reflect.TypeOf(model); t.Kind() != reflect.Ptr {
		p := reflect.New(t)
		p.Elem().Set(reflect.ValueOf(model))
		model = p.Interface()
	}
	s := reflect.MakeSlice(reflect.SliceOf(reflect.TypeOf(model)), 0, 1)
	return reflect.Append(s, reflect.ValueOf(model)).Interface()
}

// modelTableName 取模型表名（非模型值返回空串）。
func modelTableName(model any) string {
	if model == nil {
		return ""
	}
	defer func() { _ = recover() }() // 非 struct 值（如裸 id）时静默
	return inspectModel(model).table
}

// modelPrimaryKey 取模型主键（列名, 值）。
func modelPrimaryKey(model any) (string, any, bool) {
	if model == nil {
		return "", nil, false
	}
	schema, err := NewSchema(model)
	if err != nil {
		return "", nil, false
	}
	if schema.PrimaryField == nil {
		return "", nil, false
	}
	v := schema.PrimaryField.Value.Interface()
	return schema.PrimaryField.Name, v, true
}

// modelKeyValue 取模型主键值（非模型值原样返回，供 Attach 等接受裸键）。
func modelKeyValue(model any) any {
	if _, v, ok := modelPrimaryKey(model); ok {
		return v
	}
	return model
}

// setModelAttribute 按列名写模型字段（列不存在时报错）。
func setModelAttribute(model any, column string, value any) error {
	schema, err := NewSchema(model)
	if err != nil {
		return err
	}
	for _, f := range schema.Fields {
		if f.Name == column {
			return f.SetValue(value)
		}
	}
	return fmt.Errorf("torm: column %q not found on model", column)
}

// utilsIsBlankValue 值是否为零/空（关联键空值判定）。
func utilsIsBlankValue(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface:
		return rv.IsNil()
	}
	if rv.IsZero() {
		return true
	}
	return false
}

// ---- BelongsToMany pivot 写侧（对应 InteractsWithPivotTable.php）----
// pivot 写走独立 Connection 语句，不打桩预加载路径。

// pivotResolvedKeys 归一 pivot 表名与键（parent 可为模型或裸键值）。
func pivotResolvedKeys(parent any, rel BelongsToMany) (pivot, fpk, rpk, targetTable string) {
	targetTable = relationTargetTable(rel)
	parentTable := modelTableName(parent)
	pivot, fpk, rpk, _ = btmResolvedKeys(rel, parentTable)
	return pivot, fpk, rpk, targetTable
}

// Attach 建立 pivot 关联（对应 attach）：逐对插入；pivotAttrs 为附加列值，
// PivotTimestamps 时自动带 created_at/updated_at。parent 可为模型或其主键值。
func (c *Connection) Attach(parent any, rel BelongsToMany, ids []any, pivotAttrs ...map[string]any) error {
	pivot, fpk, rpk, _ := pivotResolvedKeys(parent, rel)
	// WithPivotValue 固定值打底，显式 pivotAttrs 覆盖（对应 Laravel attach
	// 每行 merge withPivotValues）
	attrs := rel.withPivotValueAttrs(nil)
	if len(pivotAttrs) > 0 {
		attrs = rel.withPivotValueAttrs(pivotAttrs[0])
	}

	cols := []string{fpk, rpk}
	vals := []string{"?", "?"}
	if rel.PivotTimestamps {
		attrs = mergePivotAttrs(attrs, map[string]any{"created_at": nowFunc(), "updated_at": nowFunc()})
	}
	for k := range attrs {
		cols = append(cols, k)
		vals = append(vals, "?")
	}
	// map 迭代顺序不定 → 固定排序保证 SQL/绑定稳定
	sort.Strings(cols[2:])

	parentKey := modelKeyValue(parent)
	for _, id := range ids {
		// 每行新分配绑定切片（连接层可能保留引用，复用会串行污染）
		args := make([]any, 0, len(cols))
		args = append(args, parentKey, id)
		for _, col := range cols[2:] {
			args = append(args, attrs[col])
		}
		sql := "insert into " + c.GetQueryGrammar().WrapTable(pivot) +
			" (" + joinWrappedCols(c.GetQueryGrammar(), cols) + ") values (" + strings.Join(vals, ", ") + ")"
		if _, _, err := c.Insert(sql, args...); err != nil {
			return err
		}
	}
	return nil
}

// Detach 解除 pivot 关联（对应 detach）：ids 为空时解除全部，返回受影响行数。
func (c *Connection) Detach(parent any, rel BelongsToMany, ids ...any) (int64, error) {
	pivot, fpk, rpk, _ := pivotResolvedKeys(parent, rel)
	g := c.GetQueryGrammar()
	sql := "delete from " + g.WrapTable(pivot) + " where " + g.Wrap(fpk) + " = ?"
	args := []any{modelKeyValue(parent)}
	if len(ids) > 0 {
		sql += " and " + g.Wrap(rpk) + " in (" + strings.Repeat("?, ", len(ids)-1) + "?)"
		args = append(args, ids...)
	}
	affected, err := c.Delete(sql, args...)
	return affected, err
}

// SyncResult sync 的返回（对应 Laravel ['attached'=>[], 'detached'=>[], 'updated'=>[]]）。
type SyncResult struct {
	Attached []any
	Detached []any
	Updated  []any
}

// Sync 全量同步 pivot（对应 sync）：附加缺失、解除多余、更新现存（带
// pivotAttrs 时），返回 attached/detached/updated。
func (c *Connection) Sync(parent any, rel BelongsToMany, ids []any, pivotAttrs ...map[string]any) (*SyncResult, error) {
	res, err := c.syncPivot(parent, rel, ids, true, pivotAttrs...)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// SyncWithoutDetaching 同步但不解除缺失项（对应 syncWithoutDetaching）。
func (c *Connection) SyncWithoutDetaching(parent any, rel BelongsToMany, ids []any, pivotAttrs ...map[string]any) (*SyncResult, error) {
	return c.syncPivot(parent, rel, ids, false, pivotAttrs...)
}

// Toggle 切换 pivot 关联（对应 toggle）：已关联的解除、未关联的附加。
func (c *Connection) Toggle(parent any, rel BelongsToMany, ids []any) (*SyncResult, error) {
	current, err := c.currentPivotKeys(parent, rel)
	if err != nil {
		return nil, err
	}
	currentSet := map[string]bool{}
	for _, k := range current {
		currentSet[fmt.Sprint(k)] = true
	}
	var toAttach, toDetach []any
	seen := map[string]bool{}
	for _, id := range ids {
		seen[fmt.Sprint(id)] = true
		if currentSet[fmt.Sprint(id)] {
			toDetach = append(toDetach, id)
		} else {
			toAttach = append(toAttach, id)
		}
	}
	_ = seen
	res := &SyncResult{}
	if len(toAttach) > 0 {
		if err := c.Attach(parent, rel, toAttach); err != nil {
			return nil, err
		}
		res.Attached = toAttach
	}
	if len(toDetach) > 0 {
		n, err := c.Detach(parent, rel, toDetach...)
		if err != nil {
			return nil, err
		}
		_ = n
		res.Detached = toDetach
	}
	return res, nil
}

// UpdateExistingPivot 更新现存 pivot 行（对应 updateExistingPivot），返回受影响行数。
func (c *Connection) UpdateExistingPivot(parent any, rel BelongsToMany, id any, attrs map[string]any) (int64, error) {
	if len(attrs) == 0 {
		return 0, nil
	}
	pivot, fpk, rpk, _ := pivotResolvedKeys(parent, rel)
	g := c.GetQueryGrammar()
	if rel.PivotTimestamps {
		attrs = mergePivotAttrs(attrs, map[string]any{"updated_at": nowFunc()})
	}
	sets := make([]string, 0, len(attrs))
	args := make([]any, 0, len(attrs)+2)
	cols := make([]string, 0, len(attrs))
	for k := range attrs {
		cols = append(cols, k)
	}
	sort.Strings(cols)
	for _, k := range cols {
		sets = append(sets, g.Wrap(k)+" = ?")
		args = append(args, attrs[k])
	}
	sql := "update " + g.WrapTable(pivot) + " set " + strings.Join(sets, ", ") +
		" where " + g.Wrap(fpk) + " = ? and " + g.Wrap(rpk) + " = ?"
	args = append(args, modelKeyValue(parent), id)
	return c.Update(sql, args...)
}

// SyncWithPivotValues 以给定 pivot 值同步 ids（对应 syncWithPivotValues）：
// 缺失的附加（带 values）、现存的更新 values；detaching=false 时
// syncWithoutDetaching 语义（不解除其它关联）。ids 可为模型或裸键。
func (c *Connection) SyncWithPivotValues(parent any, rel BelongsToMany, ids []any, values map[string]any, detaching ...bool) (*SyncResult, error) {
	detach := true
	if len(detaching) > 0 {
		detach = detaching[0]
	}
	return c.syncPivot(parent, rel, ids, detach, values)
}

// ---- pivot 写侧 OrFail 族（对应 Laravel orFail 后缀方法；失败以
// *FailError panic，可 recover 后经 Unwrap 取原始错误）----

// AttachOrFail 建立 pivot 关联，失败 panic *FailError{Op:"attach"}。
func (c *Connection) AttachOrFail(parent any, rel BelongsToMany, ids []any, pivotAttrs ...map[string]any) {
	orFail("attach", c.Attach(parent, rel, ids, pivotAttrs...))
}

// DetachOrFail 解除 pivot 关联，失败 panic *FailError{Op:"detach"}，返回受影响行数。
func (c *Connection) DetachOrFail(parent any, rel BelongsToMany, ids ...any) int64 {
	n, err := c.Detach(parent, rel, ids...)
	orFail("detach", err)
	return n
}

// SyncOrFail 全量同步 pivot，失败 panic *FailError{Op:"sync"}。
func (c *Connection) SyncOrFail(parent any, rel BelongsToMany, ids []any, pivotAttrs ...map[string]any) *SyncResult {
	res, err := c.Sync(parent, rel, ids, pivotAttrs...)
	orFail("sync", err)
	return res
}

// ToggleOrFail 切换 pivot 关联，失败 panic *FailError{Op:"toggle"}。
func (c *Connection) ToggleOrFail(parent any, rel BelongsToMany, ids []any) *SyncResult {
	res, err := c.Toggle(parent, rel, ids)
	orFail("toggle", err)
	return res
}

// UpdateExistingPivotOrFail 更新现存 pivot 行，失败 panic
// *FailError{Op:"updateExistingPivot"}，返回受影响行数。
func (c *Connection) UpdateExistingPivotOrFail(parent any, rel BelongsToMany, id any, attrs map[string]any) int64 {
	n, err := c.UpdateExistingPivot(parent, rel, id, attrs)
	orFail("updateExistingPivot", err)
	return n
}

// syncPivot sync/toggle 共用实现；detachMissing 控制是否解除缺失项。
func (c *Connection) syncPivot(parent any, rel BelongsToMany, ids []any, detachMissing bool, pivotAttrs ...map[string]any) (*SyncResult, error) {
	current, err := c.currentPivotKeys(parent, rel)
	if err != nil {
		return nil, err
	}
	currentSet := map[string]bool{}
	for _, k := range current {
		currentSet[fmt.Sprint(k)] = true
	}
	idSet := map[string]bool{}
	res := &SyncResult{}

	var toAttach []any
	for _, id := range ids {
		idSet[fmt.Sprint(id)] = true
		if currentSet[fmt.Sprint(id)] {
			if len(pivotAttrs) > 0 && len(pivotAttrs[0]) > 0 {
				if _, err := c.UpdateExistingPivot(parent, rel, id, pivotAttrs[0]); err != nil {
					return nil, err
				}
				res.Updated = append(res.Updated, id)
			}
			continue
		}
		toAttach = append(toAttach, id)
	}

	var toDetach []any
	for _, k := range current {
		if !idSet[fmt.Sprint(k)] {
			toDetach = append(toDetach, k)
		}
	}

	if len(toAttach) > 0 {
		if err := c.Attach(parent, rel, toAttach, pivotAttrs...); err != nil {
			return nil, err
		}
		res.Attached = toAttach
	}
	if detachMissing && len(toDetach) > 0 {
		if _, err := c.Detach(parent, rel, toDetach...); err != nil {
			return nil, err
		}
		res.Detached = toDetach
	}
	return res, nil
}

// currentPivotKeys 查询父模型当前的 pivot 关联键集合。
func (c *Connection) currentPivotKeys(parent any, rel BelongsToMany) ([]any, error) {
	pivot, fpk, rpk, _ := pivotResolvedKeys(parent, rel)
	sql := "select " + c.GetQueryGrammar().Wrap(rpk) + " from " +
		c.GetQueryGrammar().WrapTable(pivot) + " where " +
		c.GetQueryGrammar().Wrap(fpk) + " = ?"
	rows, err := c.SelectMaps(sql, []any{modelKeyValue(parent)})
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(rows))
	for _, row := range rows {
		if v, ok := row[rpk]; ok {
			out = append(out, v)
		}
	}
	return out, nil
}

// mergePivotAttrs 合并 pivot 附加属性（后者覆盖前者）。
func mergePivotAttrs(base, extra map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// joinWrappedCols 逗号拼接包裹后的列。
func joinWrappedCols(g grammar.Grammar, cols []string) string {
	wrapped := make([]string, 0, len(cols))
	for _, col := range cols {
		wrapped = append(wrapped, g.Wrap(col))
	}
	return strings.Join(wrapped, ", ")
}
