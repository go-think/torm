package torm

import (
	"reflect"

	"github.com/thinkoner/torm/utils"
)

// ---- 集合层（对应 Illuminate\Support/Collection 与 Eloquent Collection 的常用子集）----

// Collection 泛型集合。
type Collection[T any] []T

// Col 把切片包装为集合。
func Col[T any](items []T) Collection[T] { return items }

// Slice 返回底层切片。
func (c Collection[T]) Slice() []T { return c }

// Each 遍历；回调返回 false 停止（对应 Laravel each 的 break 语义）。
func (c Collection[T]) Each(fn func(item T, index int) bool) {
	for i, item := range c {
		if !fn(item, i) {
			return
		}
	}
}

// Filter 过滤（对应 Laravel filter）。
func (c Collection[T]) Filter(pred func(T) bool) Collection[T] {
	out := make(Collection[T], 0, len(c))
	for _, item := range c {
		if pred(item) {
			out = append(out, item)
		}
	}
	return out
}

// Map 映射为新切片（对应 Laravel map）。
func (c Collection[T]) Map[U any](fn func(T) U) []U {
	out := make([]U, 0, len(c))
	for _, item := range c {
		out = append(out, fn(item))
	}
	return out
}

// First 返回首个满足条件的元素（pred 为 nil 时取第一个）。
func (c Collection[T]) First(pred func(T) bool) (T, bool) {
	var zero T
	for _, item := range c {
		if pred == nil || pred(item) {
			return item, true
		}
	}
	return zero, false
}

// Contains 是否存在满足条件的元素。
func (c Collection[T]) Contains(pred func(T) bool) bool {
	_, ok := c.First(pred)
	return ok
}

// Pluck 按字段名（torm 列名或 Go 字段名）提取值集合。
func (c Collection[T]) Pluck(field string) []any {
	out := make([]any, 0, len(c))
	for _, item := range c {
		if v, ok := pluckValue(reflect.ValueOf(item), field); ok {
			out = append(out, v)
		}
	}
	return out
}

// ModelKeys 提取主键集合（对应 Laravel Eloquent Collection::modelKeys）。
func (c Collection[T]) ModelKeys() []any {
	out := make([]any, 0, len(c))
	for _, item := range c {
		rv := reflect.ValueOf(item)
		if rv.Kind() == reflect.Ptr {
			rv = rv.Elem()
		}
		if rv.Kind() != reflect.Struct {
			continue
		}
		if !rv.CanAddr() {
			cp := reflect.New(rv.Type())
			cp.Elem().Set(rv)
			rv = cp.Elem()
		}
		schema, err := NewSchema(rv.Addr().Interface())
		if err != nil {
			continue
		}
		for _, f := range schema.Fields {
			if f.Primary {
				out = append(out, f.Value.Interface())
				break
			}
		}
	}
	return out
}

func pluckValue(rv reflect.Value, field string) (any, bool) {
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil, false
	}
	if !rv.CanAddr() {
		cp := reflect.New(rv.Type())
		cp.Elem().Set(rv)
		rv = cp.Elem()
	}
	// 先按 Go 字段名
	fv := rv.FieldByName(field)
	if fv.IsValid() {
		return fv.Interface(), true
	}
	// 再按 torm 列名
	schema, err := NewSchema(rv.Addr().Interface())
	if err != nil {
		return nil, false
	}
	for _, f := range schema.Fields {
		if f.Name == field {
			return f.Value.Interface(), true
		}
	}
	return nil, false
}

// Studly 导出常用命名转换（集合/关系场景使用）。
func StudlyCase(s string) string { return utils.StudlyCase(s) }
