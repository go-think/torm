package torm

import (
	"reflect"
	"testing"
)

// ---- 分页器展示层方法测试（pagination.go）----

// pageItems 构造 n 条当前页条目（分页器 Items 形态与 Paginate 一致：指针切片）。
func pageItems(n int) *[]colItem {
	items := make([]colItem, n)
	for i := range items {
		items[i] = colItem{ID: int64(i + 1), Name: "row"}
	}
	return &items
}

func TestLengthAwarePaginatorItems(t *testing.T) {
	tests := []struct {
		name             string
		p                LengthAwarePaginator
		wantFirst        int
		wantLast         int
		wantHasMorePages bool
	}{
		{
			"第二页有 10 条",
			LengthAwarePaginator{Items: pageItems(10), Total: 25, PerPage: 10, CurrentPage: 2, LastPage: 3},
			11, 20, true,
		},
		{
			"末页不满",
			LengthAwarePaginator{Items: pageItems(5), Total: 25, PerPage: 10, CurrentPage: 3, LastPage: 3},
			21, 25, false,
		},
		{
			"空结果（total=0）",
			LengthAwarePaginator{Total: 0, PerPage: 10, CurrentPage: 1, LastPage: 1},
			0, 0, false,
		},
		{
			// hasMorePages 语义：currentPage < lastPage；越界页同样为 false
			"当前页越界无条目",
			LengthAwarePaginator{Items: pageItems(0), Total: 25, PerPage: 10, CurrentPage: 9, LastPage: 3},
			0, 0, false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.FirstItem(); got != tt.wantFirst {
				t.Errorf("FirstItem = %d; want %d", got, tt.wantFirst)
			}
			if got := tt.p.LastItem(); got != tt.wantLast {
				t.Errorf("LastItem = %d; want %d", got, tt.wantLast)
			}
			if got := tt.p.From(); got != tt.wantFirst {
				t.Errorf("From = %d; want %d", got, tt.wantFirst)
			}
			if got := tt.p.To(); got != tt.wantLast {
				t.Errorf("To = %d; want %d", got, tt.wantLast)
			}
			if got := tt.p.HasMorePages(); got != tt.wantHasMorePages {
				t.Errorf("HasMorePages = %v; want %v", got, tt.wantHasMorePages)
			}
		})
	}
}

func TestLengthAwarePaginatorURL(t *testing.T) {
	p := LengthAwarePaginator{Total: 25, PerPage: 10, CurrentPage: 2, LastPage: 3}
	tests := []struct {
		name  string
		page  int
		query map[string]string
		want  string
	}{
		{"仅页码", 2, nil, "?page=2"},
		{"页码归一", 0, nil, "?page=1"},
		{"负页码归一", -3, nil, "?page=1"},
		{"query 键升序拼接", 3, map[string]string{"sort": "desc", "q": "a b"}, "?q=a+b&sort=desc&page=3"},
		{"query 与页码共存", 1, map[string]string{"tag": "go"}, "?tag=go&page=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := p.URL(tt.page, tt.query); got != tt.want {
				t.Errorf("URL = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestLengthAwarePaginatorPageLinks(t *testing.T) {
	tests := []struct {
		name       string
		current    int64
		last       int64
		onEachSide int
		want       []int
	}{
		{"窗口连续无省略号", 3, 5, 1, []int{1, 2, 3, 4, 5}},
		{"前缺口", 4, 8, 1, []int{1, -1, 3, 4, 5, -1, 8}},
		{"后缺口", 2, 6, 1, []int{1, 2, 3, -1, 6}},
		{"两侧缺口", 5, 10, 2, []int{1, -1, 3, 4, 5, 6, 7, -1, 10}},
		{"首页无前省略号", 1, 5, 2, []int{1, 2, 3, -1, 5}},
		{"单页", 1, 1, 2, []int{1}},
		// 阈值判定：当前页(2) > window+2(2) 为假 → 首页与窗口相接无前省略号
		{"onEachSide 为 0", 2, 4, 0, []int{1, 2, -1, 4}},
		{"onEachSide 为负按 0 处理", 1, 2, -1, []int{1, 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := LengthAwarePaginator{Total: tt.last * 10, PerPage: 10, CurrentPage: tt.current, LastPage: tt.last}
			got := p.PageLinks(tt.onEachSide)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("PageLinks = %v; want %v", got, tt.want)
			}
		})
	}
}

func TestLengthAwarePaginatorAppends(t *testing.T) {
	p := LengthAwarePaginator{Total: 25, PerPage: 10, CurrentPage: 2, LastPage: 3, Items: pageItems(2)}
	got := p.Appends(map[string]string{"sort": "asc"})
	t.Run("返回副本且原值不变", func(t *testing.T) {
		if _, ok := any(got).(AppendedLengthAware); !ok {
			t.Fatalf("Appends 返回类型 = %T", got)
		}
		if !reflect.DeepEqual(got.LengthAwarePaginator, p) {
			t.Error("Appends 副本字段与原值不一致")
		}
	})
	t.Run("URL 合并 Appends 参数", func(t *testing.T) {
		if s := got.URL(3, nil); s != "?sort=asc&page=3" {
			t.Errorf("Appended URL = %q", s)
		}
	})
	t.Run("本次 query 覆盖同名键", func(t *testing.T) {
		if s := got.URL(3, map[string]string{"sort": "desc"}); s != "?sort=desc&page=3" {
			t.Errorf("Appended URL override = %q", s)
		}
	})
	t.Run("内嵌类型方法可用", func(t *testing.T) {
		if !got.HasMorePages() || got.FirstItem() != 11 {
			t.Errorf("Appended 转发异常: hasMore=%v first=%d", got.HasMorePages(), got.FirstItem())
		}
	})
}

func TestSimplePaginatorApi(t *testing.T) {
	tests := []struct {
		name      string
		p         SimplePaginator
		wantFirst int
		wantLast  int
		wantMore  bool
	}{
		{"第二页有 10 条", SimplePaginator{Items: pageItems(10), PerPage: 10, CurrentPage: 2, HasMore: true}, 11, 20, true},
		{"无更多", SimplePaginator{Items: pageItems(5), PerPage: 10, CurrentPage: 1, HasMore: false}, 1, 5, false},
		{"空结果", SimplePaginator{Items: pageItems(0), PerPage: 10, CurrentPage: 4}, 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.HasMorePages(); got != tt.wantMore {
				t.Errorf("HasMorePages = %v; want %v", got, tt.wantMore)
			}
			if got := tt.p.FirstItem(); got != tt.wantFirst {
				t.Errorf("FirstItem = %d; want %d", got, tt.wantFirst)
			}
			if got := tt.p.LastItem(); got != tt.wantLast {
				t.Errorf("LastItem = %d; want %d", got, tt.wantLast)
			}
			if got := tt.p.From(); got != tt.wantFirst {
				t.Errorf("From = %d; want %d", got, tt.wantFirst)
			}
			if got := tt.p.To(); got != tt.wantLast {
				t.Errorf("To = %d; want %d", got, tt.wantLast)
			}
		})
	}
	t.Run("URL 与 Appends", func(t *testing.T) {
		p := SimplePaginator{Items: pageItems(2), PerPage: 10, CurrentPage: 1}
		if s := p.URL(2, map[string]string{"b": "2", "a": "1"}); s != "?a=1&b=2&page=2" {
			t.Errorf("Simple URL = %q", s)
		}
		got := p.Appends(map[string]string{"tag": "go"})
		if s := got.URL(2, nil); s != "?tag=go&page=2" {
			t.Errorf("Simple Appended URL = %q", s)
		}
		if !reflect.DeepEqual(got.SimplePaginator, p) {
			t.Error("Simple Appends 副本字段与原值不一致")
		}
	})
}

func TestCursorPaginatorHelpers(t *testing.T) {
	t.Run("PointsToNextItems 转发", func(t *testing.T) {
		tests := []struct {
			name string
			p    CursorPaginator
			want bool
		}{
			{"指向下一页", CursorPaginator{Cursor: NewCursor(map[string]any{"id": 3}, true)}, true},
			{"指向上一页", CursorPaginator{Cursor: NewCursor(map[string]any{"id": 3}, false)}, false},
			{"无游标（首页）", CursorPaginator{}, false},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := tt.p.PointsToNextItems(); got != tt.want {
					t.Errorf("PointsToNextItems = %v; want %v", got, tt.want)
				}
			})
		}
	})

	t.Run("Parameter 转发", func(t *testing.T) {
		p := CursorPaginator{Cursor: NewCursor(map[string]any{"id": 7}, true)}
		if got := p.Parameter("id"); got != int(7) {
			t.Errorf("Parameter = %v; want 7", got)
		}
		var empty CursorPaginator
		if got := empty.Parameter("id"); got != nil {
			t.Errorf("无游标 Parameter = %v; want nil", got)
		}
		// 有游标但缺参：沿用 Cursor.Parameter 的 panic 语义
		defer func() {
			if recover() == nil {
				t.Error("缺参应沿用 Cursor.Parameter 的 panic 语义")
			}
		}()
		p.Parameter("missing")
	})

	t.Run("HasMorePages", func(t *testing.T) {
		tests := []struct {
			name string
			p    CursorPaginator
			want bool
		}{
			{"有 NextCursor", CursorPaginator{NextCursor: NewCursor(map[string]any{"id": 9}, true)}, true},
			{"无 NextCursor 无 HasMore", CursorPaginator{}, false},
			{"仅 HasMore 兜底", CursorPaginator{HasMore: true}, true},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := tt.p.HasMorePages(); got != tt.want {
					t.Errorf("HasMorePages = %v; want %v", got, tt.want)
				}
			})
		}
	})
}
