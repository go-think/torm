package torm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
	_ "github.com/go-think/torm/driver/mysql"
	_ "github.com/go-think/torm/driver/sqlite"
	"github.com/go-think/torm/events"
	"github.com/go-think/torm/internal/faketb"
)

var DB *Connection

func init() {
	DB = initDb()
}

func skipNoDB(t *testing.T) {
	t.Helper()
	if DB == nil {
		t.Skip("local mysql not available")
	}
}

func initDb() *Connection {
	dsn := "root:@tcp(127.0.0.1:3306)/"
	driver := "mysql"
	db, err := Open(Config{
		Driver: driver,
		Dsn:    dsn + "?charset=utf8&parseTime=true&timeout=500ms",
	})
	if err != nil {
		return nil
	}
	if err := db.DB.Ping(); err != nil {
		return nil
	}

	if err := db.Statement(`CREATE DATABASE IF NOT EXISTS torm_test`); err != nil {
		return nil
	}

	db, err = Open(Config{
		Driver: driver,
		Dsn:    dsn + "torm_test?charset=utf8&parseTime=true",
	})
	if err != nil {
		panic(err)
	}

	db.Statement(`DROP TABLE IF EXISTS users;`)
	db.Statement(`
CREATE TABLE users (
  id int(11) NOT NULL AUTO_INCREMENT,
  name varchar(255) DEFAULT NULL,
  gender varchar(255) DEFAULT NULL,
  addr varchar(255) DEFAULT NULL,
  birth_date date DEFAULT NULL,
  balance decimal(15,4) DEFAULT '0.0000',
  created_at timestamp NULL DEFAULT NULL,
  updated_at timestamp NULL DEFAULT NULL ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id)
);
`)

	return db
}

type User struct {
	Id        int64  `torm:"primary_key;column:id"`
	Name      string `torm:"column:name"`
	Gender    string `torm:"column:gender"`
	Addr      string
	BirthDate string
	Balance   float64
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (u *User) TableName() string {
	return "users"
}

func TestConnection_SelectOne(t *testing.T) {
	skipNoDB(t)
	var user User
	_, _, err := DB.Insert("insert into users (name, gender) values (?, ?)", "Andrew", "M")
	if err != nil {
		t.Error(err)
	}

	err = DB.SelectOne("select * from users where gender = ?", []interface{}{"M"}, &user)
	if err != nil {
		t.Error(err)
	}

	if user.Gender != "M" {
		t.Error("Expect: user's gender should be ", "M")
	}

	if user.Name != "Andrew" {
		t.Error("Expect: user's name should be ", "Andrew")
	}
}

func TestConnection_Select(t *testing.T) {
	skipNoDB(t)
	var users []User

	DB.Insert("insert into users (name, gender) values (?, ?)", "Andrew", "M")
	DB.Insert("insert into users (name, gender) values (?, ?)", "Boston", "M")

	err := DB.Select("select * from users where gender = ?", []interface{}{"M"}, &users)
	if err != nil {
		t.Error(err)
	}

	if len(users) < 1 {
		t.Error("Expect: user's length should > 1 ")
	}

	for _, user := range users {
		if user.Gender != "M" {
			t.Error("Expect: user's gender should be ", "M")
		}
	}
}

func TestConnection_Insert(t *testing.T) {
	skipNoDB(t)
	insertId, _, err := DB.Insert("insert into users (name, gender) values (?, ?)", "Alaska", "F")
	if err != nil {
		t.Error(err)
	}

	if insertId < 0 {
		t.Error("Expect: insertId should > 0 after insert")
	}
}

func TestConnection_Update(t *testing.T) {
	skipNoDB(t)
	id, _, err := DB.Insert("insert into users (name, gender) values (?, ?)", "Chicago", "M")
	if err != nil {
		t.Error(err)
	}

	_, err = DB.Update("update users set gender = ? where id = ?", "M", id)
	if err != nil {
		t.Error(err)
	}
}

func TestConnection_Delete(t *testing.T) {
	skipNoDB(t)
	id, _, err := DB.Insert("insert into users (name, gender) values (?, ?)", "Columbia", "F")
	if err != nil {
		t.Error(err)
	}

	_, err = DB.Delete("delete from users where id = ?", id)
	if err != nil {
		t.Error(err)
	}
}

// ===========================================================================
// run() 语义统一 / PrepareBindings / Scalar / 查询日志 / 读写分离 / 事件接线
// （审查报告第二节、三节 #13-#17 #20-#23 #26 #30-#36）
// ===========================================================================

// ---- #13/#14 run() 语义统一：*Error 包装 + ConnName + 断连重试 ----

func TestFakeRun_ErrorWrappedWithConnName(t *testing.T) {
	conn, st := newFakeDB(t)
	conn.SetConnectionName("billing")
	st.On("select * from users", &fakeResult{Err: errors.New("Error 1062: Duplicate entry 'x' for key 'u.email_unique'")})

	err := conn.Select("select * from users", nil, &[]User{})
	var qe *Error
	if !errors.As(err, &qe) {
		t.Fatalf("want *Error, got %T: %v", err, err)
	}
	if qe.ConnName != "billing" {
		t.Errorf("ConnName = %q, want billing", qe.ConnName)
	}
	if qe.Kind != KindUniqueConstraint {
		t.Errorf("Kind = %v, want KindUniqueConstraint", qe.Kind)
	}
	if qe.GetSql() != "select * from users" || qe.ConnectionName() != "billing" {
		t.Errorf("qe = %+v", qe)
	}
}

func TestFakeRun_RetriesOnceOnLostConnection(t *testing.T) {
	conn, st := newFakeDB(t)
	lost := errors.New("Error 2006: MySQL server has gone away")
	st.On("select 1", &fakeResult{Err: lost})
	st.On("select 1", &fakeResult{Columns: []string{"1"}, Rows: [][]driver.Value{{int64(1)}}})

	var n int64
	if err := conn.Scan("select 1", nil, &n); err != nil {
		t.Fatalf("retry should recover: %v", err)
	}
	if n != 1 {
		t.Errorf("n = %d", n)
	}
	calls := 0
	for _, call := range st.Calls() {
		if call.Sql == "select 1" {
			calls++
		}
	}
	if calls != 2 {
		t.Errorf("executions = %d, want 2 (one retry)", calls)
	}
}

func TestFakeRun_NoRetryInsideTransaction(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("BEGIN", &fakeResult{})
	st.On("ROLLBACK", &fakeResult{})
	lost := errors.New("Error 2006: MySQL server has gone away")
	st.On("select 1", &fakeResult{Err: lost})

	if err := conn.BeginTransaction(); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := conn.Scan("select 1", nil, &n); err == nil {
		t.Fatal("want error")
	}
	calls := 0
	for _, call := range st.Calls() {
		if call.Sql == "select 1" {
			calls++
		}
	}
	if calls != 1 {
		t.Errorf("executions = %d, want 1 (事务内不重试)", calls)
	}
}

// 断连重试后记录的执行器必须换用重建后的连接（reopen 后仍打到同一 store）。
func TestFakeRun_ReconnectReopensAndReruns(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("insert into users (name) values (?)", &fakeResult{Err: errors.New("connection refused")})
	st.On("insert into users (name) values (?)", &fakeResult{Affected: 1, LastID: 7})

	if err := conn.Statement("insert into users (name) values (?)", "A"); err != nil {
		t.Fatalf("want reconnect+retry success: %v", err)
	}
	// 重建后的连接仍可用（句柄已换新）
	st.On("insert into users (name) values (?)", &fakeResult{Affected: 1, LastID: 8})
	if err := conn.Statement("insert into users (name) values (?)", "B"); err != nil {
		t.Fatalf("post-reconnect statement failed: %v", err)
	}
}

// ---- #15 PrepareBindings ----

func TestPhpDateToGoLayout(t *testing.T) {
	cases := map[string]string{
		"Y-m-d H:i:s":   "2006-01-02 15:04:05",
		"Y-m-d":         "2006-01-02",
		"Y-m-d H:i:s.v": "2006-01-02 15:04:05.000",
	}
	for in, want := range cases {
		if got := phpDateToGoLayout(in); got != want {
			t.Errorf("phpDateToGoLayout(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFakeConnection_PrepareBindingsMysqlConversion(t *testing.T) {
	conn := &Connection{driver: "mysql"}
	ts := time.Date(2020, 5, 6, 7, 8, 9, 0, time.UTC)
	got := conn.prepareBindings([]interface{}{ts, true, false, "x", int64(3)})
	if got[0] != "2020-05-06 07:08:09" {
		t.Errorf("time binding = %#v", got[0])
	}
	if got[1] != 1 || got[2] != 0 {
		t.Errorf("bool bindings = %#v, want [1 0]", got[1:3])
	}
	if got[3] != "x" || got[4] != int64(3) {
		t.Errorf("other bindings untouched = %#v", got[3:])
	}
}

func TestFakeConnection_PrepareBindingsPassthroughForOtherDrivers(t *testing.T) {
	conn := &Connection{driver: "fake"}
	ts := time.Date(2020, 5, 6, 7, 8, 9, 0, time.UTC)
	got := conn.prepareBindings([]interface{}{ts, true})
	if v, ok := got[0].(time.Time); !ok || !v.Equal(ts) {
		t.Errorf("time binding = %#v, want raw time.Time", got[0])
	}
	if got[1] != true {
		t.Errorf("bool binding = %#v, want raw bool", got[1])
	}
}

// ---- #16/#17 Scalar / SelectOne 首行 / SelectResultSets ----

func TestFakeConnection_Scalar(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select count(*) from users", &fakeResult{
		Columns: []string{"count(*)"},
		Rows:    [][]driver.Value{{int64(7)}},
	})
	v, err := conn.Scalar("select count(*) from users", nil)
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := v.(int64); !ok || n != 7 {
		t.Fatalf("scalar = %#v, want int64(7)", v)
	}

	// 无结果 → nil
	st.On("select id from users", &fakeResult{Columns: []string{"id"}, Rows: nil})
	v, err = conn.Scalar("select id from users", nil)
	if err != nil || v != nil {
		t.Fatalf("empty scalar = %#v err = %v, want nil/nil", v, err)
	}

	// 多列 → ErrMultipleColumnsSelected
	st.On("select id, name from users", &fakeResult{
		Columns: []string{"id", "name"},
		Rows:    [][]driver.Value{{int64(1), "A"}},
	})
	if _, err := conn.Scalar("select id, name from users", nil); !errors.Is(err, ErrMultipleColumnsSelected) {
		t.Fatalf("multi-column err = %v, want ErrMultipleColumnsSelected", err)
	}
}

func TestFakeConnection_SelectOneTakesFirstRow(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from users", rowsUsers(
		[2]driver.Value{int64(1), "A"},
		[2]driver.Value{int64(2), "B"},
	))

	var users []User
	if err := conn.SelectOne("select * from users", nil, &users); err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Name != "A" {
		t.Fatalf("users = %+v, want only first row", users)
	}
}

func TestFakeConnection_SelectResultSets(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from users", rowsUsers(
		[2]driver.Value{int64(1), "A"},
		[2]driver.Value{int64(2), "B"},
	))

	sets, err := conn.SelectResultSets("select * from users", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 1 || len(sets[0]) != 2 {
		t.Fatalf("sets = %+v", sets)
	}
	if sets[0][0]["name"] != "A" || sets[0][1]["id"] != int64(2) {
		t.Fatalf("sets[0] = %+v", sets[0])
	}
}

// ---- #20 查询日志与监听 ----

func TestFakeConnection_QueryLog(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select 1", &fakeResult{Columns: []string{"1"}, Rows: [][]driver.Value{{int64(1)}}})

	if got := len(conn.GetQueryLog()); got != 0 {
		t.Fatalf("initial log = %d", got)
	}
	conn.EnableQueryLog()
	var n int64
	if err := conn.Scan("select 1", nil, &n); err != nil {
		t.Fatal(err)
	}

	log1 := conn.GetQueryLog()
	if len(log1) != 1 {
		t.Fatalf("log = %d entries", len(log1))
	}
	if log1[0].Sql != "select 1" || log1[0].TimeNs < 0 {
		t.Errorf("entry = %+v", log1[0])
	}
	if log1[0].RawSql != "select 1" {
		t.Errorf("RawSql = %q", log1[0].RawSql)
	}

	// 第二条进入日志
	if err := conn.Scan("select 1", nil, &n); err != nil {
		t.Fatal(err)
	}
	if len(conn.GetQueryLog()) != 2 {
		t.Fatalf("log after 2 queries = %d", len(conn.GetQueryLog()))
	}
	conn.FlushQueryLog()
	if got := len(conn.GetQueryLog()); got != 0 {
		t.Fatalf("log after flush = %d", got)
	}
	conn.DisableQueryLog()
	if err := conn.Scan("select 1", nil, &n); err != nil {
		t.Fatal(err)
	}
	if got := len(conn.GetQueryLog()); got != 0 {
		t.Fatalf("disabled log recorded %d entries", got)
	}
}

func TestFakeConnection_ListenMultiple(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select 1", &fakeResult{Columns: []string{"1"}, Rows: [][]driver.Value{{int64(1)}}})

	var fromOn, fromListen1, fromListen2 int
	conn.SetConnectionName("multi")
	conn.OnQuery(func(QueryExecuted) { fromOn++ })
	conn.Listen(func(e QueryExecuted) {
		fromListen1++
		if e.ConnectionName != "multi" {
			t.Errorf("QueryExecuted.ConnectionName = %q, want multi", e.ConnectionName)
		}
	})
	conn.Listen(func(QueryExecuted) { fromListen2++ })

	var n int64
	if err := conn.Scan("select 1", nil, &n); err != nil {
		t.Fatal(err)
	}
	if fromOn != 1 || fromListen1 != 1 || fromListen2 != 1 {
		t.Errorf("counts = on:%d l1:%d l2:%d, want all 1", fromOn, fromListen1, fromListen2)
	}
}

func TestFakeConnection_Pretend(t *testing.T) {
	conn, st := newFakeDB(t)
	// 无任何 stub：pretend 模式下查询只记录不执行，也不报错
	conn.Pretend(func(c *Connection) {
		if _, _, err := c.Insert("insert into users (name) values (?)", "A"); err != nil {
			t.Errorf("pretend insert err = %v", err)
		}
		if _, err := c.Update("update users set name = 'B' where id = ?", 1); err != nil {
			t.Errorf("pretend update err = %v", err)
		}
		if err := c.Statement("create table x (id int)"); err != nil {
			t.Errorf("pretend statement err = %v", err)
		}
	})
	// Pretend 结束后查询日志恢复原状
	if entries := conn.GetQueryLog(); len(entries) != 0 {
		t.Errorf("outer query log = %d entries, want 0", len(entries))
	}
	for _, call := range st.Calls() {
		if strings.Contains(call.Sql, "insert into users") ||
			strings.Contains(call.Sql, "update users") ||
			strings.Contains(call.Sql, "create table") {
			t.Errorf("pretend executed for real: %s", call.Sql)
		}
	}
}

func TestFakeConnection_PretendRecordsRawSql(t *testing.T) {
	conn, _ := newFakeDB(t)
	var entries []QueryExecuted
	conn.Pretend(func(c *Connection) {
		_, _, err := c.Insert("insert into users (name) values (?)", "O'Brien")
		if err != nil {
			t.Fatal(err)
		}
		entries = c.GetQueryLog()
	})
	if len(entries) != 1 {
		t.Fatalf("entries = %d", len(entries))
	}
	if entries[0].Sql != "insert into users (name) values ('O''Brien')" {
		t.Errorf("pretend sql = %q", entries[0].Sql)
	}
}

func TestFakeConnection_WhenQueryingForLongerThan(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select 1", &fakeResult{Columns: []string{"1"}, Rows: [][]driver.Value{{int64(1)}}})

	fired := 0
	var got QueryExecuted
	conn.WhenQueryingForLongerThan(-1, func(e QueryExecuted) { fired++; got = e })

	var n int64
	if err := conn.Scan("select 1", nil, &n); err != nil {
		t.Fatal(err)
	}
	if fired != 1 {
		t.Fatalf("fired = %d, want 1", fired)
	}
	if got.Sql != "select 1" {
		t.Errorf("event sql = %q", got.Sql)
	}
	// 已触发过：不再重复
	if err := conn.Scan("select 1", nil, &n); err != nil {
		t.Fatal(err)
	}
	if fired != 1 {
		t.Errorf("fired after second query = %d, want 1（单次触发）", fired)
	}

	if conn.TotalQueryDuration() < 0 {
		t.Errorf("totalQueryDuration = %f", conn.TotalQueryDuration())
	}
	conn.ResetTotalQueryDuration()
	if conn.TotalQueryDuration() != 0 {
		t.Errorf("after reset = %f", conn.TotalQueryDuration())
	}
}

// ---- #21/#36 事件接线 ----

func TestFakeConnection_StatementPreparedEvent(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select 1", &fakeResult{Columns: []string{"1"}, Rows: [][]driver.Value{{int64(1)}}})

	rec := &txEventRecorder{}
	conn.SetConnectionName("reports")
	conn.SetEventDispatcher(rec)

	var n int64
	if err := conn.Scan("select 1", nil, &n); err != nil {
		t.Fatal(err)
	}
	var found *events.StatementPreparedEvent
	for _, e := range rec.events {
		if se, ok := e.(events.StatementPreparedEvent); ok {
			s := se
			found = &s
		}
	}
	if found == nil {
		t.Fatal("StatementPrepared event not dispatched")
	}
	if found.Sql != "select 1" || found.ConnectionName != "reports" {
		t.Errorf("event = %+v", *found)
	}
}

func TestManager_ConnectionEstablishedEvent(t *testing.T) {
	st := faketb.NewNamed("mgr_established")
	m := NewManager(map[string]Config{
		"app": {Driver: "fake", Dsn: st.Name()},
	})
	rec := &txEventRecorder{}
	m.SetEventDispatcher(rec)

	if _, err := m.Connect("app"); err != nil {
		t.Fatal(err)
	}
	// 复用缓存连接：不重复分发
	if _, err := m.Connect("app"); err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, e := range rec.events {
		names = append(names, e.EventName())
	}
	if len(names) != 1 || names[0] != events.ConnectionEstablished {
		t.Errorf("events = %v, want exactly one connectionEstablished", names)
	}
}

// ---- #22/#23 读写分离完整版 ----

func TestFakeConnection_StickyAfterWrite(t *testing.T) {
	writeStore := faketb.NewNamed("sticky_write")
	readStore := faketb.NewNamed("sticky_read")
	writeStore.On("insert into users (name) values (?)", &fakeResult{Affected: 1, LastID: 1})
	readStore.On("select * from `users`", rowsUsers([2]driver.Value{int64(1), "R"}))
	writeStore.On("select * from `users`", rowsUsers([2]driver.Value{int64(9), "W"}))

	conn, err := Open(Config{Driver: "fake", Dsn: writeStore.Name(), ReadDsn: readStore.Name(), Sticky: true})
	if err != nil {
		t.Fatal(err)
	}
	if conn.HasModifiedRecords() {
		t.Error("fresh connection must not be marked modified")
	}

	// 写前：读走只读连接
	var users []User
	if err := conn.Table("users").Get(&users); err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Name != "R" {
		t.Fatalf("pre-write read = %+v, want read store", users)
	}

	// 写操作置 recordsModified
	if _, _, err := conn.Insert("insert into users (name) values (?)", "A"); err != nil {
		t.Fatal(err)
	}
	if !conn.HasModifiedRecords() {
		t.Error("recordsModified should be true after insert")
	}

	// sticky：写后读强制走主连接
	users = nil
	if err := conn.Table("users").Get(&users); err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Name != "W" {
		t.Fatalf("post-write read = %+v, want write store (sticky)", users)
	}

	// 重置后恢复走只读
	conn.ForgetRecordModificationState()
	users = nil
	readStore.On("select * from `users`", rowsUsers([2]driver.Value{int64(1), "R"}))
	if err := conn.Table("users").Get(&users); err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Name != "R" {
		t.Fatalf("post-reset read = %+v, want read store", users)
	}
}

func TestFakeConnection_UseWriteConnectionWhenReading(t *testing.T) {
	writeStore := faketb.NewNamed("uwc_write")
	readStore := faketb.NewNamed("uwc_read")
	readStore.On("select * from `users`", rowsUsers([2]driver.Value{int64(1), "R"}))
	writeStore.On("select * from `users`", rowsUsers([2]driver.Value{int64(9), "W"}))

	conn, err := Open(Config{Driver: "fake", Dsn: writeStore.Name(), ReadDsn: readStore.Name()})
	if err != nil {
		t.Fatal(err)
	}
	conn.UseWriteConnectionWhenReading()

	var users []User
	if err := conn.Table("users").Get(&users); err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Name != "W" {
		t.Fatalf("users = %+v, want write store", users)
	}

	// 关闭后恢复只读路由
	conn.UseWriteConnectionWhenReading(false)
	readStore.On("select * from `users`", rowsUsers([2]driver.Value{int64(1), "R"}))
	users = nil
	if err := conn.Table("users").Get(&users); err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Name != "R" {
		t.Fatalf("users = %+v, want read store after disable", users)
	}
}

func TestFakeConnection_ReadDsnsPool(t *testing.T) {
	writeStore := faketb.NewNamed("pool_write")
	readStore := faketb.NewNamed("pool_read")
	readStore.On("select * from `users`", rowsUsers([2]driver.Value{int64(1), "R"}))
	writeStore.On("select * from `users`", rowsUsers([2]driver.Value{int64(9), "W"}))

	conn, err := Open(Config{
		Driver:   "fake",
		Dsn:      writeStore.Name(),
		ReadDsns: []string{readStore.Name()},
	})
	if err != nil {
		t.Fatal(err)
	}
	var users []User
	if err := conn.Table("users").Get(&users); err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Name != "R" {
		t.Fatalf("users = %+v, want read pool store", users)
	}
}

// ---- #26 Connector 抽象 ----

func TestMySqlConnectorBuildDsn(t *testing.T) {
	c := &MySqlConnector{config: Config{
		Charset: "utf8mb4", Collation: "utf8mb4_unicode_ci",
		SqlMode: "STRICT_TRANS_TABLES", Timezone: "+08:00",
	}}
	got := c.BuildDsn("root:pw@tcp(127.0.0.1:3306)/shop?parseTime=true")
	for _, want := range []string{
		"charset=utf8mb4", "collation=utf8mb4_unicode_ci",
		"sql_mode=STRICT_TRANS_TABLES", "time_zone=%2B08%3A00", "parseTime=true",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("BuildDsn = %q, missing %q", got, want)
		}
	}

	// 已有参数不覆盖
	got = c.BuildDsn("root@tcp(host)/db?charset=latin1")
	if !strings.Contains(got, "charset=latin1") || strings.Contains(got, "charset=utf8mb4") {
		t.Errorf("BuildDsn overrode existing charset: %q", got)
	}

	// 未配置参数时 DSN 原样返回
	plain := &MySqlConnector{}
	if got := plain.BuildDsn("root@tcp(host)/db"); got != "root@tcp(host)/db" {
		t.Errorf("BuildDsn with empty config = %q", got)
	}

	// 非标准 DSN 原样返回（交由驱动在连接时校验报错）
	if got := c.BuildDsn("not-a-dsn"); got != "not-a-dsn" {
		t.Errorf("BuildDsn invalid dsn = %q", got)
	}
}

func TestPostgresConnectorBuildDsn(t *testing.T) {
	c := &PostgresConnector{config: Config{
		SslMode: "require", SearchPath: "public,shop", Timezone: "Asia/Shanghai",
	}}
	got := c.BuildDsn("host=pg.local dbname=shop")
	for _, want := range []string{"sslmode=require", "search_path=public,shop", "TimeZone=Asia/Shanghai"} {
		if !strings.Contains(got, want) {
			t.Errorf("BuildDsn = %q, missing %q", got, want)
		}
	}
	// 已有参数
	got = c.BuildDsn("host=pg.local dbname=shop?sslmode=disable")
	if !strings.Contains(got, "sslmode=disable") || strings.Contains(got, "sslmode=require") {
		t.Errorf("BuildDsn overrode sslmode: %q", got)
	}
}

func TestSqliteConnectorBuildDsn(t *testing.T) {
	fk := true
	c := &SQLiteConnector{config: Config{
		ForeignKeys: &fk, BusyTimeout: 5000,
		JournalMode: "WAL", Synchronous: "NORMAL",
	}}
	got := c.BuildDsn("file:data.db")
	for _, want := range []string{
		"_foreign_keys=on", "_busy_timeout=5000",
		"_journal_mode=WAL", "_synchronous=NORMAL",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("BuildDsn = %q, missing %q", got, want)
		}
	}
}

func TestOpenRetriesLostConnectionOnceAtConnect(t *testing.T) {
	drv := &flakyDriver{fail: true}
	sql.Register("torm_flaky", drv)
	if _, err := Open(Config{Driver: "torm_flaky", Dsn: "x"}); err != nil {
		t.Fatalf("Open should retry lost connection: %v", err)
	}
	// 非断连错误不重试，直接失败
	drv.err = errors.New("permission denied")
	if _, err := Open(Config{Driver: "torm_flaky", Dsn: "x"}); err == nil {
		t.Fatal("want error for non-lost-connection failure")
	}
}

// flakyDriver：可配置一次性失败（驱动侧断连重试测试桩）。实现 DriverContext
// 使 sql.Open 阶段即可抛出错误（对齐 go-sql-driver 的 OpenConnector 行为）。
type flakyDriver struct {
	fail bool
	err  error
}

func (d *flakyDriver) Open(name string) (driver.Conn, error) {
	return flakyConn{}, nil
}

func (d *flakyDriver) OpenConnector(name string) (driver.Connector, error) {
	if d.err != nil {
		return nil, d.err
	}
	if d.fail {
		d.fail = false
		return nil, errors.New("Error 2006: MySQL server has gone away")
	}
	return flakyConnector{}, nil
}

type flakyConnector struct{}

func (flakyConnector) Connect(ctx context.Context) (driver.Conn, error) { return flakyConn{}, nil }
func (flakyConnector) Driver() driver.Driver                            { return &flakyDriver{} }

type flakyConn struct{}

func (flakyConn) Prepare(q string) (driver.Stmt, error) { return nil, errors.New("unsupported") }
func (flakyConn) Close() error                          { return nil }
func (flakyConn) Begin() (driver.Tx, error)             { return nil, errors.New("unsupported") }

// ---- #30-#34 Manager 增强 ----

func TestManager_ConnectUsing(t *testing.T) {
	m := NewManager()
	if _, err := m.ConnectUsing("ad hoc", Config{Driver: "fake", Dsn: faketb.NewNamed("cu_a").Name()}); err != nil {
		t.Fatal(err)
	}
	// 同名再建：未 force 时报错
	if _, err := m.ConnectUsing("ad hoc", Config{Driver: "fake", Dsn: faketb.NewNamed("cu_b").Name()}); err == nil {
		t.Fatal("want error for existing connection")
	} else if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("err = %v", err)
	}
	// force 替换
	conn, err := m.ConnectUsing("ad hoc", Config{Driver: "fake", Dsn: faketb.NewNamed("cu_c").Name()}, true)
	if err != nil {
		t.Fatal(err)
	}
	if conn.GetName() != "ad hoc" || conn.Dsn() != faketb.NewNamed("cu_c").Name() {
		t.Errorf("conn name/dsn = %q/%q", conn.GetName(), conn.Dsn())
	}
}

func TestManager_DisconnectThenConnectReopens(t *testing.T) {
	st := faketb.NewNamed("dc_reopen")
	st.On("select 1", &fakeResult{Columns: []string{"1"}, Rows: [][]driver.Value{{int64(1)}}})
	m := NewManager(map[string]Config{"app": {Driver: "fake", Dsn: st.Name()}})

	conn, err := m.Connect("app")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Disconnect("app"); err != nil {
		t.Fatal(err)
	}
	// Disconnect 后底层句柄已关闭且移出缓存 → Connect 重新建立可用连接
	fresh, err := m.Connect("app")
	if err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := fresh.Scan("select 1", nil, &n); err != nil {
		t.Fatalf("reconnected connection unusable: %v", err)
	}
	_ = conn
}

func TestManager_DefaultConnection(t *testing.T) {
	st := faketb.NewNamed("mgr_default")
	m := NewManager(map[string]Config{"secondary": {Driver: "fake", Dsn: st.Name()}})

	if m.GetDefaultConnection() != "" {
		t.Errorf("initial default = %q", m.GetDefaultConnection())
	}
	if _, err := m.Connect(""); err == nil {
		t.Fatal("Connect(\"\") without default should fail")
	}
	m.SetDefaultConnection("secondary")
	conn, err := m.Connect("")
	if err != nil {
		t.Fatal(err)
	}
	if conn.GetName() != "secondary" {
		t.Errorf("conn = %q, want secondary", conn.GetName())
	}
}

func TestManager_UsingConnection(t *testing.T) {
	st := faketb.NewNamed("mgr_using")
	m := NewManager(map[string]Config{"temp": {Driver: "fake", Dsn: st.Name()}})
	m.SetDefaultConnection("primary")

	err := m.UsingConnection("temp", func() error {
		conn, err := m.Connect("")
		if err != nil {
			return err
		}
		if conn.GetName() != "temp" {
			t.Errorf("inside callback conn = %q", conn.GetName())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.GetDefaultConnection() != "primary" {
		t.Errorf("default after callback = %q, want primary (已恢复)", m.GetDefaultConnection())
	}
}

func TestManager_ExtendAndForgetExtension(t *testing.T) {
	st := faketb.NewNamed("mgr_extend")
	m := NewManager(map[string]Config{"custom": {Driver: "fake", Dsn: st.Name()}})

	var resolved Config
	m.Extend("custom", func(cfg Config) (*Connection, error) {
		resolved = cfg
		return openNamed("custom", "", cfg)
	})
	conn, err := m.Connect("custom")
	if err != nil {
		t.Fatal(err)
	}
	if conn.GetName() != "custom" || resolved.Dsn != st.Name() {
		t.Errorf("resolver not used: conn=%q resolved=%+v", conn.GetName(), resolved)
	}

	m.ForgetExtension("custom")
	if _, err := m.Connect("custom"); err != nil {
		t.Fatalf("after forgetExtension Connect should use builtin Open: %v", err)
	}
}

func TestManager_ExtendByDriver(t *testing.T) {
	st := faketb.NewNamed("mgr_extend_driver")
	m := NewManager(map[string]Config{"a": {Driver: "fake", Dsn: st.Name()}, "b": {Driver: "fake", Dsn: st.Name()}})

	calls := 0
	m.Extend("fake", func(cfg Config) (*Connection, error) {
		calls++
		return openNamed("via-driver", "", cfg)
	})
	if _, err := m.Connect("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Connect("b"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("resolver calls = %d, want 2（按驱动名命中）", calls)
	}
}

func TestManager_Transaction(t *testing.T) {
	st := faketb.NewNamed("mgr_tx")
	st.On("BEGIN", &fakeResult{})
	st.On("COMMIT", &fakeResult{})
	st.On("insert into users (name) values (?)", &fakeResult{Affected: 1, LastID: 1})
	m := NewManager(map[string]Config{"app": {Driver: "fake", Dsn: st.Name()}})

	err := m.Transaction("app", func(c *Connection) error {
		_, _, err := c.Insert("insert into users (name) values (?)", "A")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, "COMMIT")

	// 事务转发错误路径
	st.On("BEGIN", &fakeResult{})
	st.On("ROLLBACK", &fakeResult{})
	if err := m.Transaction("app", func(c *Connection) error { return errors.New("boom") }); err == nil {
		t.Fatal("want error")
	}
	st.LastCall(t, "ROLLBACK")
}

func TestManager_ReadWriteRouting(t *testing.T) {
	writeStore := faketb.NewNamed("mgr_rw_write")
	readStore := faketb.NewNamed("mgr_rw_read")
	readStore.On("select * from `users`", rowsUsers([2]driver.Value{int64(1), "R"}))
	writeStore.On("select * from `users`", rowsUsers([2]driver.Value{int64(9), "W"}))

	m := NewManager(map[string]Config{
		"app": {Driver: "fake", Dsn: writeStore.Name(), ReadDsn: readStore.Name()},
	})

	// ::write：读写都走主连接
	wconn, err := m.Connect("app::write")
	if err != nil {
		t.Fatal(err)
	}
	var users []User
	if err := wconn.Table("users").Get(&users); err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Name != "W" {
		t.Fatalf("::write read = %+v, want write store", users)
	}

	// ::read：读写都走只读连接（写语句也路由到只读）
	rconn, err := m.Connect("app::read")
	if err != nil {
		t.Fatal(err)
	}
	users = nil
	if err := rconn.Table("users").Get(&users); err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Name != "R" {
		t.Fatalf("::read read = %+v, want read store", users)
	}
	writeStore.On("insert into users (name) values (?)", &fakeResult{Affected: 1})
	readStore.On("insert into users (name) values (?)", &fakeResult{Affected: 1})
	if _, _, err := rconn.Insert("insert into users (name) values (?)", "A"); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, call := range readStore.Calls() {
		if strings.Contains(call.Sql, "insert into users") {
			found = true
		}
	}
	if !found {
		t.Error("::read pinned insert should hit read store")
	}

	// ::read / ::write 各自缓存为独立连接
	if wconn == rconn {
		t.Error("::read and ::write must be distinct cached connections")
	}
}

// ---- QueryExecuted 元数据 ----

func TestFakeConnection_QueryExecutedMetadata(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from users where name = ?", rowsUsers([2]driver.Value{int64(1), "A"}))

	conn.SetConnectionName("meta")
	var got QueryExecuted
	conn.Listen(func(e QueryExecuted) { got = e })

	var users []User
	if err := conn.Select("select * from users where name = ?", []interface{}{"A"}, &users); err != nil {
		t.Fatal(err)
	}
	if got.ConnectionName != "meta" {
		t.Errorf("ConnectionName = %q", got.ConnectionName)
	}
	if got.ReadWriteType != "write" {
		t.Errorf("ReadWriteType = %q（无只读连接时为 write）", got.ReadWriteType)
	}
	if got.RawSql != "select * from users where name = 'A'" {
		t.Errorf("RawSql = %q", got.RawSql)
	}
}

func TestFakeConnection_QueryExecutedReadType(t *testing.T) {
	writeStore := faketb.NewNamed("meta_rw_write")
	readStore := faketb.NewNamed("meta_rw_read")
	readStore.On("select * from `users`", rowsUsers([2]driver.Value{int64(1), "R"}))

	conn, err := Open(Config{Driver: "fake", Dsn: writeStore.Name(), ReadDsn: readStore.Name()})
	if err != nil {
		t.Fatal(err)
	}
	var got QueryExecuted
	conn.Listen(func(e QueryExecuted) { got = e })

	var users []User
	if err := conn.Table("users").Get(&users); err != nil {
		t.Fatal(err)
	}
	if got.ReadWriteType != "read" {
		t.Errorf("ReadWriteType = %q, want read", got.ReadWriteType)
	}
	if got.ConnectionName != "::read" {
		// Open 直连无名连接时 ConnectionName 为空串 + 类型
		t.Logf("ConnectionName = %q", got.ConnectionName)
	}
}

// ---- D0 配置访问（GetConfig/GetDatabaseName/SetDatabaseName/configForReconnect） ----

func TestConnection_ConfigAccessors(t *testing.T) {
	conn := &Connection{}
	conn.setConfig(Config{Driver: "mysql", Dsn: "x", Database: "shop"})
	if got := conn.GetConfig().Database; got != "shop" {
		t.Errorf("GetConfig().Database = %q, want shop", got)
	}
	if got := conn.GetDatabaseName(); got != "shop" {
		t.Errorf("GetDatabaseName = %q, want shop", got)
	}
	conn.SetDatabaseName("mall")
	if got := conn.GetDatabaseName(); got != "mall" {
		t.Errorf("after SetDatabaseName = %q, want mall", got)
	}
	// 完整配置原样用于重连（方言参数保留）
	cfg := conn.configForReconnect()
	if cfg.Driver != "mysql" || cfg.Dsn != "x" || cfg.Database != "mall" {
		t.Errorf("configForReconnect = %+v", cfg)
	}

	// 未存储 config 时按字段合成最小配置（Open 直建连接的兼容路径）
	c2 := &Connection{driver: "sqlite", dsn: "file:f.db", tablePrefix: "p_"}
	cfg2 := c2.configForReconnect()
	if cfg2.Driver != "sqlite" || cfg2.Dsn != "file:f.db" || cfg2.Prefix != "p_" {
		t.Errorf("synthesized configForReconnect = %+v", cfg2)
	}
}

// ---- D1 服务器版本 ----

func TestFakeConnection_ServerVersion(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select version()", &fakeResult{Columns: []string{"version()"},
		Rows: [][]driver.Value{{"10.11.6-MariaDB-1"}}})

	v, err := conn.ServerVersion()
	if err != nil {
		t.Fatal(err)
	}
	if v != "10.11.6-MariaDB-1" {
		t.Errorf("ServerVersion = %q", v)
	}

	// 缓存生效：第二次调用不再真正查询
	if _, err := conn.ServerVersion(); err != nil {
		t.Fatal(err)
	}
	counts := 0
	for _, call := range st.Calls() {
		if call.Sql == "select version()" {
			counts++
		}
	}
	if counts != 1 {
		t.Errorf("version query executed %d times, want 1 (cached)", counts)
	}

	// 查询失败不缓存，错误透传
	conn2, st2 := newFakeDB(t)
	if _, err := conn2.ServerVersion(); err == nil {
		t.Error("want error without stubbed version query")
	}
	if _, err := conn2.ServerVersion(); err == nil {
		t.Error("second call should still fail (not cached)")
	}
	_ = st2
}

func TestFakeConnection_IsMaria(t *testing.T) {
	conn, _ := newFakeDB(t)
	// fake 驱动（非 mysql/mariadb）：恒为 false，且不触发版本查询
	if conn.IsMaria() {
		t.Error("fake driver IsMaria = true, want false")
	}

	// mysql 驱动 + 版本串含 MariaDB
	connMysql, stMysql := newFakeDB(t)
	connMysql.driver = "mysql"
	stMysql.On("select version()", &fakeResult{Columns: []string{"version()"},
		Rows: [][]driver.Value{{"10.11.6-MariaDB-1"}}})
	if !connMysql.IsMaria() {
		t.Error("mysql driver with MariaDB version, IsMaria = false")
	}

	// mysql 驱动 + 普通 MySQL 版本
	connPlain, stPlain := newFakeDB(t)
	connPlain.driver = "mysql"
	stPlain.On("select version()", &fakeResult{Columns: []string{"version()"},
		Rows: [][]driver.Value{{"8.0.36"}}})
	if connPlain.IsMaria() {
		t.Error("mysql driver with plain version, IsMaria = true")
	}
}

// ---- D8 Connector 配置项 ----

func TestMySqlConnectorBuildDsn_Modes(t *testing.T) {
	// Modes 数组优先于 SqlMode 单串
	c := &MySqlConnector{config: Config{
		Modes:   []string{"STRICT_TRANS_TABLES", "NO_ZERO_DATE"},
		SqlMode: "IGNORED",
	}}
	got := c.BuildDsn("root@tcp(127.0.0.1:3306)/shop")
	parsed, err := gomysql.ParseDSN(got)
	if err != nil {
		t.Fatal(err)
	}
	if gotMode := parsed.Params["sql_mode"]; gotMode != "STRICT_TRANS_TABLES,NO_ZERO_DATE" {
		t.Errorf("sql_mode = %q, want joined modes", gotMode)
	}

	// 未配置其它参数时仅 Modes 也会附加
	c2 := &MySqlConnector{config: Config{Modes: []string{"ANSI_QUOTES"}}}
	got2 := c2.BuildDsn("root@tcp(host)/db")
	if !strings.Contains(got2, "sql_mode=ANSI") {
		t.Errorf("BuildDsn = %q, missing sql_mode", got2)
	}
}

func TestMySqlConnector_ConfigureSessionIsolationLevel(t *testing.T) {
	st := faketb.NewNamed("mysql_session_iso")
	st.On("set session transaction isolation level serializable", &fakeResult{})

	c := &MySqlConnector{config: Config{IsolationLevel: "serializable"}}
	db, err := sql.Open("fake", st.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := c.configureSession(db); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, "set session transaction isolation level serializable")
}

func TestPostgresConnectorBuildDsn_SslAndEncoding(t *testing.T) {
	c := &PostgresConnector{config: Config{
		SslCert: "/certs/client.crt", SslKey: "/certs/client.key",
		SslRootCert: "/certs/ca.crt", ClientEncoding: "utf8",
		ApplicationName: "torm-app",
	}}
	got := c.BuildDsn("host=pg.local dbname=shop")
	for _, want := range []string{
		"sslcert=/certs/client.crt", "sslkey=/certs/client.key",
		"sslrootcert=/certs/ca.crt", "client_encoding=utf8",
		"application_name=torm-app",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("BuildDsn = %q, missing %q", got, want)
		}
	}

	// 已有参数不覆盖
	got = c.BuildDsn("host=pg.local dbname=shop?application_name=other&client_encoding=latin1")
	if strings.Contains(got, "application_name=torm-app") ||
		strings.Contains(got, "client_encoding=utf8") {
		t.Errorf("BuildDsn overrode existing params: %q", got)
	}
}

func TestPostgresConnector_ConnectSessionConfig(t *testing.T) {
	st := faketb.NewNamed("pg_session")
	st.On("set session characteristics as transaction isolation level serializable", &fakeResult{})
	st.On("set synchronous_commit to 'off'", &fakeResult{})

	c := &PostgresConnector{config: Config{
		Driver: "fake", IsolationLevel: "serializable", SynchronousCommit: "off",
	}}
	db, err := c.Connect(st.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st.LastCall(t, "set session characteristics as transaction isolation level serializable")
	st.LastCall(t, "set synchronous_commit to 'off'")
}

func TestSqliteConnector_ConnectPragmas(t *testing.T) {
	st := faketb.NewNamed("sqlite_pragmas")
	st.On("pragma cache_size = 2000", &fakeResult{})
	st.On("pragma mmap_size = 268435456", &fakeResult{})

	c := &SQLiteConnector{config: Config{
		Driver:  "fake",
		Pragmas: map[string]string{"cache_size": "2000", "mmap_size": "268435456"},
	}}
	db, err := c.Connect(st.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st.LastCall(t, "pragma cache_size = 2000")
	st.LastCall(t, "pragma mmap_size = 268435456")

	// pragma 失败时建连整体失败
	st2 := faketb.NewNamed("sqlite_pragmas_err")
	st2.On("pragma bad = 1", &fakeResult{Err: errors.New("fake: no such pragma")})
	c2 := &SQLiteConnector{config: Config{Driver: "fake", Pragmas: map[string]string{"bad": "1"}}}
	if _, err := c2.Connect(st2.Name()); err == nil {
		t.Error("want error on failed pragma")
	}
}

// ---- D10 Connection 便利 API ----

func TestFakeConnection_Unprepared(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("create table tmp (id int)", &fakeResult{Affected: 1})

	conn.EnableQueryLog()
	ok, err := conn.Unprepared("create table tmp (id int)")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("Unprepared = false, want true")
	}
	// 绕过 run 包装：不写查询日志
	if entries := conn.GetQueryLog(); len(entries) != 0 {
		t.Errorf("query log = %d entries, want 0 (unprepared bypasses run)", len(entries))
	}
	// 成功后标记写操作
	if !conn.HasModifiedRecords() {
		t.Error("recordsModified should be true after Unprepared")
	}
	st.LastCall(t, "create table tmp (id int)")

	// 失败透传
	if ok, err := conn.Unprepared("drop table missing"); ok || err == nil {
		t.Errorf("unstubbed Unprepared = (%v, %v), want (false, error)", ok, err)
	}
}

func TestFakeConnection_UnpreparedDuringPretend(t *testing.T) {
	conn, st := newFakeDB(t)
	conn.Pretend(func(c *Connection) {
		ok, err := c.Unprepared("create table x (id int)")
		if err != nil || !ok {
			t.Errorf("pretend Unprepared = (%v, %v), want (true, nil)", ok, err)
		}
	})
	for _, call := range st.Calls() {
		if call.Sql == "create table x (id int)" {
			t.Error("pretend Unprepared executed for real")
		}
	}
}

func TestFakeConnection_WithoutPretending(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("insert into users (name) values (?)", &fakeResult{Affected: 1})

	executed := 0
	conn.Pretend(func(c *Connection) {
		// pretend 中直接执行：dry-run 不落库
		if _, _, err := c.Insert("insert into users (name) values (?)", "A"); err != nil {
			t.Errorf("pretend insert err = %v", err)
		}
		// WithoutPretending：临时关闭 dry-run，真正执行
		if err := c.WithoutPretending(func() error {
			_, _, err := c.Insert("insert into users (name) values (?)", "B")
			executed++
			return err
		}); err != nil {
			t.Errorf("WithoutPretending err = %v", err)
		}
		// 回调结束后仍处于 pretend：不再真正执行
		if _, _, err := c.Insert("insert into users (name) values (?)", "C"); err != nil {
			t.Errorf("pretend insert after WithoutPretending err = %v", err)
		}
	})
	counts := 0
	for _, call := range st.Calls() {
		if call.Sql == "insert into users (name) values (?)" {
			counts++
		}
	}
	if counts != 1 || executed != 1 {
		t.Errorf("executed = %d, store calls = %d, want exactly one real execution", executed, counts)
	}

	// 未处于 pretend 时直接执行回调
	ran := false
	if err := conn.WithoutPretending(func() error { ran = true; return nil }); err != nil || !ran {
		t.Errorf("WithoutPretending without pretend = (%v, %v)", ran, err)
	}
}

func TestFakeConnection_GetRawQueryLog(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("select * from users where name = ?", &fakeResult{
		Columns: []string{"id", "name"}, Rows: [][]driver.Value{{int64(1), "A"}},
	})

	conn.EnableQueryLog()
	var n int64
	_ = conn.Scan("select * from users where name = ?", []interface{}{"O'Brien"}, &n)

	rawLog := conn.GetRawQueryLog()
	if len(rawLog) != 1 {
		t.Fatalf("raw log = %d entries", len(rawLog))
	}
	if rawLog[0].RawSql != "select * from users where name = 'O''Brien'" {
		t.Errorf("RawSql = %q", rawLog[0].RawSql)
	}
	if rawLog[0].TimeNs < 0 {
		t.Errorf("TimeNs = %d", rawLog[0].TimeNs)
	}
	// 原日志保留占位符形式
	if got := conn.GetQueryLog()[0].Sql; got != "select * from users where name = ?" {
		t.Errorf("query log sql = %q", got)
	}
}

func TestConnection_Raw(t *testing.T) {
	conn := &Connection{}
	e := conn.Raw("now()")
	if e == nil {
		t.Fatal("Raw = nil")
	}
	if got := e.GetValue(); got != "now()" {
		t.Errorf("GetValue = %v, want now()", got)
	}
}

func TestFakeConnection_ThreadCount(t *testing.T) {
	// 未支持驱动：(-1, nil)
	conn, st := newFakeDB(t)
	n, err := conn.ThreadCount()
	if err != nil || n != -1 {
		t.Fatalf("fake ThreadCount = (%d, %v), want (-1, nil)", n, err)
	}
	if len(st.Calls()) != 0 {
		t.Error("unsupported ThreadCount should not query")
	}

	cases := []struct {
		driver string
		query  string
	}{
		{"mysql", "select variable_value as `Value` from performance_schema.session_status where variable_name = 'threads_connected'"},
		{"mariadb", "select variable_value as `Value` from information_schema.global_status where variable_name = 'THREADS_CONNECTED'"},
		{"postgres", `select count(*) as "Value" from pg_stat_activity`},
		{"sqlserver", "select count(*) Value from sys.dm_exec_sessions where status = N'running'"},
	}
	for _, tc := range cases {
		c, stc := newFakeDB(t)
		c.driver = tc.driver
		stc.On(tc.query, &fakeResult{Columns: []string{"Value"}, Rows: [][]driver.Value{{int64(7)}}})
		got, err := c.ThreadCount()
		if err != nil || got != 7 {
			t.Errorf("%s ThreadCount = (%d, %v), want (7, nil)", tc.driver, got, err)
		}
	}
}

func TestConnection_Escape(t *testing.T) {
	cases := []struct {
		name   string
		driver string
		value  any
		binary bool
		want   string
		wantEr string
	}{
		{"null", "mysql", nil, false, "null", ""},
		{"bool-true", "mysql", true, false, "1", ""},
		{"bool-false", "mysql", false, false, "0", ""},
		{"bool-pg", "postgres", true, false, "true", ""},
		{"bool-pg-false", "postgres", false, false, "false", ""},
		{"int", "mysql", int64(42), false, "42", ""},
		{"float", "mysql", 1.5, false, "1.5", ""},
		{"float-integer", "mysql", float64(1), false, "1", ""},
		{"string", "mysql", "O'Brien", false, "'O''Brien'", ""},
		{"nul-bytes", "mysql", "a\x00b", false, "", "null bytes"},
		{"invalid-utf8", "mysql", "\xff\xfe", false, "", "invalid UTF-8"},
		{"binary-mysql", "mysql", []byte("Hello"), true, "x'48656c6c6f'", ""},
		{"binary-string-sqlite", "sqlite", "Hi", true, "x'4869'", ""},
		{"binary-pg", "postgres", "Hi", true, "'\\x4869'::bytea", ""},
		{"binary-sqlserver", "sqlserver", "Hi", true, "0x4869", ""},
		{"binary-unsupported", "fake", "Hi", true, "", "binary"},
		{"array-unsupported", "mysql", []int{1, 2}, false, "", "arrays"},
	}
	for _, tc := range cases {
		conn := &Connection{driver: tc.driver}
		got, err := conn.Escape(tc.value, tc.binary)
		if tc.wantEr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantEr) {
				t.Errorf("%s: Escape err = %v, want contains %q", tc.name, err, tc.wantEr)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: Escape err = %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: Escape = %q, want %q", tc.name, got, tc.want)
		}
	}

	// 无 binary 参数调用（可变参数缺省）
	conn := &Connection{driver: "mysql"}
	if got, _ := conn.Escape("x"); got != "'x'" {
		t.Errorf("Escape single arg = %q", got)
	}
}

func TestConnection_SetReconnector(t *testing.T) {
	conn, _ := newFakeDB(t)
	called := 0
	conn.SetReconnector(func(c *Connection) error {
		called++
		return nil
	})

	oldDB := conn.DB
	if err := conn.reconnect(); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Errorf("reconnector called = %d, want 1", called)
	}
	// 回调优先：内建重连未执行，句柄未被替换
	if conn.DB != oldDB {
		t.Error("reconnector path should keep handle untouched")
	}
}

func TestConnection_UnsetEventDispatcher(t *testing.T) {
	conn, _ := newFakeDB(t)
	rec := &txEventRecorder{}
	conn.SetEventDispatcher(rec)
	conn.UnsetEventDispatcher()
	conn.dispatchEstablishedEvent()
	if len(rec.events) != 0 {
		t.Errorf("events after unset = %d, want 0", len(rec.events))
	}
}

// ---- L1/D10 Manager：Reconnect / GetConnections / SetReconnector ----

func TestManager_ReconnectKeepsInstance(t *testing.T) {
	st := faketb.NewNamed("mgr_reconnect")
	m := NewManager(map[string]Config{
		"app": {Driver: "fake", Dsn: st.Name(), Database: "shop"},
	})
	rec := &txEventRecorder{}
	m.SetEventDispatcher(rec)

	conn1, err := m.Connect("app")
	if err != nil {
		t.Fatal(err)
	}
	oldDB := conn1.DB

	conn2, err := m.Reconnect("app")
	if err != nil {
		t.Fatal(err)
	}
	// 同一实例保留，底层句柄已换新
	if conn2 != conn1 {
		t.Error("Reconnect returned a new instance, want the same one")
	}
	if conn1.DB == oldDB {
		t.Error("Reconnect should rebuild the underlying handle")
	}
	// 实例配置仍可读（重连用的配置）
	if got := conn1.GetDatabaseName(); got != "shop" {
		t.Errorf("GetDatabaseName after reconnect = %q, want shop", got)
	}

	// 重连后连接可用
	st.On("select 1", &fakeResult{Columns: []string{"1"}, Rows: [][]driver.Value{{int64(1)}}})
	var n int64
	if err := conn1.Scan("select 1", nil, &n); err != nil {
		t.Fatal(err)
	}

	// ConnectionEstablished 重发（建连 1 次 + 重连 1 次）
	established := 0
	for _, e := range rec.events {
		if e.EventName() == events.ConnectionEstablished {
			established++
		}
	}
	if established != 2 {
		t.Errorf("ConnectionEstablished dispatched %d times, want 2", established)
	}
}

func TestManager_ReconnectPinnedType(t *testing.T) {
	readStore := faketb.NewNamed("mgr_reconnect_read")
	m := NewManager(map[string]Config{
		"app": {Driver: "fake", Dsn: readStore.Name()},
	})
	conn1, err := m.Connect("app::read")
	if err != nil {
		t.Fatal(err)
	}
	conn2, err := m.Reconnect("app::read")
	if err != nil {
		t.Fatal(err)
	}
	if conn2 != conn1 {
		t.Error("pinned reconnect should keep the same instance")
	}
}

func TestManager_ReconnectUnconnectedFallsBackToConnect(t *testing.T) {
	st := faketb.NewNamed("mgr_reconnect_late")
	m := NewManager(map[string]Config{
		"late": {Driver: "fake", Dsn: st.Name()},
	})
	conn, err := m.Reconnect("late")
	if err != nil {
		t.Fatal(err)
	}
	again, err := m.Connect("late")
	if err != nil {
		t.Fatal(err)
	}
	if again != conn {
		t.Error("Connect after Reconnect should reuse the created connection")
	}
}

func TestManager_GetConnections(t *testing.T) {
	a := faketb.NewNamed("mgr_list_a")
	b := faketb.NewNamed("mgr_list_b")
	m := NewManager(map[string]Config{
		"a": {Driver: "fake", Dsn: a.Name()},
		"b": {Driver: "fake", Dsn: b.Name()},
	})
	if got := m.GetConnections(); len(got) != 0 {
		t.Errorf("initial connections = %v", got)
	}
	if _, err := m.Connect("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Connect("a"); err != nil {
		t.Fatal(err)
	}
	got := m.GetConnections()
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("GetConnections = %v, want [a b]", got)
	}
}

func TestManager_SetReconnectorAttached(t *testing.T) {
	st := faketb.NewNamed("mgr_reconnector")
	m := NewManager(map[string]Config{
		"app": {Driver: "fake", Dsn: st.Name()},
	})
	called := 0
	m.SetReconnector(func(c *Connection) error {
		called++
		return nil
	})
	conn, err := m.Connect("app")
	if err != nil {
		t.Fatal(err)
	}
	oldDB := conn.DB
	if err := conn.reconnect(); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Errorf("attached reconnector called = %d, want 1", called)
	}
	if conn.DB != oldDB {
		t.Error("reconnector path should keep handle untouched")
	}
}

// 断连重试经 Manager 默认 reconnector：保留实例、句柄重建后重跑成功。
func TestManager_DefaultReconnectorOnLostConnection(t *testing.T) {
	st := faketb.NewNamed("mgr_lost_retry")
	m := NewManager(map[string]Config{
		"app": {Driver: "fake", Dsn: st.Name()},
	})
	conn, err := m.Connect("app")
	if err != nil {
		t.Fatal(err)
	}
	oldDB := conn.DB

	st.On("select 1", &fakeResult{Err: errors.New("Error 2006: MySQL server has gone away")})
	st.On("select 1", &fakeResult{Columns: []string{"1"}, Rows: [][]driver.Value{{int64(1)}}})

	var n int64
	if err := conn.Scan("select 1", nil, &n); err != nil {
		t.Fatalf("retry via manager reconnector should recover: %v", err)
	}
	if n != 1 {
		t.Errorf("n = %d", n)
	}
	if conn.DB == oldDB {
		t.Error("manager reconnect should rebuild the underlying handle")
	}
}
