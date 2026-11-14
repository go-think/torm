package torm

import (
	"reflect"
	"sync"

	"github.com/go-think/torm/events"
)

// Observer 模型观察者：按模型类型注册可选回调。
// 回调返回 error 时中断当前操作（异常中断生命周期）。
type Observer struct {
	Retrieving func(model any) error // 水合前触发，返回 error 取消该行水合（行保持扫描零值仍留在结果中，其他实现为不进结果集）
	Retrieved  func(model any)

	Saving   func(model any) error
	Saved    func(model any) error
	Creating func(model any) error
	Created  func(model any) error
	Updating func(model any) error
	Updated  func(model any) error
	Deleting func(model any) error
	Deleted  func(model any) error
	Trashed  func(model any) error

	ForceDeleting func(model any) error

	Restoring    func(model any) error
	Restored     func(model any) error
	Replicating  func(model any) error
	ForceDeleted func(model any) error
}

var observerMu sync.Mutex
var observers = map[reflect.Type][]*Observer{}

// Observe 为模型类型注册观察者（覆盖式：替换该类型当前的全部观察者，
// 与 1.x 行为一致）。需要叠加多个观察者时使用 AddObserver。
func (c *Connection) Observe(model any, o *Observer) {
	t := reflect.TypeOf(model)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	observerMu.Lock()
	defer observerMu.Unlock()
	if o == nil {
		delete(observers, t)
		return
	}
	observers[t] = []*Observer{o}
}

// AddObserver 追加注册观察者（observe 可叠加语义；
// 按注册顺序执行，任一返回 error 中断）。
func (c *Connection) AddObserver(model any, o *Observer) {
	t := reflect.TypeOf(model)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if o == nil {
		return
	}
	observerMu.Lock()
	defer observerMu.Unlock()
	observers[t] = append(observers[t], o)
}

// Unobserve 移除模型类型的全部观察者。
func (c *Connection) Unobserve(model any) {
	t := reflect.TypeOf(model)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	observerMu.Lock()
	defer observerMu.Unlock()
	delete(observers, t)
}

// observerFor 取模型的观察者列表（无则 nil）。
func observerFor(model any) []*Observer {
	t := reflect.TypeOf(model)
	if t == nil {
		return nil
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return observerForType(t)
}

// observerForType 按模型类型取观察者列表。
func observerForType(t reflect.Type) []*Observer {
	observerMu.Lock()
	defer observerMu.Unlock()
	return observers[t]
}

// fire 安全触发单个回调。
func fire(hook func(model any) error, model any) error {
	if hook == nil {
		return nil
	}
	return hook(model)
}

// fireRetrieved 对扫描结果逐模型触发 Retrieved，并分发 retrieved 事件；
// 水合完成后为每个模型应用读侧 cast、固化 original 快照并置 exists 标记
// （水合时的 original 初始化与 exists = true）。
func (c *Connection) fireRetrieved(dest interface{}) {
	rv := reflect.Indirect(reflect.ValueOf(dest))
	var modelType reflect.Type
	switch rv.Kind() {
	case reflect.Slice:
		if rv.Len() == 0 {
			return
		}
		modelType = rv.Index(0).Type()
		if modelType.Kind() == reflect.Ptr {
			modelType = modelType.Elem()
		}
	case reflect.Struct:
		modelType = rv.Type()
	default:
		return
	}
	// retrieving：水合前逐模型触发（返回 error 取消该行——跳过水合与
	// retrieved 回调；torm 以切片元素原地水合，无法移除元素，取消的行
	// 仍留在结果中但保持扫描零值，与 "不进结果集" 行为的差异已在
	// Observer.Retrieving 注释声明）
	models := modelInstancesOf(dest, modelType)
	cancelled := make([]bool, len(models))
	for i, m := range models {
		for _, ob := range observerForType(modelType) {
			if ob.Retrieving != nil {
				if err := ob.Retrieving(m); err != nil {
					cancelled[i] = true
					break
				}
			}
		}
	}
	hydrateModels(dest, cancelled)
	for i, m := range models {
		if cancelled[i] {
			continue
		}
		for _, ob := range observerForType(modelType) {
			if ob.Retrieved != nil {
				ob.Retrieved(m)
			}
		}
		// retrieved 事件逐模型分发（每模型一条 Retrieved）
		c.dispatchEvent(EventRetrieved, m)
	}
}

// modelInstancesOf 收集扫描目标中每个模型的实例指针。
func modelInstancesOf(dest interface{}, modelType reflect.Type) []any {
	rv := reflect.Indirect(reflect.ValueOf(dest))
	var out []any
	switch rv.Kind() {
	case reflect.Slice:
		for i := 0; i < rv.Len(); i++ {
			elem := rv.Index(i)
			if elem.Kind() == reflect.Ptr {
				out = append(out, elem.Interface())
				continue
			}
			if elem.CanAddr() && elem.Type() == modelType {
				out = append(out, elem.Addr().Interface())
			}
		}
	case reflect.Struct:
		if rv.CanAddr() && rv.Type() == modelType {
			out = append(out, rv.Addr().Interface())
		}
	}
	return out
}

// hydrateModels 对扫描结果里的每个模型：读侧 cast 规范化 → original 快照 →
// exists 标记。读 cast 失败保持扫描原值（连接层扫描已保证基础类型正确）；
// cancelled 下标（Retrieving 取消的行）跳过水合。
func hydrateModels(dest interface{}, cancelled []bool) {
	skip := func(i int) bool { return i < len(cancelled) && cancelled[i] }
	rv := reflect.Indirect(reflect.ValueOf(dest))
	switch rv.Kind() {
	case reflect.Slice:
		for i := 0; i < rv.Len(); i++ {
			if skip(i) {
				continue
			}
			elem := rv.Index(i)
			if elem.Kind() == reflect.Ptr {
				hydrateModel(elem.Interface())
				continue
			}
			if elem.Kind() != reflect.Struct || !elem.CanAddr() {
				continue
			}
			hydrateModel(elem.Addr().Interface())
		}
	case reflect.Struct:
		if rv.CanAddr() && !skip(0) {
			hydrateModel(rv.Addr().Interface())
		}
	}
}

func hydrateModel(model any) {
	applyReadCasts(model)
	syncOriginal(model)
	setExists(model, true, false)
}

// applyReadCasts 对模型全部 cast 字段应用 ReadCast 规范化。
func applyReadCasts(model any) {
	schema, err := NewSchema(model)
	if err != nil {
		return
	}
	for _, f := range schema.Fields {
		if f.Ignored || f.CastName == "" {
			continue
		}
		_ = f.ReadCast()
	}
}

// fireModelEvent 触发观察者钩子并分发同名事件；钩子出错中断后续。
// withoutEvents 作用域内（Quietly 族）钩子与分发全部抑制。
// 观察者列表按注册顺序逐个执行（多观察者叠加语义）。
func (c *Connection) fireModelEvent(obs []*Observer, name string, model any) error {
	if eventsQuiet() {
		return nil
	}
	for _, ob := range obs {
		var hook func(any) error
		switch name {
		case events.Retrieving:
			hook = ob.Retrieving
		case events.Saving:
			hook = ob.Saving
		case events.Saved:
			hook = ob.Saved
		case events.Creating:
			hook = ob.Creating
		case events.Created:
			hook = ob.Created
		case events.Updating:
			hook = ob.Updating
		case events.Updated:
			hook = ob.Updated
		case events.Deleting:
			hook = ob.Deleting
		case events.Deleted:
			hook = ob.Deleted
		case events.Trashed:
			hook = ob.Trashed
		case events.ForceDeleting:
			hook = ob.ForceDeleting
		case events.Restoring:
			hook = ob.Restoring
		case events.Restored:
			hook = ob.Restored
		case events.Replicating:
			hook = ob.Replicating
		case events.ForceDeleted:
			hook = ob.ForceDeleted
		}
		if err := fire(hook, model); err != nil {
			return err
		}
	}
	c.dispatchEvent(name, model)
	return nil
}
