package torm_test

import (
	"database/sql/driver"
	"testing"

	"github.com/go-think/torm"
	"github.com/go-think/torm/events"
	"github.com/go-think/torm/internal/faketb"
	"github.com/go-think/torm/migrations"
)

// Rollback step/batch/pretend 选项与迁移事件测试（migrator_test.go 的补充）。

func newMigratorConn(t *testing.T) (*torm.Connection, *faketb.Store) {
	t.Helper()
	st := faketb.New(t)
	conn, err := torm.Open(torm.Config{Driver: "fake", Dsn: st.Name()})
	if err != nil {
		t.Fatal(err)
	}
	return conn, st
}

func TestMigratorRollbackStep(t *testing.T) {
	conn, st := newMigratorConn(t)
	// lastBatch = 2；step=1 → batch >= 2 的迁移（仅 batch 2）
	st.On("select coalesce(max(\"batch\"), 0) as b from \"migrations\"", &faketb.Result{Columns: []string{"b"}, Rows: [][]driver.Value{{int64(2)}}})
	st.On("select \"migration\" from \"migrations\" where \"batch\" >= ? order by \"batch\" asc, \"id\" asc", &faketb.Result{
		Columns: []string{"migration"},
		Rows:    [][]driver.Value{{"mig_b1"}, {"mig_b2a"}, {"mig_b2b"}},
	})
	st.On("drop table `posts`", &faketb.Result{})
	st.On("drop table `logs`", &faketb.Result{})
	st.On("delete from \"migrations\" where \"migration\" = ?", &faketb.Result{Affected: 1})

	mig := migrations.NewMigrator(conn,
		migrations.Migration{Name: "mig_b1", Up: func(*torm.SchemaBuilder) error { return nil }},
		migrations.Migration{Name: "mig_b2a", Up: func(*torm.SchemaBuilder) error { return nil }, Down: func(s *torm.SchemaBuilder) error { return s.Drop("posts") }},
		migrations.Migration{Name: "mig_b2b", Up: func(*torm.SchemaBuilder) error { return nil }, Down: func(s *torm.SchemaBuilder) error { return s.Drop("logs") }},
	)

	rolled, err := mig.Rollback(migrations.RollbackOptions{Step: 1})
	if err != nil {
		t.Fatal(err)
	}
	// 逆序：b2b → b2a；mig_b1 无 Down 不在本批
	if len(rolled) != 2 || rolled[0] != "mig_b2b" || rolled[1] != "mig_b2a" {
		t.Fatalf("rolled = %v", rolled)
	}
}

func TestMigratorRollbackBatchOption(t *testing.T) {
	conn, st := newMigratorConn(t)
	st.On("select \"migration\" from \"migrations\" where \"batch\" = ? order by \"id\"", &faketb.Result{
		Columns: []string{"migration"},
		Rows:    [][]driver.Value{{"mig_b1"}},
	})
	st.On("drop table `users`", &faketb.Result{})
	st.On("delete from \"migrations\" where \"migration\" = ?", &faketb.Result{Affected: 1})

	mig := migrations.NewMigrator(conn,
		migrations.Migration{Name: "mig_b1", Up: func(*torm.SchemaBuilder) error { return nil }, Down: func(s *torm.SchemaBuilder) error { return s.Drop("users") }},
	)

	rolled, err := mig.Rollback(migrations.RollbackOptions{Batch: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(rolled) != 1 || rolled[0] != "mig_b1" {
		t.Fatalf("rolled = %v", rolled)
	}
}

func TestMigratorRollbackPretend(t *testing.T) {
	conn, st := newMigratorConn(t)
	st.On("select coalesce(max(\"batch\"), 0) as b from \"migrations\"", &faketb.Result{Columns: []string{"b"}, Rows: [][]driver.Value{{int64(1)}}})
	st.On("select \"migration\" from \"migrations\" where \"batch\" = ? order by \"id\"", &faketb.Result{
		Columns: []string{"migration"},
		Rows:    [][]driver.Value{{"mig_a"}},
	})
	downCalled := 0
	mig := migrations.NewMigrator(conn,
		migrations.Migration{Name: "mig_a", Up: func(*torm.SchemaBuilder) error { return nil }, Down: func(s *torm.SchemaBuilder) error {
			downCalled++
			return s.Drop("users")
		}},
	)

	rolled, err := mig.Rollback(migrations.RollbackOptions{Pretend: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rolled) != 1 || rolled[0] != "mig_a" {
		t.Fatalf("rolled = %v", rolled)
	}
	if downCalled != 0 {
		t.Fatalf("pretend should not run down, called = %d", downCalled)
	}
}

func TestMigratorMigrationEvents(t *testing.T) {
	conn, st := newMigratorConn(t)
	st.On("select * from information_schema.tables where table_schema = ? and table_name = ? and table_type = 'BASE TABLE'", &faketb.Result{Rows: [][]driver.Value{{1}}})
	st.On("select \"migration\" from \"migrations\"", &faketb.Result{Columns: []string{"migration"}, Rows: [][]driver.Value{}})
	st.On("select coalesce(max(\"batch\"), 0) as b from \"migrations\"", &faketb.Result{Columns: []string{"b"}, Rows: [][]driver.Value{{int64(0)}}})
	st.On("insert into \"migrations\" (\"migration\", \"batch\") values (?, ?)", &faketb.Result{Affected: 1})

	bus := events.NewEventBus()
	got := []string{}
	bus.Listen(migrations.EventMigrationsStarted, func(e events.Event) { got = append(got, "migrationsStarted:"+e.(migrations.MigrationsStarted).Method) })
	bus.Listen(migrations.EventMigrationStarted, func(e events.Event) { got = append(got, "migrationStarted:"+e.(migrations.MigrationStarted).Method) })
	bus.Listen(migrations.EventMigrationEnded, func(e events.Event) { got = append(got, "migrationEnded:"+e.(migrations.MigrationEnded).Method) })
	bus.Listen(migrations.EventMigrationsEnded, func(e events.Event) { got = append(got, "migrationsEnded:"+e.(migrations.MigrationsEnded).Method) })

	mig := migrations.NewMigrator(conn)
	mig.Events = bus
	mig.Migrations = []migrations.Migration{
		{Name: "mig_a", Up: func(*torm.SchemaBuilder) error { return nil }},
	}

	if _, err := mig.Migrate(); err != nil {
		t.Fatal(err)
	}
	want := []string{"migrationsStarted:up", "migrationStarted:up", "migrationEnded:up", "migrationsEnded:up"}
	if len(got) != len(want) {
		t.Fatalf("events = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events[%d] = %s, want %s (all: %v)", i, got[i], want[i], got)
		}
	}
}

func TestMigratorNoPendingMigrationsEvent(t *testing.T) {
	conn, st := newMigratorConn(t)
	// 表存在，全部已运行
	st.On("select * from information_schema.tables where table_schema = ? and table_name = ? and table_type = 'BASE TABLE'", &faketb.Result{Rows: [][]driver.Value{{1}}})
	st.On("select \"migration\" from \"migrations\"", &faketb.Result{
		Columns: []string{"migration"},
		Rows:    [][]driver.Value{{"mig_a"}},
	})

	bus := events.NewEventBus()
	fired := ""
	bus.Listen(migrations.EventNoPendingMigrations, func(e events.Event) {
		fired = e.(migrations.NoPendingMigrations).Method
	})

	mig := migrations.NewMigrator(conn)
	mig.Events = bus
	mig.Migrations = []migrations.Migration{
		{Name: "mig_a", Up: func(*torm.SchemaBuilder) error { return nil }},
	}

	ran, err := mig.Migrate()
	if err != nil {
		t.Fatal(err)
	}
	if len(ran) != 0 {
		t.Fatalf("ran = %v", ran)
	}
	if fired != "up" {
		t.Fatalf("fired = %q", fired)
	}
}

func TestMigratorRollbackNoPendingEvent(t *testing.T) {
	conn, st := newMigratorConn(t)
	st.On("select coalesce(max(\"batch\"), 0) as b from \"migrations\"", &faketb.Result{Columns: []string{"b"}, Rows: [][]driver.Value{{int64(0)}}})

	bus := events.NewEventBus()
	fired := ""
	bus.Listen(migrations.EventNoPendingMigrations, func(e events.Event) {
		fired = e.(migrations.NoPendingMigrations).Method
	})

	mig := migrations.NewMigrator(conn)
	mig.Events = bus

	rolled, err := mig.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	if len(rolled) != 0 || fired != "down" {
		t.Fatalf("rolled = %v fired = %q", rolled, fired)
	}
}
