package torm

import (
	"net/url"
	"strconv"
	"strings"
)

// ---- 分页器展示层方法（firstItem/hasMorePages/
// url/appends 等公开 API）----
// 分页器结构体定义在 builder.go（不可改动），方法统一追加在本文件（同包）；
// Items 字段为 any（通常为指向切片的指针），当前页条目数经
// reflectSliceLen（builder.go）读取。

// ---- LengthAwarePaginator ----

// HasMorePages 是否还有下一页。
func (p LengthAwarePaginator) HasMorePages() bool {
	return p.CurrentPage < p.LastPage
}

// FirstItem 当前页首条目的全局序号（1 起）：
// 当前页无条目时返回 0。
func (p LengthAwarePaginator) FirstItem() int {
	if reflectSliceLen(p.Items) == 0 {
		return 0
	}
	return int((p.CurrentPage-1)*p.PerPage) + 1
}

// LastItem 当前页末条目的全局序号：无条目返回 0。
func (p LengthAwarePaginator) LastItem() int {
	n := reflectSliceLen(p.Items)
	if n == 0 {
		return 0
	}
	return p.FirstItem() + n - 1
}

// From 当前页起始条目序号（firstItem 别名）。
func (p LengthAwarePaginator) From() int { return p.FirstItem() }

// To 当前页结束条目序号（lastItem 别名）。
func (p LengthAwarePaginator) To() int { return p.LastItem() }

// URL 分页链接查询串：分页器结构未存储路径
// （path），返回以 "?" 起始的查询串，由调用方拼接到自身路径；query 键
// 升序拼接，page 参数置于末尾（覆盖 query 中的同名键）；
// page < 1 归一为 1。
func (p LengthAwarePaginator) URL(page int, query map[string]string) string {
	return paginationQuery(page, query)
}

// PageLinks 页码窗口：首页 + 当前页
// ±onEachSide + 尾页；首/尾页与窗口之间的缺口以 -1 表示省略号
// （即 "..."）。采用旧版窗口算法（输出为当前页±n + 首尾锚 + -1 省略号）：
// 当前页 > onEachSide+2 时补前省略号，当前页 < 尾页-(onEachSide+1) 时
// 补后省略号。
func (p LengthAwarePaginator) PageLinks(onEachSide int) []int {
	last := int(p.LastPage)
	cur := int(p.CurrentPage)
	if last < 1 {
		last = 1
	}
	if cur < 1 {
		cur = 1
	}
	if onEachSide < 0 {
		onEachSide = 0
	}
	out := make([]int, 0, 2*onEachSide+5)
	seen := map[int]bool{}
	add := func(v int) {
		// 页码去重；-1（省略号）可出现两次
		if v > 0 {
			if seen[v] {
				return
			}
			seen[v] = true
		}
		out = append(out, v)
	}
	add(1)
	if cur > onEachSide+2 {
		add(-1)
	}
	for i := cur - onEachSide; i <= cur+onEachSide; i++ {
		if i >= 1 && i <= last {
			add(i)
		}
	}
	if cur < last-(onEachSide+1) {
		add(-1)
	}
	add(last)
	return out
}

// Appends 追加公共查询参数，返回携带参数的链式副本：
// 分页器结构体未存储查询参数字段，以包装副本 AppendedLengthAware
// 承载（内嵌类型转发其余方法）。
func (p LengthAwarePaginator) Appends(query map[string]string) AppendedLengthAware {
	return AppendedLengthAware{LengthAwarePaginator: p, Query: cloneQueryMap(query)}
}

// AppendedLengthAware 携带 Appends 查询参数的 LengthAwarePaginator 副本。
type AppendedLengthAware struct {
	LengthAwarePaginator
	// Query 经 Appends 追加的公共查询参数。
	Query map[string]string
}

// URL 合并 Appends 参数后再生成：同名键以本次传入的 query 覆盖。
func (p AppendedLengthAware) URL(page int, query map[string]string) string {
	return p.LengthAwarePaginator.URL(page, mergeQueryMap(p.Query, query))
}

// ---- SimplePaginator ----

// HasMorePages 是否还有下一页（基于
// simplePaginate 多取一条判断出的 HasMore）。
func (p SimplePaginator) HasMorePages() bool { return p.HasMore }

// FirstItem 当前页首条目的全局序号（简单分页器
// 同样具备；无条目返回 0）。
func (p SimplePaginator) FirstItem() int {
	if reflectSliceLen(p.Items) == 0 {
		return 0
	}
	return int((p.CurrentPage-1)*p.PerPage) + 1
}

// LastItem 当前页末条目的全局序号：无条目返回 0。
func (p SimplePaginator) LastItem() int {
	n := reflectSliceLen(p.Items)
	if n == 0 {
		return 0
	}
	return p.FirstItem() + n - 1
}

// From 当前页起始条目序号（firstItem 别名）。
func (p SimplePaginator) From() int { return p.FirstItem() }

// To 当前页结束条目序号（lastItem 别名）。
func (p SimplePaginator) To() int { return p.LastItem() }

// URL 分页链接查询串（语义同 LengthAwarePaginator.URL）。
func (p SimplePaginator) URL(page int, query map[string]string) string {
	return paginationQuery(page, query)
}

// Appends 追加公共查询参数，返回携带参数的链式副本。
func (p SimplePaginator) Appends(query map[string]string) AppendedSimple {
	return AppendedSimple{SimplePaginator: p, Query: cloneQueryMap(query)}
}

// AppendedSimple 携带 Appends 查询参数的 SimplePaginator 副本。
type AppendedSimple struct {
	SimplePaginator
	// Query 经 Appends 追加的公共查询参数。
	Query map[string]string
}

// URL 合并 Appends 参数后再生成：同名键以本次传入的 query 覆盖。
func (p AppendedSimple) URL(page int, query map[string]string) string {
	return p.SimplePaginator.URL(page, mergeQueryMap(p.Query, query))
}

// ---- CursorPaginator ----

// HasMorePages 是否还有下一页（基于 nextCursor 是否非空判断）。
func (p CursorPaginator) HasMorePages() bool {
	return p.NextCursor != nil || p.HasMore
}

// PointsToNextItems 当前游标是否指向后一页（转发 Cursor 同名方法；
// 无游标返回 false）。
func (p CursorPaginator) PointsToNextItems() bool {
	if p.Cursor == nil {
		return false
	}
	return p.Cursor.PointsToNextItems()
}

// Parameter 取当前游标参数（转发 Cursor.Parameter：无游标返回 nil；
// 有游标但缺参时沿用 Cursor.Parameter 的 panic 语义）。
func (p CursorPaginator) Parameter(name string) any {
	if p.Cursor == nil {
		return nil
	}
	return p.Cursor.Parameter(name)
}

// ---- 辅助 ----

// paginationQuery 生成分页查询串：query 键升序拼接（确定性输出），
// page 参数恒在末尾；page < 1 归一为 1。
func paginationQuery(page int, query map[string]string) string {
	if page < 1 {
		page = 1
	}
	var b strings.Builder
	b.WriteByte('?')
	for _, k := range SortKeys(query) {
		if k == "page" {
			// 真实页码覆盖 query 中的同名键，
			// 避免产出重复 page 参数
			continue
		}
		b.WriteString(url.QueryEscape(k))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(query[k]))
		b.WriteByte('&')
	}
	b.WriteString("page=")
	b.WriteString(strconv.Itoa(page))
	return b.String()
}

// cloneQueryMap 拷贝查询参数 map（nil 视为空 map）。
func cloneQueryMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// mergeQueryMap 合并查询参数：over 覆盖 base 的同名键。
func mergeQueryMap(base, over map[string]string) map[string]string {
	out := cloneQueryMap(base)
	for k, v := range over {
		out[k] = v
	}
	return out
}
