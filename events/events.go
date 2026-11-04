// Package events 对应 Laravel 的 Illuminate\Database\Events：
// 查询与模型生命周期事件对象、分发器接口与默认总线实现。
package events

import "sync"

// ModelEvent 模型生命周期事件对象。
type ModelEvent struct {
	Name  string
	Model any
}

// EventName 实现 Event 接口。
func (e ModelEvent) EventName() string { return e.Name }

// Event 事件接口。
type Event interface {
	EventName() string
}

// QueryExecuted 携带一条已执行查询的信息（对应 Laravel 的 QueryExecuted）。
type QueryExecuted struct {
	Sql      string
	Bindings []any
	TimeNs   int64
}

// 模型生命周期事件名（对齐 Laravel 的模型事件）。
const (
	Retrieved    = "retrieved"
	Creating     = "creating"
	Created      = "created"
	Updating     = "updating"
	Updated      = "updated"
	Saving       = "saving"
	Saved        = "saved"
	Deleting     = "deleting"
	Deleted      = "deleted"
	Restoring    = "restoring"
	Restored     = "restored"
	ForceDeleted = "forceDeleted"
)

// Listener 事件监听器。
type Listener func(e Event)

// Dispatcher 事件分发器接口。
type Dispatcher interface {
	Dispatch(e Event)
}

// EventBus 默认分发器实现：按事件名注册监听器。
type EventBus struct {
	mu        sync.RWMutex
	listeners map[string][]Listener
}

func NewEventBus() *EventBus {
	return &EventBus{listeners: map[string][]Listener{}}
}

// Listen 注册监听器。
func (b *EventBus) Listen(name string, l Listener) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.listeners[name] = append(b.listeners[name], l)
}

// Dispatch 分发事件（监听器按注册顺序同步执行）。
func (b *EventBus) Dispatch(e Event) {
	b.mu.RLock()
	ls := b.listeners[e.EventName()]
	b.mu.RUnlock()
	for _, l := range ls {
		l(e)
	}
}
