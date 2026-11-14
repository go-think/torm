// Package torm 根包中的 events 内容已迁移至 events 子包
// （数据库事件类型）；此处保留类型与常量别名以兼容既有引用。
package torm

import "github.com/go-think/torm/events"

// Event / ModelEvent / Dispatcher / EventBus / QueryExecuted：类型别名（兼容层）。
type (
	Event         = events.Event
	ModelEvent    = events.ModelEvent
	Dispatcher    = events.Dispatcher
	Listener      = events.Listener
	EventBus      = events.EventBus
	QueryExecuted = events.QueryExecuted
)

// 模型生命周期事件名。
const (
	EventRetrieving    = events.Retrieving
	EventRetrieved     = events.Retrieved
	EventCreating      = events.Creating
	EventCreated       = events.Created
	EventUpdating      = events.Updating
	EventUpdated       = events.Updated
	EventSaving        = events.Saving
	EventSaved         = events.Saved
	EventDeleting      = events.Deleting
	EventDeleted       = events.Deleted
	EventTrashed       = events.Trashed
	EventRestoring     = events.Restoring
	EventRestored      = events.Restored
	EventReplicating   = events.Replicating
	EventForceDeleting = events.ForceDeleting
	EventForceDeleted  = events.ForceDeleted
)

var NewEventBus = events.NewEventBus
