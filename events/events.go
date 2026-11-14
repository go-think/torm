// Package events 数据库层事件包：
// 查询与模型生命周期事件对象、分发器接口与默认总线实现。
package events

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

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

// QueryExecuted 携带一条已执行查询的信息。
type QueryExecuted struct {
	Sql            string
	Bindings       []any
	TimeNs         int64
	RawSql         string // 绑定插值后的 SQL（对应 toRawSql）
	ConnectionName string // 连接标识（对应 $connectionName）
	ReadWriteType  string // "read" / "write"，读写分离时区分（对应 $readWriteType）
}

// TimeMs 以毫秒返回执行耗时（float 毫秒）。
func (q QueryExecuted) TimeMs() float64 {
	return float64(q.TimeNs) / 1e6
}

// ---- 连接/事务生命周期事件（事件名与事件对象一一对应） ----

// 生命周期事件名。
const (
	StatementPrepared     = "statementPrepared"
	TransactionBeginning  = "transactionBeginning"
	TransactionCommitting = "transactionCommitting"
	TransactionCommitted  = "transactionCommitted"
	TransactionRolledBack = "transactionRolledBack"
	ConnectionEstablished = "connectionEstablished"
)

// TransactionEvent 事务生命周期事件（对应 TransactionBeginning/Committing/
// Committed/RolledBack，均携带连接名与事务层数）。
type TransactionEvent struct {
	Name             string
	ConnectionName   string
	TransactionLevel int
}

func (e TransactionEvent) EventName() string { return e.Name }

// StatementPreparedEvent 预处理完成事件（对应 StatementPrepared，携带语句与
// 连接名；不携带 PDOStatement，Go 侧无对应句柄）。
type StatementPreparedEvent struct {
	Sql            string
	ConnectionName string
}

func (e StatementPreparedEvent) EventName() string { return StatementPrepared }

// ConnectionEstablishedEvent 首次建连事件（对应 ConnectionEstablished）。
type ConnectionEstablishedEvent struct {
	ConnectionName string
}

func (e ConnectionEstablishedEvent) EventName() string { return ConnectionEstablished }

// 模型生命周期事件名（与观察者钩子同名）。
const (
	Retrieving    = "retrieving"
	Retrieved     = "retrieved"
	Creating      = "creating"
	Created       = "created"
	Updating      = "updating"
	Updated       = "updated"
	Saving        = "saving"
	Saved         = "saved"
	Deleting      = "deleting"
	Deleted       = "deleted"
	Trashed       = "trashed"
	Restoring     = "restoring"
	Restored      = "restored"
	Replicating   = "replicating"
	ForceDeleting = "forceDeleting"
	ForceDeleted  = "forceDeleted"
)

// Listener 事件监听器。
type Listener func(e Event)

// ListenerUntil 可中断监听器：返回 false 时停止后续传播（halt
// 语义，配合 DispatchUntil 使用；返回 true 继续传播）。
type ListenerUntil func(e Event) bool

// Dispatcher 事件分发器接口。
type Dispatcher interface {
	Dispatch(e Event)
}

// listenerEntry 统一监听器注册项：覆盖普通/until/优先级/一次性/通配形态
// （既有 listeners map[string][]Listener 存储重构为该注册表，公开 API 不变）。
type listenerEntry struct {
	fn       Listener      // 普通监听器（与 until 互斥）
	until    ListenerUntil // 可中断监听器
	priority int           // 数字小先执行（ListenWithPriority），缺省 0
	seq      int64         // 全局注册序：同优先级内按注册序稳定
	once     *atomic.Bool  // 非 nil 表示一次性监听器：首次分发后失效
}

// fired 一次性监听器是否已触发（CAS 保证并发分发也仅执行一次）；
// 非一次性监听器恒返回 false。已触发的注册项保留在注册表中，
// 分发时以原子检查跳过。
func (en listenerEntry) fired() bool {
	return en.once != nil && !en.once.CompareAndSwap(false, true)
}

// EventBus 默认分发器实现：按事件名注册监听器。在既有精确名注册之上支持：
//   - 通配监听：Listen 的 name 形如 "event.*"（尾部 "*"），分发时精确匹配
//     在前，其后按通配前缀最长优先；
//   - 优先级：ListenWithPriority 数字小先执行，同优先级按注册序（稳定）；
//   - 可中断传播：ListenerUntil + DispatchUntil；
//   - 一次性监听：Once。
//
// 并发安全（沿用 RWMutex 风格）。
type EventBus struct {
	mu      sync.RWMutex
	entries map[string][]listenerEntry // 键为精确事件名或尾部 "*" 通配模式
	seq     int64                      // 全局注册序计数器
}

func NewEventBus() *EventBus {
	return &EventBus{entries: map[string][]listenerEntry{}}
}

// add 登记一个监听器项（分配全局注册序）。
func (b *EventBus) add(name string, e listenerEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	e.seq = b.seq
	b.entries[name] = append(b.entries[name], e)
}

// Listen 注册监听器。name 支持 "event.*" 尾部通配模式。
func (b *EventBus) Listen(name string, l Listener) {
	b.add(name, listenerEntry{fn: l})
}

// ListenUntil 注册可中断监听器（配合 DispatchUntil：返回 false 停止传播）。
func (b *EventBus) ListenUntil(name string, l ListenerUntil) {
	b.add(name, listenerEntry{until: l})
}

// ListenWithPriority 带优先级注册监听器：数字小先执行，同优先级按注册序
// （稳定）；通配模式监听同样适用。
func (b *EventBus) ListenWithPriority(name string, l Listener, priority int) {
	b.add(name, listenerEntry{fn: l, priority: priority})
}

// ListenUntilWithPriority 同 ListenWithPriority，但监听器可中断传播
// （配合 DispatchUntil）。
func (b *EventBus) ListenUntilWithPriority(name string, l ListenerUntil, priority int) {
	b.add(name, listenerEntry{until: l, priority: priority})
}

// Once 注册一次性监听器：首次分发执行一次后失效（并发分发也仅执行一次）。
func (b *EventBus) Once(name string, l Listener) {
	b.add(name, listenerEntry{fn: l, once: new(atomic.Bool)})
}

// Dispatch 分发事件（监听器同步执行：精确匹配在前按优先级/注册序，其后
// 命中的通配监听按前缀最长优先）。ListenerUntil 监听器在此忽略返回值。
func (b *EventBus) Dispatch(e Event) {
	for _, en := range b.resolve(e.EventName()) {
		if en.fired() {
			continue
		}
		if en.until != nil {
			en.until(e)
			continue
		}
		if en.fn != nil {
			en.fn(e)
		}
	}
}

// DispatchUntil 分发事件直至某监听器中断：ListenerUntil 返回 false 时立即
// 停止传播并返回 false（until/halt 语义）；全部执行完毕
// 返回 true。普通监听器（无返回值）视为继续传播。
func (b *EventBus) DispatchUntil(e Event) bool {
	for _, en := range b.resolve(e.EventName()) {
		if en.fired() {
			continue
		}
		if en.until != nil {
			if !en.until(e) {
				return false
			}
			continue
		}
		if en.fn != nil {
			en.fn(e)
		}
	}
	return true
}

// resolve 取事件命中的监听器快照：精确匹配在前（优先级升序，同优先级按
// 注册序稳定），其后为命中的通配监听（按通配前缀长度降序，同长按模式名
// 稳定，模式内按优先级/注册序）。
func (b *EventBus) resolve(name string) []listenerEntry {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]listenerEntry, 0, 4)
	if es, ok := b.entries[name]; ok {
		out = append(out, es...)
		sort.SliceStable(out, func(i, j int) bool { return out[i].priority < out[j].priority })
	}
	var wilds []string
	for pattern := range b.entries {
		if pattern != name && matchWildcard(pattern, name) {
			wilds = append(wilds, pattern)
		}
	}
	sort.Slice(wilds, func(i, j int) bool {
		pi, pj := wildcardPrefix(wilds[i]), wildcardPrefix(wilds[j])
		if pi != pj {
			return pi > pj // 最长前缀优先
		}
		return wilds[i] < wilds[j] // 同长按模式名稳定
	})
	for _, pattern := range wilds {
		es := make([]listenerEntry, len(b.entries[pattern]))
		copy(es, b.entries[pattern])
		sort.SliceStable(es, func(i, j int) bool { return es[i].priority < es[j].priority })
		out = append(out, es...)
	}
	return out
}

// matchWildcard 通配匹配：模式须以 "*" 结尾（如 "event.*"），事件名以
// "*" 之前的前缀开头即命中。
func matchWildcard(pattern, name string) bool {
	if !strings.HasSuffix(pattern, "*") {
		return false
	}
	return strings.HasPrefix(name, pattern[:len(pattern)-1])
}

// wildcardPrefix 通配模式的固定前缀长度（非通配模式返回完整长度）。
func wildcardPrefix(pattern string) int {
	if strings.HasSuffix(pattern, "*") {
		return len(pattern) - 1
	}
	return len(pattern)
}
