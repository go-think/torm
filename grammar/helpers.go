package grammar

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// BindingSegments 绑定段的固定顺序（与查询构造器 bindings 属性键序一致）。
// "with" 段居首：CTE 编译在最终 SQL 最前，其占位符必须最先求值。
var BindingSegments = []string{
	"with",
	"select",
	"from",
	"join",
	"where",
	"groupBy",
	"having",
	"order",
	"union",
	"unionOrder",
}

func fmtString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func fmtJoin(values []any, sep string) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = fmtString(v)
	}
	return strings.Join(parts, sep)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// flattenBinding 展平绑定值（JSON 数组值仅剥去最外层单元素包装）。
func flattenBinding(v any) any {
	if arr, ok := v.([]any); ok {
		if len(arr) == 0 {
			return nil
		}
		if len(arr) == 1 {
			return flattenBinding(arr[0])
		}
		return arr
	}
	return v
}

func intToString(n int64) string {
	return strconv.FormatInt(n, 10)
}

// replaceFirst 只替换第一次出现的子串。
func replaceFirst(search, replace, subject string) string {
	idx := strings.Index(subject, search)
	if idx < 0 {
		return subject
	}
	return subject[:idx] + replace + subject[idx+len(search):]
}
