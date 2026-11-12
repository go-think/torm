package torm

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/thinkoner/torm/utils"
)

// cast 常量：Laravel 支持的常见 cast 名（本次扩展，json 为既有行为）。
const (
	castDate     = "date"     // time.Time ↔ '2006-01-02'
	castDatetime = "datetime" // time.Time ↔ '2006-01-02 15:04:05'
	castDecimal  = "decimal"  // 数字/字符串 ↔ 定点字符串（cast:decimal:N）
	castBool     = "bool"
	castInt      = "int"   // 兼容 integer
	castFloat    = "float" // 兼容 double/real
)

// WriteCast 写入前转换：把字段 Go 值转为数据库绑定值。
//
// cast:json / cast:array —— 结构体/切片/映射序列化为字符串；空值对齐 Laravel：
// nil/零值集合写 "[]"（PHP 空数组语义），零值结构体写 null。
// cast:date / cast:datetime —— time.Time 按方言格式序列化。
// cast:decimal:N —— 数字/字符串规范化为 N 位定点字符串。
// cast:bool / cast:int / cast:float —— 规范化为对应标量。
func (f *Field) WriteCast() (any, bool) {
	switch f.CastName {
	case "json":
		return f.writeCastJSON()
	case castDate, castDatetime:
		v, ok := f.castDeref()
		if !ok {
			return nil, false
		}
		t, ok := v.Interface().(time.Time)
		if !ok {
			return nil, false
		}
		if f.CastName == castDate {
			return t.Format("2006-01-02"), true
		}
		return t.Format("2006-01-02 15:04:05"), true
	case castDecimal:
		return f.writeCastDecimal()
	case castBool:
		return f.writeCastBool()
	case castInt:
		return f.writeCastInt()
	case castFloat:
		return f.writeCastFloat()
	default:
		return nil, false
	}
}

// writeCastJSON json/array cast 写入：零值集合写 "[]"，零值结构体写 null
// （对齐 Laravel 空值语义，修正"零值跳过写入"偏差）。
func (f *Field) writeCastJSON() (any, bool) {
	switch f.Value.Kind() {
	case reflect.Slice, reflect.Map:
		if f.Value.IsZero() {
			return "[]", true
		}
	case reflect.Struct:
		if f.Value.IsZero() {
			return nil, true
		}
	default:
		return nil, false
	}
	data, err := json.Marshal(f.Value.Addr().Interface())
	if err != nil {
		return nil, false
	}
	return string(data), true
}

// writeCastDecimal decimal cast 写入：解析精度（decimal:N 或 decimal:N,P 取 P，
// 默认 2），规范化为定点字符串。
func (f *Field) writeCastDecimal() (any, bool) {
	v, ok := f.castDeref()
	if !ok {
		return nil, false
	}
	prec := 2
	if arg := strings.TrimSpace(f.CastArg); arg != "" {
		p := arg
		if i := strings.Index(arg, ","); i >= 0 {
			p = arg[i+1:]
		}
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil && n >= 0 {
			prec = n
		}
	}
	switch x := v.Interface().(type) {
	case string:
		if flt, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
			return strconv.FormatFloat(flt, 'f', prec, 64), true
		}
		return x, true
	case float32:
		return strconv.FormatFloat(float64(x), 'f', prec, 64), true
	case float64:
		return strconv.FormatFloat(x, 'f', prec, 64), true
	default:
		if v.CanInt() {
			return strconv.FormatFloat(float64(v.Int()), 'f', prec, 64), true
		}
		if v.CanUint() {
			return strconv.FormatFloat(float64(v.Uint()), 'f', prec, 64), true
		}
	}
	return nil, false
}

func (f *Field) writeCastBool() (any, bool) {
	v, ok := f.castDeref()
	if !ok {
		return nil, false
	}
	switch x := v.Interface().(type) {
	case bool:
		return x, true
	case int64:
		return x != 0, true
	case float64:
		return x != 0, true
	case string:
		b, err := strconv.ParseBool(strings.TrimSpace(x))
		if err != nil {
			return nil, false
		}
		return b, true
	default:
		return nil, false
	}
}

func (f *Field) writeCastInt() (any, bool) {
	v, ok := f.castDeref()
	if !ok {
		return nil, false
	}
	switch {
	case v.CanInt():
		return v.Int(), true
	case v.CanUint():
		return int64(v.Uint()), true
	case v.Kind() == reflect.Float32, v.Kind() == reflect.Float64:
		return int64(v.Float()), true
	case v.Kind() == reflect.String:
		n, err := strconv.ParseInt(strings.TrimSpace(v.String()), 10, 64)
		if err != nil {
			return nil, false
		}
		return n, true
	default:
		return nil, false
	}
}

func (f *Field) writeCastFloat() (any, bool) {
	v, ok := f.castDeref()
	if !ok {
		return nil, false
	}
	switch {
	case v.CanInt():
		return float64(v.Int()), true
	case v.CanUint():
		return float64(v.Uint()), true
	case v.Kind() == reflect.Float32, v.Kind() == reflect.Float64:
		return v.Float(), true
	case v.Kind() == reflect.String:
		n, err := strconv.ParseFloat(strings.TrimSpace(v.String()), 64)
		if err != nil {
			return nil, false
		}
		return n, true
	default:
		return nil, false
	}
}

// castDeref 返回解引用后的字段值（nil 指针不可转换）。
func (f *Field) castDeref() (reflect.Value, bool) {
	v := f.Value
	if v.Kind() == reflect.Ptr {
		if v.IsNil() || !v.Elem().IsValid() {
			return reflect.Value{}, false
		}
		v = v.Elem()
	}
	return v, true
}

// ReadCast 读取后转换：把扫描结果规范化为字段语义类型。
//
// json cast 把字符串反序列化进结构体/切片（connection 层 holder 已完成时为 no-op）；
// date cast 把 time.Time 截断到当天零点（对应 Laravel asDate）；
// decimal cast 把字符串值规范化到指定精度。
// 读侧要求驱动返回时间值（MySQL parseTime=true 惯例），字符串列无法在扫描后补救。
func (f *Field) ReadCast() error {
	switch f.CastName {
	case "json":
		switch f.Value.Kind() {
		case reflect.Struct, reflect.Slice, reflect.Map:
		default:
			return nil
		}
		var raw []byte
		switch v := f.Value.Interface().(type) {
		case string:
			raw = []byte(v)
		case []byte:
			raw = v
		default:
			return nil
		}
		if len(raw) == 0 {
			return nil
		}
		return json.Unmarshal(raw, f.Value.Addr().Interface())
	case castDate:
		t, ok := f.readTime()
		if !ok {
			return nil
		}
		f.setTimeValue(time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()))
		return nil
	case castDecimal:
		if f.Value.Kind() == reflect.Ptr {
			return nil
		}
		if f.Value.Kind() == reflect.String {
			if out, ok := f.writeCastDecimal(); ok {
				f.Value.SetString(out.(string))
			}
		}
		return nil
	default:
		return nil
	}
}

// readTime 取字段时间值（支持 time.Time / *time.Time）。
func (f *Field) readTime() (time.Time, bool) {
	v, ok := f.castDeref()
	if !ok {
		return time.Time{}, false
	}
	t, ok := v.Interface().(time.Time)
	return t, ok
}

// setTimeValue 把时间写回字段（指针字段自动分配）。
func (f *Field) setTimeValue(t time.Time) {
	v := f.Value
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			p := reflect.New(v.Type().Elem())
			v.Set(p)
		}
		v.Elem().Set(reflect.ValueOf(t).Convert(v.Type().Elem()))
		return
	}
	v.Set(reflect.ValueOf(t).Convert(v.Type()))
}

// Field The model field definition
type Field struct {
	Value       reflect.Value
	StructField reflect.StructField
	Attrs       map[string]string
	Primary     bool
	Name        string
	Ignored     bool
	IsBlank     bool
	Cast        string // 原始 cast 标签值（cast:json 等）；cast:array 内部归一为 "json"
	CastName    string // cast 基名：json/date/datetime/decimal/bool/int/float/uuid/ulid
	CastArg     string // cast 参数（decimal:N 的 N 等）
	UniqueID    string // unique_id 生成类型："uuid" / "ulid"（空 = 不生成）
}

// GetParams Get the attr of tag
func (f *Field) GetAttr(key string) (string, bool) {
	key = strings.ToUpper(key)
	val, ok := f.Attrs[key]

	return val, ok
}

// Set set a value to the field
func (f *Field) SetValue(value interface{}) (err error) {

	//if !f.Value.CanAddr() {
	//
	//}

	reflectValue, ok := value.(reflect.Value)
	if !ok {
		reflectValue = reflect.ValueOf(value)
	}

	fieldValue := f.Value
	if reflectValue.IsValid() {
		if reflectValue.Type().ConvertibleTo(fieldValue.Type()) {
			fieldValue.Set(reflectValue.Convert(fieldValue.Type()))
		} else {
			if fieldValue.Kind() == reflect.Ptr {
				if fieldValue.IsNil() {
					fieldValue.Set(reflect.New(f.StructField.Type.Elem()))
				}
				fieldValue = fieldValue.Elem()
			}

			if reflectValue.Type().ConvertibleTo(fieldValue.Type()) {
				fieldValue.Set(reflectValue.Convert(fieldValue.Type()))
			} else if scanner, ok := fieldValue.Addr().Interface().(sql.Scanner); ok {
				v := reflectValue.Interface()
				if valuer, ok := v.(driver.Valuer); ok {
					if v, err = valuer.Value(); err == nil {
						err = scanner.Scan(v)
					}
				} else {
					err = scanner.Scan(v)
				}
			} else {
				err = fmt.Errorf("could not convert argument of field  from %s to %s", reflectValue.Type(), fieldValue.Type())
			}
		}
	} else {
		f.Value.Set(reflect.Zero(f.Value.Type()))
	}

	f.IsBlank = utils.IsBlank(f.Value)

	return err
}

func (f *Field) initialize() {
	tag := f.StructField.Tag.Get("torm")

	for _, kv := range strings.Split(tag, ";") {
		v := strings.Split(kv, ":")
		k := strings.TrimSpace(strings.ToUpper(v[0]))
		if len(v) >= 2 {
			f.Attrs[k] = strings.Join(v[1:], ":")
		} else {
			f.Attrs[k] = ""
		}
		switch k {
		case "-":
			f.Ignored = true
		case "PRIMARY_KEY":
			f.Primary = true
		case "COLUMN":
			f.Name = f.Attrs[k]
		case "CAST":
			raw := strings.ToLower(f.Attrs[k])
			name, arg := raw, ""
			if i := strings.Index(raw, ":"); i >= 0 {
				name, arg = raw[:i], raw[i+1:]
			}
			switch name {
			case "array", "json":
				// cast:array 是 cast:json 的集合别名，复用同一编解码路径
				// （connection 层扫描按 f.Cast=="json" 走字符串 holder）。
				name, f.Cast = "json", "json"
			default:
				f.Cast = raw
			}
			f.CastName, f.CastArg = name, arg
			if name == "uuid" || name == "ulid" {
				f.UniqueID = name
			}
		case "UNIQUE_ID":
			v := strings.ToLower(f.Attrs[k])
			if v == "" {
				v = "uuid"
			}
			f.UniqueID = v
		}
	}

	if f.Name == "" || f.Name == "-" {
		f.Name = utils.SnakeCase(f.StructField.Name)
	}

	f.IsBlank = utils.IsBlank(f.Value)
}

func NewField(value reflect.Value, structField reflect.StructField) *Field {
	field := Field{
		Value:       value,
		StructField: structField,
		Attrs:       map[string]string{},
	}
	field.initialize()

	return &field
}
