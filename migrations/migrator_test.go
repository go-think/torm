package migrations_test

import (
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-think/torm"
	"github.com/go-think/torm/events"
	"github.com/go-think/torm/internal/faketb"
	"github.com/go-think/torm/migrations"
)

// SQL 文件迁移加载器（LoadDir）、Rollback step 错误上抛、Fresh、
// WithinTransaction 与 Migrate pretend 的测试（根包 migrator_test.go 的补充）。

func newMigrationsConn(t *testing.T) (*torm.Connection, *faketb.Store) {
	t.Helper()
	st := faketb.New(t)
	conn, err := torm.Open(torm.Config{Driver: "fake", Dsn: st.Name()})
	if err != nil {
		t.Fatal(err)
	}
	return conn, st
}

func writeMigFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// ---- LoadDir：表驱动测试 ----

func TestLoadDir(t *testing.T) {
	upOne := "create table users (id integer primary key)"
	downOne := "drop table users"
	upTwo := "alter table users add column email varchar(255)"
	downTwo := "alter table users drop column email"
	upThree := "create index users_email_index on users (email)"

	tests := []struct {
		name      string
		files     map[string]string // 相对文件名 → 内容
		wantNames []string
		wantDown  map[int]string // 下标 → 期望 Down SQL（缺省 = nil）
		wantErr   string         // 非空时期望错误包含该串
	}{
		{
			name: "paired files sorted by numeric prefix",
			files: map[string]string{
				// 故意乱序写入，验证按序号排序而非目录序
				"0002_add_email.up.sql":             upTwo,
				"0002_add_email.down.sql":           downTwo,
				"0001_create_users.up.sql":          upOne,
				"0001_create_users.down.sql":        downOne,
				"20260101000000_alter_x.up.sql":     upThree,
				"README.md":                         "not a migration pair",
				"0010_missing_pair_NOT_A_MIGRATION": "ignored",
			},
			wantNames: []string{"0001_create_users", "0002_add_email", "20260101000000_alter_x"},
			wantDown:  map[int]string{0: downOne, 1: downTwo},
		},
		{
			name: "same prefix sorts by name",
			files: map[string]string{
				"0001_b_second.up.sql": "select 1",
				"0001_a_first.up.sql":  "select 2",
			},
			wantNames: []string{"0001_a_first", "0001_b_second"},
		},
		{
			name: "orphan down file skipped",
			files: map[string]string{
				"0003_only_down.down.sql": "select 3",
			},
			wantNames: []string{},
		},
		{
			name: "empty dir",
			files: map[string]string{
				"README.md": "no pairs here",
			},
			wantNames: []string{},
		},
		{
			name: "missing numeric prefix errors",
			files: map[string]string{
				"no_prefix.up.sql": "select 4",
			},
			wantErr: "NNNN_name",
		},
		{
			name: "prefix without name errors",
			files: map[string]string{
				"0001.up.sql": "select 5",
			},
			wantErr: "NNNN_name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range tt.files {
				writeMigFile(t, dir, name, content)
			}
			got, err := migrations.LoadDir(dir)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.wantNames) {
				t.Fatalf("len = %d, names = %v, want %v", len(got), names(got), tt.wantNames)
			}
			for i, m := range got {
				if m.Name != tt.wantNames[i] {
					t.Fatalf("names[%d] = %q, want %q (all: %v)", i, m.Name, tt.wantNames[i], names(got))
				}
				if m.Up == nil {
					t.Fatalf("%s: Up = nil", m.Name)
				}
				_, hasDown := tt.wantDown[i]
				switch {
				case hasDown && m.Down == nil:
					t.Fatalf("%s: Down = nil, want SQL", m.Name)
				case !hasDown && m.Down != nil:
					t.Fatalf("%s: Down = non-nil, want nil (no .down.sql)", m.Name)
				}
			}
		})
	}
}

func names(ms []migrations.Migration) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Name)
	}
	return out
}

// LoadDir 生成的迁移经 Migrator.Migrate 端到端执行：Up 闭包整段执行文件 SQL。
func TestLoadDirMigrationsRun(t *testing.T) {
	conn, st := newMigrationsConn(t)

	dir := t.TempDir()

	const upOne = "create table users (id integer primary key)"
	const upTwo = "alter table users add column email varchar(255)"
	writeMigFile(t, dir, "0001_create_users.up.sql", upOne+"\n")
	writeMigFile(t, dir, "0002_add_email.up.sql", upTwo+"\n")

	migs, err := migrations.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	// HasTable：迁移仓库表已存在
	st.On("select * from information_schema.tables where table_schema = ? and table_name = ? and table_type = 'BASE TABLE'", &faketb.Result{Rows: [][]driver.Value{{1}}})
	// ranNames：空；nextBatch：0 → batch 1
	st.On(`select "migration" from "migrations"`, &faketb.Result{Columns: []string{"migration"}, Rows: [][]driver.Value{}})
	st.On(`select coalesce(max("batch"), 0) as b from "migrations"`, &faketb.Result{Columns: []string{"b"}, Rows: [][]driver.Value{{int64(0)}}})
	// 迁移 SQL（LoadDir 原样执行文件内容）
	st.On(upOne, &faketb.Result{})
	st.On(upTwo, &faketb.Result{})
	st.On(`insert into "migrations" ("migration", "batch") values (?, ?)`, &faketb.Result{Affected: 1})

	ran, err := migrations.NewMigrator(conn, migs...).Migrate()
	if err != nil {
		t.Fatal(err)
	}
	if len(ran) != 2 || ran[0] != "0001_create_users" || ran[1] != "0002_add_email" {
		t.Fatalf("ran = %v", ran)
	}
	// 两条迁移 SQL 都按文件内容整段执行
	st.LastCall(t, upTwo)
	st.LastCall(t, upOne)
}

// ---- Rollback：step 路径查询错误上抛 ----

func TestRollbackStepLastBatchErrorPropagates(t *testing.T) {
	conn, st := newMigrationsConn(t)
	boom := errors.New("boom")
	st.On(`select coalesce(max("batch"), 0) as b from "migrations"`, &faketb.Result{Err: boom})

	mig := migrations.NewMigrator(conn,
		migrations.Migration{Name: "mig_a", Up: func(*torm.SchemaBuilder) error { return nil }, Down: func(*torm.SchemaBuilder) error { return nil }},
	)
	rolled, err := mig.Rollback(migrations.RollbackOptions{Step: 1})
	if err == nil {
		t.Fatalf("err = nil, want lastBatch error; rolled = %v", rolled)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if len(rolled) != 0 {
		t.Fatalf("rolled = %v", rolled)
	}
}

func TestRollbackStepAfterBatchErrorPropagates(t *testing.T) {
	conn, st := newMigrationsConn(t)
	boom := errors.New("boom")
	// lastBatch 正常（= 2），migrationsAfterBatch 报错 → 必须上抛而非按 0 处理
	st.On(`select coalesce(max("batch"), 0) as b from "migrations"`, &faketb.Result{Columns: []string{"b"}, Rows: [][]driver.Value{{int64(2)}}})
	st.On(`select "migration" from "migrations" where "batch" >= ? order by "batch" asc, "id" asc`, &faketb.Result{Err: boom})

	mig := migrations.NewMigrator(conn,
		migrations.Migration{Name: "mig_a", Up: func(*torm.SchemaBuilder) error { return nil }, Down: func(*torm.SchemaBuilder) error { return nil }},
	)
	rolled, err := mig.Rollback(migrations.RollbackOptions{Step: 1})
	if err == nil {
		t.Fatalf("err = nil, want migrationsAfterBatch error; rolled = %v", rolled)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if len(rolled) != 0 {
		t.Fatalf("rolled = %v", rolled)
	}
}

// ---- Fresh：DropAllTables 后全量重跑 ----

func TestFresh(t *testing.T) {
	conn, st := newMigrationsConn(t)

	// DropAllTables（fake 驱动按 MySQL 方言）：表清单 → 外键开关 → drop
	st.On("select table_name as `name`, table_schema as `schema`, (data_length + index_length) as `size`, "+
		"table_comment as `comment`, engine as `engine`, table_collation as `collation` "+
		"from information_schema.tables where table_type in ('BASE TABLE', 'SYSTEM VERSIONED') and table_schema = ? order by table_schema, table_name",
		&faketb.Result{
			Columns: []string{"name", "schema", "size", "comment", "engine", "collation"},
			Rows:    [][]driver.Value{{"migrations", "probe", int64(0), "", "", ""}},
		})
	st.On("SET FOREIGN_KEY_CHECKS=0;", &faketb.Result{})
	st.On("drop table `migrations`", &faketb.Result{})
	st.On("SET FOREIGN_KEY_CHECKS=1;", &faketb.Result{})

	// Migrate：仓库表已随 DropAllTables 删除 → 重建 + 全量重跑
	st.On("select * from information_schema.tables where table_schema = ? and table_name = ? and table_type = 'BASE TABLE'", &faketb.Result{})
	st.On("create table `migrations` (`id` int unsigned not null auto_increment primary key, `migration` varchar(255) not null, `batch` int not null)", &faketb.Result{})
	st.On(`select "migration" from "migrations"`, &faketb.Result{Columns: []string{"migration"}, Rows: [][]driver.Value{}})
	st.On(`select coalesce(max("batch"), 0) as b from "migrations"`, &faketb.Result{Columns: []string{"b"}, Rows: [][]driver.Value{{int64(0)}}})
	st.On("create table `users` (id int)", &faketb.Result{})
	st.On(`insert into "migrations" ("migration", "batch") values (?, ?)`, &faketb.Result{Affected: 1})

	upCalled := 0
	mig := migrations.NewMigrator(conn,
		migrations.Migration{Name: "2026_01_01_create_users", Up: func(s *torm.SchemaBuilder) error {
			upCalled++
			return s.Statement("create table `users` (id int)")
		}},
	)
	ran, err := mig.Fresh()
	if err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 || ran[0] != "2026_01_01_create_users" {
		t.Fatalf("ran = %v", ran)
	}
	if upCalled != 1 {
		t.Fatalf("upCalled = %d", upCalled)
	}
	// drop 与重建都发生过
	st.LastCall(t, "drop table `migrations`")
	st.LastCall(t, "create table `users` (id int)")
}

// ---- WithinTransaction：nil 包裹 / false 不包裹 ----

func TestMigrationWithinTransaction(t *testing.T) {
	t.Run("default wrapped", func(t *testing.T) {
		conn, st := newMigrationsConn(t)
		st.On(`select coalesce(max("batch"), 0) as b from "migrations"`, &faketb.Result{Columns: []string{"b"}, Rows: [][]driver.Value{{int64(1)}}})
		st.On(`select "migration" from "migrations" where "batch" = ? order by "id"`, &faketb.Result{
			Columns: []string{"migration"},
			Rows:    [][]driver.Value{{"mig_a"}},
		})
		st.On("select 1", &faketb.Result{})
		st.On(`delete from "migrations" where "migration" = ?`, &faketb.Result{Affected: 1})

		mig := migrations.NewMigrator(conn,
			migrations.Migration{Name: "mig_a", Up: func(*torm.SchemaBuilder) error { return nil }, Down: func(s *torm.SchemaBuilder) error { return s.Statement("select 1") }},
		)
		if _, err := mig.Rollback(); err != nil {
			t.Fatal(err)
		}
		var began bool
		for _, c := range st.Calls() {
			if c.Sql == "BEGIN" {
				began = true
			}
		}
		if !began {
			t.Fatalf("default migration should run inside a transaction; calls = %v", callSQLs(st))
		}
	})

	t.Run("disabled", func(t *testing.T) {
		conn, st := newMigrationsConn(t)
		st.On(`select coalesce(max("batch"), 0) as b from "migrations"`, &faketb.Result{Columns: []string{"b"}, Rows: [][]driver.Value{{int64(1)}}})
		st.On(`select "migration" from "migrations" where "batch" = ? order by "id"`, &faketb.Result{
			Columns: []string{"migration"},
			Rows:    [][]driver.Value{{"mig_a"}},
		})
		st.On("select 1", &faketb.Result{})
		st.On(`delete from "migrations" where "migration" = ?`, &faketb.Result{Affected: 1})

		noTx := false
		mig := migrations.NewMigrator(conn,
			migrations.Migration{Name: "mig_a", Up: func(*torm.SchemaBuilder) error { return nil }, Down: func(s *torm.SchemaBuilder) error { return s.Statement("select 1") }, WithinTransaction: &noTx},
		)
		if _, err := mig.Rollback(); err != nil {
			t.Fatal(err)
		}
		for _, c := range st.Calls() {
			if c.Sql == "BEGIN" || c.Sql == "COMMIT" {
				t.Fatalf("WithinTransaction=false must not wrap in a transaction; got %q", c.Sql)
			}
		}
	})
}

// ---- Migrate pretend：仅列出，不执行、不登记；事件携带 Options ----

func TestMigratePretend(t *testing.T) {
	conn, st := newMigrationsConn(t)
	st.On("select * from information_schema.tables where table_schema = ? and table_name = ? and table_type = 'BASE TABLE'", &faketb.Result{Rows: [][]driver.Value{{1}}})
	st.On(`select "migration" from "migrations"`, &faketb.Result{
		Columns: []string{"migration"},
		Rows:    [][]driver.Value{{"mig_a"}},
	})
	st.On(`select coalesce(max("batch"), 0) as b from "migrations"`, &faketb.Result{Columns: []string{"b"}, Rows: [][]driver.Value{{int64(1)}}})

	upCalled := 0
	mig := migrations.NewMigrator(conn,
		migrations.Migration{Name: "mig_a", Up: func(*torm.SchemaBuilder) error { return nil }},
		migrations.Migration{Name: "mig_b", Up: func(s *torm.SchemaBuilder) error { upCalled++; return s.Statement("select 1") }},
	)

	bus := events.NewEventBus()
	var startedOptions map[string]any
	var endedOptions map[string]any
	bus.Listen(migrations.EventMigrationsStarted, func(e events.Event) {
		startedOptions = e.(migrations.MigrationsStarted).Options
	})
	bus.Listen(migrations.EventMigrationsEnded, func(e events.Event) {
		endedOptions = e.(migrations.MigrationsEnded).Options
	})
	mig.Events = bus

	ran, err := mig.Migrate(migrations.MigrateOptions{Pretend: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 || ran[0] != "mig_b" {
		t.Fatalf("ran = %v", ran)
	}
	if upCalled != 0 {
		t.Fatalf("pretend must not run Up, upCalled = %d", upCalled)
	}
	// 不登记记录
	for _, c := range st.Calls() {
		if strings.HasPrefix(c.Sql, "insert into") {
			t.Fatalf("pretend must not log migrations; got %q", c.Sql)
		}
	}
	if startedOptions == nil || endedOptions == nil {
		t.Fatalf("MigrationsStarted/Ended Options must not be nil: %v / %v", startedOptions, endedOptions)
	}
	if v, ok := startedOptions["pretend"].(bool); !ok || !v {
		t.Fatalf("startedOptions = %v, want pretend=true", startedOptions)
	}
}

// ---- 孤儿迁移：Reset 保留记录；Migrate 打印警告 ----

func TestResetKeepsOrphanRecords(t *testing.T) {
	conn, st := newMigrationsConn(t)
	// 批 1 含孤儿（未注册）与已注册迁移
	st.On(`select distinct "batch" from "migrations" order by "batch" desc`, &faketb.Result{
		Columns: []string{"batch"},
		Rows:    [][]driver.Value{{int64(1)}},
	})
	st.On(`select "migration" from "migrations" where "batch" = ? order by "id"`, &faketb.Result{
		Columns: []string{"migration"},
		Rows:    [][]driver.Value{{"orphan_a"}, {"reg_b"}},
	})
	st.On("drop table `reg_b`", &faketb.Result{})
	st.On(`delete from "migrations" where "migration" = ?`, &faketb.Result{Affected: 1})

	mig := migrations.NewMigrator(conn,
		migrations.Migration{Name: "reg_b", Up: func(*torm.SchemaBuilder) error { return nil }, Down: func(s *torm.SchemaBuilder) error { return s.Drop("reg_b") }},
	)
	rolled, err := mig.Reset()
	if err != nil {
		t.Fatal(err)
	}
	if len(rolled) != 1 || rolled[0] != "reg_b" {
		t.Fatalf("rolled = %v, want [reg_b] (孤儿迁移跳过)", rolled)
	}
	// 孤儿记录不得被整批抹掉：不允许出现按 batch 的删除（旧 deleteBatch 行为）
	for _, c := range st.Calls() {
		if strings.Contains(c.Sql, `delete from "migrations" where "batch" = ?`) {
			t.Fatalf("Reset must not wipe batch records (orphan records must survive); got %q", c.Sql)
		}
	}
}

func callSQLs(st *faketb.Store) []string {
	var out []string
	for _, c := range st.Calls() {
		out = append(out, c.Sql)
	}
	return out
}
