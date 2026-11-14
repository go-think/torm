package torm

import (
	"encoding/json"
	"reflect"

	"github.com/go-think/torm/grammar"
)

// ---- 模型序列化（toArray/toJson + hidden/visible 属性控制）----
//
// torm 模型是普通结构体，序列化按"数据库列视角"输出：以 Attributes() 的
// 列集合为基础（cast 字段输出写入侧表示、json cast 输出解码后的对象），
// hidden/visible/MakeHidden/MakeVisible 在此路径真正生效（此前注册表无消费方）。
// 支持 map[string]any 字段按 json tag / 字段名输出子键。

// SerializeOptions 控制单次序列化的隐藏/可见列（实例级语义——类型级
// MakeHidden/MakeVisible 之外的一次性覆盖，临时 makeHidden 形态）。
type SerializeOptions struct {
	Hidden  []string // 追加隐藏的列
	Visible []string // 仅输出的列（非空时优先于 hidden）
}

// WithHidden 追加隐藏列的选项（makeHidden 的临时形态）。
func WithHidden(keys ...string) SerializeOptions { return SerializeOptions{Hidden: keys} }

// WithVisible 仅输出指定列（makeVisible / visible 的临时形态）。
func WithVisible(keys ...string) SerializeOptions { return SerializeOptions{Visible: keys} }

// ToMap 把模型序列化为 map：
// hidden（tag `torm:"hidden"` + MakeHidden + options.Hidden）列被剔除；
// options.Visible 非空时仅输出其交集；json cast 字段输出解码后的值。
func ToMap(model any, opts ...SerializeOptions) (map[string]any, error) {
	schema, err := NewSchema(model)
	if err != nil {
		return nil, err
	}
	meta := metaFor(model)

	hidden := map[string]bool{}
	for _, h := range meta.hidden {
		hidden[h] = true
	}
	visible := map[string]bool{}
	onlyVisible := false
	for _, o := range opts {
		for _, h := range o.Hidden {
			hidden[h] = true
		}
		if len(o.Visible) > 0 {
			onlyVisible = true
			for _, v := range o.Visible {
				visible[v] = true
			}
		}
	}

	out := make(map[string]any, len(schema.Fields))
	for _, f := range schema.Fields {
		// 过滤顺序：Ignored/hidden/visible 优先于 nil 指针（否则非列字段与
		// 被隐藏列会以 null 泄露进输出）
		if f.Ignored {
			continue
		}
		if hidden[f.Name] {
			continue
		}
		if onlyVisible && !visible[f.Name] {
			continue
		}
		if f.Value.Kind() == reflect.Ptr && f.Value.IsNil() {
			out[f.Name] = nil
			continue
		}
		if cast, ok := f.WriteCast(); ok {
			// json cast 输出解码后的对象（对齐 array cast 语义）
			if f.CastName == "json" {
				var decoded any
				if s, isStr := cast.(string); isStr && json.Unmarshal([]byte(s), &decoded) == nil {
					out[f.Name] = decoded
					continue
				}
			}
			out[f.Name] = cast
			continue
		}
		out[f.Name] = snapshotFieldValue(f)
	}
	return out, nil
}

// ToJson 把模型序列化为 JSON（hidden 语义同 ToMap）。
func ToJson(model any, opts ...SerializeOptions) (string, error) {
	m, err := ToMap(model, opts...)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// MapsToSlice 把模型切片/数组序列化为 []map。
func MapsToSlice(slice any, opts ...SerializeOptions) ([]map[string]any, error) {
	rv := reflect.Indirect(reflect.ValueOf(slice))
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, grammar.ErrUnsupported{Msg: "torm: MapsToSlice requires a slice or array"}
	}
	out := make([]map[string]any, 0, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		elem := rv.Index(i)
		if elem.Kind() == reflect.Ptr {
			if elem.IsNil() {
				out = append(out, nil)
				continue
			}
			elem = elem.Elem()
		}
		if elem.Kind() != reflect.Struct {
			return nil, grammar.ErrUnsupported{Msg: "torm: MapsToSlice requires struct elements"}
		}
		m, err := ToMap(elem.Addr().Interface(), opts...)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// SliceToJson 把模型切片序列化为 JSON 数组。
func SliceToJson(slice any, opts ...SerializeOptions) (string, error) {
	maps, err := MapsToSlice(slice, opts...)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(maps)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ---- Replicate（模型复制 + replicating 事件）----

// Replicate 把已持久化模型复制为未持久化新实例写入 dst：
// 主键与时间戳清零、状态清除（对齐 replicate 语义：excluding
// $primaryKey / createdAt / updatedAt）；replicating 观察者在副本上触发
// （可修改副本、返回 error 取消）。
func (c *Connection) Replicate(dst, src any) error {
	sv := reflect.ValueOf(src)
	if sv.Kind() != reflect.Ptr || sv.IsNil() || reflect.Indirect(sv).Kind() != reflect.Struct {
		return grammar.ErrUnsupported{Msg: "torm: Replicate requires struct pointers"}
	}
	dv := reflect.ValueOf(dst)
	if dv.Kind() != reflect.Ptr || dv.IsNil() || reflect.Indirect(dv).Kind() != reflect.Struct {
		return grammar.ErrUnsupported{Msg: "torm: Replicate requires struct pointers"}
	}
	if reflect.Indirect(dv).Type() != reflect.Indirect(sv).Type() {
		return grammar.ErrUnsupported{Msg: "torm: Replicate requires same model types"}
	}
	info := inspectModel(src)

	// 整体复制后清空主键/时间戳与状态（副本未持久化，exists=false）
	reflect.Indirect(dv).Set(reflect.Indirect(sv))
	resetModelState(dst)
	if err := clearModelFields(dst, info.primaryKey, info.createdAtField, info.updatedAtField); err != nil {
		return err
	}

	return c.fireModelEvent(observerFor(dst), EventReplicating, dst)
}

// clearModelFields 把模型指定列置零（nil 指针/零值）。
func clearModelFields(model any, names ...string) error {
	schema, err := NewSchema(model)
	if err != nil {
		return err
	}
	for _, name := range names {
		if name == "" {
			continue
		}
		f := schemaField(schema, name)
		if f == nil {
			continue
		}
		if err := f.SetValue(reflect.Value{}); err != nil {
			return err
		}
	}
	return nil
}
