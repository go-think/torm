package torm

import (
	"reflect"
	"sync"

	"github.com/thinkoner/torm/events"
)

// Observer 对应 Laravel 的模型观察者：按模型类型注册可选回调。
// 回调返回 error 时中断当前操作（对应 Laravel 异常中断生命周期）。
type Observer struct {
	Retrieved func(model any)

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
	ForceDeleted func(model any) error
}

var observerMu sync.Mutex
var observers = map[reflect.Type]*Observer{}

// Observe 为模型类型注册观察者（多次注册覆盖）。
func (c *Connection) Observe(model any, o *Observer) {
	t := reflect.TypeOf(model)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	observerMu.Lock()
	defer observerMu.Unlock()
	observers[t] = o
}

// observerFor 取模型的观察者（无则 nil）。
func observerFor(model any) *Observer {
	t := reflect.TypeOf(model)
	if t == nil {
		return nil
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return observerForType(t)
}

// observerForType 按模型类型取观察者。
func observerForType(t reflect.Type) *Observer {
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
// （对应 Laravel 水合时的 original 初始化与 exists = true）。
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
	hydrateModels(dest)
	if ob := observerForType(modelType); ob != nil && ob.Retrieved != nil {
		if rv.Kind() == reflect.Slice {
			for i := 0; i < rv.Len(); i++ {
				elem := rv.Index(i)
				if elem.Kind() == reflect.Ptr {
					ob.Retrieved(elem.Interface())
					continue
				}
				ob.Retrieved(elem.Addr().Interface())
			}
		} else {
			ob.Retrieved(rv.Addr().Interface())
		}
	}
	c.dispatchEvent(EventRetrieved, dest)
}

// hydrateModels 对扫描结果里的每个模型：读侧 cast 规范化 → original 快照 →
// exists 标记。读 cast 失败保持扫描原值（连接层扫描已保证基础类型正确）。
func hydrateModels(dest interface{}) {
	rv := reflect.Indirect(reflect.ValueOf(dest))
	switch rv.Kind() {
	case reflect.Slice:
		for i := 0; i < rv.Len(); i++ {
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
		if rv.CanAddr() {
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
func (c *Connection) fireModelEvent(ob *Observer, name string, model any) error {
	if eventsQuiet() {
		return nil
	}
	if ob != nil {
		var hook func(any) error
		switch name {
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
