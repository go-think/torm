package torm

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-think/torm/field"
	"github.com/go-think/torm/grammar"
	"github.com/go-think/torm/utils"
)

// Model 是内嵌属性集合（兼容旧用法）。
type Model struct {
	field.IDAttr
	field.CreatedAtAttr
	field.UpdatedAtAttr
}

// SoftDeletes 是软删除内嵌属性（兼容旧用法）。
type SoftDeletes struct {
	field.DeletedAtAttr
}

// modelInfo 模型元数据（对应 Laravel Model 的 $table/$primaryKey/$timestamps 等）。
type modelInfo struct {
	table          string
	primaryKey     string
	hasTimestamps  bool
	softDeletes    bool
	deletedAtField string
}

// typeMeta 按模型类型缓存的元数据 + 质量赋值/隐藏字段状态
// （对应 Laravel 模型的 $fillable/$guarded/$hidden 属性）。
type typeMeta struct {
	info     modelInfo
	fillable []string
	guarded  []string
	hidden   []string
}

var (
	typeMetaMu sync.RWMutex
	typeMetas  = map[reflect.Type]*typeMeta{}
)

// metaFor 取（并按需构建）模型类型的元数据。
func metaFor(model interface{}) *typeMeta {
	t := reflect.TypeOf(model)
	if t == nil {
		panic("torm: nil model")
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		panic("unsupported model, should be slice or struct")
	}
	typeMetaMu.RLock()
	m, ok := typeMetas[t]
	typeMetaMu.RUnlock()
	if ok {
		return m
	}
	m = buildTypeMeta(t)
	typeMetaMu.Lock()
	if existing, ok := typeMetas[t]; ok {
		m = existing
	} else {
		typeMetas[t] = m
	}
	typeMetaMu.Unlock()
	return m
}

// buildTypeMeta 反射解析模型类型：表名/主键/软删/时间戳开关 + fillable/guarded/hidden tag。
func buildTypeMeta(t reflect.Type) *typeMeta {
	m := &typeMeta{info: modelInfo{primaryKey: "id"}}

	probe := reflect.New(t).Interface()
	if tn, ok := probe.(TableName); ok {
		m.info.table = tn.TableName()
	} else {
		m.info.table = utils.SnakeCase(t.Name())
	}

	schema, err := NewSchema(probe)
	if err != nil {
		return m
	}
	for _, f := range schema.Fields {
		switch f.Name {
		case "created_at", "updated_at":
			m.info.hasTimestamps = true
		case "deleted_at":
			m.info.softDeletes = true
			m.info.deletedAtField = f.Name
		}
		// struct tag torm:"timestamps=false"（可挂任意字段，配 "-" 忽略该字段本身）
		if v, ok := f.GetAttr("timestamps"); ok && strings.EqualFold(strings.TrimSpace(v), "false") {
			m.info.hasTimestamps = false
		}
		if _, ok := f.GetAttr("fillable"); ok {
			m.fillable = append(m.fillable, tagNames(f, "fillable")...)
		}
		if _, ok := f.GetAttr("guarded"); ok {
			m.guarded = append(m.guarded, tagNames(f, "guarded")...)
		}
		if _, ok := f.GetAttr("hidden"); ok {
			m.hidden = append(m.hidden, tagNames(f, "hidden")...)
		}
		if f.Primary {
			m.info.primaryKey = f.Name
		}
	}
	return m
}

// tagNames 解析 fillable/guarded/hidden tag：空值 = 字段名本身，
// 否则逗号分隔的列名列表（对应 Laravel 的数组属性）。
func tagNames(f *Field, key string) []string {
	v, _ := f.GetAttr(key)
	v = strings.TrimSpace(v)
	if v == "" {
		return []string{f.Name}
	}
	names := strings.Split(v, ",")
	for i := range names {
		names[i] = strings.TrimSpace(names[i])
	}
	return names
}

// inspectModel 反射解析模型元数据（命中类型缓存）。
func inspectModel(model interface{}) *modelInfo {
	info := &metaFor(model).info

	kind := reflect.Indirect(reflect.ValueOf(model)).Kind()
	if kind != reflect.Struct {
		panic("unsupported model, should be slice or struct")
	}

	return info
}

// setModelAttrs 按列名把值写入模型字段。
func setModelAttrs(model interface{}, attrs map[string]interface{}) error {
	schema, err := NewSchema(model)
	if err != nil {
		return err
	}
	for _, f := range schema.Fields {
		if v, ok := attrs[f.Name]; ok {
			if err := f.SetValue(v); err != nil {
				return err
			}
		}
	}
	return nil
}

// nowFunc 可测试的时间源。
var nowFunc = time.Now

// ---- 模型状态表：original 快照 / 变更集 / exists 标记 ----
//
// 对应 Laravel 的 original/isDirty/syncOriginal/getChanges/exists。
// torm 模型是普通结构体，没有基类可挂 $original；这里以模型指针为键在连接层
// 之外维护一张全局状态表（互斥锁保护）：水合（fireRetrieved）与写入成功
// （Create/Save/软删）之后固化 original，Save 时对比得到 dirty 列。
//
// 并发与生命周期取舍：快照键持有模型强引用，进程内不自动回收（与
// Laravel 每请求重建对象不同）；同指针重复水合/保存会覆盖旧快照，
// ForgetModel 可显式释放。所有访问经 modelStateMu 串行化。
type modelState struct {
	original           map[string]any
	changes            map[string]any
	exists             bool
	wasRecentlyCreated bool
}

var (
	modelStateMu sync.Mutex
	// modelStates 以模型指针（*T）为键保存最近一次同步的状态。
	modelStates = map[any]*modelState{}
)

// stateOf 只读获取模型状态（不创建）。
func stateOf(model any) *modelState {
	modelStateMu.Lock()
	defer modelStateMu.Unlock()
	return modelStates[model]
}

// setModelState 原子地获取或创建状态并应用 fn。
func setModelState(model any, fn func(s *modelState)) {
	modelStateMu.Lock()
	defer modelStateMu.Unlock()
	s, ok := modelStates[model]
	if !ok {
		s = &modelState{}
		modelStates[model] = s
	}
	fn(s)
}

// ForgetModel 释放模型的状态快照（对应 Laravel unset 生命周期；
// 状态表以模型指针为键持强引用，长生命周期进程可显式调用防泄漏）。
func ForgetModel(model any) {
	modelStateMu.Lock()
	defer modelStateMu.Unlock()
	delete(modelStates, model)
}

// syncOriginal 以模型当前属性整体刷新快照（对应 Laravel syncOriginal）。
// 快照存 comparable 值（cast 字段为写入侧表示），保证脏对比两侧口径一致。
func syncOriginal(model any) {
	snap := buildOriginalSnapshot(model)
	if snap == nil {
		return
	}
	setModelState(model, func(s *modelState) { s.original = snap })
}

// buildOriginalSnapshot 取当前属性的 comparable 快照。
func buildOriginalSnapshot(model any) map[string]any {
	schema, err := NewSchema(model)
	if err != nil {
		return nil
	}
	snap := make(map[string]any, len(schema.Fields))
	for _, f := range schema.Fields {
		if f.Ignored {
			continue
		}
		snap[f.Name] = comparableFieldValue(f)
	}
	return snap
}

// syncOriginalAttributes 局部刷新快照的指定列（对应 Laravel syncOriginalAttributes）。
// 模型尚无快照时退化为整体同步（此时所有列的当前值即"已持久化"状态）。
func syncOriginalAttributes(model any, names ...string) {
	if stateOf(model) == nil {
		syncOriginal(model)
		return
	}
	schema, err := NewSchema(model)
	if err != nil {
		return
	}
	byName := map[string]*Field{}
	for _, f := range schema.Fields {
		byName[f.Name] = f
	}
	for _, name := range names {
		if f, ok := byName[name]; ok && !f.Ignored {
			v := comparableFieldValue(f)
			setModelState(model, func(s *modelState) {
				if s.original == nil {
					s.original = map[string]any{}
				}
				s.original[name] = v
			})
		}
	}
}

// recordChanges 记录最近一次写操作的变更集（对应 Laravel syncChanges）。
func recordChanges(model any, changes map[string]any) {
	setModelState(model, func(s *modelState) { s.changes = changes })
}

// ---- 脏跟踪导出 API（对应 Laravel HasAttributes 的 dirty/clean 族）----

// IsDirty 判断模型（或指定列）相对 original 是否有未持久化的变更。
func IsDirty(model any, keys ...string) bool {
	if len(keys) == 0 {
		return len(dirtyAttributes(model)) > 0
	}
	for _, k := range keys {
		if isAttributeDirty(model, k) {
			return true
		}
	}
	return false
}

// IsClean 是 IsDirty 的否定（对应 Laravel isClean）。
func IsClean(model any, keys ...string) bool {
	return !IsDirty(model, keys...)
}

// GetDirty 返回相对 original 的脏列（对应 Laravel getDirty）。
func GetDirty(model any) map[string]any {
	return dirtyAttributes(model)
}

// GetOriginal 返回列的 original 值；无快照时退回当前值（对应 Laravel getOriginal）。
// 不传 key 时返回整个 original map 的副本（对应 HasAttributes.php:2084 的
// 无 $key 全量形态）；无快照时退回当前属性全量快照。
func GetOriginal(model any, keys ...string) any {
	if len(keys) == 0 {
		if s := stateOf(model); s != nil && s.original != nil {
			out := make(map[string]any, len(s.original))
			for k, v := range s.original {
				out[k] = v
			}
			return out
		}
		return buildOriginalSnapshot(model)
	}
	key := keys[0]
	if s := stateOf(model); s != nil && s.original != nil {
		if v, ok := s.original[key]; ok {
			return v
		}
	}
	schema, err := NewSchema(model)
	if err != nil {
		return nil
	}
	for _, f := range schema.Fields {
		if f.Name == key && !f.Ignored {
			return comparableFieldValue(f)
		}
	}
	return nil
}

// GetChanges 返回最近一次成功更新写库的变更集（对应 Laravel getChanges）。
func GetChanges(model any) map[string]any {
	s := stateOf(model)
	if s == nil || s.changes == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(s.changes))
	for k, v := range s.changes {
		out[k] = v
	}
	return out
}

// WasChanged 判断最近一次更新是否有（指定列的）实际写库变更（对应 Laravel wasChanged）。
func WasChanged(model any, keys ...string) bool {
	s := stateOf(model)
	if s == nil || len(s.changes) == 0 {
		return false
	}
	if len(keys) == 0 {
		return true
	}
	for _, k := range keys {
		if _, ok := s.changes[k]; ok {
			return true
		}
	}
	return false
}

// SyncOriginal 把当前属性整体固化为 original（对应 Laravel syncOriginal）。
func SyncOriginal(model any) { syncOriginal(model) }

// SyncChanges 依据 original 重算变更集（对应 Laravel syncChanges）。
func SyncChanges(model any) {
	if s := stateOf(model); s == nil || s.original == nil {
		recordChanges(model, nil)
		return
	}
	recordChanges(model, dirtyAttributes(model))
}

// DiscardChanges 把属性回滚到 original（对应 Laravel discardChanges）；
// 无快照的新模型清零全部字段。json 快照为序列化字符串，回滚时反序列化还原。
func DiscardChanges(model any) error {
	schema, err := NewSchema(model)
	if err != nil {
		return err
	}
	s := stateOf(model)
	if s == nil || s.original == nil {
		for _, f := range schema.Fields {
			if f.Ignored {
				continue
			}
			if err := f.SetValue(nil); err != nil {
				return err
			}
		}
		return nil
	}
	for _, f := range schema.Fields {
		if f.Ignored {
			continue
		}
		v, ok := s.original[f.Name]
		if !ok {
			continue
		}
		if err := f.SetValue(v); err != nil {
			// json cast 的快照是序列化字符串，无法直接反射赋值 → 反序列化还原
			if str, isStr := v.(string); isStr && f.CastName == "json" {
				if uerr := json.Unmarshal([]byte(str), f.Value.Addr().Interface()); uerr != nil {
					return uerr
				}
				continue
			}
			return err
		}
	}
	return nil
}

// ---- exists / wasRecentlyCreated（对应 Laravel $exists/$wasRecentlyCreated）----

// Exists 判断模型是否对应一条已持久化的记录（水合或写入成功后为 true）。
func Exists(model any) bool {
	s := stateOf(model)
	return s != nil && s.exists
}

// WasRecentlyCreated 判断模型是否由最近一次 Create 写入。
func WasRecentlyCreated(model any) bool {
	s := stateOf(model)
	return s != nil && s.wasRecentlyCreated
}

// setExists 内部：更新 exists / wasRecentlyCreated 标记。
// setExistsKeepRecent 仅置 exists，不动 wasRecentlyCreated
// （Laravel restore() 不触碰该标志）。
func setExistsKeepRecent(model any) {
	setModelState(model, func(s *modelState) {
		s.exists = true
	})
}

func setExists(model any, exists, recent bool) {
	setModelState(model, func(s *modelState) {
		s.exists = exists
		s.wasRecentlyCreated = recent
	})
}

// ---- timestamps 开关（对应 Laravel HasTimestamps::withoutTimestamps）----

var (
	tsIgnoreMu sync.Mutex
	tsIgnoreN  int
)

// TimestampsIgnored 当前是否处于 WithoutTimestamps 作用域内。
func TimestampsIgnored() bool {
	tsIgnoreMu.Lock()
	defer tsIgnoreMu.Unlock()
	return tsIgnoreN > 0
}

// WithoutTimestamps 在 fn 执行期间禁用时间戳写入（Save/Create/软删均不写
// created_at/updated_at；支持嵌套）。注意：作用域计数是进程级的，与 Laravel
// 静态开关一致，作用域内不要并发执行其它模型的写入。
func WithoutTimestamps(fn func()) {
	tsIgnoreMu.Lock()
	tsIgnoreN++
	tsIgnoreMu.Unlock()
	defer func() {
		tsIgnoreMu.Lock()
		tsIgnoreN--
		tsIgnoreMu.Unlock()
	}()
	fn()
}

// snapshotFieldValue 复制字段当前值（指针字段解引用后深拷贝一层），
// 返回值既可安全用于后续对比，也可直接作为 SQL 绑定参数。
func snapshotFieldValue(f *Field) any {
	v := f.Value
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return nil
		}
		cp := reflect.New(v.Type().Elem())
		cp.Elem().Set(v.Elem())
		return cp.Interface()
	}
	cp := reflect.New(v.Type())
	cp.Elem().Set(v)
	return cp.Elem().Interface()
}

// comparableFieldValue 返回用于脏对比的字段值：cast 字段取写入侧表示
// （json 序列化字符串、date 格式化字符串等），两侧口径一致才能正确判脏。
func comparableFieldValue(f *Field) any {
	if cast, ok := f.WriteCast(); ok {
		return cast
	}
	return snapshotFieldValue(f)
}

// valuesEqual 对比两个快照值；time.Time 用 Equal（忽略时区/单调钟表示差异）。
func valuesEqual(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ta, tb := reflect.TypeOf(a), reflect.TypeOf(b)
	if ta != tb {
		return false
	}
	if t, ok := a.(time.Time); ok {
		return t.Equal(b.(time.Time))
	}
	return reflect.DeepEqual(a, b)
}

// dirtyAttributes 计算相对 original 的脏列（对应 Laravel getDirty）：
// 无快照时除主键/nil 指针外全部视为脏（与 Attributes() 的写入口径一致）。
func dirtyAttributes(model any) map[string]any {
	schema, err := NewSchema(model)
	if err != nil {
		return nil
	}
	var original map[string]any
	if s := stateOf(model); s != nil {
		original = s.original
	}
	hasOriginal := original != nil
	dirty := make(map[string]any)
	for _, f := range schema.Fields {
		if f.Ignored || f.Primary {
			continue
		}
		cur := comparableFieldValue(f)
		if !hasOriginal {
			if f.Value.Kind() == reflect.Ptr && f.Value.IsNil() {
				continue
			}
			dirty[f.Name] = cur
			continue
		}
		if old, ok := original[f.Name]; !ok || !valuesEqual(old, cur) {
			dirty[f.Name] = cur
		}
	}
	return dirty
}

// isAttributeDirty 判断单列是否相对 original 发生变化（对应 Laravel isDirty($key)）。
func isAttributeDirty(model any, name string) bool {
	schema, err := NewSchema(model)
	if err != nil {
		return false
	}
	var original map[string]any
	if s := stateOf(model); s != nil {
		original = s.original
	}
	hasOriginal := original != nil
	for _, f := range schema.Fields {
		if f.Name != name {
			continue
		}
		cur := comparableFieldValue(f)
		if !hasOriginal {
			return !f.Value.IsZero()
		}
		old, ok := original[name]
		return !ok || !valuesEqual(old, cur)
	}
	return false
}

// isModelTrashed 判断软删模型当前是否处于已删除状态（对应 Laravel trashed()）。
func isModelTrashed(model any, info *modelInfo) bool {
	if !info.softDeletes {
		return false
	}
	schema, err := NewSchema(model)
	if err != nil {
		return false
	}
	for _, f := range schema.Fields {
		if f.Name != info.deletedAtField {
			continue
		}
		if f.Value.Kind() == reflect.Ptr {
			return !f.Value.IsNil()
		}
		return !f.Value.IsZero()
	}
	return false
}

// setFieldTime 把时间写入字段（指针字段自动分配）。
func setFieldTime(f *Field, t time.Time) {
	if f.Value.Kind() == reflect.Ptr {
		rv := reflect.ValueOf(t).Convert(f.Value.Type().Elem())
		if f.Value.IsNil() {
			p := reflect.New(f.Value.Type().Elem())
			p.Elem().Set(rv)
			f.Value.Set(p)
			return
		}
		f.Value.Elem().Set(rv)
		return
	}
	f.Value.Set(reflect.ValueOf(t).Convert(f.Value.Type()))
}

// applyCreateTimestamps 在 creating 事件之后、insert 之前固化时间戳
// （对齐 Laravel updateTimestamps 的 create 分支：created_at/updated_at
// 仅在未被设置时写 now，保留用户显式赋值）。
func applyCreateTimestamps(model interface{}) {
	if TimestampsIgnored() || !inspectModel(model).hasTimestamps {
		return
	}
	schema, err := NewSchema(model)
	if err != nil {
		return
	}
	for _, f := range schema.Fields {
		if (f.Name != "created_at" && f.Name != "updated_at") || !f.Value.CanSet() {
			continue
		}
		if f.Value.IsZero() {
			setFieldTime(f, nowFunc())
		}
	}
}

// touchUpdatedAt 仅在 updated_at 未被用户修改时刷新为 now
// （对齐 Laravel updateTimestamps 的 update 分支：!isDirty(updatedAt) 才 set）；
// WithoutTimestamps 作用域内不触碰。
func touchUpdatedAt(model interface{}) {
	if TimestampsIgnored() || !inspectModel(model).hasTimestamps {
		return
	}
	schema, err := NewSchema(model)
	if err != nil {
		return
	}
	var original map[string]any
	if s := stateOf(model); s != nil {
		original = s.original
	}
	hasOriginal := original != nil
	for _, f := range schema.Fields {
		if f.Name != "updated_at" || !f.Value.CanSet() {
			continue
		}
		if hasOriginal {
			if old, ok := original["updated_at"]; ok && valuesEqual(old, comparableFieldValue(f)) {
				setFieldTime(f, nowFunc())
			}
			return
		}
		// 无快照：字段未设置（零值/nil）时写 now，用户已赋值则保留
		if f.Value.IsZero() {
			setFieldTime(f, nowFunc())
		}
		return
	}
}

// Model Begin a fluent query against a database Model.
// 含软删除全局 scope（deleted_at IS NULL）。
func (c *Connection) Model(model interface{}) *Builder {
	info := inspectModel(model)
	b := c.Table(info.table)
	b.modelInfo = info
	return b
}

// newModelQuery 模型内部路径使用的查询建器：等价 Laravel newModelQuery——
// 不注册全局 scope（软删行也参与 save/软删/restore 的 UPDATE）。
func (c *Connection) newModelQuery(model interface{}) *Builder {
	b := c.Model(model)
	b.modelInternalQuery = true
	return b
}

// Create Save a new model to the database（对应 Laravel save()+performInsert：
// saving → creating → 时间戳 → insert → created → saved）。
func (c *Connection) Create(model interface{}) error {
	if err := c.fireModelEvent(observerFor(model), EventSaving, model); err != nil {
		return err
	}
	return c.performCreate(model)
}

// performCreate 对应 Laravel performInsert：creating → 唯一 ID/时间戳 →
// INSERT → created → saved（saving 已由调用方触发，避免重复）。
func (c *Connection) performCreate(model interface{}) error {
	schema, err := NewSchema(model)
	if err != nil {
		return err
	}

	ob := observerFor(model)
	if err := c.fireModelEvent(ob, EventCreating, model); err != nil {
		return err
	}

	// creating 事件之后生成唯一 ID（对应 Laravel addUniqueIdentifiers），
	// 再固化时间戳，观察者对属性的修改会进入 SQL
	if err := applyUniqueIDs(schema); err != nil {
		return err
	}
	applyCreateTimestamps(model)
	attributes := schema.Attributes()
	if TimestampsIgnored() {
		// WithoutTimestamps 作用域：不写 created_at/updated_at（对齐 Laravel
		// usesTimestamps=false：列整体不参与 insert）
		delete(attributes, "created_at")
		delete(attributes, "updated_at")
	}

	insertId, _, err := c.Model(model).Insert(attributes)
	if err != nil {
		return err
	}

	// 自增回填仅在主键仍为空时生效：UUID/ULID 主键已在 insert 前生成，
	// 不能被 LastInsertId 覆盖（对应 Laravel incrementing 判定）
	if schema.PrimaryField == nil || schema.PrimaryField.IsBlank {
		if err := schema.SetId(insertId); err != nil {
			return err
		}
	}
	setExists(model, true, true)
	syncOriginal(model)

	if err := c.fireModelEvent(ob, EventCreated, model); err != nil {
		return err
	}
	return c.fireModelEvent(ob, EventSaved, model)
}

// Save Save the model to the database（对应 Laravel save()/performUpdate）：
// saving → 干净模型 short-circuit（仅 saved，不发 SQL 不触发 updating/updated，
// Model.php:1435-1436）；存在记录（exists）→ updating → 时间戳 → 脏列 UPDATE →
// updated；否则 → creating → 时间戳 → INSERT（对齐 Laravel 以 exists 而非主键
// 是否为空决定插入/更新）。
func (c *Connection) Save(model interface{}) error {
	schema, err := NewSchema(model)
	if err != nil {
		return err
	}

	info := inspectModel(model)
	ob := observerFor(model)

	if err := c.fireModelEvent(ob, EventSaving, model); err != nil {
		return err
	}

	// Laravel 先判 isDirty：干净的水合模型不发 SQL、不触发 updating/updated，
	// 但 saved 事件仍触发（finishSave）。
	dirty := dirtyAttributes(model)
	isExisting := Exists(model)

	if isExisting && len(dirty) == 0 {
		return c.fireModelEvent(ob, EventSaved, model)
	}

	if isExisting {
		// updating 事件先触发：观察者此时修改属性会生效
		if err := c.fireModelEvent(ob, EventUpdating, model); err != nil {
			return err
		}
		// updating 之后才固化时间戳（仅当 updated_at 未被修改）
		touchUpdatedAt(model)
		dirty = dirtyAttributes(model)
		if len(dirty) == 0 {
			// updating 观察者清空了变更：无 SQL，仍走 finishSave 语义
			if err := c.fireModelEvent(ob, EventSaved, model); err != nil {
				return err
			}
			syncOriginal(model)
			return nil
		}

		_, err = c.newModelQuery(model).
			Where(info.primaryKey, schema.PrimaryField.Value.Addr().Interface()).
			Update(dirty)
		if err != nil {
			return err
		}

		// 对齐 Laravel performUpdate：updated 事件前固化变更集，观察者可读 GetChanges
		recordChanges(model, dirty)

		if err := c.fireModelEvent(ob, EventUpdated, model); err != nil {
			return err
		}
		if err := c.fireModelEvent(ob, EventSaved, model); err != nil {
			return err
		}
		syncOriginal(model)
		return nil
	}

	// 未持久化：走 performInsert（Laravel save() 分支到 performInsert，手动
	// 设置主键的新模型同样插入；saving 已在本方法开头触发过一次）。
	return c.performCreate(model)
}

// Destroy 按主键硬删除模型行（对应 Laravel delete() 的硬删分支）；
// 主键为空时返回错误，绝不执行整表 DELETE。
func (c *Connection) Destroy(model interface{}) error {
	info := inspectModel(model)
	schema, err := NewSchema(model)
	if err != nil {
		return err
	}
	// Laravel Model::delete：主键检查在 exists 判断之前（:1741-1750）
	if schema.PrimaryField == nil || schema.PrimaryField.IsBlank {
		return errors.New("torm: cannot destroy model without primary key")
	}
	if !Exists(model) {
		// Laravel Model::delete: !exists → return
		return nil
	}
	ob := observerFor(model)
	if err := c.fireModelEvent(ob, EventDeleting, model); err != nil {
		return err
	}
	if _, err = c.Table(info.table).
		Where(info.primaryKey, schema.PrimaryField.Value.Addr().Interface()).
		Delete(); err != nil {
		return err
	}
	setExists(model, false, false)
	return c.fireModelEvent(ob, EventDeleted, model)
}

// DestroyByIDs 按主键批量删除（对应 Laravel 静态 destroy：逐个加载模型并
// 走完整删除生命周期，软删模型执行软删）。返回成功删除的个数。
func (c *Connection) DestroyByIDs(model interface{}, ids ...interface{}) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	info := inspectModel(model)
	count := 0
	for _, id := range ids {
		inst := newModelInstance(model)
		if inst == nil {
			return count, errors.New("unsupported model, should be struct")
		}
		if err := c.Model(model).Where(info.primaryKey, id).First(inst); err != nil {
			return count, err
		}
		if isEmptyDest(inst) {
			continue
		}
		if err := c.DeleteModel(inst); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// newModelInstance 创建与 model 同类型的新实例指针。
func newModelInstance(model interface{}) interface{} {
	t := reflect.TypeOf(model)
	if t == nil {
		return nil
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	return reflect.New(t).Interface()
}

// DeleteModel 删除模型：软删除模型置 deleted_at（同步 updated_at），
// 否则硬删除（命名避开 Connection.Delete 的原生 SQL 方法；对应 Laravel 模型 delete()
// 与 SoftDeletes::runSoftDelete：deleting → UPDATE → trashed → deleted）。
func (c *Connection) DeleteModel(model interface{}) error {
	info := inspectModel(model)
	// 非软删模型走硬删（对应 Laravel performDeleteOnModel 默认分支）
	if !info.softDeletes {
		return c.Destroy(model)
	}
	schema, err := NewSchema(model)
	if err != nil {
		return err
	}
	if schema.PrimaryField == nil || schema.PrimaryField.IsBlank {
		return errors.New("cannot delete model without primary key")
	}
	if !Exists(model) {
		// Laravel Model::delete: 模型不存在（未持久化或已删除）时静默返回
		return nil
	}
	ob := observerFor(model)
	if err := c.fireModelEvent(ob, EventDeleting, model); err != nil {
		return err
	}
	now := nowFunc()
	columns := map[string]interface{}{info.deletedAtField: now}
	if info.hasTimestamps && !TimestampsIgnored() {
		columns["updated_at"] = now
	}
	if err := setModelAttrs(model, columns); err != nil {
		return err
	}
	if _, err = c.newModelQuery(model).
		Where(info.primaryKey, schema.PrimaryField.Value.Addr().Interface()).
		Update(columns); err != nil {
		return err
	}
	syncOriginalAttributes(model, info.deletedAtField, "updated_at")
	// Laravel runSoftDelete 不改 exists（保持 true，SoftDeletes.php:137-158）；
	// 仅硬删路径（performDeleteOnModel forceDeleting 分支）置 exists=false。
	// 软删后 save() 仍走 UPDATE，不会产生重复行。
	if err := c.fireModelEvent(ob, EventTrashed, model); err != nil {
		return err
	}
	return c.fireModelEvent(ob, EventDeleted, model)
}

// ForceDelete 绕过软删除硬删除；主键为空时返回错误，绝不执行整表 DELETE。
func (c *Connection) ForceDelete(model interface{}) error {
	info := inspectModel(model)
	schema, err := NewSchema(model)
	if err != nil {
		return err
	}
	if schema.PrimaryField == nil || schema.PrimaryField.IsBlank {
		return errors.New("torm: cannot force delete model without primary key")
	}
	ob := observerFor(model)
	// Laravel forceDelete（SoftDeletes.php:51-66）：forceDeleting（可取消）→
	// delete()（触发 deleting/deleted，!exists 时静默返回）→ forceDeleted
	// 仅当删除真发生；硬删经 newModelQuery（软删行也参与）
	if err := c.fireModelEvent(ob, EventForceDeleting, model); err != nil {
		return err
	}
	if !Exists(model) {
		// delete() 对 !exists 静默返回 → forceDeleted 不触发
		return nil
	}
	if err := c.fireModelEvent(ob, EventDeleting, model); err != nil {
		return err
	}
	if _, err = c.newModelQuery(model).
		Where(info.primaryKey, schema.PrimaryField.Value.Addr().Interface()).
		ForceDelete(); err != nil {
		return err
	}
	setExists(model, false, false)
	if err := c.fireModelEvent(ob, EventDeleted, model); err != nil {
		return err
	}
	return c.fireModelEvent(ob, EventForceDeleted, model)
}

// Restore 恢复软删除的模型（对应 Laravel SoftDeletes::restore：
// Restoring → deleted_at 置空 + updated_at 刷新（经完整 save 生命周期）→ Restored）。
func (c *Connection) Restore(model interface{}) error {
	info := inspectModel(model)
	if !info.softDeletes {
		return nil
	}
	schema, err := NewSchema(model)
	if err != nil {
		return err
	}
	if schema.PrimaryField == nil || schema.PrimaryField.IsBlank {
		return errors.New("cannot restore model without primary key")
	}
	ob := observerFor(model)
	if err := c.fireModelEvent(ob, EventRestoring, model); err != nil {
		return err
	}
	if err := setModelAttrs(model, map[string]interface{}{info.deletedAtField: nil}); err != nil {
		return err
	}
	// Laravel restore(): exists=true before save so Save performs an UPDATE
	// （软删已把 exists 置 false）
	setExistsKeepRecent(model)
	if err := c.Save(model); err != nil {
		return err
	}
	return c.fireModelEvent(ob, EventRestored, model)
}

// whereModelAttributes 生成按 attributes 过滤的模型查询。
func (c *Connection) whereModelAttributes(model interface{}, attributes map[string]interface{}) *Builder {
	b := c.Model(model)
	for _, k := range sortedColumnNames(attributes) {
		b.Where(k, attributes[k])
	}
	return b
}

// mergeAttributeMaps 合并 attributes 与可选 values（后者覆盖前者）。
func mergeAttributeMaps(attributes map[string]interface{}, values []map[string]interface{}) map[string]interface{} {
	size := len(attributes)
	for _, v := range values {
		size += len(v)
	}
	merged := make(map[string]interface{}, size)
	for k, v := range attributes {
		merged[k] = v
	}
	for _, v := range values {
		for k, vv := range v {
			merged[k] = vv
		}
	}
	return merged
}

// FirstOrCreate 按 attributes 查找；未命中则合并 attributes+values 创建模型
// （对应 Laravel firstOrCreate(attributes, values)）。
func (c *Connection) FirstOrCreate(model interface{}, attributes map[string]interface{}, values ...map[string]interface{}) error {
	b := c.whereModelAttributes(model, attributes)
	if err := b.First(model); err != nil {
		return err
	}
	if isEmptyDest(model) {
		if err := setModelAttrs(model, mergeAttributeMaps(attributes, values)); err != nil {
			return err
		}
		return c.Create(model)
	}
	return nil
}

// FirstOrNew 按 attributes 查找；未命中仅以 attributes+values 构造不落库
// （对应 Laravel firstOrNew(attributes, values)）。
func (c *Connection) FirstOrNew(model interface{}, attributes map[string]interface{}, values ...map[string]interface{}) error {
	b := c.whereModelAttributes(model, attributes)
	if err := b.First(model); err != nil {
		return err
	}
	if isEmptyDest(model) {
		return setModelAttrs(model, mergeAttributeMaps(attributes, values))
	}
	return nil
}

// CreateOrFirst 先尝试创建，唯一约束冲突时回退按 attributes 查询
// （对应 Laravel createOrFirst 的并发安全语义）。
func (c *Connection) CreateOrFirst(model interface{}, attributes map[string]interface{}, values ...map[string]interface{}) error {
	if err := setModelAttrs(model, mergeAttributeMaps(attributes, values)); err != nil {
		return err
	}
	err := c.Create(model)
	if err == nil {
		return nil
	}
	var te *Error
	if !errors.As(err, &te) || te.Kind != KindUniqueConstraint {
		return err
	}
	// Laravel Builder.php:755：回退查询 useWritePdo()——读写分离 + 主从延迟
	// 场景必须走写连接才能读到刚冲突的行
	b := c.whereModelAttributes(model, attributes).UseWritePdo()
	fresh := newModelInstance(model)
	if err := b.First(fresh); err != nil {
		return err
	}
	if isEmptyDest(fresh) {
		// 冲突但查不到（如软删行占用唯一键）：返回原始冲突错误
		return te
	}
	copyModelInto(model, fresh)
	return nil
}

// RestoreOrCreate 软删模型版 firstOrCreate：withTrashed 查找，命中且已软删
// 则恢复（对应 Laravel SoftDeletingScope::restoreOrCreate 宏）。
func (c *Connection) RestoreOrCreate(model interface{}, attributes map[string]interface{}, values ...map[string]interface{}) error {
	b := c.whereModelAttributes(model, attributes)
	b.WithTrashed()
	if err := b.First(model); err != nil {
		return err
	}
	if isEmptyDest(model) {
		if err := setModelAttrs(model, mergeAttributeMaps(attributes, values)); err != nil {
			return err
		}
		if err := c.Create(model); err != nil {
			return err
		}
	}
	return c.Restore(model)
}

// CreateOrRestore 软删模型版 createOrFirst：先创建，唯一约束冲突时
// withTrashed 查回已存在行并恢复（对应 Laravel SoftDeletingScope::createOrRestore 宏）。
func (c *Connection) CreateOrRestore(model interface{}, attributes map[string]interface{}, values ...map[string]interface{}) error {
	if err := setModelAttrs(model, mergeAttributeMaps(attributes, values)); err != nil {
		return err
	}
	err := c.Create(model)
	if err != nil {
		var te *Error
		if !errors.As(err, &te) || te.Kind != KindUniqueConstraint {
			return err
		}
		b := c.whereModelAttributes(model, attributes)
		b.WithTrashed()
		fresh := newModelInstance(model)
		if err := b.First(fresh); err != nil {
			return err
		}
		if isEmptyDest(fresh) {
			return te
		}
		copyModelInto(model, fresh)
	}
	return c.Restore(model)
}

// copyModelInto 把 src 的扫描结果复制进 dst（同类型模型指针），并同步 dst 的快照。
func copyModelInto(dst, src interface{}) {
	dv := reflect.ValueOf(dst).Elem()
	sv := reflect.ValueOf(src).Elem()
	dv.Set(sv)
	syncOriginal(dst)
}

// UpdateOrCreate 按 attributes 查找；命中则更新 values，未命中则创建
// （对应 Laravel updateOrCreate(attributes, values)）。
func (c *Connection) UpdateOrCreate(model interface{}, attributes, values map[string]interface{}) error {
	b := c.whereModelAttributes(model, attributes)
	if err := b.First(model); err != nil {
		return err
	}
	if isEmptyDest(model) {
		if err := setModelAttrs(model, mergeAttributeMaps(attributes, []map[string]interface{}{values})); err != nil {
			return err
		}
		return c.Create(model)
	}
	if err := setModelAttrs(model, values); err != nil {
		return err
	}
	return c.Save(model)
}

// ---- 质量赋值守卫（对应 Laravel GuardsAttributes）----
//
// 声明方式（任选其一，按类型缓存）：
//   - 字段 tag：`torm:"fillable"` / `torm:"guarded"` / `torm:"guarded:*"`；
//   - 代码注册：SetFillable / SetGuarded（keys 为空 = 全部）。
//
// 语义对齐 Laravel：fillable 列表非空时仅列表内字段可填；否则按 guarded
// 排除；guarded == ["*"] 且无 fillable 时 TotallyGuarded。

// MassAssignmentError 对应 Laravel 的 MassAssignmentException：
// Fill 时试图写入被守卫的字段。
type MassAssignmentError struct {
	Key string
}

func (e *MassAssignmentError) Error() string {
	return "torm: mass assignment blocked on field [" + e.Key + "]"
}

// SetFillable 设置模型类型的可填充列（keys 为空 = 清空列表）。
func SetFillable(model any, keys ...string) {
	m := metaFor(model)
	typeMetaMu.Lock()
	m.fillable = append([]string(nil), keys...)
	typeMetaMu.Unlock()
}

// SetGuarded 设置模型类型的守卫列；不传 keys 等价 ["*"]（全部守卫）。
func SetGuarded(model any, keys ...string) {
	m := metaFor(model)
	if len(keys) == 0 {
		keys = []string{"*"}
	}
	typeMetaMu.Lock()
	m.guarded = append([]string(nil), keys...)
	typeMetaMu.Unlock()
}

// IsGuarded 判断列是否被守卫（对应 Laravel isGuarded）。
func IsGuarded(model any, key string) bool {
	m := metaFor(model)
	typeMetaMu.RLock()
	defer typeMetaMu.RUnlock()
	for _, g := range m.guarded {
		if g == "*" || g == key {
			return true
		}
	}
	return false
}

// IsFillable 判断列是否可被 Fill 赋值（对应 Laravel isFillable）。
func IsFillable(model any, key string) bool {
	m := metaFor(model)
	typeMetaMu.RLock()
	fillable := m.fillable
	typeMetaMu.RUnlock()
	for _, f := range fillable {
		if f == key {
			return true
		}
	}
	if IsGuarded(model, key) {
		return false
	}
	return len(fillable) == 0 && !TotallyGuarded(model)
}

// TotallyGuarded 判断模型是否完全守卫（无 fillable 且 guarded == ["*"]）。
func TotallyGuarded(model any) bool {
	m := metaFor(model)
	typeMetaMu.RLock()
	defer typeMetaMu.RUnlock()
	if len(m.fillable) > 0 {
		return false
	}
	for _, g := range m.guarded {
		if g == "*" {
			return true
		}
	}
	return false
}

// Fill 按守卫规则批量赋值（对应 Laravel fill）：遇到不可填字段返回
// *MassAssignmentException 语义的 *MassAssignmentError，已过滤字段不写入。
func Fill(model any, attrs map[string]any) error {
	allowed := make(map[string]any, len(attrs))
	for _, k := range sortedColumnNames(attrs) {
		if !IsFillable(model, k) {
			return &MassAssignmentError{Key: k}
		}
		allowed[k] = attrs[k]
	}
	return setModelAttrs(model, allowed)
}

// ForceFill 绕过守卫批量赋值（对应 Laravel forceFill）。
func ForceFill(model any, attrs map[string]any) error {
	return setModelAttrs(model, attrs)
}

// ---- 隐藏字段（对应 Laravel HidesAttributes）----
//
// torm 模型是普通结构体，暂无统一的 Map/ToJSON 输出路径，这里维护
// 每类型的 hidden 注册表（tag `torm:"hidden"` 声明，MakeHidden/MakeVisible
// 运行时增删），供后续序列化路径过滤使用。

// GetHidden 返回隐藏列列表（副本）。
func GetHidden(model any) []string {
	m := metaFor(model)
	typeMetaMu.RLock()
	defer typeMetaMu.RUnlock()
	return append([]string(nil), m.hidden...)
}

// MakeHidden 追加隐藏列（对应 Laravel makeHidden）。
func MakeHidden(model any, keys ...string) {
	m := metaFor(model)
	typeMetaMu.Lock()
	defer typeMetaMu.Unlock()
	for _, k := range keys {
		if !containsString(m.hidden, k) {
			m.hidden = append(m.hidden, k)
		}
	}
}

// MakeVisible 解除隐藏列（对应 Laravel makeVisible）。
func MakeVisible(model any, keys ...string) {
	m := metaFor(model)
	typeMetaMu.Lock()
	defer typeMetaMu.Unlock()
	kept := m.hidden[:0]
	for _, h := range m.hidden {
		if !containsString(keys, h) {
			kept = append(kept, h)
		}
	}
	m.hidden = kept
}

// IsHidden 判断列是否隐藏。
func IsHidden(model any, key string) bool {
	m := metaFor(model)
	typeMetaMu.RLock()
	defer typeMetaMu.RUnlock()
	return containsString(m.hidden, key)
}

func containsString(list []string, key string) bool {
	for _, s := range list {
		if s == key {
			return true
		}
	}
	return false
}

// ---- Quietly / OrFail 族（对应 Laravel saveQuietly/saveOrFail 等）----
//
// Quietly = 抑制观察者钩子与事件分发；OrFail = 失败时以 *FailError 包装
// 原始 error panic（对齐 Laravel orFail 的 FailException 语义，可 recover
// 后经 Unwrap 取回原始错误）。

// FailError 对应 Laravel 的 FailException：OrFail 族失败时的 panic 载荷。
type FailError struct {
	Op  string
	Err error
}

func (e *FailError) Error() string { return "torm: " + e.Op + " or fail: " + e.Err.Error() }
func (e *FailError) Unwrap() error { return e.Err }

var (
	quietMu    sync.Mutex
	quietDepth int
)

// eventsQuiet 当前是否处于 withoutEvents 作用域内（观察者与事件全部抑制）。
func eventsQuiet() bool {
	quietMu.Lock()
	defer quietMu.Unlock()
	return quietDepth > 0
}

// withoutEvents 在 fn 执行期间抑制本连接的事件分发与全部模型观察者
// （对应 Laravel Model::withoutEvents）。抑制计数是进程级的（同步执行、
// 作用域极短），作用域内其它 goroutine 的事件也会被抑制——与 Laravel
// 静态 withoutEvents 的取舍一致。
func (c *Connection) withoutEvents(fn func() error) error {
	quietMu.Lock()
	quietDepth++
	saved := c.dispatcher
	c.dispatcher = nil
	quietMu.Unlock()
	defer func() {
		quietMu.Lock()
		quietDepth--
		quietMu.Unlock()
		c.dispatcher = saved
	}()
	return fn()
}

// orFail 包装 Laravel orFail 语义：err 非 nil 时以 *FailError panic。
func orFail(op string, err error) {
	if err != nil {
		panic(&FailError{Op: op, Err: err})
	}
}

// SaveQuietly 保存模型但不触发任何事件（对应 Laravel saveQuietly）。
func (c *Connection) SaveQuietly(model any) error {
	return c.withoutEvents(func() error { return c.Save(model) })
}

// UpdateModel 模型级批量赋值更新（对应 Laravel Model::update = fill + save）：
// Fill 走守卫规则，Save 的干净保存语义（无脏列不发 SQL）自动生效。
// 命名避开 Connection.Update（原生 SQL 方法），与 DestroyByIDs 先例一致。
func (c *Connection) UpdateModel(model any, attrs map[string]any) error {
	if err := Fill(model, attrs); err != nil {
		return err
	}
	return c.Save(model)
}

// Push 保存模型及其全部已加载关系（递归，对应 Laravel push）：
// 关系取 LoadedRelation 缓存（Load/LoadMany 显式加载的结果），
// 逐个递归 Push；已访问模型跳过（对应 Laravel withoutRecursion 防环）。
func (c *Connection) Push(model any) error {
	return c.push(model, map[uintptr]bool{})
}

// PushQuietly 保存模型与已加载关系但不触发任何事件（对应 Laravel pushQuietly）。
func (c *Connection) PushQuietly(model any) error {
	return c.withoutEvents(func() error { return c.Push(model) })
}

func (c *Connection) push(model any, visiting map[uintptr]bool) error {
	rv := reflect.ValueOf(model)
	if model == nil || rv.Kind() != reflect.Ptr || rv.IsNil() {
		return nil
	}
	key := rv.Pointer()
	if visiting[key] {
		return nil
	}
	visiting[key] = true
	if err := c.Save(model); err != nil {
		return err
	}
	for _, name := range loadedRelationNames(model) {
		models, ok := LoadedRelation(model, name)
		if !ok {
			continue
		}
		for _, m := range models {
			if err := c.push(m, visiting); err != nil {
				return err
			}
		}
	}
	return nil
}

// loadedRelationNames 列出模型已加载的全部关系名（LoadedRelation 缓存键）。
func loadedRelationNames(model any) []string {
	loadedRelationMu.RLock()
	defer loadedRelationMu.RUnlock()
	m, ok := loadedRelations[model]
	if !ok {
		return nil
	}
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// SaveOrFail 事务内保存模型，失败 panic *FailError（对应 Laravel saveOrFail）。
func (c *Connection) SaveOrFail(model any) error {
	orFail("save", c.Transaction(func(tx *Connection) error { return tx.Save(model) }))
	return nil
}

// UpdateQuietly 批量赋值后静默保存。
func (c *Connection) UpdateQuietly(model any, attrs map[string]any) error {
	return c.withoutEvents(func() error {
		if err := setModelAttrs(model, attrs); err != nil {
			return err
		}
		return c.Save(model)
	})
}

// CreateQuietly 创建模型但不触发任何事件（对应 Laravel createQuietly）。
func (c *Connection) CreateQuietly(model any) error {
	return c.withoutEvents(func() error { return c.Create(model) })
}

// CreateManyQuietly 以 model 为原型批量静默创建（每行一个新实例）。
func (c *Connection) CreateManyQuietly(model any, rows []map[string]any) error {
	return c.withoutEvents(func() error {
		for _, row := range rows {
			inst := newModelInstance(model)
			if inst == nil {
				return errors.New("torm: unsupported model, should be struct")
			}
			if err := setModelAttrs(inst, row); err != nil {
				return err
			}
			if err := c.Create(inst); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteQuietly 删除模型但不触发任何事件（对应 Laravel deleteQuietly）。
func (c *Connection) DeleteQuietly(model any) error {
	return c.withoutEvents(func() error { return c.DeleteModel(model) })
}

// DeleteOrFail 删除模型，失败 panic *FailError（对应 Laravel deleteOrFail）。
func (c *Connection) DeleteOrFail(model any) error {
	orFail("delete", c.DeleteModel(model))
	return nil
}

// RestoreQuietly 恢复软删模型但不触发任何事件（对应 Laravel restoreQuietly）。
func (c *Connection) RestoreQuietly(model any) error {
	return c.withoutEvents(func() error { return c.Restore(model) })
}

// ForceDeleteQuietly 硬删模型但不触发任何事件。
func (c *Connection) ForceDeleteQuietly(model any) error {
	return c.withoutEvents(func() error { return c.ForceDelete(model) })
}

// Touch 以 now 刷新 updated_at 后走完整 save 生命周期（对应 Laravel touch）。
func (c *Connection) Touch(model any) error {
	return c.touch(model)
}

// TouchQuietly 刷新 updated_at 但不触发任何事件。
func (c *Connection) TouchQuietly(model any) error {
	return c.withoutEvents(func() error { return c.touch(model) })
}

func (c *Connection) touch(model any) error {
	schema, err := NewSchema(model)
	if err != nil {
		return err
	}
	if schema.PrimaryField == nil || schema.PrimaryField.IsBlank {
		return errors.New("torm: cannot touch model without primary key")
	}
	for _, f := range schema.Fields {
		if f.Name == "updated_at" && f.Value.CanSet() {
			setFieldTime(f, nowFunc())
			break
		}
	}
	return c.Save(model)
}

// ---- fresh / refresh（对应 Laravel Model.php:2089-2135）----
//
// 两者都用 newQueryWithoutScopes 语义（不带软删等全局 scope）按主键重查，
// 走写连接（useWritePdo）。

// Fresh 按主键重查一个全新实例（对应 Laravel fresh）：原模型保持不变；
// 模型未持久化或行未命中返回 nil。
func (c *Connection) Fresh(model any) (any, error) {
	if !Exists(model) {
		return nil, nil
	}
	schema, err := NewSchema(model)
	if err != nil {
		return nil, err
	}
	if schema.PrimaryField == nil || schema.PrimaryField.IsBlank {
		return nil, nil
	}
	info := inspectModel(model)
	inst := newModelInstance(model)
	b := c.Model(model).WithTrashed().UseWritePdo().
		Where(info.primaryKey, schema.PrimaryField.Value.Interface())
	if err := b.First(inst); err != nil {
		return nil, err
	}
	if isEmptyDest(inst) {
		return nil, nil
	}
	return inst, nil
}

// Refresh 重查并刷新原模型属性（对应 Laravel refresh）：按主键重查
// （不带软删 scope，已软删模型同样可刷新），把查得属性整体覆盖进原模型
// 并同步 original 快照；行已消失返回 *ErrNotFound（firstOrFail 语义）。
func (c *Connection) Refresh(model any) error {
	if !Exists(model) {
		return nil
	}
	schema, err := NewSchema(model)
	if err != nil {
		return err
	}
	if schema.PrimaryField == nil || schema.PrimaryField.IsBlank {
		return errors.New("torm: cannot refresh model without primary key")
	}
	info := inspectModel(model)
	inst := newModelInstance(model)
	b := c.Model(model).WithTrashed().UseWritePdo().
		Where(info.primaryKey, schema.PrimaryField.Value.Interface())
	if err := b.First(inst); err != nil {
		return err
	}
	if isEmptyDest(inst) {
		return ErrNotFound{Msg: "No query results for record."}
	}
	copyModelInto(model, inst)
	return nil
}

// ---- 模型实例级关系聚合（对应 Laravel Model.php:900-1050 loadAggregate 族）----
//
// 单模型按主键查询 (select fn(col) from target where keys...) as 别名，
// 把聚合值写回模型同名属性字段（如 posts_count；字段缺失时静默跳过）。

// LoadAggregate 通用关系聚合加载（对应 Laravel loadAggregate）：
// function/column 组合如 ("count","*") / ("sum","votes")；rest 支持
// 约束闭包、嵌套 Relation 与 string 别名（与 Builder.WithAggregate 一致）。
func (c *Connection) LoadAggregate(model any, name string, rel Relation, function, column string, rest ...any) error {
	schema, err := NewSchema(model)
	if err != nil {
		return err
	}
	if schema.PrimaryField == nil {
		return errors.New("torm: cannot load aggregate without primary key")
	}
	info := inspectModel(model)
	b := c.Model(model).Where(info.primaryKey, schema.PrimaryField.Value.Interface())
	b.withAggregate(name, rel, function, column, rest...)
	b.applyModelScopes()
	b.Take(1)
	rows, err := c.SelectMaps(b.ToSql(), b.GetBindings())
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	for _, k := range sortedColumnNames(rows[0]) {
		v := rows[0][k]
		// bool 属性回填：exists 聚合经驱动返回整型 0/1（对应 Laravel 的布尔 cast）
		if f := schemaField(schema, k); f != nil && f.Value.Kind() == reflect.Bool {
			if n, ok := toIncrementAmount(v); ok {
				v = n != 0
			}
		}
		// 模型无同名属性字段时静默跳过（Go 结构体无动态属性的取舍）
		_ = setModelAttribute(model, k, v)
	}
	return nil
}

// LoadCount 加载关系计数到模型 {relation}_count 属性（对应 Laravel loadCount）。
func (c *Connection) LoadCount(model any, name string, rel Relation, rest ...any) error {
	return c.LoadAggregate(model, name, rel, "count", "*", rest...)
}

// LoadMax 加载关系列最大值到模型 {relation}_max_{column} 属性（对应 loadMax）。
func (c *Connection) LoadMax(model any, name string, rel Relation, column string, rest ...any) error {
	return c.LoadAggregate(model, name, rel, "max", column, rest...)
}

// LoadMin 加载关系列最小值（对应 loadMin）。
func (c *Connection) LoadMin(model any, name string, rel Relation, column string, rest ...any) error {
	return c.LoadAggregate(model, name, rel, "min", column, rest...)
}

// LoadSum 加载关系列求和（对应 loadSum）。
func (c *Connection) LoadSum(model any, name string, rel Relation, column string, rest ...any) error {
	return c.LoadAggregate(model, name, rel, "sum", column, rest...)
}

// LoadAvg 加载关系列平均（对应 loadAvg）。
func (c *Connection) LoadAvg(model any, name string, rel Relation, column string, rest ...any) error {
	return c.LoadAggregate(model, name, rel, "avg", column, rest...)
}

// LoadExists 加载关系存在性到模型 {relation}_exists 属性（对应 loadExists）。
func (c *Connection) LoadExists(model any, name string, rel Relation, rest ...any) error {
	return c.LoadAggregate(model, name, rel, "exists", "*", rest...)
}

// ---- 模型实例级多态加载（提升自 builder 内部 loadMorph）----

// LoadMorph 加载单模型的多态关系（对应 Laravel loadMorph 的实例级入口）：
// MorphMany/MorphOne 经 builder 内部 loadMorph 查回子模型并填充同名关系
// 字段，同时写入 LoadedRelation 缓存；其它关系类型走 Load 通道。
func (c *Connection) LoadMorph(model any, name string, rel Relation) error {
	switch rel.(type) {
	case MorphMany, MorphOne:
	default:
		return c.Load(model, name, rel)
	}
	info := inspectModel(model)
	single := false
	switch rel.(type) {
	case MorphOne:
		single = true
	}
	pr := pendingRelation{name: name, rel: rel}
	slice := modelSliceOf(model)
	b := c.Model(model)
	b.loadMorph(reflect.ValueOf(slice), pr, info.table, single)

	// 结果镜像进 LoadedRelation 缓存（对齐 Load 的可观测语义）
	field := reflect.Indirect(reflect.ValueOf(model)).FieldByName(utils.StudlyCase(name))
	if !field.IsValid() {
		return nil
	}
	var results []any
	switch field.Kind() {
	case reflect.Slice:
		for i := 0; i < field.Len(); i++ {
			results = append(results, field.Index(i).Interface())
		}
	case reflect.Ptr:
		if !field.IsNil() {
			results = append(results, field.Interface())
		}
	}
	if results == nil {
		results = []any{}
	}
	SetLoadedRelation(model, name, results)
	return nil
}

// LoadMorphCount 加载多态关系计数到模型 {relation}_count 属性
// （对应 Laravel loadMorphCount 的实例级入口）。
func (c *Connection) LoadMorphCount(model any, name string, rel Relation) error {
	return c.LoadAggregate(model, name, rel, "count", "*")
}

// ---- 模型级自增/自减（对应 Laravel Model.php:1090-1286）----
//
// 走原子 UPDATE（`col` = `col` + N 表达式）触发 updating/updated 事件
// （对齐 Laravel：increment 复用 update 生命周期），并把新值同步回模型属性；
// extra 附加列一并更新；带时间戳的模型同时触碰 updated_at
// （WithoutTimestamps 作用域内除外）。

// Increment 自增一列；amount 缺省 1，可传 map[string]any 作为 extra 附加列。
func (c *Connection) Increment(model any, column string, amount ...any) error {
	return c.incrementOrDecrement(model, column, amount, 1)
}

// Decrement 自减一列；amount 缺省 1，可传 map[string]any 作为 extra 附加列。
func (c *Connection) Decrement(model any, column string, amount ...any) error {
	return c.incrementOrDecrement(model, column, amount, -1)
}

func (c *Connection) incrementOrDecrement(model any, column string, args []any, sign int) error {
	schema, err := NewSchema(model)
	if err != nil {
		return err
	}
	if schema.PrimaryField == nil || schema.PrimaryField.IsBlank {
		return errors.New("torm: cannot increment model without primary key")
	}
	info := inspectModel(model)

	amount := int64(1)
	extra := map[string]any{}
	for _, a := range args {
		switch v := a.(type) {
		case int:
			amount = int64(v)
		case int64:
			amount = int64(v)
		case float64:
			amount = int64(v)
		case map[string]any:
			for k, vv := range v {
				extra[k] = vv
			}
		}
	}
	if amount < 0 {
		amount = -amount
		sign = -sign
	}

	ob := observerFor(model)
	if err := c.fireModelEvent(ob, EventUpdating, model); err != nil {
		return err
	}

	if info.hasTimestamps && !TimestampsIgnored() {
		if _, ok := extra["updated_at"]; !ok {
			extra["updated_at"] = nowFunc()
		}
	}

	b := c.Table(info.table).
		Where(info.primaryKey, schema.PrimaryField.Value.Addr().Interface())
	// 统一走 Builder.Increment（负金额编译为减法）：Builder.Decrement 不透传 extra
	amt := amount
	if sign < 0 {
		amt = -amount
	}
	if _, err := b.Increment(column, amt, extra); err != nil {
		return err
	}

	// 同步模型内存值与快照，记录变更集（updated 观察者可读）
	delta := float64(amount)
	if sign < 0 {
		delta = -delta
	}
	adjustNumericField(model, column, delta)
	touched := []string{column}
	for k, v := range extra {
		if k == column {
			continue
		}
		if _, isExpr := v.(grammar.Expression); isExpr {
			continue
		}
		if err := setModelAttrs(model, map[string]any{k: v}); err != nil {
			return err
		}
		touched = append(touched, k)
	}
	syncOriginalAttributes(model, touched...)

	changes := map[string]any{column: comparableFieldValue(schemaField(schema, column))}
	for k, v := range extra {
		changes[k] = v
	}
	recordChanges(model, changes)

	return c.fireModelEvent(ob, EventUpdated, model)
}

// IncrementEach 一条 UPDATE 内自增多列（对应 Laravel incrementEach）；
// extra 为附加列。金额为负值时转为自减。
func (c *Connection) IncrementEach(model any, columns map[string]int64, extra ...map[string]any) error {
	return c.incrementEach(model, columns, extra, 1)
}

// DecrementEach 一条 UPDATE 内自减多列（对应 Laravel decrementEach）。
func (c *Connection) DecrementEach(model any, columns map[string]int64, extra ...map[string]any) error {
	return c.incrementEach(model, columns, extra, -1)
}

func (c *Connection) incrementEach(model any, columns map[string]int64, extraArgs []map[string]any, sign int) error {
	if len(columns) == 0 {
		return nil
	}
	schema, err := NewSchema(model)
	if err != nil {
		return err
	}
	if schema.PrimaryField == nil || schema.PrimaryField.IsBlank {
		return errors.New("torm: cannot increment model without primary key")
	}
	info := inspectModel(model)

	extra := map[string]any{}
	for _, m := range extraArgs {
		for k, v := range m {
			extra[k] = v
		}
	}

	ob := observerFor(model)
	if err := c.fireModelEvent(ob, EventUpdating, model); err != nil {
		return err
	}

	if info.hasTimestamps && !TimestampsIgnored() {
		if _, ok := extra["updated_at"]; !ok {
			extra["updated_at"] = nowFunc()
		}
	}

	values := map[string]any{}
	for col, amt := range columns {
		s := sign
		if amt < 0 {
			s = -s
			amt = -amt
		}
		wrapped := c.Model(model).GetGrammar().Wrap(col)
		op := " + "
		if s < 0 {
			op = " - "
		}
		values[col] = grammar.NewExpression(wrapped + op + strconv.FormatInt(amt, 10))
	}
	for k, v := range extra {
		if _, ok := values[k]; !ok {
			values[k] = v
		}
	}

	if _, err := c.newModelQuery(model).
		Where(info.primaryKey, schema.PrimaryField.Value.Addr().Interface()).
		Update(values); err != nil {
		return err
	}

	touched := make([]string, 0, len(columns)+len(extra))
	changes := map[string]any{}
	for col, amt := range columns {
		delta := float64(amt)
		if sign < 0 {
			delta = -delta
		}
		adjustNumericField(model, col, delta)
		touched = append(touched, col)
		changes[col] = comparableFieldValue(schemaField(schema, col))
	}
	for k, v := range extra {
		setModelAttrs(model, map[string]any{k: v})
		touched = append(touched, k)
		changes[k] = v
	}
	syncOriginalAttributes(model, touched...)
	recordChanges(model, changes)

	return c.fireModelEvent(ob, EventUpdated, model)
}

// adjustNumericField 把模型数值列的内存值加上 delta（对应 Laravel increment
// 后的属性同步；nil 指针视为 0）。
func adjustNumericField(model any, column string, delta float64) {
	schema, err := NewSchema(model)
	if err != nil {
		return
	}
	f := schemaField(schema, column)
	if f == nil {
		return
	}
	v := f.Value
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			p := reflect.New(v.Type().Elem())
			v.Set(p)
		}
		v = v.Elem()
	}
	var cur float64
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		cur = float64(v.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		cur = float64(v.Uint())
	case reflect.Float32, reflect.Float64:
		cur = v.Float()
	default:
		return
	}
	next := reflect.ValueOf(cur + delta).Convert(v.Type())
	v.Set(next)
	f.IsBlank = utils.IsBlank(f.Value)
}

// schemaField 按列名取字段（无则 nil）。
func schemaField(schema *Schema, name string) *Field {
	for _, f := range schema.Fields {
		if f.Name == name && !f.Ignored {
			return f
		}
	}
	return nil
}

// ---- 唯一 ID（对应 Laravel HasUniqueIds）----
//
// `torm:"unique_id"`（缺省 uuid）/ `torm:"unique_id:ulid"` / `cast:uuid` /
// `cast:ulid` 声明；Create 时在 creating 事件之后、insert 之前生成并写入字段，
// 已显式赋值的字段不覆盖；主键为 unique_id 时跳过自增回填。

// applyUniqueIDs 为声明了 unique_id 的空字段生成 ID（对应 Laravel addUniqueIdentifiers）。
// 直接在调用方的 schema 上操作，保证 IsBlank/Value 对 Attributes() 即时可见。
func applyUniqueIDs(schema *Schema) error {
	for _, f := range schema.Fields {
		if f.UniqueID == "" || f.Ignored || !f.IsBlank {
			continue
		}
		switch f.UniqueID {
		case "uuid":
			if err := f.SetValue(newUUID()); err != nil {
				return err
			}
		case "ulid":
			if err := f.SetValue(newULID()); err != nil {
				return err
			}
		}
	}
	return nil
}

// newUUID 生成 RFC 4122 v4 UUID（crypto/rand）。
func newUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("torm: crypto/rand unavailable: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ulidEncoding Crockford Base32（ULID 规范字符表）。
const ulidEncoding = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newULID 生成 26 字符 ULID：48 位毫秒时间戳 + 80 位 crypto/rand 随机数
// （最小实现，不含单调性保证）。
func newULID() string {
	b := make([]byte, 16)
	ms := uint64(nowFunc().UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	if _, err := rand.Read(b[6:]); err != nil {
		panic("torm: crypto/rand unavailable: " + err.Error())
	}
	n := new(big.Int).SetBytes(b)
	mask := big.NewInt(31)
	low := new(big.Int)
	out := make([]byte, 26)
	for i := 25; i >= 0; i-- {
		low.And(n, mask)
		out[i] = ulidEncoding[low.Uint64()]
		n.Rsh(n, 5)
	}
	return string(out)
}
