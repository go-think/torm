package query

// KV 是有序键值对；Row 对应 PHP 有序数组的 Go 等价物（保持列序）。
// map[string]any 在 Go 中无序，插入场景若依赖列序请使用 Row。
type KV struct {
	Key   string
	Value any
}

type Row []KV

// ToRow 把 map 转为按 key 排序的 Row（确定性顺序）。
func ToRow(m map[string]any) Row {
	row := make(Row, 0, len(m))
	for _, k := range SortedKeys(m) {
		row = append(row, KV{Key: k, Value: m[k]})
	}
	return row
}

// SortedKeys 返回 map 的排序键。
func SortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// 简单插入排序（列数很小）
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// Columns 返回行内列名（保序）。
func (r Row) Columns() []string {
	cols := make([]string, len(r))
	for i, kv := range r {
		cols[i] = kv.Key
	}
	return cols
}

// Value 取某列的值（不存在返回 nil, false）。
func (r Row) Value(key string) (any, bool) {
	for _, kv := range r {
		if kv.Key == key {
			return kv.Value, true
		}
	}
	return nil, false
}
