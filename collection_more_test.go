package torm

import (
	"reflect"
	"sort"
	"testing"
)

// ---- Collection 补全（二）测试：任务清单方法（collection_more.go）----

func TestCollectionMoreLast(t *testing.T) {
	tests := []struct {
		name  string
		items []int
		pred  func(int) bool
		want  int
		ok    bool
	}{
		{"nil pred 取最后一个", []int{1, 2, 3}, nil, 3, true},
		{"谓词命中末尾", []int{1, 2, 3, 4}, func(v int) bool { return v%2 == 0 }, 4, true},
		{"谓词命中中间", []int{1, 2, 3}, func(v int) bool { return v > 1 }, 3, true},
		{"无匹配", []int{1, 2, 3}, func(v int) bool { return v > 9 }, 0, false},
		{"空集合 nil pred", nil, nil, 0, false},
		{"空集合谓词", []int{}, func(v int) bool { return true }, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Col(tt.items).Last(tt.pred)
			if ok != tt.ok || got != tt.want {
				t.Errorf("Last = %v, %v; want %v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestCollectionMoreEverySome(t *testing.T) {
	tests := []struct {
		name  string
		items []int
		pred  func(int) bool
		every bool
		some  bool
	}{
		{"全部满足", []int{2, 4, 6}, func(v int) bool { return v%2 == 0 }, true, true},
		{"部分满足", []int{1, 2, 3}, func(v int) bool { return v%2 == 0 }, false, true},
		{"全不满足", []int{1, 3, 5}, func(v int) bool { return v%2 == 0 }, false, false},
		{"空集合", []int{}, func(v int) bool { return false }, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Col(tt.items)
			if got := c.Every(tt.pred); got != tt.every {
				t.Errorf("Every = %v; want %v", got, tt.every)
			}
			if got := c.Some(tt.pred); got != tt.some {
				t.Errorf("Some = %v; want %v", got, tt.some)
			}
		})
	}
}

func TestCollectionMorePushPopPull(t *testing.T) {
	t.Run("Push 追加单个不改原集合", func(t *testing.T) {
		c := Col([]int{1, 2})
		got := c.Push(3)
		if !reflect.DeepEqual([]int(got), []int{1, 2, 3}) {
			t.Errorf("Push = %v", got)
		}
		if !reflect.DeepEqual([]int(c), []int{1, 2}) {
			t.Errorf("原集合被修改: %v", c)
		}
	})

	tests := []struct {
		name  string
		items []int
		want  int
		rest  []int
		ok    bool
	}{
		{"Pop 非空", []int{1, 2, 3}, 3, []int{1, 2}, true},
		{"Pop 单元素", []int{9}, 9, []int{}, true},
		{"Pop 空集合", []int{}, 0, []int{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item, rest, ok := Col(tt.items).Pop()
			if ok != tt.ok || item != tt.want || !reflect.DeepEqual([]int(rest), tt.rest) {
				t.Errorf("Pop = %v, %v, %v; want %v, %v, %v", item, rest, ok, tt.want, tt.rest, tt.ok)
			}
		})
	}

	tests2 := []struct {
		name  string
		items []int
		index int
		want  int
		rest  []int
		ok    bool
	}{
		{"Pull 取走首元素", []int{1, 2, 3}, 0, 1, []int{2, 3}, true},
		{"Pull 取走中间", []int{1, 2, 3}, 1, 2, []int{1, 3}, true},
		{"Pull 越界", []int{1, 2}, 5, 0, []int{1, 2}, false},
		{"Pull 负下标", []int{1, 2}, -1, 0, []int{1, 2}, false},
		{"Pull 空集合", []int{}, 0, 0, []int{}, false},
	}
	for _, tt := range tests2 {
		t.Run(tt.name, func(t *testing.T) {
			item, rest, ok := Col(tt.items).Pull(tt.index)
			if ok != tt.ok || item != tt.want || !reflect.DeepEqual([]int(rest), tt.rest) {
				t.Errorf("Pull = %v, %v, %v; want %v, %v, %v", item, rest, ok, tt.want, tt.rest, tt.ok)
			}
		})
	}
}

func TestCollectionMoreForget(t *testing.T) {
	tests := []struct {
		name    string
		items   []int
		indices []int
		want    []int
	}{
		{"移除单个", []int{1, 2, 3}, []int{1}, []int{1, 3}},
		{"移除多个", []int{1, 2, 3, 4}, []int{0, 2}, []int{2, 4}},
		{"越界忽略", []int{1, 2, 3}, []int{9, -1, 1}, []int{1, 3}},
		{"无下标等价 Clone", []int{1, 2}, nil, []int{1, 2}},
		{"空集合", []int{}, []int{0}, []int{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Col(tt.items).Forget(tt.indices...)
			if !reflect.DeepEqual([]int(got), tt.want) {
				t.Errorf("Forget = %v; want %v", got, tt.want)
			}
		})
	}
}

func TestCollectionMoreChunk(t *testing.T) {
	tests := []struct {
		name  string
		items []int
		size  int
		want  [][]int
	}{
		{"整除", []int{1, 2, 3, 4}, 2, [][]int{{1, 2}, {3, 4}}},
		{"末块更短", []int{1, 2, 3, 4, 5}, 2, [][]int{{1, 2}, {3, 4}, {5}}},
		{"size 大于长度", []int{1, 2}, 10, [][]int{{1, 2}}},
		{"size 为 0 返回 nil", []int{1, 2}, 0, nil},
		{"size 为负返回 nil", []int{1, 2}, -1, nil},
		{"空集合", []int{}, 2, [][]int{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Col(tt.items).Chunk(tt.size)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Chunk = %v; want %v", got, tt.want)
			}
		})
	}
}

func TestCollectionMoreSplit(t *testing.T) {
	tests := []struct {
		name  string
		items []int
		n     int
		want  [][]int
	}{
		// split：组大小 = ceil(count/n)，末组可更短
		{"10 个分 3 组", []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 3, [][]int{{1, 2, 3, 4}, {5, 6, 7, 8}, {9, 10}}},
		{"均分", []int{1, 2, 3, 4}, 2, [][]int{{1, 2}, {3, 4}}},
		{"组数多于元素", []int{1, 2}, 5, [][]int{{1}, {2}}},
		{"n 为 0 返回 nil", []int{1}, 0, nil},
		{"n 为负返回 nil", []int{1}, -2, nil},
		{"空集合", []int{}, 3, [][]int{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Col(tt.items).Split(tt.n)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Split = %v; want %v", got, tt.want)
			}
		})
	}
}

func TestCollectionMoreKeyBy(t *testing.T) {
	c := testCol()
	tests := []struct {
		name string
		key  func(colItem) string
		want map[string]int64
	}{
		{"按名称键", func(m colItem) string { return m.Name }, map[string]int64{"A": 1, "B": 2, "C": 3}},
		{"重复键后者覆盖", func(m colItem) string { return "k" }, map[string]int64{"k": 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := c.KeyBy(tt.key)
			want := map[string]colItem{}
			for k, id := range tt.want {
				want[k] = colItem{ID: id}
			}
			if len(got) != len(want) {
				t.Fatalf("KeyBy len = %d; want %d", len(got), len(want))
			}
			for k, v := range want {
				if g, ok := got[k]; !ok || g.ID != v.ID {
					t.Errorf("KeyBy[%s] = %+v; want ID %d", k, g, v.ID)
				}
			}
		})
	}
	t.Run("空集合", func(t *testing.T) {
		if got := Col([]colItem{}).KeyBy(func(m colItem) string { return m.Name }); len(got) != 0 {
			t.Errorf("KeyBy on empty = %v", got)
		}
	})
}

func TestCollectionMoreGroupBy(t *testing.T) {
	tests := []struct {
		name  string
		items []int
		key   func(int) string
		want  map[string][]int
	}{
		{"按奇偶分组", []int{1, 2, 3, 4}, func(v int) string {
			if v%2 == 0 {
				return "even"
			}
			return "odd"
		}, map[string][]int{"odd": {1, 3}, "even": {2, 4}}},
		{"组内保持出现顺序", []int{1, 1, 2, 1}, func(int) string { return "all" }, map[string][]int{"all": {1, 1, 2, 1}}},
		{"空集合", []int{}, func(int) string { return "x" }, map[string][]int{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Col(tt.items).GroupBy(tt.key)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GroupBy = %v; want %v", got, tt.want)
			}
		})
	}
}

func TestCollectionMoreSortKeys(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]int
		want []string
	}{
		{"升序", map[string]int{"b": 2, "a": 1, "c": 3}, []string{"a", "b", "c"}},
		{"空 map", map[string]int{}, []string{}},
		{"nil map", nil, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SortKeys(tt.m)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("SortKeys = %v; want %v", got, tt.want)
			}
		})
	}
	t.Run("配合 GroupBy 取确定性键序", func(t *testing.T) {
		grouped := Col([]int{1, 2, 3, 4}).GroupBy(func(v int) string {
			if v%2 == 0 {
				return "even"
			}
			return "odd"
		})
		keys := SortKeys(grouped)
		if !reflect.DeepEqual(keys, []string{"even", "odd"}) {
			t.Errorf("GroupBy keys = %v", keys)
		}
	})
}

func TestCollectionMoreSortByDesc(t *testing.T) {
	type row struct {
		Name  string
		Score int
	}
	tests := []struct {
		name  string
		items []row
		less  func(a, b row) bool
		want  []row
	}{
		{
			"按分数降序",
			[]row{{"A", 80}, {"B", 90}, {"C", 70}},
			func(a, b row) bool { return a.Score < b.Score },
			[]row{{"B", 90}, {"A", 80}, {"C", 70}},
		},
		{
			// 稳定：同分保持原相对顺序
			"同分稳定",
			[]row{{"A", 80}, {"B", 90}, {"C", 80}},
			func(a, b row) bool { return a.Score < b.Score },
			[]row{{"B", 90}, {"A", 80}, {"C", 80}},
		},
		{
			"空集合",
			[]row{},
			func(a, b row) bool { return a.Score < b.Score },
			[]row{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Col(tt.items).SortByDesc(tt.less)
			if !reflect.DeepEqual([]row(got), tt.want) {
				t.Errorf("SortByDesc = %+v; want %+v", got, tt.want)
			}
		})
	}
}

func TestCollectionMoreTakeUntil(t *testing.T) {
	tests := []struct {
		name  string
		items []int
		pred  func(int) bool
		want  []int
	}{
		{"首个满足即止（不含）", []int{1, 2, 3, 4}, func(v int) bool { return v >= 3 }, []int{1, 2}},
		{"无满足返回全量", []int{1, 2}, func(v int) bool { return v > 9 }, []int{1, 2}},
		{"首元素即满足返回空", []int{1, 2}, func(v int) bool { return v >= 1 }, []int{}},
		{"空集合", []int{}, func(int) bool { return true }, []int{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Col(tt.items).TakeUntil(tt.pred)
			if !reflect.DeepEqual([]int(got), tt.want) {
				t.Errorf("TakeUntil = %v; want %v", got, tt.want)
			}
		})
	}
}

func TestCollectionMoreDuplicates(t *testing.T) {
	tests := []struct {
		name  string
		items []int
		want  []int
	}{
		// 语义约定：首现不返回，返回除首现外的全部出现
		{"除首现外保留原序", []int{1, 2, 2, 3, 3}, []int{2, 3}},
		{"无重复返回空", []int{1, 2, 3}, []int{}},
		{"三连重复", []int{7, 7, 7}, []int{7, 7}},
		{"空集合", []int{}, []int{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Col(tt.items).Duplicates()
			if !reflect.DeepEqual([]int(got), tt.want) {
				t.Errorf("Duplicates = %v; want %v", got, tt.want)
			}
		})
	}
}

func TestCollectionMoreMedian(t *testing.T) {
	tests := []struct {
		name  string
		items []int
		want  float64
	}{
		{"奇数个取中位", []int{3, 1, 2}, 2},
		{"偶数个取中间平均", []int{4, 1, 3, 2}, 2.5},
		{"单元素", []int{5}, 5},
		{"空集合返回 0", []int{}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Col(tt.items).Median(); got != tt.want {
				t.Errorf("Median = %v; want %v", got, tt.want)
			}
		})
	}
	t.Run("按字段取中位", func(t *testing.T) {
		if got := testCol().Median("score"); got != 80 {
			t.Errorf("Median(score) = %v; want 80", got)
		}
	})
}

func TestCollectionMoreMode(t *testing.T) {
	tests := []struct {
		name  string
		items []int
		want  int
		ok    bool
	}{
		{"单众数", []int{1, 2, 2, 3}, 2, true},
		{"并列取最先出现", []int{1, 1, 2, 2}, 1, true},
		{"全不同取首个", []int{3, 1, 2}, 3, true},
		{"空集合", []int{}, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Col(tt.items).Mode()
			if ok != tt.ok || got != tt.want {
				t.Errorf("Mode = %v, %v; want %v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestCollectionMoreCrossJoin(t *testing.T) {
	tests := []struct {
		name  string
		items []int
		other []string
		want  []ZipPair[int, string]
	}{
		{
			"笛卡尔积",
			[]int{1, 2}, []string{"a", "b"},
			[]ZipPair[int, string]{{1, "a"}, {1, "b"}, {2, "a"}, {2, "b"}},
		},
		{"other 为空返回空", []int{1}, []string{}, []ZipPair[int, string]{}},
		{"集合为空返回空", []int{}, []string{"a"}, []ZipPair[int, string]{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Col(tt.items).CrossJoin(tt.other)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CrossJoin = %v; want %v", got, tt.want)
			}
		})
	}
}

func TestCollectionMoreShuffle(t *testing.T) {
	t.Run("打乱后保持多重集合一致且原集合不变", func(t *testing.T) {
		c := Col([]int{1, 2, 3, 4, 5})
		got := c.Shuffle()
		if !reflect.DeepEqual([]int(c), []int{1, 2, 3, 4, 5}) {
			t.Errorf("原集合被修改: %v", c)
		}
		orig := append([]int{}, []int(c)...)
		shuffled := append([]int{}, got...)
		sort.Ints(shuffled)
		if !reflect.DeepEqual(shuffled, orig) {
			t.Errorf("Shuffle 元素集变化: %v", got)
		}
	})
	t.Run("空集合", func(t *testing.T) {
		got := Col([]int{}).Shuffle()
		if len(got) != 0 {
			t.Errorf("Shuffle on empty = %v", got)
		}
	})
}

func TestCollectionMoreRandom(t *testing.T) {
	tests := []struct {
		name    string
		items   []int
		n       int
		wantLen int
	}{
		{"取 2 个", []int{1, 2, 3, 4, 5}, 2, 2},
		{"n 大于长度收敛", []int{1, 2, 3}, 10, 3},
		{"n 为 0", []int{1, 2}, 0, 0},
		{"n 为负", []int{1, 2}, -1, 0},
		{"空集合", []int{}, 3, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Col(tt.items).Random(tt.n)
			if len(got) != tt.wantLen {
				t.Fatalf("Random len = %d; want %d", len(got), tt.wantLen)
			}
			// 元素必须来自原集合且不重复（原集合本身无重复）
			seen := map[int]bool{}
			for _, v := range got {
				if seen[v] {
					t.Errorf("Random 返回重复元素: %v", got)
				}
				seen[v] = true
			}
			for _, v := range got {
				if !Col(tt.items).ContainsValue(v) {
					t.Errorf("Random 元素 %v 不在原集合", v)
				}
			}
		})
	}
	t.Run("保持原相对顺序", func(t *testing.T) {
		c := Col([]int{1, 2, 3, 4, 5})
		got := c.Random(3)
		if !reflect.DeepEqual([]int(got), sortIntsByOriginal([]int(c), got)) {
			t.Errorf("Random 未按原顺序输出: %v", got)
		}
	})
}

// sortIntsByOriginal 校验辅助：把 picked 按其在 original 中的出现顺序排列。
func sortIntsByOriginal(original []int, picked Collection[int]) []int {
	idx := map[int][]int{}
	for i, v := range original {
		idx[v] = append(idx[v], i)
	}
	pos := make([]int, 0, len(picked))
	for _, v := range picked {
		pos = append(pos, idx[v][0])
		idx[v] = idx[v][1:]
	}
	sorted := make([]int, len(pos))
	copy(sorted, pos)
	sort.Ints(sorted)
	out := make([]int, 0, len(picked))
	for _, p := range sorted {
		out = append(out, original[p])
	}
	return out
}
