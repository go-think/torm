package torm

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/thinkoner/torm/grammar"
	"github.com/thinkoner/torm/query"
	"github.com/thinkoner/torm/utils"
)

// Relation 关系定义（对应 Laravel 关系的键约定）。
type Relation interface {
	kind() string
	targetModel() any
}

// BelongsTo 反向一对一：本表持有外键。
type BelongsTo struct {
	ForeignKey string // 本表列（默认 *_id）
	OwnerKey   string // 目标表键（默认 id）
	Model      any    // 目标模型零值
}

func (r BelongsTo) kind() string     { return "belongsTo" }
func (r BelongsTo) targetModel() any { return r.Model }

// HasOne 正向一对一：目标表持有外键。
type HasOne struct {
	ForeignKey string // 目标表列
	LocalKey   string // 本表键（默认 id）
	Model      any
}

func (r HasOne) kind() string     { return "hasOne" }
func (r HasOne) targetModel() any { return r.Model }

// HasMany 一对多。
type HasMany struct {
	ForeignKey string
	LocalKey   string
	Model      any
}

func (r HasMany) kind() string     { return "hasMany" }
func (r HasMany) targetModel() any { return r.Model }

type pendingRelation struct {
	name        string
	rel         Relation
	constraints []func(*Builder)
	limit       *int64
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
// 支持 HasOne/HasMany 形态的关系。
func (b *Builder) WhereHas(rel Relation, constraints func(*Builder)) *Builder {
	return b.addHasWhere(rel, constraints, "and")
}

func (b *Builder) OrWhereHas(rel Relation, constraints func(*Builder)) *Builder {
	return b.addHasWhere(rel, constraints, "or")
}

func (b *Builder) addHasWhere(rel Relation, constraints func(*Builder), boolean string) *Builder {
	sub := b.hasSubQuery(rel)
	if constraints != nil {
		constraints(sub)
	}
	// 子查询绑定并入父查询（对齐 Laravel whereExists）
	b.AddBinding(sub.GetRawBindings()["where"], "where")
	b.Query.Wheres = append(b.Query.Wheres, &query.Where{
		Type:    "Exists",
		Query:   sub.Query,
		Boolean: boolean,
	})
	return b
}

// hasSubQuery 构造关系存在性子查询。
func (b *Builder) hasSubQuery(rel Relation) *Builder {
	table := relationTargetTable(rel)
	sub := NewBuilder(b.Connection, b.grammar)
	sub.From(table)

	parentTable := fmtString(b.Query.From)
	fk, parentKey := relationKeysForHas(rel, parentTable)
	sub.WhereColumn(parentTable+"."+parentKey, "=", table+"."+fk, "and")
	return sub
}

// relationKeysForHas WhereHas 用：HasOne/HasMany 形态（目标表持有外键）。
func relationKeysForHas(rel Relation, parentTable string) (string, string) {
	fk, lk := relationKeys(rel, parentTable)
	return fk, lk
}

// WithCount 关系计数子查询：select (select count(*) ...) as "name_count"。
func (b *Builder) WithCount(name string, rel Relation) *Builder {
	table := relationTargetTable(rel)
	fk, parentKey := relationKeys(rel, fmtString(b.Query.From))
	sql := "(select count(*) from " + b.grammar.WrapTable(table) +
		" where " + b.grammar.Wrap(table+"."+fk) +
		" = " + b.grammar.Wrap(fmtString(b.Query.From)+"."+parentKey) + ") as " +
		b.grammar.Wrap(name+"_count")
	b.Query.Columns = append(b.Query.Columns, grammar.NewExpression(sql))
	return b
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
}

func (r BelongsToMany) kind() string     { return "belongsToMany" }
func (r BelongsToMany) targetModel() any { return r.RelatedModel }

// loadBelongsToMany 经 pivot 表预加载。
func (b *Builder) loadBelongsToMany(parents reflect.Value, pr pendingRelation, parentTable string) {
	rel, ok := pr.rel.(BelongsToMany)
	if !ok {
		return
	}
	targetTable := relationTargetTable(rel)
	rk := rel.RelatedKey
	if rk == "" {
		rk = "id"
	}
	fpk := rel.ForeignPivotKey
	if fpk == "" {
		fpk = stripPlural(parentTable) + "_id"
	}
	rpk := rel.RelatedPivotKey
	if rpk == "" {
		rpk = stripPlural(targetTable) + "_id"
	}
	pivot := rel.Table
	if pivot == "" {
		a, c := stripPlural(parentTable), stripPlural(targetTable)
		if a > c {
			a, c = c, a
		}
		pivot = a + "_" + c
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

	// select t.*, p.foreign as pivot_fk from t inner join pivot p on t.rk = p.rp where p.fp in (...)
	wrappedTarget := b.grammar.WrapTable(targetTable)
	wrappedPivot := b.grammar.WrapTable(pivot)
	sql := "select " + wrappedTarget + ".*, " + wrappedPivot + "." + b.grammar.Wrap(fpk) +
		" as " + b.grammar.Wrap("pivot_"+fpk) +
		" from " + wrappedTarget +
		" inner join " + wrappedPivot + " on " + wrappedTarget + "." + b.grammar.Wrap(rk) +
		" = " + wrappedPivot + "." + b.grammar.Wrap(rpk) +
		" where " + wrappedPivot + "." + b.grammar.Wrap(fpk) + " in (" +
		strings.Repeat("?, ", len(keys)-1) + "?)"
	bindings := append([]any{}, keys...)

	rows, err := b.Connection.SelectMaps(sql, bindings)
	if err != nil {
		return
	}

	// 按父键分组并填充目标结构体
	targetType := reflect.TypeOf(rel.RelatedModel)
	if targetType.Kind() == reflect.Ptr {
		targetType = targetType.Elem()
	}
	fieldName := utils.StudlyCase(pr.name)
	pivotCol := "pivot_" + fpk

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
}

func (r HasManyThrough) kind() string     { return "hasManyThrough" }
func (r HasManyThrough) targetModel() any { return r.TargetModel }

// loadHasManyThrough 远程一对多预加载：
// select t.*, through.lk as laravel_through_key from t inner join through on t.second_key = through.second_local_key
// where through.first_key in (父键集合)
func (b *Builder) loadHasManyThrough(parents reflect.Value, pr pendingRelation, parentTable string) {
	rel, ok := pr.rel.(HasManyThrough)
	if !ok {
		return
	}
	targetTable := relationTargetTable(rel)
	throughTable := inspectModel(rel.ThroughModel).table

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

	wrappedTarget := b.grammar.WrapTable(targetTable)
	wrappedThrough := b.grammar.WrapTable(throughTable)
	throughKeyAlias := "laravel_through_key"

	sql := "select " + wrappedTarget + ".*, " + wrappedThrough + "." + b.grammar.Wrap(firstKey) +
		" as " + b.grammar.Wrap(throughKeyAlias) +
		" from " + wrappedTarget +
		" inner join " + wrappedThrough + " on " + wrappedThrough + "." + b.grammar.Wrap(slk) +
		" = " + wrappedTarget + "." + b.grammar.Wrap(secondKey) +
		" where " + wrappedThrough + "." + b.grammar.Wrap(firstKey) + " in (" +
		strings.Repeat("?, ", len(keys)-1) + "?)"
	bindings := append([]any{}, keys...)

	rows, err := b.Connection.SelectMaps(sql, bindings)
	if err != nil {
		return
	}

	targetType := reflect.TypeOf(rel.TargetModel)
	if targetType.Kind() == reflect.Ptr {
		targetType = targetType.Elem()
	}
	fieldName := utils.StudlyCase(pr.name)

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
