package torm

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/thinkoner/torm/utils"
)

// ---- 集合层（对应 Illuminate\Support/Collection 与 Eloquent Collection）----
// 方法名与语义逐条对照 Illuminate/Collections/Collection.php 与
// Eloquent/Collection.php（Laravel 13）。

// Collection 泛型集合。
type Collection[T any] []T

// Col 把切片包装为集合。
func Col[T any](items []T) Collection[T] { return items }

// Slice 返回底层切片。
func (c Collection[T]) Slice() []T { return c }

// Len 元素个数（对应 Laravel count）。
func (c Collection[T]) Len() int { return len(c) }

// IsEmpty / IsNotEmpty（对应 Laravel 同名方法）。
func (c Collection[T]) IsEmpty() bool    { return len(c) == 0 }
func (c Collection[T]) IsNotEmpty() bool { return len(c) > 0 }

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

// FilterBoth 带索引的过滤（对应 Laravel filter($value, $key) 双参形态）。
func (c Collection[T]) FilterBoth(pred func(item T, index int) bool) Collection[T] {
	out := make(Collection[T], 0, len(c))
	for i, item := range c {
		if pred(item, i) {
			out = append(out, item)
		}
	}
	return out
}

// Reject 过滤的逆操作（对应 Laravel reject）。
func (c Collection[T]) Reject(pred func(T) bool) Collection[T] {
	return c.Filter(func(item T) bool { return !pred(item) })
}

// Map 映射为新切片（对应 Laravel map）。
func (c Collection[T]) Map[U any](fn func(T) U) []U {
	out := make([]U, 0, len(c))
	for _, item := range c {
		out = append(out, fn(item))
	}
	return out
}

// MapWithKeys 映射为 map（对应 Laravel mapWithKeys：回调返回 (key, value)）。
func (c Collection[T]) MapWithKeys[K comparable, V any](fn func(T) (K, V)) map[K]V {
	out := make(map[K]V, len(c))
	for _, item := range c {
		k, v := fn(item)
		out[k] = v
	}
	return out
}

// Reduce 折叠（对应 Laravel reduce）。
func (c Collection[T]) Reduce[U any](fn func(acc U, item T) U, initial U) U {
	acc := initial
	for _, item := range c {
		acc = fn(acc, item)
	}
	return acc
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

// FirstWhere 按字段条件取首个元素（对应 Laravel firstWhere）：
//
//	FirstWhere("name", "A")            // 等于
//	FirstWhere("age", ">", 18)         // 运算符形态（= != <> < <= > >=）
func (c Collection[T]) FirstWhere(field string, args ...any) (T, bool) {
	match := whereMatcher[T](field, args...)
	return c.First(match)
}

// Find 按主键值查找（对应 Eloquent Collection::find）；Go 侧按 pluck 字段比对。
func (c Collection[T]) Find(id any) (T, bool) {
	return c.FirstWhere("id", id)
}

// FindOrFail 按主键查找，找不到返回错误（对应 Eloquent findOrFail）。
func (c Collection[T]) FindOrFail(id any) (T, error) {
	var zero T
	item, ok := c.Find(id)
	if !ok {
		return zero, fmt.Errorf("torm: collection item with id %v not found", id)
	}
	return item, nil
}

// Contains 是否存在满足条件的元素（谓词形态）。
func (c Collection[T]) Contains(pred func(T) bool) bool {
	_, ok := c.First(pred)
	return ok
}

// ContainsWhere 字段条件形态的 contains（对应 Laravel contains('field', 'value')
// 与 contains('field', '>', value)）。
func (c Collection[T]) ContainsWhere(field string, args ...any) bool {
	_, ok := c.FirstWhere(field, args...)
	return ok
}

// ContainsValue 精确值形态（对应 Laravel contains($item)，DeepEqual 比较）。
func (c Collection[T]) ContainsValue(want T) bool {
	for _, item := range c {
		if reflect.DeepEqual(item, want) {
			return true
		}
	}
	return false
}

// DoesntContain contains 的否定形态（对应 Laravel doesntContain）。
func (c Collection[T]) DoesntContain(pred func(T) bool) bool {
	return !c.Contains(pred)
}

// DoesntContainWhere 字段条件的否定形态。
func (c Collection[T]) DoesntContainWhere(field string, args ...any) bool {
	return !c.ContainsWhere(field, args...)
}

// Merge 追加合并（对应 Laravel merge）。
func (c Collection[T]) Merge(other Collection[T]) Collection[T] {
	out := make(Collection[T], 0, len(c)+len(other))
	out = append(out, c...)
	out = append(out, other...)
	return out
}

// Union 合并去重（保留首次出现，对应 Laravel union；Go 侧按 Sprint 键判定重复）。
func (c Collection[T]) Union(other Collection[T]) Collection[T] {
	return c.Merge(other).Unique()
}

// Unique 去重（保留首次出现；对应 Laravel unique）。
func (c Collection[T]) Unique() Collection[T] {
	return c.UniqueBy(func(item T) any { return item })
}

// UniqueBy 按键函数去重（对应 Laravel unique($key)）。
func (c Collection[T]) UniqueBy(key func(T) any) Collection[T] {
	out := make(Collection[T], 0, len(c))
	seen := map[string]bool{}
	for _, item := range c {
		k := fmt.Sprint(key(item))
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, item)
	}
	return out
}

// Diff 差集：本集合有、other 没有的元素（对应 Laravel diff）。
func (c Collection[T]) Diff(other Collection[T]) Collection[T] {
	otherKeys := map[string]bool{}
	for _, item := range other {
		otherKeys[fmt.Sprint(item)] = true
	}
	out := make(Collection[T], 0, len(c))
	for _, item := range c {
		if !otherKeys[fmt.Sprint(item)] {
			out = append(out, item)
		}
	}
	return out
}

// Intersect 交集：两边都有的元素，保留本集合顺序（对应 Laravel intersect）。
func (c Collection[T]) Intersect(other Collection[T]) Collection[T] {
	otherKeys := map[string]bool{}
	for _, item := range other {
		otherKeys[fmt.Sprint(item)] = true
	}
	out := make(Collection[T], 0, len(c))
	seen := map[string]bool{}
	for _, item := range c {
		k := fmt.Sprint(item)
		if otherKeys[k] && !seen[k] {
			seen[k] = true
			out = append(out, item)
		}
	}
	return out
}

// Only 取每项的指定字段投影为 map（对应 Laravel only；结构体集合上按
// Go 字段名/torm 列名提取）。
func (c Collection[T]) Only(keys ...string) []map[string]any {
	out := make([]map[string]any, 0, len(c))
	for _, item := range c {
		m := map[string]any{}
		for _, k := range keys {
			if v, ok := pluckValue(reflect.ValueOf(item), k); ok {
				m[k] = v
			}
		}
		out = append(out, m)
	}
	return out
}

// Except 排除指定字段后的投影（对应 Laravel except）。
func (c Collection[T]) Except(keys ...string) []map[string]any {
	skip := map[string]bool{}
	for _, k := range keys {
		skip[k] = true
	}
	out := make([]map[string]any, 0, len(c))
	for _, item := range c {
		m := map[string]any{}
		for k, v := range structAttributes(item) {
			if !skip[k] {
				m[k] = v
			}
		}
		out = append(out, m)
	}
	return out
}

// Partition 按谓词一分为二：(满足, 不满足)（对应 Laravel partition）。
func (c Collection[T]) Partition(pred func(T) bool) (Collection[T], Collection[T]) {
	passed := make(Collection[T], 0, len(c))
	rest := make(Collection[T], 0, len(c))
	for _, item := range c {
		if pred(item) {
			passed = append(passed, item)
		} else {
			rest = append(rest, item)
		}
	}
	return passed, rest
}

// Zip 与另一切片按位配对，长度取较小者（对应 Laravel zip；Go 侧不产生
// null 占位，多余元素丢弃）。
func (c Collection[T]) Zip[U any](other []U) []ZipPair[T, U] {
	n := len(c)
	if len(other) < n {
		n = len(other)
	}
	out := make([]ZipPair[T, U], 0, n)
	for i := 0; i < n; i++ {
		out = append(out, ZipPair[T, U]{First: c[i], Second: other[i]})
	}
	return out
}

// ZipPair zip 配对结果。
type ZipPair[T any, U any] struct {
	First  T
	Second U
}

// Tap 副作用钩子：调用 fn(c) 后原样返回集合（对应 Laravel tap）。
func (c Collection[T]) Tap(fn func(Collection[T])) Collection[T] {
	fn(c)
	return c
}

// When 条件为真时应用变换（对应 Laravel when）。
func (c Collection[T]) When(cond bool, fn func(Collection[T]) Collection[T]) Collection[T] {
	if cond {
		return fn(c)
	}
	return c
}

// Unless 条件为假时应用变换（对应 Laravel unless）。
func (c Collection[T]) Unless(cond bool, fn func(Collection[T]) Collection[T]) Collection[T] {
	if !cond {
		return fn(c)
	}
	return c
}

// Flatten 展开嵌套切片（对应 Laravel flatten；depth 缺省 1 层，
// 元素展开为 any）。
func (c Collection[T]) Flatten(depth ...int) Collection[any] {
	d := 1
	if len(depth) > 0 {
		d = depth[0]
	}
	out := make(Collection[any], 0, len(c))
	for _, item := range c {
		flattenInto(reflect.ValueOf(item), d, &out)
	}
	return out
}

func flattenInto(rv reflect.Value, depth int, out *Collection[any]) {
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if depth > 0 && rv.Kind() == reflect.Slice || depth > 0 && rv.Kind() == reflect.Array {
		for i := 0; i < rv.Len(); i++ {
			flattenInto(rv.Index(i), depth-1, out)
		}
		return
	}
	if rv.IsValid() {
		*out = append(*out, rv.Interface())
	}
}

// SliceRange 子切片（对应 Laravel slice($offset, $length)）：offset 起始，
// length 为个数，负值 length 表示取到末尾（Laravel null 语义）。
// 与 Slice()（返回底层切片）并存，取 Laravel 语义名加 Range 后缀。
func (c Collection[T]) SliceRange(offset, length int) Collection[T] {
	if offset < 0 {
		offset = len(c) + offset
	}
	if offset < 0 {
		offset = 0
	}
	if offset > len(c) {
		return Collection[T]{}
	}
	end := len(c)
	if length >= 0 {
		end = offset + length
		if end > len(c) {
			end = len(c)
		}
	}
	return Collection[T](c[offset:end:end])
}

// Take 取前 n 个（对应 Laravel take）。
func (c Collection[T]) Take(n int) Collection[T] {
	if n < 0 {
		// Laravel take(-n) 取末尾 n 个
		if -n > len(c) {
			return c.Clone()
		}
		return c.Clone()[len(c)+n:]
	}
	if n > len(c) {
		n = len(c)
	}
	return c.Clone()[:n]
}

// Skip 跳过前 n 个（对应 Laravel skip）。
func (c Collection[T]) Skip(n int) Collection[T] {
	if n < 0 {
		n = 0
	}
	if n > len(c) {
		n = len(c)
	}
	return c.Clone()[n:]
}

// Reverse 倒序（对应 Laravel reverse）。
func (c Collection[T]) Reverse() Collection[T] {
	out := c.Clone()
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// SortBy 比较函数排序（稳定，对应 Laravel sortBy；Go 侧传 less 闭包）。
func (c Collection[T]) SortBy(less func(a, b T) bool) Collection[T] {
	out := c.Clone()
	sort.SliceStable(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}

// Clone 浅拷贝（返回独立切片）。
func (c Collection[T]) Clone() Collection[T] {
	out := make(Collection[T], len(c))
	copy(out, c)
	return out
}

// Sum 数值求和：无字段名时直接对元素求和，有字段名时按字段提取
// （对应 Laravel sum('field') / sum(fn)；非数值跳过）。
func (c Collection[T]) Sum(fields ...string) float64 {
	total := 0.0
	for _, item := range c {
		v := any(item)
		if len(fields) > 0 {
			pv, ok := pluckValue(reflect.ValueOf(item), fields[0])
			if !ok {
				continue
			}
			v = pv
		}
		if f, ok := toFloat(v); ok {
			total += f
		}
	}
	return total
}

// Average 数值平均（对应 Laravel avg；空集合返回 0）。
func (c Collection[T]) Average(fields ...string) float64 {
	total, n := 0.0, 0
	for _, item := range c {
		v := any(item)
		if len(fields) > 0 {
			pv, ok := pluckValue(reflect.ValueOf(item), fields[0])
			if !ok {
				continue
			}
			v = pv
		}
		if f, ok := toFloat(v); ok {
			total += f
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return total / float64(n)
}

// Min 最小值（对应 Laravel min）：无字段名时比较元素本身，
// 返回 any（空集合返回 nil）。
func (c Collection[T]) Min(fields ...string) any {
	return c.extreme(fields, func(a, b float64) bool { return a < b }, func(a, b any) bool { return lessAny(a, b) })
}

// Max 最大值（对应 Laravel max）。
func (c Collection[T]) Max(fields ...string) any {
	return c.extreme(fields, func(a, b float64) bool { return a > b }, func(a, b any) bool { return !lessAny(a, b) })
}

func (c Collection[T]) extreme(fields []string, numBetter func(a, b float64) bool, anyBetter func(a, b any) bool) any {
	var best any
	var bestNum float64
	have := false
	for _, item := range c {
		v := any(item)
		if len(fields) > 0 {
			pv, ok := pluckValue(reflect.ValueOf(item), fields[0])
			if !ok {
				continue
			}
			v = pv
		}
		if !have {
			best, bestNum, have = v, orZeroFloat(v), true
			continue
		}
		if f, ok := toFloat(v); ok {
			if numBetter(f, bestNum) {
				best, bestNum = v, f
			}
			continue
		}
		if anyBetter(v, best) {
			best = v
		}
	}
	return best
}

// Implode 拼接（对应 Laravel implode）：给定时按字段提取，否则对元素
// Sprint 后拼接。
func (c Collection[T]) Implode(glue string, fields ...string) string {
	parts := make([]string, 0, len(c))
	for _, item := range c {
		if len(fields) > 0 {
			if v, ok := pluckValue(reflect.ValueOf(item), fields[0]); ok {
				parts = append(parts, valueToString(v))
			}
			continue
		}
		parts = append(parts, valueToString(item))
	}
	return strings.Join(parts, glue)
}

// ToJson 序列化为 JSON（对应 Laravel toJson）。
func (c Collection[T]) ToJson() (string, error) {
	b, err := json.Marshal([]T(c))
	if err != nil {
		return "", err
	}
	return string(b), nil
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

// ---- Laravel Collection 补全（countBy/collapse/flip/keys/pad 等）----

// CountBy 按回调结果分组计数（对应 Laravel countBy：返回 map[回调值]计数；
// 仅有 bool 谓词形态时键为 "true"/"false"）。
func (c Collection[T]) CountBy(keyFn func(T) string) map[string]int {
	out := map[string]int{}
	for _, item := range c {
		out[keyFn(item)]++
	}
	return out
}

// CountWhere 满足谓词的元素个数（torm 保留的原"CountBy"语义）。
func (c Collection[T]) CountWhere(pred func(T) bool) int {
	n := 0
	for _, item := range c {
		if pred(item) {
			n++
		}
	}
	return n
}

// Collapse 展开嵌套集合一层（对应 Laravel collect([[1,2],[3]])->collapse()）。
// 集合元素为切片类型时的包级形态（Go 方法不能按 T 约束特化）。
func Collapse[T any](nested Collection[[]T]) Collection[T] {
	out := make(Collection[T], 0, len(nested))
	for _, inner := range nested {
		out = append(out, inner...)
	}
	return out
}

// Flip 值翻转为键（对应 Laravel flip）：元素值 → 首次出现的下标；
// 重复值保留首次出现（Laravel 同语义）。Go 方法不能新增 comparable 约束，
// 以包级泛型函数提供。
func Flip[K comparable](c Collection[K]) map[K]int {
	out := make(map[K]int, len(c))
	for i, item := range c {
		if _, ok := out[item]; !ok {
			out[item] = i
		}
	}
	return out
}

// Keys 键序列（对应 Laravel keys）：Go 集合底座为切片，键即下标 0..n-1。
func (c Collection[T]) Keys() []int {
	out := make([]int, len(c))
	for i := range c {
		out[i] = i
	}
	return out
}

// Pad 补齐到指定长度（对应 Laravel pad）：size 为正从尾部补 def，
// 为负从头部补；|size| 不大于原长度时截断（Laravel pad 同为截断语义）。
func (c Collection[T]) Pad(size int, def T) Collection[T] {
	n := size
	if n < 0 {
		n = -n
	}
	// PHP array_pad：|size| <= len 时原样返回（不截断）
	if n <= len(c) {
		return c.Clone()
	}
	pad := n - len(c)
	out := make(Collection[T], 0, n)
	if size < 0 {
		for i := 0; i < pad; i++ {
			out = append(out, def)
		}
		out = append(out, c...)
	} else {
		out = append(out, c...)
		for i := 0; i < pad; i++ {
			out = append(out, def)
		}
	}
	return out
}

// ToQuery 首个模型的新查询构造器（对应 Laravel toQuery()：以集合首模型
// 的 newQuery 为底座）。集合为空、conn 为 nil 或元素不是模型时报错
// （Go 模型不内嵌连接，需显式传入连接上下文）。
func (c Collection[T]) ToQuery(conn *Connection) (*Builder, error) {
	if c.IsEmpty() {
		return nil, fmt.Errorf("torm: toQuery on empty collection")
	}
	if conn == nil {
		return nil, fmt.Errorf("torm: toQuery requires a connection")
	}
	first := any(c[0])
	if _, _, ok := modelPrimaryKey(first); !ok {
		return nil, fmt.Errorf("torm: toQuery requires model elements")
	}
	return conn.Model(first), nil
}

// Fresh 按主键重查每个模型为新集合（对应 Laravel fresh，
// Eloquent/Collection.php:1571）：原集合不变；行未命中的位置保留
// 零值占位（指针元素为 nil，对齐 Laravel 保留 null 键位的语义）。
func (c Collection[T]) Fresh(conn *Connection) (Collection[T], error) {
	if conn == nil {
		return nil, fmt.Errorf("torm: fresh requires a connection")
	}
	out := make(Collection[T], 0, len(c))
	for _, item := range c {
		f, err := conn.Fresh(any(item))
		if err != nil {
			return nil, err
		}
		if f == nil {
			// 行消失/未持久化：保留零值占位（Laravel fresh 的 null 键位）
			var zero T
			out = append(out, zero)
			continue
		}
		if v, ok := f.(T); ok {
			out = append(out, v)
			continue
		}
		// T 为非指针结构体：Fresh 返回 *T，解引用后回填；转换失败保留占位
		rv := reflect.ValueOf(f)
		if rv.Kind() == reflect.Ptr && !rv.IsNil() {
			d := rv.Elem().Interface()
			if v, ok := d.(T); ok {
				out = append(out, v)
				continue
			}
		}
		var zero T
		out = append(out, zero)
	}
	return out, nil
}

// GetDictionary 按主键的字典 map（对应 Laravel getDictionary /
// InteractsWithDictionary）：键 = 模型主键的 Sprint 形式；非模型元素
// 以元素本身的 Sprint 为键。
func (c Collection[T]) GetDictionary() map[string]T {
	out := make(map[string]T, len(c))
	for _, item := range c {
		key := ""
		if _, pk, ok := modelPrimaryKey(any(item)); ok {
			key = fmt.Sprint(pk)
		} else {
			key = fmt.Sprint(item)
		}
		out[key] = item
	}
	return out
}

// ---- 模型属性转发（对应 Eloquent Collection 的 makeHidden/makeVisible
// 等向每个模型转发的形态；torm 的 hidden 为类型级注册表，效果等价）----

// collectionEachModel 对集合逐元素应用 fn（元素须为模型指针；非模型类型
// 返回 error，与 ToQuery 的风格一致）。
func (c Collection[T]) collectionEachModel(op string, fn func(model any)) error {
	if c.IsEmpty() {
		return nil
	}
	first := any(c[0])
	if _, _, ok := modelPrimaryKey(first); !ok {
		return fmt.Errorf("torm: %s requires model elements", op)
	}
	for _, item := range c {
		fn(any(item))
	}
	return nil
}

// MakeHidden 集合内全部模型追加隐藏列（对应 makeHidden）。
func (c Collection[T]) MakeHidden(keys ...string) (Collection[T], error) {
	err := c.collectionEachModel("makeHidden", func(model any) { MakeHidden(model, keys...) })
	return c, err
}

// MakeVisible 集合内全部模型解除隐藏列（对应 makeVisible）。
func (c Collection[T]) MakeVisible(keys ...string) (Collection[T], error) {
	err := c.collectionEachModel("makeVisible", func(model any) { MakeVisible(model, keys...) })
	return c, err
}

// SetHidden 替换集合内全部模型的隐藏列集（对应 setHidden）：先全量
// MakeVisible 清空再追加新集。
func (c Collection[T]) SetHidden(keys ...string) (Collection[T], error) {
	err := c.collectionEachModel("setHidden", func(model any) {
		current := GetHidden(model)
		if len(current) > 0 {
			MakeVisible(model, current...)
		}
		MakeHidden(model, keys...)
	})
	return c, err
}

// MergeHidden 集合内全部模型合并隐藏列（对应 mergeHidden，语义与
// makeHidden 一致，追加去重）。
func (c Collection[T]) MergeHidden(keys ...string) (Collection[T], error) {
	return c.MakeHidden(keys...)
}

// GetVisible 可见列集（对应 Laravel getVisible 返回 $visible；torm 只维护
// hidden 注册表，此处返回首模型全部列减去隐藏列的可见集）。
func (c Collection[T]) GetVisible() ([]string, error) {
	if c.IsEmpty() {
		return nil, nil
	}
	first := any(c[0])
	if _, _, ok := modelPrimaryKey(first); !ok {
		return nil, fmt.Errorf("torm: getVisible requires model elements")
	}
	hidden := map[string]bool{}
	for _, h := range GetHidden(first) {
		hidden[h] = true
	}
	var out []string
	for k := range structAttributes(first) {
		if !hidden[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out, nil
}

// ---- 辅助 ----

// pluckValue 取结构体（或指针）中指定字段的值：先 Go 字段名，再 torm 列名。
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
	fv := rv.FieldByName(field)
	if fv.IsValid() {
		return fv.Interface(), true
	}
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

// whereMatcher 构造字段条件谓词（firstWhere/contains 共用）。
func whereMatcher[T any](field string, args ...any) func(T) bool {
	op, val := "=", any(nil)
	switch len(args) {
	case 1:
		val = args[0]
	case 2:
		if s, ok := args[0].(string); ok {
			op = s
		}
		val = args[1]
	}
	return func(item T) bool {
		v, ok := pluckValue(reflect.ValueOf(item), field)
		if !ok {
			return false
		}
		return compareValues(v, op, val)
	}
}

// compareValues 支持 =/!=/<>/</<=/>/>=，数值优先、可比较值回退字符串/DeepEqual。
func compareValues(a any, op string, b any) bool {
	af, aok := toFloat(a)
	bf, bok := toFloat(b)
	if aok && bok {
		switch op {
		case "=", "==":
			return af == bf
		case "!=", "<>":
			return af != bf
		case "<":
			return af < bf
		case "<=":
			return af <= bf
		case ">":
			return af > bf
		case ">=":
			return af >= bf
		}
		return false
	}
	switch op {
	case "=", "==":
		return reflect.DeepEqual(a, b) || valueToString(a) == valueToString(b)
	case "!=", "<>":
		return !(reflect.DeepEqual(a, b) || valueToString(a) == valueToString(b))
	}
	as, a1 := valueStringOk(a)
	bs, b1 := valueStringOk(b)
	if a1 && b1 {
		switch op {
		case "<":
			return as < bs
		case "<=":
			return as <= bs
		case ">":
			return as > bs
		case ">=":
			return as >= bs
		}
	}
	return false
}

// toFloat 数值提取（int/uint/float 全谱 + 数字字符串）。
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	case string:
		if f, err := strconv.ParseFloat(n, 64); err == nil {
			return f, true
		}
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.IsValid() && rv.CanInt() {
		return float64(rv.Int()), true
	}
	if rv.IsValid() && rv.CanUint() {
		return float64(rv.Uint()), true
	}
	if rv.IsValid() && rv.Kind() == reflect.Float64 {
		return rv.Float(), true
	}
	return 0, false
}

func orZeroFloat(v any) float64 {
	if f, ok := toFloat(v); ok {
		return f
	}
	return 0
}

func lessAny(a, b any) bool {
	as, aok := valueStringOk(a)
	bs, bok := valueStringOk(b)
	if aok && bok {
		return as < bs
	}
	return valueToString(a) < valueToString(b)
}

func valueStringOk(v any) (string, bool) {
	switch s := v.(type) {
	case string:
		return s, true
	case []byte:
		return string(s), true
	}
	return "", false
}

func valueToString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	case fmt.Stringer:
		return s.String()
	}
	return fmt.Sprint(v)
}

// structAttributes 把结构体（或指针）投影为字段 map（键 = torm 列名/Go 字段名）。
func structAttributes(item any) map[string]any {
	out := map[string]any{}
	rv := reflect.ValueOf(item)
	if rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return out
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return out
	}
	if !rv.CanAddr() {
		cp := reflect.New(rv.Type())
		cp.Elem().Set(rv)
		rv = cp.Elem()
	}
	schema, err := NewSchema(rv.Addr().Interface())
	if err != nil {
		return out
	}
	for _, f := range schema.Fields {
		out[f.Name] = f.Value.Interface()
	}
	return out
}

// Studly 导出常用命名转换（集合/关系场景使用）。
func StudlyCase(s string) string { return utils.StudlyCase(s) }
