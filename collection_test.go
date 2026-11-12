package torm

import (
	"database/sql/driver"
	"reflect"
	"testing"
)

// ---- Collection 补全测试（对照 Illuminate/Collections/Collection.php）----

type colItem struct {
	ID    int64  `torm:"primary_key;column:id"`
	Name  string `torm:"column:name"`
	Score int    `torm:"column:score"`
}

func testCol() Collection[colItem] {
	return Col([]colItem{
		{ID: 1, Name: "A", Score: 80},
		{ID: 2, Name: "B", Score: 90},
		{ID: 3, Name: "C", Score: 70},
	})
}

func TestCollectionFindAndFindOrFail(t *testing.T) {
	c := testCol()
	item, ok := c.Find(int64(2))
	if !ok || item.Name != "B" {
		t.Errorf("find = %+v ok=%v", item, ok)
	}
	if _, ok := c.Find(int64(99)); ok {
		t.Error("missing id should not be found")
	}
	if _, err := c.FindOrFail(int64(99)); err == nil {
		t.Error("findOrFail should error for missing id")
	}
	if item, err := c.FindOrFail(int64(1)); err != nil || item.Name != "A" {
		t.Errorf("findOrFail = %+v err=%v", item, err)
	}
}

func TestCollectionFirstWhere(t *testing.T) {
	c := testCol()
	item, ok := c.FirstWhere("name", "C")
	if !ok || item.ID != 3 {
		t.Errorf("firstWhere eq = %+v ok=%v", item, ok)
	}
	item, ok = c.FirstWhere("score", ">", 80)
	if !ok || item.Name != "B" {
		t.Errorf("firstWhere gt = %+v ok=%v", item, ok)
	}
	if _, ok = c.FirstWhere("score", ">", 999); ok {
		t.Error("no match expected")
	}
}

func TestCollectionContainsForms(t *testing.T) {
	c := testCol()
	if !c.ContainsWhere("name", "B") {
		t.Error("containsWhere eq")
	}
	if !c.ContainsWhere("score", ">=", 90) {
		t.Error("containsWhere gte")
	}
	if c.ContainsWhere("name", "Z") {
		t.Error("no such name")
	}
	if !c.ContainsValue(colItem{ID: 1, Name: "A", Score: 80}) {
		t.Error("containsValue")
	}
	if !c.DoesntContainWhere("name", "Z") {
		t.Error("doesntContainWhere")
	}
	if !c.DoesntContain(func(i colItem) bool { return i.Score > 500 }) {
		t.Error("doesntContain pred")
	}
}

func TestCollectionOnlyExcept(t *testing.T) {
	c := testCol()
	only := c.Only("id", "name")
	if len(only) != 3 || only[0]["id"] != int64(1) || only[0]["name"] != "A" {
		t.Errorf("only = %#v", only)
	}
	if _, has := only[0]["score"]; has {
		t.Error("only should exclude score")
	}
	except := c.Except("score")
	if len(except) != 3 {
		t.Errorf("except len = %d", len(except))
	}
	if _, has := except[0]["score"]; has {
		t.Error("except should drop score")
	}
	if except[1]["name"] != "B" {
		t.Errorf("except name = %#v", except[1])
	}
}

func TestCollectionMergeUnionUniqueDiffIntersect(t *testing.T) {
	a := Col([]int{1, 2, 3})
	b := Col([]int{3, 4, 5})

	if got := a.Merge(b); !reflect.DeepEqual([]int(got), []int{1, 2, 3, 3, 4, 5}) {
		t.Errorf("merge = %v", got)
	}
	if got := a.Union(b); !reflect.DeepEqual([]int(got), []int{1, 2, 3, 4, 5}) {
		t.Errorf("union = %v", got)
	}
	if got := Col([]int{1, 1, 2, 2, 3}).Unique(); !reflect.DeepEqual([]int(got), []int{1, 2, 3}) {
		t.Errorf("unique = %v", got)
	}
	if got := a.Diff(b); !reflect.DeepEqual([]int(got), []int{1, 2}) {
		t.Errorf("diff = %v", got)
	}
	if got := a.Intersect(b); !reflect.DeepEqual([]int(got), []int{3}) {
		t.Errorf("intersect = %v", got)
	}
	// UniqueBy 按键去重
	items := testCol()
	uniq := items.UniqueBy(func(i colItem) any { return i.Score > 75 })
	if len(uniq) != 2 {
		t.Errorf("uniqueBy = %d items", len(uniq))
	}
}

func TestCollectionMapWithKeysReduceReject(t *testing.T) {
	c := testCol()
	m := c.MapWithKeys(func(i colItem) (int64, string) { return i.ID, i.Name })
	if len(m) != 3 || m[2] != "B" {
		t.Errorf("mapWithKeys = %#v", m)
	}
	total := c.Reduce(func(acc float64, i colItem) float64 { return acc + float64(i.Score) }, 0)
	if total != 240 {
		t.Errorf("reduce = %v", total)
	}
	rejected := c.Reject(func(i colItem) bool { return i.Score < 75 })
	if len(rejected) != 2 || rejected[0].Name != "A" {
		t.Errorf("reject = %+v", rejected)
	}
}

func TestCollectionFilterBothPartition(t *testing.T) {
	c := testCol()
	pass, rest := c.Partition(func(i colItem) bool { return i.Score >= 80 })
	if len(pass) != 2 || len(rest) != 1 || rest[0].Name != "C" {
		t.Errorf("partition = %+v / %+v", pass, rest)
	}
	both := c.FilterBoth(func(i colItem, idx int) bool { return idx != 0 })
	if len(both) != 2 || both[0].Name != "B" {
		t.Errorf("filterBoth = %+v", both)
	}
}

func TestCollectionZip(t *testing.T) {
	c := testCol()
	pairs := c.Zip([]string{"x", "y"})
	if len(pairs) != 2 || pairs[0].First.Name != "A" || pairs[1].Second != "y" {
		t.Errorf("zip = %+v", pairs)
	}
}

func TestCollectionTapWhenUnless(t *testing.T) {
	c := testCol()
	tapped := 0
	got := c.Tap(func(cc Collection[colItem]) { tapped = len(cc) })
	if tapped != 3 || len(got) != 3 {
		t.Errorf("tap = %d/%d", tapped, len(got))
	}
	grew := c.When(true, func(cc Collection[colItem]) Collection[colItem] {
		return cc.Merge(Col([]colItem{{ID: 4, Name: "D", Score: 60}}))
	})
	if len(grew) != 4 {
		t.Errorf("when = %d", len(grew))
	}
	same := c.When(false, func(cc Collection[colItem]) Collection[colItem] { return nil })
	if len(same) != 3 {
		t.Errorf("when false should be noop")
	}
	shrunk := c.Unless(false, func(cc Collection[colItem]) Collection[colItem] { return cc.Take(1) })
	if len(shrunk) != 1 {
		t.Errorf("unless = %d", len(shrunk))
	}
}

func TestCollectionFlatten(t *testing.T) {
	nested := Col([][]int{{1, 2}, {3}, {4, 5}})
	got := nested.Flatten()
	if !reflect.DeepEqual([]any(got), []any{1, 2, 3, 4, 5}) {
		t.Errorf("flatten = %#v", got)
	}
	deep := Col([][][]int{{{1}, {2}}, {{3}}})
	got2 := deep.Flatten(2)
	if !reflect.DeepEqual([]any(got2), []any{1, 2, 3}) {
		t.Errorf("flatten depth2 = %#v", got2)
	}
}

func TestCollectionSliceTakeSkipReverse(t *testing.T) {
	c := Col([]int{1, 2, 3, 4, 5})
	if got := c.SliceRange(1, 2); !reflect.DeepEqual([]int(got), []int{2, 3}) {
		t.Errorf("sliceRange = %v", got)
	}
	if got := c.SliceRange(3, -1); !reflect.DeepEqual([]int(got), []int{4, 5}) {
		t.Errorf("sliceRange tail = %v", got)
	}
	if got := c.Take(2); !reflect.DeepEqual([]int(got), []int{1, 2}) {
		t.Errorf("take = %v", got)
	}
	if got := c.Take(-2); !reflect.DeepEqual([]int(got), []int{4, 5}) {
		t.Errorf("take neg = %v", got)
	}
	if got := c.Skip(2); !reflect.DeepEqual([]int(got), []int{3, 4, 5}) {
		t.Errorf("skip = %v", got)
	}
	if got := c.Reverse(); !reflect.DeepEqual([]int(got), []int{5, 4, 3, 2, 1}) {
		t.Errorf("reverse = %v", got)
	}
	if got := c.SliceRange(10, 2); len(got) != 0 {
		t.Errorf("out of range slice = %v", got)
	}
}

func TestCollectionSortBy(t *testing.T) {
	c := testCol()
	sorted := c.SortBy(func(a, b colItem) bool { return a.Score < b.Score })
	if sorted[0].Name != "C" || sorted[2].Name != "B" {
		t.Errorf("sortBy = %+v", sorted)
	}
	if c[0].Name != "A" {
		t.Error("sortBy should not mutate source")
	}
}

func TestCollectionSumAverageMinMaxImplode(t *testing.T) {
	c := testCol()
	if got := c.Sum("score"); got != 240 {
		t.Errorf("sum field = %v", got)
	}
	if got := Col([]int{1, 2, 3}).Sum(); got != 6 {
		t.Errorf("sum = %v", got)
	}
	if got := c.Average("score"); got != 80 {
		t.Errorf("avg = %v", got)
	}
	if got := c.Min("score"); got != 70 {
		t.Errorf("min = %v", got)
	}
	if got := c.Max("score"); got != 90 {
		t.Errorf("max = %v", got)
	}
	if got := Col([]string{"a", "b"}).Min(); got != "a" {
		t.Errorf("min string = %v", got)
	}
	if got := c.Implode(",", "name"); got != "A,B,C" {
		t.Errorf("implode field = %q", got)
	}
	if got := Col([]string{"x", "y"}).Implode("-"); got != "x-y" {
		t.Errorf("implode = %q", got)
	}
}

func TestCollectionToJson(t *testing.T) {
	c := Col([]map[string]any{{"a": 1}, {"b": 2}})
	got, err := c.ToJson()
	if err != nil || got != `[{"a":1},{"b":2}]` {
		t.Errorf("toJson = %q err=%v", got, err)
	}
}

func TestCollectionLenAndEmpty(t *testing.T) {
	var empty Collection[int]
	if !empty.IsEmpty() || empty.IsNotEmpty() || empty.Len() != 0 {
		t.Error("empty collection semantics")
	}
	if testCol().IsEmpty() || testCol().Len() != 3 {
		t.Error("non-empty collection semantics")
	}
}

func TestCollectionModelKeys(t *testing.T) {
	keys := testCol().ModelKeys()
	if len(keys) != 3 || keys[0] != int64(1) || keys[2] != int64(3) {
		t.Errorf("modelKeys = %#v", keys)
	}
}

// ---- Collection 补全（countBy/collapse/flip/keys/pad/dictionary）----

func TestCollectionCountBy(t *testing.T) {
	// Laravel countBy：按回调结果分组计数
	c := Collection[colItem]{{Name: "a", Score: 80}, {Name: "b", Score: 90}, {Name: "c", Score: 70}}
	counts := c.CountBy(func(i colItem) string {
		if i.Score >= 80 {
			return "pass"
		}
		return "fail"
	})
	if counts["pass"] != 2 || counts["fail"] != 1 {
		t.Errorf("counts = %v", counts)
	}

	c2 := Collection[colItem]{{Name: "a", Score: 80}, {Name: "b", Score: 90}}
	if n := c2.CountWhere(func(i colItem) bool { return i.Score >= 80 }); n != 2 {
		t.Errorf("countWhere = %d, want 2", n)
	}
	if n := c2.CountWhere(func(i colItem) bool { return false }); n != 0 {
		t.Errorf("countWhere empty = %d", n)
	}
}

func TestCollectionCountWhere(t *testing.T) {
	c := testCol()
	if n := c.CountWhere(func(i colItem) bool { return i.Score >= 80 }); n != 2 {
		t.Errorf("countWhere = %d, want 2", n)
	}
	if n := c.CountWhere(func(i colItem) bool { return false }); n != 0 {
		t.Errorf("countWhere empty = %d", n)
	}
}

func TestCollectionCollapse(t *testing.T) {
	nested := Col([][]int{{1, 2}, {3}, nil})
	got := Collapse(nested)
	if len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Errorf("collapse = %#v", got)
	}
	// Collection[Collection[T]] 经 Map 转换后同样适用
	cols := Col([]Collection[string]{{"a"}, {"b", "c"}})
	flat := Collapse(cols.Map(func(c Collection[string]) []string { return c.Slice() }))
	if len(flat) != 3 || flat[2] != "c" {
		t.Errorf("collapse collections = %#v", flat)
	}
}

func TestCollectionFlipKeysPad(t *testing.T) {
	f := Flip(Col([]string{"a", "b", "a"}))
	if len(f) != 2 || f["a"] != 0 || f["b"] != 1 {
		t.Errorf("flip = %#v", f)
	}

	keys := testCol().Keys()
	if len(keys) != 3 || keys[0] != 0 || keys[2] != 2 {
		t.Errorf("keys = %#v", keys)
	}

	padded := Col([]int{1, 2}).Pad(4, 0)
	if len(padded) != 4 || padded[2] != 0 || padded[3] != 0 {
		t.Errorf("pad end = %#v", padded)
	}
	paddedFront := Col([]int{1, 2}).Pad(-4, 9)
	if len(paddedFront) != 4 || paddedFront[0] != 9 || paddedFront[1] != 9 || paddedFront[2] != 1 {
		t.Errorf("pad front = %#v", paddedFront)
	}
	// PHP array_pad：|size| <= len 原样返回（不截断）
	if got := Col([]int{1, 2, 3}).Pad(2, 0); len(got) != 3 || got[2] != 3 {
		t.Errorf("pad no-truncate = %#v", got)
	}
}

func TestCollectionGetDictionary(t *testing.T) {
	dict := testCol().GetDictionary()
	if len(dict) != 3 {
		t.Fatalf("dictionary = %d", len(dict))
	}
	if item, ok := dict["2"]; !ok || item.Name != "B" {
		t.Errorf("dict[2] = %#v ok=%v", item, ok)
	}
	// 非模型元素：以元素本身为键
	dict2 := Flip(Col([]string{"x"}))
	if _, ok := dict2["x"]; !ok {
		t.Error("non-model dictionary key missing")
	}
}

// ---- Collection 模型上下文方法（ToQuery/Fresh/hidden 族）----

type hiddenItem struct {
	ID    int64  `torm:"primary_key;column:id"`
	Name  string `torm:"column:name"`
	Token string `torm:"column:token"`
}

func TestCollectionToQuery(t *testing.T) {
	conn, _ := newFakeDB(t)
	items := Col([]*colItem{{ID: 1}, {ID: 2}})
	b, err := items.ToQuery(conn)
	if err != nil {
		t.Fatal(err)
	}
	// 首模型的等价 builder
	if got := b.ToSql(); got != "select * from `col_item`" {
		t.Errorf("toQuery sql = %s", got)
	}

	// 空集合 / nil 连接 / 非模型元素报错
	if _, err = (Collection[*colItem]{}).ToQuery(conn); err == nil {
		t.Error("empty collection should error")
	}
	if _, err = items.ToQuery(nil); err == nil {
		t.Error("nil connection should error")
	}
	if _, err = Col([]int{1}).ToQuery(conn); err == nil {
		t.Error("non-model elements should error")
	}
}

func TestCollectionFresh(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `col_item` where `id` = ? limit 1", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:    [][]driver.Value{{int64(1), "A2"}},
	})
	st.On("select * from `col_item` where `id` = ? limit 1", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:    [][]driver.Value{}, // 第二个模型行已消失
	})

	items := Col([]*colItem{{ID: 1, Name: "A"}, {ID: 2, Name: "B"}})
	// Fresh 仅对已持久化模型重查（对应 Laravel fresh 的 exists 判定）
	setExists(items[0], true, false)
	setExists(items[1], true, false)
	fresh, err := items.Fresh(conn)
	if err != nil {
		t.Fatal(err)
	}
	// 行消失的元素保留 nil 占位（对应 Laravel Eloquent Collection.php:1571
	// 的 null 键位语义）；原集合不变
	if len(fresh) != 2 || fresh[0] == nil || fresh[0].Name != "A2" || fresh[0] == items[0] {
		t.Errorf("fresh = %#v", fresh)
	}
	if fresh[1] != nil {
		t.Errorf("missing row should be nil placeholder, got %#v", fresh[1])
	}
	if len(items) != 2 || items[0].Name != "A" {
		t.Errorf("original mutated = %#v", items)
	}
	// 原集合元素指针未被刷新（Laravel fresh 语义：返回新实例）
	if items[0].Name != "A" {
		t.Errorf("original[0] = %#v", items[0])
	}
}

func TestCollectionHiddenVisibility(t *testing.T) {
	items := Col([]*hiddenItem{{ID: 1, Name: "A", Token: "t"}, {ID: 2, Name: "B", Token: "t2"}})

	// MakeHidden：类型级 hidden 注册表生效
	if _, err := items.MakeHidden("token"); err != nil {
		t.Fatal(err)
	}
	if !IsHidden(items[0], "token") {
		t.Error("token should be hidden")
	}
	visible, err := items.GetVisible()
	if err != nil {
		t.Fatal(err)
	}
	if len(visible) != 2 || visible[0] != "id" || visible[1] != "name" {
		t.Errorf("getVisible = %#v", visible)
	}

	// SetHidden：整体替换
	if _, err := items.SetHidden("name"); err != nil {
		t.Fatal(err)
	}
	if IsHidden(items[0], "token") || !IsHidden(items[1], "name") {
		t.Errorf("setHidden = token hidden %v, name hidden %v", IsHidden(items[0], "token"), IsHidden(items[1], "name"))
	}

	// MergeHidden：追加
	if _, err := items.MergeHidden("id"); err != nil {
		t.Fatal(err)
	}
	if !IsHidden(items[0], "id") || !IsHidden(items[0], "name") {
		t.Error("mergeHidden should append")
	}

	// MakeVisible：解除
	if _, err := items.MakeVisible("name", "token", "id"); err != nil {
		t.Fatal(err)
	}
	if IsHidden(items[0], "name") {
		t.Error("makeVisible should clear")
	}

	// 空集合 / 非模型元素
	if _, err := (Collection[*hiddenItem]{}).MakeHidden("x"); err != nil {
		t.Errorf("empty collection = %v", err)
	}
	if _, err := Col([]int{1}).MakeHidden("x"); err == nil {
		t.Error("non-model elements should error")
	}
}

// ★ Fresh null 占位补充：未持久化模型不查库、保留零值占位
func TestCollectionFreshPlaceholderUnpersisted(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from `col_item` where `id` = ? limit 1", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:    [][]driver.Value{}, // 行未命中
	})
	items := Col([]*colItem{{ID: 1, Name: "A"}, {ID: 2, Name: "B"}})
	// items[0] 未持久化（不发查询）、items[1] 已持久化但行未命中
	setExists(items[1], true, false)
	fresh, err := items.Fresh(conn)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) != 2 || fresh[0] != nil || fresh[1] != nil {
		t.Errorf("fresh = %#v", fresh)
	}
}
