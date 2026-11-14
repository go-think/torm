package torm

import (
	"errors"
	"reflect"
	"strconv"
)

// Schema model definition
type Schema struct {
	Fields       []*Field
	PrimaryField *Field
	attributes   map[string]interface{}
}

func (s *Schema) SetId(id int64) error {
	for _, f := range s.Fields {
		if f.Primary {
			// 自增回填只对整型主键有意义；字符串主键写十进制形式，
			// 其余类型（含无法安全转换的）显式报错而非静默产出垃圾值
			// （旧实现 int64→string Convert 会写入 NUL 字符）
			switch f.Value.Kind() {
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
				reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				return f.SetValue(id)
			case reflect.String:
				return f.SetValue(strconv.FormatInt(id, 10))
			default:
				return errors.New("torm: auto-increment backfill requires an integer or string primary key, got " + f.Value.Kind().String())
			}
		}
	}
	return nil
}

func (s *Schema) Attributes() map[string]interface{} {
	if s.attributes == nil {
		s.attributes = make(map[string]interface{})
		for _, field := range s.Fields {
			if field.Ignored {
				continue
			}
			// 自增主键为零值时不参与写入（incrementing 约定）
			if field.Primary && field.IsBlank {
				continue
			}
			// nil 指针字段不参与写入
			if field.Value.Kind() == reflect.Ptr && field.Value.IsNil() {
				continue
			}
			// cast:json —— 结构体/切片序列化为字符串
			if cast, ok := field.WriteCast(); ok {
				s.attributes[field.Name] = cast
				continue
			}
			s.attributes[field.Name] = field.Value.Addr().Interface()
		}
	}

	return s.attributes
}

// NewSchema 反射解析模型结构为字段列表：
//   - 跳过未导出字段（旧实现对未导出字段 Addr().Interface() 会 panic）；
//   - 内嵌（anonymous）结构体字段扁平化为其子字段（子字段保留自身 tag），
//     使 field.IDAttr / torm.Model / SoftDeletes 等基座可用；
//   - 带 torm tag 的内嵌字段按原语义视为普通列。
func NewSchema(model interface{}) (*Schema, error) {
	results := reflect.Indirect(reflect.ValueOf(model))

	kind := results.Kind()
	if kind != reflect.Struct {
		return nil, errors.New("unsupported value, should be struct")
	}

	var schema Schema
	collectFields(&schema, results)

	return &schema, nil
}

// collectFields 递归收集（扁平化内嵌结构体）。
func collectFields(schema *Schema, rv reflect.Value) {
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		sf := rt.Field(i)
		fv := rv.Field(i)

		// 未导出字段不能 Addr().Interface()，跳过（导出性判断与 embed 解耦）
		if sf.PkgPath != "" && !sf.Anonymous {
			continue
		}

		if sf.Anonymous && sf.Tag.Get("torm") == "" && sf.Tag.Get("tormstate") == "" {
			// 内嵌结构体且无 torm tag：扁平化。指针型内嵌（*Base）先解引用，
			// nil 指针基座按零值处理（展开其字段而非作为整体列）
			target := fv
			if target.Kind() == reflect.Ptr {
				if target.IsNil() {
					target = reflect.New(target.Type().Elem()).Elem()
				} else {
					target = target.Elem()
				}
			}
			if target.Kind() == reflect.Struct {
				collectFields(schema, target)
				continue
			}
		}

		// 状态挂载字段（tormstate tag）不是列，跳过
		if sf.Tag.Get("tormstate") != "" {
			continue
		}

		field := NewField(fv, sf)
		if schema.PrimaryField == nil && field.Primary {
			schema.PrimaryField = field
		}
		schema.Fields = append(schema.Fields, field)
	}
}
