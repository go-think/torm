package torm

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/thinkoner/torm/utils"
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

// MorphOne 多态一对一。
type MorphOne struct {
	MorphName   string
	MorphKey    string
	TypeColumn  string
	IDColumn    string
	TargetModel any
}

func (r MorphOne) kind() string     { return "morphOne" }
func (r MorphOne) targetModel() any { return r.TargetModel }

// MorphTo 反向多态：本表持有类型列与 id 列；TypeMap 把类型串映射到模型零值。
type MorphTo struct {
	MorphName  string
	TypeColumn string
	IDColumn   string
	TypeMap    map[string]any
}

func (r MorphTo) kind() string     { return "morphTo" }
func (r MorphTo) targetModel() any { return nil }

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
	for typeVal, bk := range buckets {
		nb := NewBuilder(b.Connection, b.grammar).From(inspectModel(bk.model).table)
		nb.WhereIn("id", bk.keyVals)
		related := reflect.New(reflect.SliceOf(reflect.TypeOf(bk.model).Elem()))
		if err := nb.Get(related.Interface()); err != nil {
			continue
		}

		byID := map[string]reflect.Value{}
		rsv := related.Elem()
		for j := 0; j < rsv.Len(); j++ {
			row := rsv.Index(j)
			v := keyValue(row, "id")
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

// MorphedByMany 反向多态多对多（对应 Laravel morphedByMany）：
// 与 morphToMany 共用同一 pivot 表，仅语义方向相反。
type MorphedByMany = MorphToMany

// loadMorphToMany 多态多对多预加载：
// select t.*, pivot.morph_id as pivot_morph_id from t inner join pivot
//
//	on t.rk = pivot.rp where pivot.type_col = ? and pivot.morph_id in (...)
func (b *Builder) loadMorphToMany(parents reflect.Value, pr pendingRelation, parentTable string) {
	rel, ok := pr.rel.(MorphToMany)
	if !ok {
		return
	}
	targetTable := relationTargetTable(rel)
	morphName := rel.MorphName
	if morphName == "" {
		morphName = pr.name
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

	wrappedTarget := b.grammar.WrapTable(targetTable)
	wrappedPivot := b.grammar.WrapTable(pivot)
	pivotAlias := "pivot_" + fpk
	sql := "select " + wrappedTarget + ".*, " + wrappedPivot + "." + b.grammar.Wrap(fpk) +
		" as " + b.grammar.Wrap(pivotAlias) +
		" from " + wrappedTarget +
		" inner join " + wrappedPivot + " on " + wrappedTarget + "." + b.grammar.Wrap(rk) +
		" = " + wrappedPivot + "." + b.grammar.Wrap(rpk) +
		" where " + wrappedPivot + "." + b.grammar.Wrap(typeColumn) + " = ?" +
		" and " + wrappedPivot + "." + b.grammar.Wrap(fpk) + " in (" +
		strings.Repeat("?, ", len(keys)-1) + "?)"
	bindings := append([]any{morphKey}, keys...)

	rows, err := b.Connection.SelectMaps(sql, bindings)
	if err != nil {
		return
	}

	targetType := reflect.TypeOf(rel.RelatedModel)
	if targetType.Kind() == reflect.Ptr {
		targetType = targetType.Elem()
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
