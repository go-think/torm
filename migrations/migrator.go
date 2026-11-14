package migrations

import (
	"context"
	"database/sql"
	"errors"

	"fmt"
	"log"
	"sort"
	"strconv"

	torm "github.com/go-think/torm"
	"github.com/go-think/torm/events"
	"github.com/go-think/torm/grammar"
)

// Migration 表示单个迁移（Go 侧为注册式而非文件反射）。
type Migration struct {
	Name string
	Up   func(*torm.SchemaBuilder) error
	Down func(*torm.SchemaBuilder) error
	// WithinTransaction 迁移是否包裹事务（默认 true）：
	// nil = 包裹；显式 false = 不包裹（DDL 在部分方言上无法事务化）。
	WithinTransaction *bool
}

// RollbackOptions 对应 Migrator::rollback 的 $options
// （step / batch / pretend，对齐 Migrator.php getMigrationsForRollback）。
type RollbackOptions struct {
	// Step 大于 0 时回滚最近 Step 批（batch >= last-Step+1 的迁移，逆序）。
	Step int
	// Batch 大于 0 时回滚指定批次。
	Batch int
	// Pretend 为 true 时仅列出将回滚的迁移，不执行 Down、不删除记录
	// （注册式迁移无法内省出 SQL，故 pretend 语义收敛为列出）。
	Pretend bool
}

// MigrateOptions 对应 Migrator::run 的 $options。
type MigrateOptions struct {
	// Pretend 为 true 时仅列出待执行迁移，不执行 Up、不登记记录
	// （runUp 的 pretend 分支语义：仅列出待执行迁移，不执行）。
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

// acquireMigrationLock 获取迁移并发锁（常见迁移锁语义的简化版）：
//   - postgres：会话级 advisory lock，select pg_advisory_lock(72340172838076673)；
//   - mysql：命名用户锁，select get_lock('torm_migrations', 0)；
//   - 其他方言跳过（SQLite 单文件单写者；MariaDB / SQL Server 暂未实现，
//     需要时由上层部署约束保护）。
//
// 锁必须承载在**专用物理连接**上：会话级锁经 *sql.DB 池执行会落在随机连接，
// 取/放不在同一会话（锁永不释放）且 Fresh→Migrate 嵌套时不保证重入。此处用
// db.Conn 取出独占连接，获取/释放/整个迁移过程都在其上；MySQL 侧检查返回值
// （0 = 抢锁失败，必须报错而非静默放行）。
func (m *Migrator) acquireMigrationLock() (unlock func(), err error) {
	driver := m.Conn.Driver()
	if driver != "postgres" && driver != "mysql" {
		// 其他方言跳过（见函数注释）。
		return func() {}, nil
	}
	ctx := context.Background()
	var db *sql.DB
	// 经 Connection 的写句柄取专用连接（连接池参数由 db 层保证）
	db = m.Conn.WriteDB()
	if db == nil {
		return func() {}, errors.New("torm: migrations: no database handle for lock")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return func() {}, err
	}
	lock := func(query string) error {
		rows, qerr := conn.QueryContext(ctx, query)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		// MySQL get_lock 返回值 0 = 抢锁失败（必须报错，静默放行毫无互斥）
		if rows.Next() {
			var v any
			if serr := rows.Scan(&v); serr == nil {
				if iv, ok := v.(int64); ok && iv == 0 {
					return errors.New("torm: migrations: could not acquire lock (held by another process)")
				}
			}
		}
		return rows.Err()
	}
	switch driver {
	case "postgres":
		const pgKey = 72340172838076673
		if err := lock("select pg_advisory_lock(" + strconv.FormatInt(pgKey, 10) + ")"); err != nil {
			conn.Close()
			return func() {}, err
		}
		return func() {
			_ = lock("select pg_advisory_unlock(" + strconv.FormatInt(pgKey, 10) + ")")
			conn.Close()
		}, nil
	default: // mysql
		if err := lock("select get_lock('torm_migrations', 0)"); err != nil {
			conn.Close()
			return func() {}, err
		}
		return func() {
			_ = lock("select release_lock('torm_migrations')")
			conn.Close()
		}, nil
	}
}

// Migrate 创建迁移表（仅缺失时）并执行未运行迁移；同一批使用相同 batch，
// 每个迁移默认包裹在事务中（对应 runMigration 的 supportsSchemaTransactions
// 语义，可用 Migration.WithinTransaction 关闭）。
func (m *Migrator) Migrate(options ...MigrateOptions) (ran []string, err error) {
	opts := mergeMigrateOptions(options)
	s := m.Conn.Schema()

	unlock, err := m.acquireMigrationLock()
	if err != nil {
		return nil, err
	}
	defer unlock()

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

	// 孤儿迁移（已登记但未注册）打印警告而非静默跳过（以标准 log 提示）。
	m.warnOrphans(ranSet)

	// 待执行迁移（无待执行迁移时 fire NoPendingMigrations("up")）
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

	runOptions := map[string]any{"pretend": opts.Pretend}
	m.fireMigrationEvent(MigrationsStarted{Name: EventMigrationsStarted, Method: "up", Options: runOptions})

	// 注意：MigrationsEnded 仅在成功路径触发（迁移失败时错误直接向上传播，
	// 不会触发 MigrationsEnded）。
	for _, mig := range pending {
		m.fireMigrationEvent(MigrationStarted{Name: EventMigrationStarted, Migration: mig, Method: "up"})
		if opts.Pretend {
			// pretend：仅列出（不执行 Up、不登记记录）。
			m.fireMigrationEvent(MigrationEnded{Name: EventMigrationEnded, Migration: mig, Method: "up"})
			ran = append(ran, mig.Name)
			continue
		}
		if err := m.runMigrationFn(s, mig.Up, mig.WithinTransaction); err != nil {
			return ran, fmt.Errorf("migration [%s] failed: %w", mig.Name, err)
		}
		if _, _, err := m.Conn.Insert(
			"insert into "+m.q(m.table())+" ("+m.q("migration")+", "+m.q("batch")+") values (?, ?)",
			mig.Name, batch,
		); err != nil {
			return ran, err
		}
		m.fireMigrationEvent(MigrationEnded{Name: EventMigrationEnded, Migration: mig, Method: "up"})
		ran = append(ran, mig.Name)
	}
	m.fireMigrationEvent(MigrationsEnded{Name: EventMigrationsEnded, Method: "up", Options: runOptions})
	return ran, nil
}

// runMigrationFn 执行迁移闭包：WithinTransaction 为 nil（默认）时包裹事务
// （对应 supportsSchemaTransactions && withinTransaction 语义），
// 显式 false 时不包裹。
func (m *Migrator) runMigrationFn(s *torm.SchemaBuilder, fn func(*torm.SchemaBuilder) error, withinTransaction *bool) error {
	if withinTransaction != nil && !*withinTransaction {
		return fn(s)
	}
	return m.Conn.Transaction(func(c *torm.Connection) error {
		return fn(s)
	})
}

// RollbackBatch 回滚指定 batch（逆序执行 Down）。
func (m *Migrator) RollbackBatch(batch int) (rolled []string, err error) {
	names, err := m.batchNames(batch)
	if err != nil {
		return nil, err
	}
	return m.rollbackNamesWith(m.Conn.Schema(), reverse(names), map[string]any{"pretend": false, "batch": batch})
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

func mergeMigrateOptions(opts []MigrateOptions) MigrateOptions {
	out := MigrateOptions{}
	for _, o := range opts {
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

	unlock, err := m.acquireMigrationLock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	s := m.Conn.Schema()

	var names []string
	switch {
	case opts.Step > 0:
		// lastBatch 查询错误必须上抛：吞掉错误返回 0 会让 migrationsAfterBatch
		// 以 batch >= -Step+1 命中全部迁移，把整个仓库当作该 step 回滚。
		last, lastErr := m.lastBatch()
		if lastErr != nil {
			return nil, lastErr
		}
		names, err = m.migrationsAfterBatch(last - opts.Step + 1)
	case opts.Batch > 0:
		names, err = m.batchNames(opts.Batch)
	default:
		last, lastErr := m.lastBatch()
		if lastErr != nil {
			return nil, lastErr
		}
		if last == 0 {
			// 无批可回滚时 fire NoPendingMigrations("down")
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

	// 逆序（getMigrations 按 batch desc, id desc 取出）
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

	runOptions := map[string]any{"pretend": opts.Pretend, "step": opts.Step, "batch": opts.Batch}
	return m.rollbackNamesWith(s, names, runOptions)
}

// rollbackNamesWith 逆序回滚给定迁移名并删除记录（批次级事件包裹；
// MigrationsEnded 仅在成功路径触发，对应 rollbackMigrations 语义）。
func (m *Migrator) rollbackNamesWith(s *torm.SchemaBuilder, names []string, runOptions map[string]any) ([]string, error) {
	m.fireMigrationEvent(MigrationsStarted{Name: EventMigrationsStarted, Method: "down", Options: runOptions})
	rolled, err := m.rollbackNamesCore(s, names)
	if err != nil {
		return rolled, err
	}
	m.fireMigrationEvent(MigrationsEnded{Name: EventMigrationsEnded, Method: "down", Options: runOptions})
	return rolled, nil
}

// rollbackNamesCore 按传入顺序（应为逆序，batch desc / id desc）逐个回滚，
// 不触发批次级事件。
func (m *Migrator) rollbackNamesCore(s *torm.SchemaBuilder, names []string) (rolled []string, err error) {
	for _, name := range names {
		mig := m.find(name)
		if mig == nil || mig.Down == nil {
			// 迁移未注册（或无 Down）时跳过，迁移记录
			// 保留在仓库表中（仅在成功执行 Down 后才删除记录）。
			// 记录仍在，下次回滚仍会列出该迁移。
			continue
		}
		m.fireMigrationEvent(MigrationStarted{Name: EventMigrationStarted, Migration: mig, Method: "down"})
		if err := m.runMigrationFn(s, mig.Down, mig.WithinTransaction); err != nil {
			return rolled, fmt.Errorf("rollback [%s] failed: %w", name, err)
		}
		if _, err := m.Conn.Delete(
			"delete from "+m.q(m.table())+" where "+m.q("migration")+" = ?", name,
		); err != nil {
			return rolled, err
		}
		m.fireMigrationEvent(MigrationEnded{Name: EventMigrationEnded, Migration: mig, Method: "down"})
		rolled = append(rolled, name)
	}
	return rolled, nil
}

// Reset 回滚全部已运行迁移（按 batch 从新到旧，对齐 Migrator::reset）。
// 未注册（孤儿）迁移与无 Down 的迁移记录保留（仅在成功执行 Down 后
// 删除记录，未知的迁移记录不触碰）。
func (m *Migrator) Reset() (rolled []string, err error) {
	rolled = []string{}

	unlock, err := m.acquireMigrationLock()
	if err != nil {
		return rolled, err
	}
	defer unlock()

	batches, err := m.distinctBatches()
	if err != nil {
		return rolled, err
	}
	if len(batches) == 0 {
		return rolled, nil
	}

	runOptions := map[string]any{"pretend": false}
	m.fireMigrationEvent(MigrationsStarted{Name: EventMigrationsStarted, Method: "down", Options: runOptions})
	for _, b := range batches {
		names, err := m.batchNames(b)
		if err != nil {
			return rolled, err
		}
		if len(names) == 0 {
			continue
		}
		r, err := m.rollbackNamesCore(m.Conn.Schema(), reverse(names))
		if err != nil {
			return rolled, err
		}
		rolled = append(rolled, r...)
	}
	m.fireMigrationEvent(MigrationsEnded{Name: EventMigrationsEnded, Method: "down", Options: runOptions})
	return rolled, nil
}

// Fresh 删除全部表（含迁移仓库表）后重新执行全部迁移（对齐 Migrator::fresh：
// dropAllTables → migrate；DropAllTables 连仓库表一并删除，Migrate 重建并全量重跑）。
func (m *Migrator) Fresh() (ran []string, err error) {
	unlock, err := m.acquireMigrationLock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	if err := m.Conn.Schema().DropAllTables(); err != nil {
		return nil, err
	}
	// Migrate 内部再次取锁：advisory / get_lock 对同会话可重入，释放配对。
	return m.Migrate()
}

// warnOrphans 打印孤儿迁移警告（已登记但未注册；按名字排序保证输出稳定）。
func (m *Migrator) warnOrphans(ranSet map[string]bool) {
	var orphans []string
	for name := range ranSet {
		if m.find(name) == nil {
			orphans = append(orphans, name)
		}
	}
	if len(orphans) == 0 {
		return
	}
	sort.Strings(orphans)
	for _, name := range orphans {
		log.Printf("migrations: orphan migration %q recorded in %q but not registered; it will neither run nor roll back", name, m.table())
	}
}

// migrationsAfterBatch 取 batch >= b 的迁移名（batch asc, id asc）。
func (m *Migrator) migrationsAfterBatch(b int) ([]string, error) {
	rows, err := m.Conn.SelectMaps(
		"select "+m.q("migration")+" from "+m.q(m.table())+" where "+m.q("batch")+" >= ? order by "+m.q("batch")+" asc, "+m.q("id")+" asc", []any{b},
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
		"select "+m.q("migration")+" from "+m.q(m.table())+"", nil,
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

// distinctBatches 仓库表中全部已用批号（batch desc）。
func (m *Migrator) distinctBatches() ([]int, error) {
	rows, err := m.Conn.SelectMaps(
		"select distinct "+m.q("batch")+" from "+m.q(m.table())+" order by "+m.q("batch")+" desc", nil,
	)
	if err != nil {
		return nil, err
	}
	out := make([]int, 0, len(rows))
	for _, row := range rows {
		out = append(out, int(columnInt64(row["batch"])))
	}
	return out, nil
}

func (m *Migrator) nextBatch() (int, error) {
	var max int64
	if err := m.Conn.Scan(
		"select coalesce(max("+m.q("batch")+"), 0) as b from "+m.q(m.table())+"", nil, &max,
	); err != nil {
		return 0, err
	}
	return int(max) + 1, nil
}

func (m *Migrator) lastBatch() (int, error) {
	var max int64
	if err := m.Conn.Scan(
		"select coalesce(max("+m.q("batch")+"), 0) as b from "+m.q(m.table())+"", nil, &max,
	); err != nil {
		return 0, err
	}
	return int(max), nil
}

func (m *Migrator) batchNames(batch int) ([]string, error) {
	rows, err := m.Conn.SelectMaps(
		"select "+m.q("migration")+" from "+m.q(m.table())+" where "+m.q("batch")+" = ? order by "+m.q("id")+"", []any{batch},
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

// columnInt64 把驱动返回的列值（int64 / int / 文本）转为 int64。
func columnInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case uint64:
		return int64(x)
	case float64:
		return int64(x)
	case []byte:
		n, _ := strconv.ParseInt(string(x), 10, 64)
		return n
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	case nil:
		return 0
	default:
		n, _ := strconv.ParseInt(fmt.Sprint(x), 10, 64)
		return n
	}
}
