package torm

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
)

// ---- Collection 补齐（二）：任务清单方法 ----
// 与 collection.go 的约定一致：泛型 + 注释标注对应方法语义；
// 既有不可变风格（值接收者，返回新集合，不改写原集合）。

// Last 返回最后一个满足条件的元素：pred 为 nil 时取
// 最后一个；空集合或无匹配返回零值与 false。
func (c Collection[T]) Last(pred func(T) bool) (T, bool) {
	var zero T
	for i := len(c) - 1; i >= 0; i-- {
		if pred == nil || pred(c[i]) {
			return c[i], true
		}
	}
	return zero, false
}

// Every 是否全部元素满足条件：空集合返回 true。
func (c Collection[T]) Every(pred func(T) bool) bool {
	for _, item := range c {
		if !pred(item) {
			return false
		}
	}
	return true
}

// Some 是否存在满足条件的元素（contains 的别名）。
func (c Collection[T]) Some(pred func(T) bool) bool {
	return c.Contains(pred)
}

// Push 追加单个元素。Go 侧沿用本集合层的不可变
// 约定：返回追加后的新集合，原集合不变。
func (c Collection[T]) Push(item T) Collection[T] {
	out := make(Collection[T], 0, len(c)+1)
	out = append(out, c...)
	return append(out, item)
}

// Pop 弹出最后一个元素：返回 (末元素, 去掉末元素的
// 新集合, 是否成功)；空集合返回 (零值, 空集合, false)。
func (c Collection[T]) Pop() (T, Collection[T], bool) {
	var zero T
	if c.IsEmpty() {
		return zero, Collection[T]{}, false
	}
	return c[len(c)-1], c.Clone()[:len(c)-1], true
}

// Pull 取走指定下标元素并从集合移除（键即切片
// 下标）：返回 (元素, 移除后的新集合, 是否成功)；下标越界返回
// (零值, 原集合, false)。
func (c Collection[T]) Pull(index int) (T, Collection[T], bool) {
	var zero T
	if index < 0 || index >= len(c) {
		return zero, c, false
	}
	out := make(Collection[T], 0, len(c)-1)
	out = append(out, c[:index]...)
	out = append(out, c[index+1:]...)
	return c[index], out, true
}

// Forget 移除指定下标（可变参）的元素：
// 越界下标忽略；返回保留原顺序的新集合；无下标时等价 Clone。
func (c Collection[T]) Forget(indices ...int) Collection[T] {
	skip := make(map[int]bool, len(indices))
	for _, i := range indices {
		if i >= 0 && i < len(c) {
			skip[i] = true
		}
	}
	out := make(Collection[T], 0, len(c))
	for i, item := range c {
		if !skip[i] {
			out = append(out, item)
		}
	}
	return out
}

// Chunk 按 size 固定大小切块：最后一块可小于 size；
// size <= 0 返回 nil；空集合返回空切片。
func (c Collection[T]) Chunk(size int) [][]T {
	if size <= 0 {
		return nil
	}
	out := make([][]T, 0, (len(c)+size-1)/size)
	for i := 0; i < len(c); i += size {
		end := i + size
		if end > len(c) {
			end = len(c)
		}
		chunk := make([]T, end-i)
		copy(chunk, c[i:end])
		out = append(out, chunk)
	}
	return out
}

// Split 按组数均分：组大小取
// ceil(len/n)（向上取整），末组可更短；n <= 0 返回 nil；空集合返回空切片。
func (c Collection[T]) Split(n int) [][]T {
	if n <= 0 {
		return nil
	}
	if c.IsEmpty() {
		return [][]T{}
	}
	size := (len(c) + n - 1) / n
	return c.Chunk(size)
}

// KeyBy 以回调结果为键重建 map：重复键后出现覆盖
// 先出现（PHP 数组赋值语义）；空集合返回空 map。
func (c Collection[T]) KeyBy(keyFn func(T) string) map[string]T {
	out := make(map[string]T, len(c))
	for _, item := range c {
		out[keyFn(item)] = item
	}
	return out
}

// GroupBy 按回调结果分组：键为分组值，组内保持
// 出现顺序；Go map 无序，确定性键序用 SortKeys(grouped) 取。
func (c Collection[T]) GroupBy(keyFn func(T) string) map[string][]T {
	out := map[string][]T{}
	for _, item := range c {
		k := keyFn(item)
		out[k] = append(out[k], item)
	}
	return out
}

// SortKeys 返回 map 键的升序切片（Go 形态：
// map 遍历无序，供 GroupBy/CountBy/MapWithKeys 结果取确定性键序）。
func SortKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// SortByDesc 降序排序：对齐既有 SortBy 的
// less 比较器约定，稳定排序；返回新集合。
func (c Collection[T]) SortByDesc(less func(a, b T) bool) Collection[T] {
	out := c.Clone()
	sort.SliceStable(out, func(i, j int) bool { return less(out[j], out[i]) })
	return out
}

// TakeUntil 取到首个满足条件的元素为止（不含该元素，任务清单中的
// Until 方法）：无元素满足时返回全量；空集合返回空。
func (c Collection[T]) TakeUntil(pred func(T) bool) Collection[T] {
	out := make(Collection[T], 0, len(c))
	for _, item := range c {
		if pred(item) {
			break
		}
		out = append(out, item)
	}
	return out
}

// Duplicates 重复元素：所有出现次数大于 1 的
// 值的全部出现位置，保持原顺序（与 Unique/Diff 一致按 Sprint 键判定）。
func (c Collection[T]) Duplicates() Collection[T] {
	seen := map[string]bool{}
	counts := map[string]int{}
	for _, item := range c {
		counts[fmt.Sprint(item)]++
	}
	out := make(Collection[T], 0, len(c))
	for _, item := range c {
		// 语义：每个值的首次出现不返回，返回除首现外的全部出现
		if counts[fmt.Sprint(item)] > 1 {
			if seen[fmt.Sprint(item)] {
				out = append(out, item)
			}
			seen[fmt.Sprint(item)] = true
		}
	}
	return out
}

// Median 中位数：数值升序取中位，偶数个取中间
// 两数的平均；可传字段名（sum/average 同风格）；无数值或空集合返回 0。
func (c Collection[T]) Median(fields ...string) float64 {
	nums := make([]float64, 0, len(c))
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
			nums = append(nums, f)
		}
	}
	if len(nums) == 0 {
		return 0
	}
	sort.Float64s(nums)
	mid := len(nums) / 2
	if len(nums)%2 == 1 {
		return nums[mid]
	}
	return (nums[mid-1] + nums[mid]) / 2
}

// Mode 众数：按 Sprint 键计数取频次最高的元素；
// 并列时取原顺序中最先出现者；空集合返回 (零值, false)。
func (c Collection[T]) Mode() (T, bool) {
	var zero T
	if c.IsEmpty() {
		return zero, false
	}
	counts := map[string]int{}
	best, bestCount := zero, 0
	for _, item := range c {
		k := fmt.Sprint(item)
		counts[k]++
		// 严格大于保证并列时保留最先出现者
		if counts[k] > bestCount {
			best, bestCount = item, counts[k]
		}
	}
	return best, bestCount > 0
}

// CrossJoin 与另一切片做笛卡尔积（单数组形态）：
// 复用 ZipPair 泛型对承载 (本集合元素, other 元素)；任一侧为空返回空切片。
func (c Collection[T]) CrossJoin[U any](other []U) []ZipPair[T, U] {
	out := make([]ZipPair[T, U], 0, len(c)*len(other))
	for _, a := range c {
		for _, b := range other {
			out = append(out, ZipPair[T, U]{First: a, Second: b})
		}
	}
	return out
}

// Shuffle 随机打乱：math/rand 全局 rand.Shuffle，
// 返回打乱后的新集合，原集合不变。
func (c Collection[T]) Shuffle() Collection[T] {
	out := c.Clone()
	rand.Shuffle(len(out), func(i, j int) {
		out[i], out[j] = out[j], out[i]
	})
	return out
}

// Random 随机取 n 个：抽 n 个不重复下标后按
// 原顺序输出（保序语义）；n <= 0 返回空集合；
// n 大于长度时收敛为长度（Go 侧按惯例收敛）。
func (c Collection[T]) Random(n int) Collection[T] {
	if n <= 0 || c.IsEmpty() {
		return Collection[T]{}
	}
	if n > len(c) {
		n = len(c)
	}
	picked := rand.Perm(len(c))[:n]
	sort.Ints(picked)
	out := make(Collection[T], 0, n)
	for _, i := range picked {
		out = append(out, c[i])
	}
	return out
}
