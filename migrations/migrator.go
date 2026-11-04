package migrations

import (
	"fmt"

	"github.com/thinkoner/torm/grammar"
	"github.com/thinkoner/torm"
)

// Migration 对应 Laravel 的迁移文件（Go 侧为注册式而非文件反射）。
type Migration struct {
	Name string
	Up   func(*torm.SchemaBuilder) error
	Down func(*torm.SchemaBuilder) error
}

// Migrator 注册式迁移执行器。
type Migrator struct {
	Conn       *torm.Connection
	Migrations []Migration
	Table      string // 默认 migrations
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

	batch, err := m.nextBatch()
	if err != nil {
		return nil, err
	}

	for _, mig := range m.Migrations {
		if ranSet[mig.Name] {
			continue
		}
		if err := m.Conn.Transaction(func(c *torm.Connection) error {
			return mig.Up(s)
		}); err != nil {
			return ran, fmt.Errorf("migration [%s] failed: %w", mig.Name, err)
		}
		if _, _, err := m.Conn.Insert(
			"insert into `"+m.table()+"` (`migration`, `batch`) values (?, ?)",
			mig.Name, batch,
		); err != nil {
			return ran, err
		}
		ran = append(ran, mig.Name)
	}
	return ran, nil
}

// RollbackBatch 回滚指定 batch（逆序执行 Down）。
func (m *Migrator) RollbackBatch(batch int) (rolled []string, err error) {
	s := m.Conn.Schema()

	names, err := m.batchNames(batch)
	if err != nil {
		return nil, err
	}

	// 逆序回滚
	for i := len(names) - 1; i >= 0; i-- {
		name := names[i]
		mig := m.find(name)
		if mig == nil || mig.Down == nil {
			continue
		}
		if err := mig.Down(s); err != nil {
			return rolled, fmt.Errorf("rollback [%s] failed: %w", name, err)
		}
		if _, err := m.Conn.Delete(
			"delete from `"+m.table()+"` where `migration` = ?", name,
		); err != nil {
			return rolled, err
		}
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
			// 该批无可回滚迁移：删除记录避免死循环（对齐 Laravel 跳过语义）
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
		"delete from `"+m.table()+"` where `batch` = ?", batch,
	)
	return err
}

// Rollback 回滚最后一批迁移（逆序执行 Down）。
func (m *Migrator) Rollback() (rolled []string, err error) {
	last, err := m.lastBatch()
	if err != nil {
		return nil, err
	}
	if last == 0 {
		return nil, nil
	}
	return m.RollbackBatch(last)
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
		"select `migration` from `"+m.table()+"`", nil,
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
		"select coalesce(max(`batch`), 0) as b from `"+m.table()+"`", nil, &max,
	); err != nil {
		return 0, err
	}
	return int(max) + 1, nil
}

func (m *Migrator) lastBatch() (int, error) {
	var max int64
	if err := m.Conn.Scan(
		"select coalesce(max(`batch`), 0) as b from `"+m.table()+"`", nil, &max,
	); err != nil {
		return 0, err
	}
	return int(max), nil
}

func (m *Migrator) batchNames(batch int) ([]string, error) {
	rows, err := m.Conn.SelectMaps(
		"select `migration` from `"+m.table()+"` where `batch` = ? order by `id`", []any{batch},
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
