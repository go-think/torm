package torm_test

import (
	"database/sql/driver"
	"testing"

	"github.com/thinkoner/torm"
	"github.com/thinkoner/torm/grammar"
	"github.com/thinkoner/torm/internal/faketb"
	"github.com/thinkoner/torm/migrations"
)

func TestMigratorRunsPendingMigrations(t *testing.T) {
	st := faketb.New(t)
	conn, err := torm.Open(torm.Config{Driver: "fake", Dsn: st.Name()})
	if err != nil {
		t.Fatal(err)
	}

	// HasTable（repositoryExists）：首次运行表缺失
	st.On("select * from information_schema.tables where table_schema = ? and table_name = ? and table_type = 'BASE TABLE'", &faketb.Result{})
	createMigTable := "create table `migrations` (`id` int unsigned not null auto_increment primary key, `migration` varchar(255) not null, `batch` int not null)"
	st.On(createMigTable, &faketb.Result{})
	// ranNames：空
	st.On("select \"migration\" from \"migrations\"", &faketb.Result{Columns: []string{"migration"}, Rows: [][]driver.Value{}})
	// nextBatch：0 → batch 1
	st.On("select coalesce(max(\"batch\"), 0) as b from \"migrations\"", &faketb.Result{Columns: []string{"b"}, Rows: [][]driver.Value{{int64(0)}}})

	createUsers := "create table `users` (`id` bigint unsigned not null auto_increment primary key, `name` varchar(255) not null)"
	createPosts := "create table `posts` (`id` bigint unsigned not null auto_increment primary key)"
	st.On(createUsers, &faketb.Result{})
	st.On(createPosts, &faketb.Result{})
	st.On("insert into \"migrations\" (\"migration\", \"batch\") values (?, ?)", &faketb.Result{Affected: 1})

	upCalled := 0
	mig := migrations.NewMigrator(conn,
		migrations.Migration{Name: "2026_01_01_create_users", Up: func(s *torm.SchemaBuilder) error {
			upCalled++
			return s.CreateTable("users", func(t *grammar.Blueprint) {
				t.ID()
				t.String("name")
			})
		}},
		migrations.Migration{Name: "2026_01_02_create_posts", Up: func(s *torm.SchemaBuilder) error {
			upCalled++
			return s.CreateTable("posts", func(t *grammar.Blueprint) {
				t.ID()
			})
		}},
	)

	ran, err := mig.Migrate()
	if err != nil {
		t.Fatal(err)
	}
	if len(ran) != 2 {
		t.Fatalf("ran = %v", ran)
	}
	if upCalled != 2 {
		t.Errorf("upCalled = %d", upCalled)
	}
	st.LastCall(t, "insert into \"migrations\" (\"migration\", \"batch\") values (?, ?)")
}

func TestMigratorSkipsRan(t *testing.T) {
	st := faketb.New(t)
	conn, err := torm.Open(torm.Config{Driver: "fake", Dsn: st.Name()})
	if err != nil {
		t.Fatal(err)
	}

	// HasTable（repositoryExists）：表已存在
	st.On("select * from information_schema.tables where table_schema = ? and table_name = ? and table_type = 'BASE TABLE'", &faketb.Result{Rows: [][]driver.Value{{1}}})
	st.On("create table `migrations` (`id` int unsigned not null auto_increment primary key, `migration` varchar(255) not null, `batch` int not null)", &faketb.Result{})
	st.On("select \"migration\" from \"migrations\"", &faketb.Result{
		Columns: []string{"migration"},
		Rows:   [][]driver.Value{{"2026_01_01_create_users"}},
	})
	st.On("select coalesce(max(\"batch\"), 0) as b from \"migrations\"", &faketb.Result{Columns: []string{"b"}, Rows: [][]driver.Value{{int64(1)}}})
	st.On("insert into \"migrations\" (\"migration\", \"batch\") values (?, ?)", &faketb.Result{Affected: 1})

	upCalled := 0
	mig := migrations.NewMigrator(conn,
		migrations.Migration{Name: "2026_01_01_create_users", Up: func(s *torm.SchemaBuilder) error {
			upCalled++
			return nil
		}},
		migrations.Migration{Name: "2026_01_02_create_posts", Up: func(s *torm.SchemaBuilder) error {
			upCalled++
			return nil
		}},
	)

	ran, err := mig.Migrate()
	if err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 || ran[0] != "2026_01_02_create_posts" {
		t.Fatalf("ran = %v", ran)
	}
	if upCalled != 1 {
		t.Errorf("upCalled = %d", upCalled)
	}
}

func TestMigratorRollbackLastBatch(t *testing.T) {
	st := faketb.New(t)
	conn, err := torm.Open(torm.Config{Driver: "fake", Dsn: st.Name()})
	if err != nil {
		t.Fatal(err)
	}

	// lastBatch = 1
	st.On("select coalesce(max(\"batch\"), 0) as b from \"migrations\"", &faketb.Result{Columns: []string{"b"}, Rows: [][]driver.Value{{int64(1)}}})
	st.On("select \"migration\" from \"migrations\" where \"batch\" = ? order by \"id\"", &faketb.Result{
		Columns: []string{"migration"},
		Rows:   [][]driver.Value{{"2026_01_01_create_users"}, {"2026_01_02_create_posts"}},
	})
	dropUsers := "drop table `users`"
	dropPosts := "drop table `posts`"
	st.On(dropUsers, &faketb.Result{})
	st.On(dropPosts, &faketb.Result{})
	st.On("delete from \"migrations\" where \"migration\" = ?", &faketb.Result{Affected: 1})

	downOrder := []string{}
	mig := migrations.NewMigrator(conn,
		migrations.Migration{
			Name: "2026_01_01_create_users",
			Up:   func(s *torm.SchemaBuilder) error { return nil },
			Down: func(s *torm.SchemaBuilder) error {
				downOrder = append(downOrder, "users")
				return s.Drop("users")
			},
		},
		migrations.Migration{
			Name: "2026_01_02_create_posts",
			Up:   func(s *torm.SchemaBuilder) error { return nil },
			Down: func(s *torm.SchemaBuilder) error {
				downOrder = append(downOrder, "posts")
				return s.Drop("posts")
			},
		},
	)

	rolled, err := mig.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	// 逆序回滚
	if len(rolled) != 2 || rolled[0] != "2026_01_02_create_posts" || rolled[1] != "2026_01_01_create_users" {
		t.Errorf("rolled = %v", rolled)
	}
	if len(downOrder) != 2 || downOrder[0] != "posts" || downOrder[1] != "users" {
		t.Errorf("downOrder = %v", downOrder)
	}
}

func TestMigratorRollbackEmpty(t *testing.T) {
	st := faketb.New(t)
	conn, err := torm.Open(torm.Config{Driver: "fake", Dsn: st.Name()})
	if err != nil {
		t.Fatal(err)
	}
	st.On("select coalesce(max(\"batch\"), 0) as b from \"migrations\"", &faketb.Result{Columns: []string{"b"}, Rows: [][]driver.Value{{int64(0)}}})

	mig := migrations.NewMigrator(conn)
	rolled, err := mig.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	if len(rolled) != 0 {
		t.Errorf("rolled = %v", rolled)
	}
}
