package migrations

import (
	"fmt"

	torm "github.com/thinkoner/torm"
	"github.com/thinkoner/torm/events"
	"github.com/thinkoner/torm/grammar"
)

// Migration 对应 Laravel 的迁移文件（Go 侧为注册式而非文件反射）。
type Migration struct {
	Name string
	Up   func(*torm.SchemaBuilder) error
	Down func(*torm.SchemaBuilder) error
}

// RollbackOptions 对应 Migrator::rollback 的 $options
// （step / batch / pretend，对齐 Migrator.php getMigrationsForRollback）。
type RollbackOptions struct {
	// Step 大于 0 时回滚最近 Step 批（batch >= last-Step+1 的迁移，逆序）。
	Step int
	// Batch 大于 0 时回滚指定批次。
	Batch int
	// Pretend 为 true 时仅列出将回滚的迁移，不执行 Down、不删除记录
	// （注册式迁移无法像 Laravel 那样内省出 SQL，故 pretend 语义收敛为列出）。
	Pretend bool
}

// Migrator 注册式迁移执行器。
type Migrator struct {
	Conn       *torm.Connection
	Migrations []Migration
	Table      string // 默认 migrations
	// Events 迁移事件分发器（nil 时不分发；事件类型见 migrations/events.go，
	// 可传 torm/events.EventBus）。
	Events events.Dispatcher
}

// NewMigrator 创建迁移器。
func NewMigrator(conn *torm.Connection, migrations ...Migration) *Migrator {
	return &Migrator{Conn: conn, Migrations: migrations, Table: "migrations"}
}

func (m *Migrator) table() string {
	if m.Table == "" {
		return "migrations"
	}
	return m.Table
}

// q 按驱动包裹标识符：MySQL 系反引号，其余双引号（PG/SQLite/SqlServer）。
func (m *Migrator) q(ident string) string {
	switch m.Conn.Driver() {
	case "mysql", "mariadb":
		return "`" + ident + "`"
	default:
		return `"` + ident + `"`
	}
}

// fireMigrationEvent 分发迁移事件（nil dispatcher 忽略，对齐 fireMigrationEvent）。
func (m *Migrator) fireMigrationEvent(e events.Event) {
	if m.Events == nil {
		return
	}
	m.Events.Dispatch(e)
}

// Migrate 创建迁移表（仅缺失时，对齐 Laravel repositoryExists）并执行未运行
// 迁移；同一批使用相同 batch，每个迁移包裹在事务中（对齐 Migrator::runMigration
// 的 supportsSchemaTransactions 语义）。
func (m *Migrator) Migrate() (ran []string, err error) {
	s := m.Conn.Schema()

	exists, err := s.HasTable(m.table())
	if err != nil {
		return nil, err
	}
	if !exists {
		if err := s.CreateTable(m.table(), func(t *grammar.Blueprint) {
			t.Increments("id")
			t.String("migration")
			t.Integer("batch")
		}); err != nil {
			return nil, err
		}
	}

	ranSet, err := m.ranNames()
	if err != nil {
		return nil, err
	}

	// 待执行迁移（对齐 Laravel：无待执行迁移时 fire NoPendingMigrations("up")）
	pending := make([]*Migration, 0, len(m.Migrations))
	for i := range m.Migrations {
		if !ranSet[m.Migrations[i].Name] {
			pending = append(pending, &m.Migrations[i])
		}
	}
	if len(pending) == 0 {
		m.fireMigrationEvent(NoPendingMigrations{Method: "up"})
		return nil, nil
	}

	batch, err := m.nextBatch()
	if err != nil {
		return nil, err
	}

	m.fireMigrationEvent(MigrationsStarted{Name: EventMigrationsStarted, Method: "up"})
	defer m.fireMigrationEvent(MigrationsEnded{Name: EventMigrationsEnded, Method: "up"})

	for _, mig := range pending {
		m.fireMigrationEvent(MigrationStarted{Name: EventMigrationStarted, Migration: mig, Method: "up"})
		if err := m.Conn.Transaction(func(c *torm.Connection) error {
			return mig.Up(s)
		}); err != nil {
			return ran, fmt.Errorf("migration [%s] failed: %w", mig.Name, err)
		}
		if _, _, err := m.Conn.Insert(
			"insert into " + m.q(m.table()) + " (" + m.q("migration") + ", " + m.q("batch") + ") values (?, ?)",
			mig.Name, batch,
		); err != nil {
			return ran, err
		}
		m.fireMigrationEvent(MigrationEnded{Name: EventMigrationEnded, Migration: mig, Method: "up"})
		ran = append(ran, mig.Name)
	}
	return ran, nil
}

// RollbackBatch 回滚指定 batch（逆序执行 Down）。
func (m *Migrator) RollbackBatch(batch int) (rolled []string, err error) {
	names, err := m.batchNames(batch)
	if err != nil {
		return nil, err
	}
	return m.rollbackNamesWith(m.Conn.Schema(), reverse(names))
}

// RollbackOptions 合并多个选项（后者覆盖前者）。
func mergeRollbackOptions(opts []RollbackOptions) RollbackOptions {
	out := RollbackOptions{}
	for _, o := range opts {
		if o.Step != 0 {
			out.Step = o.Step
		}
		if o.Batch != 0 {
			out.Batch = o.Batch
		}
		if o.Pretend {
			out.Pretend = true
		}
	}
	return out
}

// Rollback 回滚迁移；可传 RollbackOptions 指定 step/batch/pretend
// （对齐 Migrator::rollback / getMigrationsForRollback：step 优先于 batch，
// 二者均缺省时回滚最后一批）。
func (m *Migrator) Rollback(options ...RollbackOptions) (rolled []string, err error) {
	opts := mergeRollbackOptions(options)
	s := m.Conn.Schema()

	var names []string
	switch {
	case opts.Step > 0:
		names, err = m.migrationsAfterBatch(m.lastBatchMust() - opts.Step + 1)
	case opts.Batch > 0:
		names, err = m.batchNames(opts.Batch)
	default:
		last, lastErr := m.lastBatch()
		if lastErr != nil {
			return nil, lastErr
		}
		if last == 0 {
			// 对齐 Laravel：无批可回滚时 fire NoPendingMigrations("down")
			m.fireMigrationEvent(NoPendingMigrations{Method: "down"})
			return nil, nil
		}
		names, err = m.batchNames(last)
	}
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		m.fireMigrationEvent(NoPendingMigrations{Method: "down"})
		return nil, nil
	}

	// 逆序（Laravel getMigrations 按 batch desc, id desc 取出）
	names = reverse(names)

	if opts.Pretend {
		// pretend：仅列出（不执行 Down、不删记录）
		for _, name := range names {
			if mig := m.find(name); mig != nil {
				m.fireMigrationEvent(MigrationStarted{Name: EventMigrationStarted, Migration: mig, Method: "down"})
				m.fireMigrationEvent(MigrationEnded{Name: EventMigrationEnded, Migration: mig, Method: "down"})
			}
			rolled = append(rolled, name)
		}
		return rolled, nil
	}

	return m.rollbackNamesWith(s, names)
}

// rollbackNames 逆序回滚给定迁移名并删除记录。
func (m *Migrator) rollbackNames(names []string) ([]string, error) {
	return m.rollbackNamesWith(m.Conn.Schema(), names)
}

func (m *Migrator) rollbackNamesWith(s *torm.SchemaBuilder, names []string) (rolled []string, err error) {
	m.fireMigrationEvent(MigrationsStarted{Name: EventMigrationsStarted, Method: "down"})
	defer m.fireMigrationEvent(MigrationsEnded{Name: EventMigrationsEnded, Method: "down"})

	// 按传入顺序（应为逆序，batch desc / id desc）逐个回滚
	for _, name := range names {
		mig := m.find(name)
		if mig == nil || mig.Down == nil {
			// 对齐 Laravel：迁移未注册（或无 Down）时静默跳过，迁移记录
			// 保留在仓库表中（Laravel 行为即如此：runDown 仅在成功执行
			// Down 后才 repository->delete）。记录仍在，下次回滚仍会列出
			// 该迁移（Laravel 则在文件缺失时输出 "Migration not found"）。
			continue
		}
		m.fireMigrationEvent(MigrationStarted{Name: EventMigrationStarted, Migration: mig, Method: "down"})
		if err := m.Conn.Transaction(func(c *torm.Connection) error {
			return mig.Down(s)
		}); err != nil {
			return rolled, fmt.Errorf("rollback [%s] failed: %w", name, err)
		}
		if _, err := m.Conn.Delete(
			"delete from " + m.q(m.table()) + " where " + m.q("migration") + " = ?", name,
		); err != nil {
			return rolled, err
		}
		m.fireMigrationEvent(MigrationEnded{Name: EventMigrationEnded, Migration: mig, Method: "down"})
		rolled = append(rolled, name)
	}
	return rolled, nil
}

// Reset 回滚全部已运行迁移（按 batch 从新到旧，对齐 Migrator::reset）。
func (m *Migrator) Reset() (rolled []string, err error) {
	for {
		last, err := m.lastBatch()
		if err != nil {
			return rolled, err
		}
		if last == 0 {
			return rolled, nil
		}
		batchRolled, err := m.RollbackBatch(last)
		if err != nil {
			return rolled, err
		}
		if len(batchRolled) == 0 {
			// 该批无可回滚迁移（迁移未注册或无 Down）：删除记录避免死循环。
			// 注意这是对 Laravel 的偏离（Laravel reset 按 getRan 列表一次性
			// 回滚，无死循环问题；注册式迁移需要该保护），仅当整批跳过时触发。
			if err := m.deleteBatch(last); err != nil {
				return rolled, err
			}
			continue
		}
		rolled = append(rolled, batchRolled...)
	}
}

// Refresh 回滚全部迁移后重新执行（对齐 Migrator::refresh）。
func (m *Migrator) Refresh() (rolled []string, ran []string, err error) {
	if rolled, err = m.Reset(); err != nil {
		return nil, nil, err
	}
	if ran, err = m.Migrate(); err != nil {
		return rolled, nil, err
	}
	return rolled, ran, nil
}

// deleteBatch 删除指定批次的迁移记录（Down 缺失时避免 Reset 死循环）。
func (m *Migrator) deleteBatch(batch int) error {
	_, err := m.Conn.Delete(
		"delete from " + m.q(m.table()) + " where " + m.q("batch") + " = ?", batch,
	)
	return err
}

// lastBatchMust lastBatch 的内部快版（错误时返回 0）。
func (m *Migrator) lastBatchMust() int {
	v, err := m.lastBatch()
	if err != nil {
		return 0
	}
	return v
}

// migrationsAfterBatch 取 batch >= b 的迁移名（batch asc, id asc）。
func (m *Migrator) migrationsAfterBatch(b int) ([]string, error) {
	rows, err := m.Conn.SelectMaps(
		"select " + m.q("migration") + " from " + m.q(m.table()) + " where "+m.q("batch")+" >= ? order by "+m.q("batch")+" asc, "+m.q("id")+" asc", []any{b},
	)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if v, ok := row["migration"].(string); ok {
			out = append(out, v)
		}
	}
	return out, nil
}

func reverse(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[len(names)-1-i] = n
	}
	return out
}

func (m *Migrator) find(name string) *Migration {
	for i := range m.Migrations {
		if m.Migrations[i].Name == name {
			return &m.Migrations[i]
		}
	}
	return nil
}

func (m *Migrator) ranNames() (map[string]bool, error) {
	set := map[string]bool{}
	rows, err := m.Conn.SelectMaps(
		"select " + m.q("migration") + " from " + m.q(m.table()) + "", nil,
	)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if v, ok := row["migration"].(string); ok {
			set[v] = true
		}
	}
	return set, nil
}

func (m *Migrator) nextBatch() (int, error) {
	var max int64
	if err := m.Conn.Scan(
		"select coalesce(max(" + m.q("batch") + "), 0) as b from " + m.q(m.table()) + "", nil, &max,
	); err != nil {
		return 0, err
	}
	return int(max) + 1, nil
}

func (m *Migrator) lastBatch() (int, error) {
	var max int64
	if err := m.Conn.Scan(
		"select coalesce(max(" + m.q("batch") + "), 0) as b from " + m.q(m.table()) + "", nil, &max,
	); err != nil {
		return 0, err
	}
	return int(max), nil
}

func (m *Migrator) batchNames(batch int) ([]string, error) {
	rows, err := m.Conn.SelectMaps(
		"select " + m.q("migration") + " from " + m.q(m.table()) + " where "+m.q("batch")+" = ? order by "+m.q("id")+"", []any{batch},
	)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if v, ok := row["migration"].(string); ok {
			out = append(out, v)
		}
	}
	return out, nil
}
