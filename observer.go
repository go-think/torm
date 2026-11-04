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

// fireRetrieved 对扫描结果逐模型触发 Retrieved，并分发 retrieved 事件。
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

// fireModelEvent 触发观察者钩子并分发同名事件；钩子出错中断后续。
func (c *Connection) fireModelEvent(ob *Observer, name string, model any) error {
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
