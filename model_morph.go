package torm

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/go-think/torm/grammar"
	"github.com/go-think/torm/utils"
)

// ---- 多态关系（对应 Laravel morphMany/morphOne/morphTo）----

// MorphMany 多态一对多：目标表持有 type 列（commentable_type）与 id 列（commentable_id）。
// MorphKey 为类型列取值，缺省 = 父表名（Go 侧约定；Laravel 默认为 FQCN）。
type MorphMany struct {
	MorphName   string // 多态名（决定默认列名 morphName_type / morphName_id）
	MorphKey    string
	TypeColumn  string
	IDColumn    string
	TargetModel any
}

func (r MorphMany) kind() string     { return "morphMany" }
func (r MorphMany) targetModel() any { return r.TargetModel }

// GetResults 单父查询：按父键 + 类型列取多态关联（对应 MorphMany::getResults）。
func (r MorphMany) GetResults(parent any, c *Connection, constraints ...func(*Builder)) ([]any, error) {
	return morphManyOrOneResults(r, parent, c, constraints)
}

// MorphOne 多态一对一。
type MorphOne struct {
	MorphName   string
	MorphKey    string
	TypeColumn  string
	IDColumn    string
	TargetModel any
	// withDefault（对应 SupportsDefaultModels，HasOne.php 同款实现）：
	// 关系未命中时返回目标模型新实例而非 nil；DefaultFn 非 nil 时以新实例
	// 调用，返回值非 nil 则使用之
	HasDefault bool
	DefaultFn  func(inst any) any
	// one-of-many 变体（对应 CanBeOneOfMany，语义同 HasOne 同名字段）
	OfManyColumn    string
	OfManyAggregate string // "max" | "min"；空串 = 关闭
	Partial         bool   // one()：显式标记只取单行
}

func (r MorphOne) kind() string     { return "morphOne" }
func (r MorphOne) targetModel() any { return r.TargetModel }

// LatestOfMany 取该列最新一行（对应 MorphOne::latestOfMany，缺省列 created_at）。
func (r MorphOne) LatestOfMany(column ...string) MorphOne {
	r.OfManyColumn, r.OfManyAggregate = ofManyColumnArg(column, "created_at"), "max"
	return r
}

// OldestOfMany 取该列最早一行（对应 MorphOne::oldestOfMany，缺省列 created_at）。
func (r MorphOne) OldestOfMany(column ...string) MorphOne {
	r.OfManyColumn, r.OfManyAggregate = ofManyColumnArg(column, "created_at"), "min"
	return r
}

// OfMany 自定义极值列与聚合（对应 ofMany(column, aggregate)）。
func (r MorphOne) OfMany(column, aggregate string) MorphOne {
	r.OfManyColumn, r.OfManyAggregate = column, aggregate
	return r
}

// One 标记关系为单行形态（对应 SupportsPartialRelations::one()）。
func (r MorphOne) One() MorphOne { r.Partial = true; return r }

// WithDefault 启用默认模型（对应 SupportsDefaultModels::withDefault）：
// 关系未命中时返回目标模型新实例而非空；fn 可选，非 nil 时以新实例调用，
// 返回值非 nil 则使用返回值。
func (r MorphOne) WithDefault(fn ...func(inst any) any) MorphOne {
	r.HasDefault = true
	if len(fn) > 0 {
		r.DefaultFn = fn[0]
	}
	return r
}

// defaultResults withDefault 的默认结果（对应 getDefaultFor）。
func (r MorphOne) defaultResults() []any {
	return relationDefaultResults(r, r.DefaultFn)
}

func (r MorphOne) ofManyOrder() (string, string, bool) {
	if r.OfManyColumn == "" || r.OfManyAggregate == "" {
		return "", "", false
	}
	dir := "desc"
	if r.OfManyAggregate == "min" {
		dir = "asc"
	}
	return r.OfManyColumn, dir, true
}

func (r MorphOne) isPartial() bool { return r.Partial }

// GetResults 单父查询：按父键 + 类型列取多态关联（对应 MorphOne::getResults）。
// one-of-many 取极值一行（极值列 + 主键 tie-break 排序，对应 CanBeOneOfMany）；
// withDefault 生效时未命中返回默认实例。
func (r MorphOne) GetResults(parent any, c *Connection, constraints ...func(*Builder)) ([]any, error) {
	results, err := morphManyOrOneResults(r, parent, c, appendOneOfManyConstraint(r, constraints))
	if err != nil {
		return nil, err
	}
	if len(results) == 0 && r.HasDefault {
		return r.defaultResults(), nil
	}
	return results, nil
}

func morphManyOrOneResults(rel any, parent any, c *Connection, constraints []func(*Builder)) ([]any, error) {
	var typeColumn, idColumn, morphKey string
	var targetModel any
	switch r := rel.(type) {
	case MorphMany:
		typeColumn, idColumn = morphColumns(r.MorphName, r.TypeColumn, r.IDColumn)
		morphKey, targetModel = r.MorphKey, r.TargetModel
	case MorphOne:
		typeColumn, idColumn = morphColumns(r.MorphName, r.TypeColumn, r.IDColumn)
		morphKey, targetModel = r.MorphKey, r.TargetModel
	}
	parentTable := modelTableName(parent)
	morphKey = morphKeyForParent(morphKey, parentTable, reflect.ValueOf(modelSliceOf(parent)))
	v := keyValue(reflect.ValueOf(parent), "id")
	if v == nil || utilsIsBlankValue(v) {
		return nil, nil
	}
	nb := NewBuilder(c, c.GetQueryGrammar()).From(relationTargetTable(rel.(Relation))).
		Where(typeColumn, "=", morphKey).
		WhereIn(idColumn, []any{v})
	for _, cons := range constraints {
		cons(nb)
	}
	return queryTargetModels(nb, targetModel)
}

// MorphTo 反向多态：本表持有类型列与 id 列；TypeMap 把类型串映射到模型零值。
type MorphTo struct {
	MorphName  string
	TypeColumn string
	IDColumn   string
	TypeMap    map[string]any
	// OwnerKey 目标表键（默认 id，对应 MorphTo $ownerKey）
	OwnerKey string
	// WithTrashed 目标模型为软删时跳过 deleted_at 过滤（对应 morphTo withTrashed）
	WithTrashed bool
	// OnlyTrashed 仅取已软删的 owner（对应 morphTo onlyTrashed；描述符级变体，
	// 预加载级变体见 Builder.MorphToOnlyTrashed）
	OnlyTrashed bool
	// withDefault（对应 SupportsDefaultModels）：关系未命中时返回空实例而非 nil；
	// DefaultFn 非 nil 时以新实例调用，返回值非 nil 则使用之
	HasDefault bool
	DefaultFn  func(inst any) any
}

// WithTrashedVariants 描述符级软删变体链式设置（字段 WithTrashed 为既有导出
// 字段，方法名避开重名）。

// WithDeleted 跳过目标模型软删过滤（对应 withTrashed 的链式形态）。
func (r MorphTo) WithDeleted() MorphTo {
	r.WithTrashed = true
	return r
}

// WithOnlyTrashed 仅取已软删的 owner（对应 onlyTrashed 的链式形态）。
func (r MorphTo) WithOnlyTrashed() MorphTo {
	r.OnlyTrashed = true
	return r
}

// WithDefault 启用默认模型（对应 SupportsDefaultModels::withDefault）：
// 关系未命中时返回默认实例而非 nil。Go 侧 MorphTo 描述符无解析出的目标
// 类型（targetModel 为 nil），无回调时仅在类型已解析（行存在但查询未命中）
// 的情况下能以 TypeMap 的零值类型实例化；类型列也为空时必须提供回调
// 构造实例（fn 的 inst 参可能为 nil）。返回值非 nil 时使用返回值。
func (r MorphTo) WithDefault(fn ...func(inst any) any) MorphTo {
	r.HasDefault = true
	if len(fn) > 0 {
		r.DefaultFn = fn[0]
	}
	return r
}

func (r MorphTo) kind() string     { return "morphTo" }
func (r MorphTo) targetModel() any { return nil }

// defaultResults withDefault 的默认结果（对应 getDefaultFor）：
// resolved 为已解析的目标模型零值（类型列/TypeMap 命中时）或 nil。
func (r MorphTo) defaultResults(resolved any) []any {
	if r.DefaultFn != nil {
		if out := r.DefaultFn(resolved); out != nil {
			return []any{out}
		}
	}
	if resolved != nil {
		t := reflect.TypeOf(resolved)
		if t.Kind() == reflect.Ptr {
			t = t.Elem()
		}
		if t.Kind() == reflect.Struct {
			return []any{reflect.New(t).Interface()}
		}
	}
	return nil
}

// GetResults 单父查询：按类型列解析目标模型后取 owner（对应 MorphTo::getResults）。
// MorphName/TypeColumn 均空时从父模型 schema 推断（唯一 *_type 列）。
func (r MorphTo) GetResults(parent any, c *Connection, constraints ...func(*Builder)) ([]any, error) {
	typeColumn, idColumn := morphToColumns(r, parent)
	typeVal := morphStringField(reflect.ValueOf(parent), typeColumn)
	idVal := keyValue(reflect.ValueOf(parent), idColumn)
	if typeVal == "" || idVal == nil || utilsIsBlankValue(idVal) {
		if r.HasDefault {
			return r.defaultResults(nil), nil
		}
		return nil, nil
	}
	model, ok := r.TypeMap[typeVal]
	if !ok || model == nil {
		model = MorphModelForAlias(typeVal)
	}
	if model == nil {
		return nil, nil
	}
	ownerKey := r.OwnerKey
	if ownerKey == "" {
		ownerKey = "id"
	}
	nb := NewBuilder(c, c.GetQueryGrammar()).From(inspectModel(model).table)
	// 软删变体：nb 无 modelInfo，软删 scope 不生效，用显式 where 表达 only
	if info := inspectModel(model); info.softDeletes && r.OnlyTrashed {
		nb.WhereNotNull(info.deletedAtField)
	}
	nb.WhereIn(ownerKey, []any{idVal})
	for _, cons := range constraints {
		cons(nb)
	}
	results, err := queryTargetModels(nb, model)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 && r.HasDefault {
		return r.defaultResults(model), nil
	}
	return results, nil
}

// morphColumns 归一多态列名。
func morphColumns(morphName, typeColumn, idColumn string) (string, string) {
	if typeColumn == "" {
		typeColumn = morphName + "_type"
	}
	if idColumn == "" {
		idColumn = morphName + "_id"
	}
	return typeColumn, idColumn
}

// morphToColumns 反向多态列名归一：MorphName/TypeColumn 均缺省时，
// 从父模型 schema 找唯一 *_type 列推断（如 subject_type → subject_id）。
func morphToColumns(rel MorphTo, parent any) (string, string) {
	if rel.TypeColumn != "" || rel.IDColumn != "" || rel.MorphName != "" {
		return morphColumns(rel.MorphName, rel.TypeColumn, rel.IDColumn)
	}
	rv := reflect.ValueOf(parent)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return "", ""
	}
	schema, err := NewSchema(rv.Addr().Interface())
	if err != nil {
		return "", ""
	}
	for _, f := range schema.Fields {
		if strings.HasSuffix(f.Name, "_type") {
			return f.Name, strings.TrimSuffix(f.Name, "_type") + "_id"
		}
	}
	return "", ""
}

// loadMorph 多态一对多/一对一预加载：
// select * from target where type_col = ? and id_col in (父键集合)，按 id_col 分组回填。
func (b *Builder) loadMorph(parents reflect.Value, pr pendingRelation, parentTable string, single bool) {
	var morphKey, typeColumn, idColumn string
	var targetModel any
	switch r := pr.rel.(type) {
	case MorphMany:
		morphKey, typeColumn, idColumn, targetModel = r.MorphKey, r.TypeColumn, r.IDColumn, r.TargetModel
		if typeColumn == "" && idColumn == "" {
			morphName := r.MorphName
			if morphName == "" {
				morphName = pr.name
			}
			typeColumn, idColumn = morphColumns(morphName, typeColumn, idColumn)
		}
	case MorphOne:
		morphKey, typeColumn, idColumn, targetModel = r.MorphKey, r.TypeColumn, r.IDColumn, r.TargetModel
		if typeColumn == "" && idColumn == "" {
			morphName := r.MorphName
			if morphName == "" {
				morphName = pr.name
			}
			typeColumn, idColumn = morphColumns(morphName, typeColumn, idColumn)
		}
	}
	if morphKey == "" {
		morphKey = morphKeyForParent(morphKey, parentTable, parents)
	}

	targetType := reflect.TypeOf(targetModel)
	if targetType.Kind() == reflect.Ptr {
		targetType = targetType.Elem()
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

	nb := NewBuilder(b.Connection, b.grammar).From(relationTargetTable(pr.rel))
	nb.Where(typeColumn, "=", morphKey)
	nb.WhereIn(idColumn, keys)
	related := reflect.New(reflect.SliceOf(targetType))
	if err := nb.Get(related.Interface()); err != nil {
		return
	}

	fieldName := utils.StudlyCase(pr.name)
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
		if single {
			for j := 0; j < related.Elem().Len(); j++ {
				row := related.Elem().Index(j)
				rv := keyValue(row, idColumn)
				if rv == nil || fmt.Sprint(rv) != want {
					continue
				}
				p := reflect.New(field.Type().Elem())
				p.Elem().Set(row)
				field.Set(p)
				break
			}
			continue
		}
		out := reflect.MakeSlice(field.Type(), 0, 4)
		for j := 0; j < related.Elem().Len(); j++ {
			row := related.Elem().Index(j)
			rv := keyValue(row, idColumn)
			if rv == nil || fmt.Sprint(rv) != want {
				continue
			}
			if field.Type().Elem().Kind() == reflect.Ptr {
				p := reflect.New(field.Type().Elem().Elem())
				p.Elem().Set(row)
				out = reflect.Append(out, p)
			} else {
				out = reflect.Append(out, row)
			}
		}
		field.Set(out)
	}
}

// loadMorphTo 反向多态预加载：按类型列分组，逐类型 whereIn(id) 查询。
// 字段类型建议为 interface{}（填充具体模型指针）。
func (b *Builder) loadMorphTo(parents reflect.Value, pr pendingRelation) {
	rel, ok := pr.rel.(MorphTo)
	if !ok {
		return
	}
	typeColumn, idColumn := morphColumns(pr.name, rel.TypeColumn, rel.IDColumn)

	// 按类型分组收集 id
	type bucket struct {
		model   any
		keyIdx  []int
		keyVals []interface{}
	}
	buckets := map[string]*bucket{}
	for i := 0; i < parents.Len(); i++ {
		parent := parents.Index(i)
		if parent.Kind() == reflect.Ptr {
			parent = parent.Elem()
		}
		typeVal := morphStringField(parent, typeColumn)
		idVal := keyValue(parent, idColumn)
		if typeVal == "" || idVal == nil {
			continue
		}
		bk, ok := buckets[typeVal]
		if !ok {
			model, ok2 := rel.TypeMap[typeVal]
			if !ok2 || model == nil {
				model = MorphModelForAlias(typeVal)
			}
			if model == nil {
				continue
			}
			bk = &bucket{model: model}
			buckets[typeVal] = bk
		}
		bk.keyIdx = append(bk.keyIdx, i)
		bk.keyVals = append(bk.keyVals, idVal)
	}

	fieldName := utils.StudlyCase(pr.name)
	ownerKey := rel.OwnerKey
	if ownerKey == "" {
		ownerKey = "id"
	}
	// 软删变体归一（对应 MorphTo withTrashed/withoutTrashed/onlyTrashed）
	trashMode := pr.morphTrashMode
	if trashMode == "" && pr.morphTrashed {
		trashMode = "with"
	}
	for typeVal, bk := range buckets {
		nb := NewBuilder(b.Connection, b.grammar).From(inspectModel(bk.model).table)
		// constrain：全类型通用约束（对应 MorphTo::constrain）
		for _, c := range pr.constrainAll {
			if c != nil {
				c(nb)
			}
		}
		if cons, ok := pr.morphWith[typeVal]; ok {
			for _, c := range cons {
				if c != nil {
					c(nb)
				}
			}
		}
		// morphWithCount：按类型的计数加载（对应 MorphTo::morphWithCount）；
		// WithCount 追加 select 列，默认 select 需显式补 *
		if len(pr.morphWithCount[typeVal]) > 0 && len(nb.Query.Columns) == 0 {
			nb.Select("*")
		}
		for _, spec := range pr.morphWithCount[typeVal] {
			nb.WithCount(spec.Name, spec.Relation)
		}
		// 软删目标模型默认排除已删除行；变体按 morphTrashMode 覆盖。
		// （nb 无 modelInfo，软删 scope 不生效，用显式 where 表达变体）
		if info := inspectModel(bk.model); info.softDeletes {
			switch {
			case trashMode == "with" || rel.WithTrashed:
				// with：不加过滤
			case trashMode == "only" || rel.OnlyTrashed:
				nb.WhereNotNull(info.deletedAtField)
			default:
				nb.WhereNull(info.deletedAtField)
			}
		}
		nb.WhereIn(ownerKey, bk.keyVals)
		related := reflect.New(reflect.SliceOf(reflect.TypeOf(bk.model).Elem()))
		if err := nb.Get(related.Interface()); err != nil {
			continue
		}

		byID := map[string]reflect.Value{}
		rsv := related.Elem()
		for j := 0; j < rsv.Len(); j++ {
			row := rsv.Index(j)
			v := keyValue(row, ownerKey)
			if v != nil {
				byID[fmt.Sprint(v)] = row
			}
		}

		for _, idx := range bk.keyIdx {
			parent := parents.Index(idx)
			if parent.Kind() == reflect.Ptr {
				parent = parent.Elem()
			}
			idVal := keyValue(parent, idColumn)
			if idVal == nil {
				continue
			}
			row, ok := byID[fmt.Sprint(idVal)]
			if !ok {
				continue
			}
			field := parent.FieldByName(fieldName)
			if !field.IsValid() || !field.CanSet() {
				continue
			}
			p := reflect.New(row.Type())
			p.Elem().Set(row)
			switch field.Kind() {
			case reflect.Interface:
				field.Set(p)
			case reflect.Ptr:
				if field.Type().Elem() == row.Type() {
					field.Set(p)
				}
			}
			_ = typeVal
		}
	}
}

// MorphWith morphTo 预加载按类型附加约束（对应 Laravel morphWith）：
// perType 键为类型串（type 列取值），值为该类型 owner 查询的约束闭包。
func (b *Builder) MorphWith(name string, rel MorphTo, perType map[string]func(*Builder)) *Builder {
	m := map[string][]func(*Builder){}
	for t, c := range perType {
		m[t] = append(m[t], c)
	}
	return b.setPendingRelation(pendingRelation{name: name, rel: rel, morphWith: m})
}

// MorphToWithTrashed morphTo 预加载跳过目标模型软删过滤（对应 morphTo->withTrashed()）。
func (b *Builder) MorphToWithTrashed(name string, rel MorphTo) *Builder {
	return b.setPendingRelation(pendingRelation{name: name, rel: rel, morphTrashed: true})
}

// MorphToWithoutTrashed morphTo 预加载显式排除已软删 owner（对应 morphTo->withoutTrashed()；
// 与默认行为一致，作为显式变体提供）。
func (b *Builder) MorphToWithoutTrashed(name string, rel MorphTo) *Builder {
	return b.setPendingRelation(pendingRelation{name: name, rel: rel, morphTrashMode: "without"})
}

// MorphToOnlyTrashed morphTo 预加载仅取已软删 owner（对应 morphTo->onlyTrashed()）。
func (b *Builder) MorphToOnlyTrashed(name string, rel MorphTo) *Builder {
	return b.setPendingRelation(pendingRelation{name: name, rel: rel, morphTrashMode: "only"})
}

// MorphToConstrain morphTo 预加载全类型通用约束（对应 MorphTo::constrain）：
// 约束闭包应用在每个类型的 owner 查询上（先于 MorphWith 的按类型约束）。
func (b *Builder) MorphToConstrain(name string, rel MorphTo, constraints ...func(*Builder)) *Builder {
	return b.setPendingRelation(pendingRelation{name: name, rel: rel, constrainAll: constraints})
}

// MorphToWithCount morphTo 预加载按类型的计数聚合（对应 MorphTo::morphWithCount）：
// perType 键为类型串，值为该类型 owner 查询要加载的计数规格
// （Laravel 形态 morphWithCount([Post::class => ['comments']])；Go 侧需显式
// 携带关系描述符，见 MorphCountSpec）。
func (b *Builder) MorphToWithCount(name string, rel MorphTo, perType map[string][]MorphCountSpec) *Builder {
	return b.setPendingRelation(pendingRelation{name: name, rel: rel, morphWithCount: perType})
}

// morphStringField 取结构体中指定列的字符串值。
func morphStringField(structVal reflect.Value, col string) string {
	if structVal.Kind() == reflect.Ptr {
		structVal = structVal.Elem()
	}
	if structVal.Kind() != reflect.Struct {
		return ""
	}
	schema, err := NewSchema(structVal.Addr().Interface())
	if err != nil {
		return ""
	}
	for _, f := range schema.Fields {
		if f.Name == col {
			v := f.Value.Interface()
			if s, ok := v.(string); ok {
				return s
			}
			if b, ok := v.([]byte); ok {
				return string(b)
			}
			return ""
		}
	}
	return ""
}

// morphStringsJoin 调试辅助。
func morphStringsJoin(parts []string) string { return strings.Join(parts, ",") }

// MorphToMany 多态多对多（对应 Laravel morphToMany）：
// pivot 表（缺省 = morphName 的复数，如 taggable → taggables）持有
// morphName_type / morphName_id 与目标表外键（目标单数_id）。
type MorphToMany struct {
	RelatedModel    any
	MorphName       string
	MorphKey        string // 类型列取值（缺省 = 父表名）
	Table           string // 缺省 = plural(morphName)
	TypeColumn      string // 缺省 morphName_type
	ForeignPivotKey string // 缺省 morphName_id（本表在 pivot 的键）
	RelatedPivotKey string // 缺省 目标单数_id
	RelatedKey      string // 缺省 id
}

func (r MorphToMany) kind() string     { return "morphToMany" }
func (r MorphToMany) targetModel() any { return r.RelatedModel }

// GetResults 单父查询：经 pivot 表按类型列取多态多对多关联。
func (r MorphToMany) GetResults(parent any, c *Connection, constraints ...func(*Builder)) ([]any, error) {
	parentTable := modelTableName(parent)
	v := keyValue(reflect.ValueOf(parent), "id")
	if v == nil || utilsIsBlankValue(v) {
		return nil, nil
	}
	q := buildMorphToManyQuery(c.GetQueryGrammar(), r, parentTable, "", []any{v})
	rows, err := c.SelectMaps(q.sql, q.bindings)
	if err != nil {
		return nil, err
	}
	return modelAnySlice(rowsToModels(rows, reflect.TypeOf(r.RelatedModel))), nil
}

// MorphedByMany 反向多态多对多（对应 Laravel morphedByMany）：
// 与 morphToMany 共用同一 pivot 表，仅语义方向相反。
type MorphedByMany = MorphToMany

// mtmQuery morphToMany join 查询构造结果。
type mtmQuery struct {
	sql      string
	bindings []any
	pivotCol string
}

// buildMorphToManyQuery 构造多态多对多 join 查询。
func buildMorphToManyQuery(g grammar.Grammar, rel MorphToMany, parentTable, relName string, keys []any) mtmQuery {
	targetTable := relationTargetTable(rel)
	morphName := rel.MorphName
	if morphName == "" {
		morphName = relName
	}
	typeColumn, idColumn := morphColumns(morphName, rel.TypeColumn, "")

	rk := rel.RelatedKey
	if rk == "" {
		rk = "id"
	}
	morphKey := rel.MorphKey
	if morphKey == "" {
		morphKey = parentTable
	}
	fpk := rel.ForeignPivotKey
	if fpk == "" {
		fpk = idColumn
	}
	rpk := rel.RelatedPivotKey
	if rpk == "" {
		rpk = stripPlural(targetTable) + "_id"
	}
	pivot := rel.Table
	if pivot == "" {
		pivot = pluralize(morphName)
	}

	wrappedTarget := g.WrapTable(targetTable)
	wrappedPivot := g.WrapTable(pivot)
	pivotAlias := "pivot_" + fpk
	sql := "select " + wrappedTarget + ".*, " + wrappedPivot + "." + g.Wrap(fpk) +
		" as " + g.Wrap(pivotAlias) +
		" from " + wrappedTarget +
		" inner join " + wrappedPivot + " on " + wrappedTarget + "." + g.Wrap(rk) +
		" = " + wrappedPivot + "." + g.Wrap(rpk) +
		" where " + wrappedPivot + "." + g.Wrap(typeColumn) + " = ?" +
		" and " + wrappedPivot + "." + g.Wrap(fpk) + " in (" +
		strings.Repeat("?, ", len(keys)-1) + "?)"
	bindings := append([]any{morphKey}, keys...)
	return mtmQuery{sql: sql, bindings: bindings, pivotCol: pivotAlias}
}

// loadMorphToMany 多态多对多预加载：
// select t.*, pivot.morph_id as pivot_morph_id from t inner join pivot
//
//	on t.rk = pivot.rp where pivot.type_col = ? and pivot.morph_id in (...)
func (b *Builder) loadMorphToMany(parents reflect.Value, pr pendingRelation, parentTable string) {
	rel, ok := pr.rel.(MorphToMany)
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

	q := buildMorphToManyQuery(b.grammar, rel, parentTable, pr.name, keys)
	rows, err := b.Connection.SelectMaps(q.sql, q.bindings)
	if err != nil {
		return
	}

	targetType := reflect.TypeOf(rel.RelatedModel)
	if targetType.Kind() == reflect.Ptr {
		targetType = targetType.Elem()
	}
	fieldName := utils.StudlyCase(pr.name)
	pivotAlias := q.pivotCol

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
			pv, has := row[pivotAlias]
			if !has || fmt.Sprint(pv) != want {
				continue
			}
			elem := reflect.New(targetType).Elem()
			schema, err := NewSchema(elem.Addr().Interface())
			if err != nil {
				continue
			}
			for _, f := range schema.Fields {
				if rv, ok := row[f.Name]; ok && f.Value.CanSet() {
					f.SetValue(rv)
				}
			}
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

// pluralize 极简英文复数（覆盖 -y → -ies 与缺省 +s；多态表名约定场景足够）。
func pluralize(name string) string {
	if len(name) > 2 && name[len(name)-1] == 'y' {
		vowels := "aeiou"
		if !strings.ContainsRune(vowels, rune(name[len(name)-2])) {
			return name[:len(name)-1] + "ies"
		}
	}
	return name + "s"
}
