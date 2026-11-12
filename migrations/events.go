package migrations

import (
	"github.com/thinkoner/torm/events"
)

// 迁移事件：对齐 Illuminate\Database\Events 下的迁移事件族
// （MigrationsEvent / MigrationEvent / NoPendingMigrations），实现 events.Event
// 接口，可由 events.Dispatcher 分发。

// 迁移事件名（对应 Laravel 各迁移事件类名，camelCase）。
const (
	EventMigrationsStarted   = "migrationsStarted"
	EventMigrationsEnded     = "migrationsEnded"
	EventMigrationStarted    = "migrationStarted"
	EventMigrationEnded      = "migrationEnded"
	EventMigrationSkipped    = "migrationSkipped"
	EventNoPendingMigrations = "noPendingMigrations"
	EventMigrationsPruned    = "migrationsPruned"
)

// MigrationsEvent 批次级事件（对应 Laravel MigrationsEvent：携带 method 与 options）。
type MigrationsEvent struct {
	Name    string
	Method  string // "up" / "down"
	Options map[string]any
}

func (e MigrationsEvent) EventName() string { return e.Name }

// MigrationsStarted 批次开始（对应 Laravel MigrationsStarted）。
type MigrationsStarted struct{ MigrationsEvent }

// MigrationsEnded 批次结束（对应 Laravel MigrationsEnded）。
type MigrationsEnded struct{ MigrationsEvent }

// MigrationEvent 单个迁移级事件（对应 Laravel MigrationEvent：携带迁移与方法名）。
type MigrationEvent struct {
	Name      string
	Migration *Migration
	Method    string // "up" / "down"
}

func (e MigrationEvent) EventName() string { return e.Name }

// MigrationStarted 单个迁移开始（对应 Laravel MigrationStarted）。
type MigrationStarted struct{ MigrationEvent }

// MigrationEnded 单个迁移结束（对应 Laravel MigrationEnded）。
type MigrationEnded struct{ MigrationEvent }

// MigrationSkipped 迁移被跳过（对应 Laravel MigrationSkipped，如 shouldRun 为假）。
type MigrationSkipped struct{ MigrationEvent }

// NoPendingMigrations 无待执行/待回滚迁移（对应 Laravel NoPendingMigrations）。
type NoPendingMigrations struct {
	Method string // "up" / "down"
}

func (e NoPendingMigrations) EventName() string { return EventNoPendingMigrations }

// MigrationsPruned 迁移文件被清理（对应 Laravel MigrationsPruned）。
type MigrationsPruned struct {
	MigrationsEvent
	Path string
}

// 编译期断言：实现 events.Event。
var (
	_ events.Event = MigrationsStarted{}
	_ events.Event = MigrationsEnded{}
	_ events.Event = MigrationStarted{}
	_ events.Event = MigrationEnded{}
	_ events.Event = MigrationSkipped{}
	_ events.Event = NoPendingMigrations{}
	_ events.Event = MigrationsPruned{}
)
