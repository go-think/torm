package torm

import (
	"database/sql/driver"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/go-think/torm/grammar"
)

// 原有模型字段映射测试
func TestNewSchema(t *testing.T) {
	schema, err := NewSchema(&User{})
	if err != nil {
		t.Error(err)
	}
	t.Log(schema.Fields)
}

// 期望 SQL 取自既有 golden 基线。

func mysqlSchemaGrammar(prefix ...string) grammar.SchemaGrammar {
	g := grammar.NewMySqlGrammar()
	if len(prefix) > 0 {
		g.SetTablePrefix(prefix[0])
	}
	return g
}

func pgSchemaGrammar() grammar.SchemaGrammar {
	return grammar.NewPostgresGrammar()
}

func TestSchemaMySQLBasicCreate(t *testing.T) {
	b := grammar.NewBlueprint("users")
	b.Create = true
	b.Charset = "utf8"
	b.Collation = "utf8_unicode_ci"
	b.Increments("id")
	b.String("email")

	got := b.ToSql(mysqlSchemaGrammar())
	want := []string{"create table `users` (`id` int unsigned not null auto_increment primary key, `email` varchar(255) not null) default character set utf8 collate 'utf8_unicode_ci'"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSchemaMySQLAlter(t *testing.T) {
	b := grammar.NewBlueprint("users")
	b.Increments("id")
	b.String("email")

	got := b.ToSql(mysqlSchemaGrammar())
	want := []string{
		"alter table `users` add `id` int unsigned not null auto_increment primary key",
		"alter table `users` add `email` varchar(255) not null",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSchemaMySQLUuidPrimary(t *testing.T) {
	b := grammar.NewBlueprint("users")
	b.Create = true
	b.UUID("id").Primary()

	got := b.ToSql(mysqlSchemaGrammar())
	want := []string{"create table `users` (`id` char(36) not null, primary key (`id`))"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSchemaMySQLAutoIncrementStartingValue(t *testing.T) {
	b := grammar.NewBlueprint("users")
	b.ID().StartingValue(100)
	b.String("name")

	got := b.ToSql(mysqlSchemaGrammar())
	want := []string{
		"alter table `users` add `id` bigint unsigned not null auto_increment primary key",
		"alter table `users` add `name` varchar(255) not null",
		"alter table `users` auto_increment = 100",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSchemaMySQLEngine(t *testing.T) {
	b := grammar.NewBlueprint("users")
	b.Create = true
	b.Charset = "utf8"
	b.Collation = "utf8_unicode_ci"
	b.Engine = "InnoDB"
	b.Increments("id")
	b.String("email")

	got := b.ToSql(mysqlSchemaGrammar())
	want := []string{"create table `users` (`id` int unsigned not null auto_increment primary key, `email` varchar(255) not null) default character set utf8 collate 'utf8_unicode_ci' engine = InnoDB"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSchemaMySQLForeign(t *testing.T) {
	b := grammar.NewBlueprint("orders")
	b.Create = true
	b.ID()
	b.ForeignID("user_id")
	b.Foreign("user_id").References("id").On("users").CascadeOnDelete()

	got := b.ToSql(mysqlSchemaGrammar())
	if len(got) != 2 {
		t.Fatalf("statements = %v", got)
	}
	// MySQL 建表只含列，外键为独立 alter 语句
	if !strings.Contains(got[1], "alter table `orders` add constraint `orders_user_id_foreign` foreign key (`user_id`) references `users` (`id`) on delete cascade") {
		t.Errorf("sql = %s", got[1])
	}
}

func TestSchemaMySQLDrop(t *testing.T) {
	b := grammar.NewBlueprint("users")
	b.Drop()
	if got := b.ToSql(mysqlSchemaGrammar()); !reflect.DeepEqual(got, []string{"drop table `users`"}) {
		t.Errorf("sql = %v", got)
	}

	b2 := grammar.NewBlueprint("users")
	b2.DropIfExists()
	if got := b2.ToSql(mysqlSchemaGrammar()); !reflect.DeepEqual(got, []string{"drop table if exists `users`"}) {
		t.Errorf("sql = %v", got)
	}
}

func TestSchemaPGCreate(t *testing.T) {
	b := grammar.NewBlueprint("users")
	b.Create = true
	b.Increments("id")
	b.String("email")

	got := b.ToSql(pgSchemaGrammar())
	want := []string{`create table "users" ("id" serial not null primary key, "email" varchar(255) not null)`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSchemaPGTimestampsAndJSON(t *testing.T) {
	b := grammar.NewBlueprint("posts")
	b.Create = true
	b.ID()
	b.JSON("meta").Nullable()
	b.Timestamps()
	b.SoftDeletes()

	got := b.ToSql(pgSchemaGrammar())
	if len(got) != 1 {
		t.Fatalf("statements = %v", got)
	}
	for _, frag := range []string{`"id" bigserial not null primary key`, `"meta" json null`, `"created_at" timestamp(0) without time zone null`, `"deleted_at" timestamp(0) without time zone null`} {
		if !strings.Contains(got[0], frag) {
			t.Errorf("missing %q in %s", frag, got[0])
		}
	}
}

func TestSchemaPGIndex(t *testing.T) {
	b := grammar.NewBlueprint("users")
	b.String("email")
	b.Unique([]string{"email"})

	got := b.ToSql(pgSchemaGrammar())
	want := []string{
		`alter table "users" add column "email" varchar(255) not null`,
		`create unique index "users_email_unique" on "users" ("email")`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sql =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSchemaPGDropIfExists(t *testing.T) {
	b := grammar.NewBlueprint("users")
	b.DropIfExists()
	if got := b.ToSql(pgSchemaGrammar()); !reflect.DeepEqual(got, []string{`drop table if exists "users"`}) {
		t.Errorf("sql = %v", got)
	}
}

// ---- SchemaBuilder 执行测试（fake driver）----

func TestSchemaBuilderExecutes(t *testing.T) {
	conn, st := newFakeDB(t)
	createSQL := "create table `users` (`id` bigint unsigned not null auto_increment primary key, `email` varchar(255) not null)"
	st.On(createSQL, &fakeResult{})
	st.On("drop table if exists `old_users`", &fakeResult{})

	if err := conn.Schema().CreateTable("users", func(t *grammar.Blueprint) {
		t.ID()
		t.String("email")
	}); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, createSQL)

	if err := conn.Schema().DropIfExists("old_users"); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, "drop table if exists `old_users`")
}

func TestSchemaBuilderHasTable(t *testing.T) {
	conn, st := newFakeDB(t)
	sql := "select * from information_schema.tables where table_schema = ? and table_name = ? and table_type = 'BASE TABLE'"
	st.On(sql, &fakeResult{Columns: []string{"table_name"}, Rows: [][]driver.Value{{"users"}}})

	ok, err := conn.Schema().HasTable("users")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expect table exists")
	}
	call := st.LastCall(t, sql)
	if len(call.Args) != 2 || call.Args[1] != "users" {
		t.Errorf("args = %#v", call.Args)
	}
}

// ---- SchemaBuilder：方言分发（#45） ----

func TestSchemaBuilderGetSchemaGrammarDialects(t *testing.T) {
	conn, _ := newFakeDB(t)

	cases := []struct {
		driver string
		want   grammar.SchemaGrammar
	}{
		{"mysql", grammar.NewMySqlGrammar()},
		{"mariadb", grammar.NewMariaDbGrammar()},
		{"postgres", grammar.NewPostgresGrammar()},
		{"postgresql", grammar.NewPostgresGrammar()},
		{"sqlite", grammar.NewSqliteQueryGrammar()},
		{"sqlserver", grammar.NewSqlServerGrammar()},
	}
	for _, tc := range cases {
		conn.driver = tc.driver
		got := conn.GetSchemaGrammar()
		if fmt.Sprintf("%T", got) != fmt.Sprintf("%T", tc.want) {
			t.Errorf("driver %s: got %T, want %T", tc.driver, got, tc.want)
		}
	}
}

// ---- SchemaBuilder：Table / Create 命名语义（#46） ----

func TestSchemaBuilderTableAndCreateNaming(t *testing.T) {
	conn, st := newFakeDB(t)
	alterSQL := "alter table `users` add `name` varchar(255) not null"
	createSQL := "create table `users` (`id` bigint unsigned not null auto_increment primary key, `email` varchar(255) not null)"
	st.On(alterSQL, &fakeResult{})
	st.On(createSQL, &fakeResult{})

	// Table = alter 语义
	if err := conn.Schema().Table("users", func(t *grammar.Blueprint) {
		t.String("name")
	}); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, alterSQL)

	// Create = 新建语义
	if err := conn.Schema().Create("users", func(t *grammar.Blueprint) {
		t.ID()
		t.String("email")
	}); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, createSQL)

	// 兼容别名
	if err := conn.Schema().AlterTable("users", func(t *grammar.Blueprint) {
		t.String("name")
	}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Schema().CreateTable("users", func(t *grammar.Blueprint) {
		t.ID()
		t.String("email")
	}); err != nil {
		t.Fatal(err)
	}
	if n := len(st.Calls()); n != 4 {
		t.Errorf("expect 4 statements, got %d", n)
	}
}

// ---- SchemaBuilder：schema.table 引用解析（#44） ----

func TestSchemaBuilderParseSchemaAndTable(t *testing.T) {
	conn, _ := newFakeDB(t)
	s := conn.Schema()

	schema, table, err := s.parseSchemaAndTable("users")
	if err != nil || schema != "" || table != "users" {
		t.Errorf("plain: schema=%q table=%q err=%v", schema, table, err)
	}
	schema, table, err = s.parseSchemaAndTable("shop.users")
	if err != nil || schema != "shop" || table != "users" {
		t.Errorf("qualified: schema=%q table=%q err=%v", schema, table, err)
	}
	if _, _, err = s.parseSchemaAndTable("a.b.c"); err == nil {
		t.Error("expect error for three-part reference")
	}
	if _, err = s.HasTable("a.b.c"); err == nil {
		t.Error("expect error from HasTable for three-part reference")
	}
}

func TestSchemaBuilderHasTableSchemaQualified(t *testing.T) {
	conn, st := newFakeDB(t)
	g := grammar.NewMySqlGrammar()
	sql, _ := g.CompileTableExists("shop", "users")
	st.On(sql, &fakeResult{Columns: []string{"exists"}, Rows: [][]driver.Value{{int64(1)}}})

	ok, err := conn.Schema().HasTable("shop.users")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expect qualified table exists")
	}
	call := st.LastCall(t, sql)
	if len(call.Args) != 2 || call.Args[0] != "shop" || call.Args[1] != "users" {
		t.Errorf("args = %#v", call.Args)
	}
}

func TestSchemaBuilderDatabaseNameKeywordDsn(t *testing.T) {
	conn, _ := newFakeDB(t)
	// 先用常规 fake DSN 确认不受关键字解析影响
	if got := conn.Schema().databaseName(); got != conn.Schema().currentSchemaName() {
		t.Errorf("fake dsn: databaseName=%q currentSchemaName=%q", got, conn.Schema().currentSchemaName())
	}

	pg, err := Open(Config{Driver: "fake", Dsn: "host=localhost port=5432 dbname=appdb user=pg"})
	if err != nil {
		t.Fatal(err)
	}
	if got := pg.Schema().databaseName(); got != "appdb" {
		t.Errorf("keyword dsn: databaseName = %q, want appdb", got)
	}
}

func TestSchemaBuilderSearchPathDefaultSchema(t *testing.T) {
	pg, err := Open(Config{Driver: "fake", Dsn: "postgres://u:p@h:5432/appdb?search_path=shop,public"})
	if err != nil {
		t.Fatal(err)
	}
	pg.driver = "postgres"
	if got := pg.Schema().currentSchemaName(); got != "shop" {
		t.Errorf("currentSchemaName = %q, want shop (search_path first entry)", got)
	}
	if got := pg.Schema().databaseName(); got != "appdb" {
		t.Errorf("databaseName = %q, want appdb", got)
	}

	// 无 search_path 参数时回退 public
	pg2, err := Open(Config{Driver: "fake", Dsn: "postgres://u:p@h:5432/appdb"})
	if err != nil {
		t.Fatal(err)
	}
	pg2.driver = "postgres"
	if got := pg2.Schema().currentSchemaName(); got != "public" {
		t.Errorf("currentSchemaName = %q, want public", got)
	}
}

// ---- SchemaBuilder：列内省 ----

func stubMySQLColumnListing(t *testing.T, s *SchemaBuilder, st *fakeStore, table string, cols []string) string {
	t.Helper()
	g := grammar.NewMySqlGrammar()
	sql, _ := g.CompileColumnListing(s.databaseName(), table)
	rows := make([][]driver.Value, 0, len(cols))
	for _, c := range cols {
		rows = append(rows, []driver.Value{c})
	}
	st.On(sql, &fakeResult{Columns: []string{"column_name"}, Rows: rows})
	return sql
}

func TestSchemaBuilderHasColumn(t *testing.T) {
	conn, st := newFakeDB(t)
	s := conn.Schema()
	sql := stubMySQLColumnListing(t, s, st, "users", []string{"id", "email"})

	ok, err := s.HasColumn("users", "EMAIL")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expect has email column")
	}
	ok, err = s.HasColumn("users", "missing")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expect missing column absent")
	}
	st.LastCall(t, sql)
}

func TestSchemaBuilderHasColumns(t *testing.T) {
	conn, st := newFakeDB(t)
	s := conn.Schema()
	stubMySQLColumnListing(t, s, st, "users", []string{"id", "email"})

	ok, err := s.HasColumns("users", []string{"id", "email"})
	if err != nil || !ok {
		t.Errorf("HasColumns(all present) = %v, %v", ok, err)
	}
	ok, err = s.HasColumns("users", []string{"id", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("HasColumns(one missing) should be false")
	}
}

func TestSchemaBuilderGetColumnsMySQL(t *testing.T) {
	conn, st := newFakeDB(t)
	s := conn.Schema()
	sql, _ := compileColumnsSQL(dialectMysql, s.databaseName(), "users")
	st.On(sql, &fakeResult{
		Columns: []string{"name", "type_name", "type", "nullable", "default", "comment", "extra"},
		Rows: [][]driver.Value{
			{"id", "bigint", "bigint unsigned", "NO", nil, "", "auto_increment"},
			{"email", "varchar", "varchar(255)", "YES", nil, "邮箱", ""},
			{"status", "int", "int", "NO", "0", "", ""},
		},
	})

	cols, err := s.GetColumns("users")
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 3 {
		t.Fatalf("cols = %+v", cols)
	}
	if cols[0].Name != "id" || !cols[0].AutoIncrement || cols[0].Nullable {
		t.Errorf("id col = %+v", cols[0])
	}
	if cols[1].Type != "varchar(255)" || cols[1].TypeName != "varchar" || !cols[1].Nullable || cols[1].Comment != "邮箱" {
		t.Errorf("email col = %+v", cols[1])
	}
	if cols[2].Default != "0" || cols[2].Nullable {
		t.Errorf("status col = %+v", cols[2])
	}
	st.LastCall(t, sql)
}

func TestSchemaBuilderGetColumnsPostgres(t *testing.T) {
	conn, st := newFakeDB(t)
	conn.driver = "postgres"
	s := conn.Schema()
	sql, _ := compileColumnsSQL(dialectPostgres, "public", "users")
	st.On(sql, &fakeResult{
		Columns: []string{"name", "type_name", "type", "nullable", "default", "generated", "comment"},
		Rows: [][]driver.Value{
			{"id", "int4", "integer", true, "nextval('users_id_seq'::regclass)", "", ""},
			{"email", "varchar", "character varying(255)", false, nil, "", "邮箱"},
		},
	})

	cols, err := s.GetColumns("users")
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 2 {
		t.Fatalf("cols = %+v", cols)
	}
	if !cols[0].AutoIncrement || !cols[0].Nullable {
		t.Errorf("id col = %+v", cols[0])
	}
	if cols[1].Type != "character varying(255)" || cols[1].Nullable || cols[1].Comment != "邮箱" {
		t.Errorf("email col = %+v", cols[1])
	}
	st.LastCall(t, sql)
}

func TestSchemaBuilderGetColumnType(t *testing.T) {
	conn, st := newFakeDB(t)
	s := conn.Schema()
	sql, _ := compileColumnsSQL(dialectMysql, s.databaseName(), "users")
	st.On(sql, &fakeResult{
		Columns: []string{"name", "type_name", "type", "nullable", "default", "comment", "extra"},
		Rows: [][]driver.Value{
			{"email", "varchar", "varchar(255)", "YES", nil, "", ""},
		},
	})

	got, err := s.GetColumnType("users", "email")
	if err != nil {
		t.Fatal(err)
	}
	if got != "varchar(255)" {
		t.Errorf("GetColumnType = %q, want varchar(255)", got)
	}
	if _, err := s.GetColumnType("users", "missing"); err == nil {
		t.Error("expect error for missing column")
	}
}

// ---- SchemaBuilder：索引 / 外键内省 ----

func mysqlSchemaIndexesRows() *fakeResult {
	return &fakeResult{
		Columns: []string{"name", "columns", "type", "unique"},
		Rows: [][]driver.Value{
			{"users_email_unique", "email", "BTREE", int64(1)},
			{"primary", "id", "BTREE", int64(1)},
		},
	}
}

func TestSchemaBuilderGetIndexesAndHasIndex(t *testing.T) {
	conn, st := newFakeDB(t)
	s := conn.Schema()
	sql, _ := compileIndexesSQL(dialectMysql, s.databaseName(), "users")
	st.On(sql, mysqlSchemaIndexesRows())

	indexes, err := s.GetIndexes("users")
	if err != nil {
		t.Fatal(err)
	}
	if len(indexes) != 2 {
		t.Fatalf("indexes = %+v", indexes)
	}
	if !indexes[0].Unique || indexes[0].Type != "BTREE" || !reflect.DeepEqual(indexes[0].Columns, []string{"email"}) {
		t.Errorf("index[0] = %+v", indexes[0])
	}
	if !indexes[1].Primary {
		t.Errorf("mysql primary index should be detected by name, got %+v", indexes[1])
	}

	listing, err := s.GetIndexListing("users")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(listing, []string{"users_email_unique", "primary"}) {
		t.Errorf("listing = %v", listing)
	}

	// HasIndex：名称 / 列集 / 类型过滤
	for _, tc := range []struct {
		index any
		typ   string
		want  bool
	}{
		{"users_email_unique", "", true},
		{[]string{"id"}, "", true},
		{[]string{"email", "id"}, "", false},
		{"users_email_unique", "primary", false},
		{"primary", "PRIMARY", true},
		{"missing", "", false},
	} {
		var got bool
		var err error
		if tc.typ == "" {
			got, err = s.HasIndex("users", tc.index)
		} else {
			got, err = s.HasIndex("users", tc.index, tc.typ)
		}
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("HasIndex(%v, %q) = %v, want %v", tc.index, tc.typ, got, tc.want)
		}
	}
}

func TestSchemaBuilderGetForeignKeysAndHasForeignKey(t *testing.T) {
	conn, st := newFakeDB(t)
	s := conn.Schema()
	sql, _ := compileForeignKeysSQL(dialectMysql, s.databaseName(), "orders")
	st.On(sql, &fakeResult{
		Columns: []string{"name", "columns", "foreign_schema", "foreign_table", "foreign_columns", "on_update", "on_delete"},
		Rows: [][]driver.Value{
			{"orders_user_id_foreign", "user_id", "app", "users", "id", "NO ACTION", "CASCADE"},
		},
	})

	keys, err := s.GetForeignKeys("orders")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].ForeignTable != "users" || keys[0].OnDelete != "CASCADE" ||
		!reflect.DeepEqual(keys[0].Columns, []string{"user_id"}) {
		t.Fatalf("keys = %+v", keys)
	}

	ok, err := s.HasForeignKey("orders", "orders_user_id_foreign")
	if err != nil || !ok {
		t.Errorf("HasForeignKey(name) = %v, %v", ok, err)
	}
	ok, err = s.HasForeignKey("orders", []string{"user_id"})
	if err != nil || !ok {
		t.Errorf("HasForeignKey(columns) = %v, %v", ok, err)
	}
	ok, err = s.HasForeignKey("orders", []string{"missing"})
	if err != nil || ok {
		t.Errorf("HasForeignKey(missing columns) = %v, %v", ok, err)
	}
}

func TestSchemaBuilderForeignKeysPostgresActions(t *testing.T) {
	conn, st := newFakeDB(t)
	conn.driver = "postgres"
	s := conn.Schema()
	sql, _ := compileForeignKeysSQL(dialectPostgres, "public", "orders")
	st.On(sql, &fakeResult{
		Columns: []string{"name", "columns", "foreign_schema", "foreign_table", "foreign_columns", "on_update", "on_delete"},
		Rows: [][]driver.Value{
			{"orders_user_id_foreign", "user_id", "public", "users", "id", "c", "n"},
		},
	})

	keys, err := s.GetForeignKeys("orders")
	if err != nil {
		t.Fatal(err)
	}
	if keys[0].OnUpdate != "cascade" || keys[0].OnDelete != "set null" {
		t.Errorf("pg action mapping = %+v", keys[0])
	}
}

// ---- SchemaBuilder：表 / 视图 / 类型内省 ----

func TestSchemaBuilderGetTablesAndListing(t *testing.T) {
	conn, st := newFakeDB(t)
	s := conn.Schema()
	// 未指定 schema：默认过滤系统库（含视图排除 table_type）
	sql, _ := compileTablesSQL(dialectMysql, "")
	st.On(sql, &fakeResult{
		Columns: []string{"name", "schema", "size", "comment", "engine", "collation"},
		Rows: [][]driver.Value{
			{"users", "appdb", int64(2048), "用户表", "InnoDB", "utf8mb4_general_ci"},
		},
	})

	tables, err := s.GetTables()
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 1 || tables[0].Name != "users" || tables[0].Size != 2048 || tables[0].Engine != "InnoDB" {
		t.Fatalf("tables = %+v", tables)
	}
	st.LastCall(t, sql) // SQL 含 table_type in ('BASE TABLE', ...) 与系统库排除

	listing, err := s.GetTableListing()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(listing, []string{"users"}) {
		t.Errorf("listing = %v", listing)
	}
}

func TestSchemaBuilderGetTablesNamedSchema(t *testing.T) {
	conn, st := newFakeDB(t)
	s := conn.Schema()
	sql, _ := compileTablesSQL(dialectMysql, "shop")
	st.On(sql, &fakeResult{
		Columns: []string{"name", "schema", "size", "comment", "engine", "collation"},
		Rows:    [][]driver.Value{{"orders", "shop", int64(0), "", "InnoDB", ""}},
	})

	listing, err := s.GetTableListing("shop")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(listing, []string{"orders"}) {
		t.Errorf("listing = %v", listing)
	}
	call := st.LastCall(t, sql)
	if len(call.Args) != 1 || call.Args[0] != "shop" {
		t.Errorf("args = %#v", call.Args)
	}
}

func TestSchemaBuilderGetViewsAndHasView(t *testing.T) {
	conn, st := newFakeDB(t)
	s := conn.Schema()
	sql, _ := compileViewsSQL(dialectMysql, "")
	st.On(sql, &fakeResult{
		Columns: []string{"name", "schema", "definition"},
		Rows: [][]driver.Value{
			{"active_users", "appdb", "select * from users where active = 1"},
		},
	})

	views, err := s.GetViews()
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].Name != "active_users" || views[0].Definition == "" {
		t.Fatalf("views = %+v", views)
	}

	ok, err := s.HasView("ACTIVE_USERS")
	if err != nil || !ok {
		t.Errorf("HasView(existing) = %v, %v", ok, err)
	}
	ok, err = s.HasView("missing_view")
	if err != nil || ok {
		t.Errorf("HasView(missing) = %v, %v", ok, err)
	}
}

func TestSchemaBuilderGetTypes(t *testing.T) {
	conn, st := newFakeDB(t)
	conn.driver = "postgres"
	s := conn.Schema()
	clause, _ := schemaWhereClause(dialectPostgres, "n.nspname", "")
	sql, _ := compileTypesSQL(dialectPostgres, clause, nil)
	st.On(sql, &fakeResult{
		Columns: []string{"name", "schema", "type", "category", "implicit"},
		Rows: [][]driver.Value{
			{"mood", "public", "e", "E", false},
		},
	})

	types, err := s.GetTypes()
	if err != nil {
		t.Fatal(err)
	}
	if len(types) != 1 || types[0].Name != "mood" || types[0].Type != "e" || types[0].Implicit {
		t.Fatalf("types = %+v", types)
	}

	// 非 PG 驱动不支持
	conn2, _ := newFakeDB(t)
	if _, err := conn2.Schema().GetTypes(); err == nil {
		t.Error("expect error for non-postgres driver")
	}
}

// ---- SchemaBuilder：数据库级与表级 DDL ----

func TestSchemaBuilderCreateAndDropDatabase(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("create database `appdb`", &fakeResult{})
	st.On("drop database if exists `appdb`", &fakeResult{})

	s := conn.Schema()
	if err := s.CreateDatabase("appdb"); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, "create database `appdb`")
	if err := s.DropDatabaseIfExists("appdb"); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, "drop database if exists `appdb`")
}

func TestSchemaBuilderRename(t *testing.T) {
	conn, st := newFakeDB(t)
	// Rename 走各方言 grammar 编译（Builder.php:610）：MySQL 为 rename table
	st.On("rename table `users` to `people`", &fakeResult{})
	if err := conn.Schema().Rename("users", "people"); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, "rename table `users` to `people`")
}

func TestSchemaBuilderDropColumns(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("alter table `users` drop `age`, drop `city`", &fakeResult{})
	if err := conn.Schema().DropColumns("users", "age", "city"); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, "alter table `users` drop `age`, drop `city`")

	// PG 方言：drop column 关键字 + 双引号包裹
	pg, st2 := newFakeDB(t)
	pg.driver = "postgres"
	st2.On(`alter table "users" drop column "age", drop column "city"`, &fakeResult{})
	if err := pg.Schema().DropColumns("users", "age", "city"); err != nil {
		t.Fatal(err)
	}
	st2.LastCall(t, `alter table "users" drop column "age", drop column "city"`)
}

func TestSchemaBuilderForeignKeyConstraints(t *testing.T) {
	conn, st := newFakeDB(t)
	st.On("SET FOREIGN_KEY_CHECKS=1;", &fakeResult{})
	st.On("SET FOREIGN_KEY_CHECKS=0;", &fakeResult{})

	s := conn.Schema()
	if err := s.DisableForeignKeyConstraints(); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableForeignKeyConstraints(); err != nil {
		t.Fatal(err)
	}

	ran := false
	err := s.WithoutForeignKeyConstraints(func() error {
		ran = true
		return nil
	})
	if err != nil || !ran {
		t.Errorf("WithoutForeignKeyConstraints: ran=%v err=%v", ran, err)
	}
	calls := st.Calls()
	if len(calls) != 4 || calls[0].Sql != "SET FOREIGN_KEY_CHECKS=0;" || calls[3].Sql != "SET FOREIGN_KEY_CHECKS=1;" {
		t.Errorf("calls = %+v", calls)
	}
}

func TestSchemaBuilderForeignKeyConstraintsPostgres(t *testing.T) {
	conn, st := newFakeDB(t)
	conn.driver = "postgres"
	st.On("SET CONSTRAINTS ALL IMMEDIATE;", &fakeResult{})
	st.On("SET CONSTRAINTS ALL DEFERRED;", &fakeResult{})

	s := conn.Schema()
	if err := s.DisableForeignKeyConstraints(); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableForeignKeyConstraints(); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaBuilderDropAllTables(t *testing.T) {
	conn, st := newFakeDB(t)
	s := conn.Schema()
	listSQL, _ := compileTablesSQL(dialectMysql, s.databaseName())
	st.On(listSQL, &fakeResult{
		Columns: []string{"name", "schema", "size", "comment", "engine", "collation"},
		Rows: [][]driver.Value{
			{"users", "appdb", int64(0), "", "InnoDB", ""},
			{"orders", "appdb", int64(0), "", "InnoDB", ""},
		},
	})
	st.On("SET FOREIGN_KEY_CHECKS=0;", &fakeResult{})
	st.On("drop table `users`, `orders`", &fakeResult{})
	st.On("SET FOREIGN_KEY_CHECKS=1;", &fakeResult{})

	if err := s.DropAllTables(); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range st.Calls() {
		got = append(got, c.Sql)
	}
	want := []string{listSQL, "SET FOREIGN_KEY_CHECKS=0;", "drop table `users`, `orders`", "SET FOREIGN_KEY_CHECKS=1;"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("calls =\n  %v\nwant\n  %v", got, want)
	}

	// 空库时不开关外键直接返回
	conn2, st2 := newFakeDB(t)
	s2 := conn2.Schema()
	listSQL2, _ := compileTablesSQL(dialectMysql, s2.databaseName())
	st2.On(listSQL2, &fakeResult{Columns: []string{"name", "schema", "size", "comment", "engine", "collation"}, Rows: [][]driver.Value{}})
	if err := s2.DropAllTables(); err != nil {
		t.Fatal(err)
	}
	if len(st2.Calls()) != 1 {
		t.Errorf("empty db should not toggle constraints, calls = %v", st2.Calls())
	}
}

func TestSchemaBuilderDropAllViews(t *testing.T) {
	conn, st := newFakeDB(t)
	s := conn.Schema()
	viewsSQL, _ := compileViewsSQL(dialectMysql, s.databaseName())
	st.On(viewsSQL, &fakeResult{
		Columns: []string{"name", "schema", "definition"},
		Rows:    [][]driver.Value{{"active_users", "appdb", "select 1"}},
	})
	st.On("drop view `active_users`", &fakeResult{})

	if err := s.DropAllViews(); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, "drop view `active_users`")
}

func TestSchemaBuilderDropAllTypes(t *testing.T) {
	conn, st := newFakeDB(t)
	conn.driver = "postgres"
	s := conn.Schema()
	clause, _ := schemaWhereClause(dialectPostgres, "n.nspname", "")
	typesSQL, _ := compileTypesSQL(dialectPostgres, clause, nil)
	st.On(typesSQL, &fakeResult{
		Columns: []string{"name", "schema", "type", "category", "implicit"},
		Rows:    [][]driver.Value{{"mood", "public", "e", "E", false}},
	})
	st.On(`drop type "mood" cascade`, &fakeResult{})

	if err := s.DropAllTypes(); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, `drop type "mood" cascade`)

	// 非 PG 驱动不支持
	conn2, _ := newFakeDB(t)
	if err := conn2.Schema().DropAllTypes(); err == nil {
		t.Error("expect error for non-postgres driver")
	}
}

// ---- SchemaBuilder：条件表构建 ----

func TestSchemaBuilderWhenTableHasColumn(t *testing.T) {
	conn, st := newFakeDB(t)
	s := conn.Schema()
	stubMySQLColumnListing(t, s, st, "users", []string{"id", "email"})
	addAge := "alter table `users` add `age` int not null"
	addCity := "alter table `users` add `city` varchar(255) not null"
	st.On(addAge, &fakeResult{})

	// 列存在 → 执行
	if err := s.WhenTableHasColumn("users", "email", func(t *grammar.Blueprint) {
		t.Integer("age")
	}); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, addAge)

	// 列存在 → Doesnt 分支不执行
	if err := s.WhenTableDoesntHaveColumn("users", "email", func(t *grammar.Blueprint) {
		t.String("city")
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range st.Calls() {
		if c.Sql == addCity {
			t.Error("WhenTableDoesntHaveColumn should not run when column exists")
		}
	}
}

func TestSchemaBuilderWhenTableDoesntHaveColumn(t *testing.T) {
	conn, st := newFakeDB(t)
	s := conn.Schema()
	stubMySQLColumnListing(t, s, st, "users", []string{"id"})
	addCity := "alter table `users` add `city` varchar(255) not null"
	st.On(addCity, &fakeResult{})

	// 列不存在 → Doesnt 分支执行
	if err := s.WhenTableDoesntHaveColumn("users", "city", func(t *grammar.Blueprint) {
		t.String("city")
	}); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, addCity)

	// Has 分支不执行
	if err := s.WhenTableHasColumn("users", "city", func(t *grammar.Blueprint) {
		t.String("other")
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range st.Calls() {
		if strings.Contains(c.Sql, "`other`") {
			t.Error("WhenTableHasColumn should not run when column missing")
		}
	}
}

func TestSchemaBuilderWhenTableHasIndex(t *testing.T) {
	conn, st := newFakeDB(t)
	s := conn.Schema()
	idxSQL, _ := compileIndexesSQL(dialectMysql, s.databaseName(), "users")
	st.On(idxSQL, mysqlSchemaIndexesRows())
	addCol := "alter table `users` add `age` int not null"
	st.On(addCol, &fakeResult{})

	// 索引存在（按名称）→ 执行
	if err := s.WhenTableHasIndex("users", "users_email_unique", func(t *grammar.Blueprint) {
		t.Integer("age")
	}); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, addCol)

	// 索引存在 → Doesnt 分支不执行（未打桩，执行即报错）
	if err := s.WhenTableDoesntHaveIndex("users", "users_email_unique", func(t *grammar.Blueprint) {
		t.Integer("age")
	}); err != nil {
		t.Fatal(err)
	}

	// 索引不存在 → Doesnt 分支执行
	if err := s.WhenTableDoesntHaveIndex("users", []string{"missing_col"}, func(t *grammar.Blueprint) {
		t.Integer("age")
	}); err != nil {
		t.Fatal(err)
	}
}

// ---- SchemaBuilder：SQLite 内省方言（D2）----

func TestSchemaBuilderGetColumnsSQLite(t *testing.T) {
	conn, st := newFakeDB(t)
	conn.driver = "sqlite"
	s := conn.Schema()
	sql, _ := compileColumnsSQL(dialectSqlite, "main", "users")
	st.On(sql, &fakeResult{
		Columns: []string{"name", "type", "nullable", "default", "primary", "extra"},
		Rows: [][]driver.Value{
			{"id", "INTEGER", false, nil, true, int64(0)},
			{"email", "varchar(255)", true, nil, false, int64(0)},
			{"status", "tinyinteger", false, "1", false, int64(0)},
		},
	})

	cols, err := s.GetColumns("users")
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 3 {
		t.Fatalf("cols = %+v", cols)
	}
	// SQLiteProcessor::processColumns：hasPrimaryKey(sum(pk)==1) && pk && integer → auto_increment
	if cols[0].TypeName != "integer" || cols[0].Type != "integer" || cols[0].Nullable || !cols[0].AutoIncrement {
		t.Errorf("id col = %+v", cols[0])
	}
	if cols[1].Type != "varchar(255)" || cols[1].TypeName != "varchar" || !cols[1].Nullable {
		t.Errorf("email col = %+v", cols[1])
	}
	if cols[2].AutoIncrement || cols[2].Default != "1" {
		t.Errorf("status col = %+v", cols[2])
	}
	st.LastCall(t, sql)
}

func TestSchemaBuilderGetColumnsSQLiteCompositePkNotAutoIncrement(t *testing.T) {
	conn, st := newFakeDB(t)
	conn.driver = "sqlite"
	s := conn.Schema()
	sql, _ := compileColumnsSQL(dialectSqlite, "main", "users")
	st.On(sql, &fakeResult{
		Columns: []string{"name", "type", "nullable", "default", "primary", "extra"},
		Rows: [][]driver.Value{
			{"id", "integer", false, nil, true, int64(0)},
			{"user_id", "integer", false, nil, true, int64(0)},
		},
	})

	cols, err := s.GetColumns("users")
	if err != nil {
		t.Fatal(err)
	}
	// 复合主键（pk 计数 > 1）：不判自增
	for _, c := range cols {
		if c.AutoIncrement {
			t.Errorf("composite pk col should not be auto increment: %+v", c)
		}
	}
}

func TestSchemaBuilderGetIndexesSQLite(t *testing.T) {
	conn, st := newFakeDB(t)
	conn.driver = "sqlite"
	s := conn.Schema()
	sql, _ := compileIndexesSQL(dialectSqlite, "main", "users")
	st.On(sql, &fakeResult{
		Columns: []string{"name", "columns", "unique", "primary"},
		Rows: [][]driver.Value{
			{"PRIMARY", "email,id", int64(1), int64(1)},
			{"sqlite_autoindex_users_1", "email,id", int64(1), int64(1)},
			{"USERS_EMAIL_UNIQUE", "email", int64(1), int64(0)},
		},
	})

	indexes, err := s.GetIndexes("users")
	if err != nil {
		t.Fatal(err)
	}
	// SQLiteProcessor::processIndexes：primary 计数 > 1 时剔除合成 'primary' 行；
	// 名称小写化；type 为空
	if len(indexes) != 2 {
		t.Fatalf("indexes = %+v", indexes)
	}
	if indexes[0].Name != "sqlite_autoindex_users_1" || !indexes[0].Primary || indexes[0].Type != "" {
		t.Errorf("index[0] = %+v", indexes[0])
	}
	if indexes[1].Name != "users_email_unique" || indexes[1].Primary {
		t.Errorf("index[1] = %+v", indexes[1])
	}
}

func TestSchemaBuilderGetForeignKeysSQLite(t *testing.T) {
	conn, st := newFakeDB(t)
	conn.driver = "sqlite"
	s := conn.Schema()
	sql, _ := compileForeignKeysSQL(dialectSqlite, "main", "posts")
	st.On(sql, &fakeResult{
		Columns: []string{"columns", "foreign_schema", "foreign_table", "foreign_columns", "on_update", "on_delete"},
		Rows: [][]driver.Value{
			{"user_id", "main", "users", "id", "NO ACTION", "CASCADE"},
		},
	})

	keys, err := s.GetForeignKeys("posts")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 {
		t.Fatalf("keys = %+v", keys)
	}
	// SQLiteProcessor::processForeignKeys：name 为 null、动作值小写化
	if keys[0].Name != "" || keys[0].OnUpdate != "no action" || keys[0].OnDelete != "cascade" ||
		!reflect.DeepEqual(keys[0].Columns, []string{"user_id"}) {
		t.Errorf("keys[0] = %+v", keys[0])
	}
}

func TestSchemaBuilderGetTablesAndViewsSQLite(t *testing.T) {
	conn, st := newFakeDB(t)
	conn.driver = "sqlite"
	s := conn.Schema()
	tablesSQL, _ := compileTablesSQL(dialectSqlite, "")
	st.On(tablesSQL, &fakeResult{
		Columns: []string{"name", "schema"},
		Rows:    [][]driver.Value{{"users", "main"}},
	})
	viewsSQL, _ := compileViewsSQL(dialectSqlite, "main")
	st.On(viewsSQL, &fakeResult{
		Columns: []string{"name", "schema", "definition"},
		Rows:    [][]driver.Value{{"active_users", "main", "select 1"}},
	})

	tables, err := s.GetTables()
	if err != nil {
		t.Fatal(err)
	}
	// SQLite 简化：不取 dbstat size（可选段），schema 默认 main
	if len(tables) != 1 || tables[0].Name != "users" || tables[0].Schema != "main" || tables[0].Size != 0 {
		t.Fatalf("tables = %+v", tables)
	}

	views, err := s.GetViews()
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].Name != "active_users" || views[0].Definition != "select 1" {
		t.Fatalf("views = %+v", views)
	}
}

func TestSchemaBuilderDropAllTablesSQLite(t *testing.T) {
	conn, st := newFakeDB(t)
	conn.driver = "sqlite"
	s := conn.Schema()
	g := grammar.NewSqliteQueryGrammar()
	st.On("PRAGMA writable_schema = 1", &fakeResult{})
	st.On(g.CompileDropAllTables("main"), &fakeResult{})
	st.On("PRAGMA writable_schema = 0", &fakeResult{})
	st.On(g.CompileRebuild("main"), &fakeResult{})

	if err := s.DropAllTables(); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range st.Calls() {
		got = append(got, c.Sql)
	}
	want := []string{
		"PRAGMA writable_schema = 1",
		`delete from "main".sqlite_master where type in ('table', 'index', 'trigger')`,
		"PRAGMA writable_schema = 0",
		`vacuum "main"`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("calls =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSchemaBuilderDropAllViewsSQLite(t *testing.T) {
	conn, st := newFakeDB(t)
	conn.driver = "sqlite"
	s := conn.Schema()
	g := grammar.NewSqliteQueryGrammar()
	st.On("PRAGMA writable_schema = 1", &fakeResult{})
	st.On(g.CompileDropAllViews("main"), &fakeResult{})
	st.On("PRAGMA writable_schema = 0", &fakeResult{})
	st.On(g.CompileRebuild("main"), &fakeResult{})

	if err := s.DropAllViews(); err != nil {
		t.Fatal(err)
	}
	if got := st.LastCall(t, g.CompileRebuild("main")); got.Sql != `vacuum "main"` {
		t.Errorf("last call = %+v", got)
	}
}

func TestSchemaBuilderCreateAndDropDatabaseSQLite(t *testing.T) {
	conn, _ := newFakeDB(t)
	conn.driver = "sqlite"
	s := conn.Schema()

	path := t.TempDir() + "/app.db"
	// SQLiteBuilder::createDatabase：File::put($name, '') —— 创建空文件
	if err := s.CreateDatabase(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file should exist: %v", err)
	}

	// dropDatabaseIfExists：存在则删除
	if err := s.DropDatabaseIfExists(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("database file should be removed")
	}

	// 文件不存在视为成功（! File::exists($name) || File::delete($name)）
	if err := s.DropDatabaseIfExists(path); err != nil {
		t.Errorf("drop missing database should succeed, got %v", err)
	}
}

// ---- SchemaBuilder：SQL Server 内省方言（D4 / D5）----

func TestSchemaBuilderGetColumnsSqlServer(t *testing.T) {
	conn, st := newFakeDB(t)
	conn.driver = "sqlserver"
	s := conn.Schema()
	sql, _ := compileColumnsSQL(dialectSqlserver, s.databaseName(), "users")
	st.On(sql, &fakeResult{
		Columns: []string{"name", "type_name", "length", "precision", "places", "nullable", "default", "autoincrement", "comment"},
		Rows: [][]driver.Value{
			{"id", "int", int64(4), int64(10), int64(0), false, nil, true, nil},
			{"email", "nvarchar", int64(-1), int64(0), int64(0), true, nil, false, nil},
			{"price", "decimal", int64(9), int64(8), int64(2), false, "((0))", false, nil},
			{"created_at", "datetime2", int64(6), int64(3), int64(0), false, nil, false, "创建时间"},
		},
	})

	cols, err := s.GetColumns("users")
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 4 {
		t.Fatalf("cols = %+v", cols)
	}
	// SqlServerProcessor::processColumns：type 由 type_name + length/precision/places 组装
	if cols[0].Type != "int" || !cols[0].AutoIncrement || cols[0].Nullable {
		t.Errorf("id col = %+v", cols[0])
	}
	if cols[1].Type != "nvarchar(max)" || !cols[1].Nullable {
		t.Errorf("email col = %+v", cols[1])
	}
	if cols[2].Type != "decimal(8,2)" || cols[2].Default != "((0))" {
		t.Errorf("price col = %+v", cols[2])
	}
	if cols[3].Type != "datetime2(3)" || cols[3].Comment != "创建时间" {
		t.Errorf("created_at col = %+v", cols[3])
	}
}

func TestSchemaBuilderGetIndexesSqlServer(t *testing.T) {
	conn, st := newFakeDB(t)
	conn.driver = "sqlserver"
	s := conn.Schema()
	sql, _ := compileIndexesSQL(dialectSqlserver, s.databaseName(), "users")
	st.On(sql, &fakeResult{
		Columns: []string{"name", "columns", "type", "unique", "primary"},
		Rows: [][]driver.Value{
			{"PK_USERS", "id", "CLUSTERED", int64(1), int64(1)},
			{"USERS_EMAIL_UNIQUE", "email", "NONCLUSTERED", int64(1), int64(0)},
		},
	})

	indexes, err := s.GetIndexes("users")
	if err != nil {
		t.Fatal(err)
	}
	// SqlServerProcessor::processIndexes：name/type 小写化
	if indexes[0].Name != "pk_users" || indexes[0].Type != "clustered" || !indexes[0].Primary || !indexes[0].Unique {
		t.Errorf("index[0] = %+v", indexes[0])
	}
	if indexes[1].Name != "users_email_unique" || indexes[1].Type != "nonclustered" || indexes[1].Primary {
		t.Errorf("index[1] = %+v", indexes[1])
	}
}

func TestSchemaBuilderGetForeignKeysSqlServer(t *testing.T) {
	conn, st := newFakeDB(t)
	conn.driver = "sqlserver"
	s := conn.Schema()
	sql, _ := compileForeignKeysSQL(dialectSqlserver, s.databaseName(), "orders")
	st.On(sql, &fakeResult{
		Columns: []string{"name", "columns", "foreign_schema", "foreign_table", "foreign_columns", "on_update", "on_delete"},
		Rows: [][]driver.Value{
			{"orders_user_id_foreign", "user_id", "dbo", "users", "id", "NO_ACTION", "CASCADE"},
		},
	})

	keys, err := s.GetForeignKeys("orders")
	if err != nil {
		t.Fatal(err)
	}
	// SqlServerProcessor::processForeignKeys：下划线转空格 + 小写（不做额外语义映射）
	if keys[0].OnUpdate != "no action" || keys[0].OnDelete != "cascade" {
		t.Errorf("keys[0] = %+v", keys[0])
	}
}

func TestSchemaBuilderDropAllTablesSqlServer(t *testing.T) {
	conn, st := newFakeDB(t)
	conn.driver = "sqlserver"
	s := conn.Schema()
	g := grammar.NewSqlServerGrammar()
	st.On(g.CompileDropAllForeignKeys(), &fakeResult{})
	st.On(g.CompileDropAllTables(), &fakeResult{})

	if err := s.DropAllTables(); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range st.Calls() {
		got = append(got, c.Sql)
	}
	// SqlServerBuilder::dropAllTables：先 drop 全部外键再 sp_msforeachtable
	want := []string{g.CompileDropAllForeignKeys(), "EXEC sp_msforeachtable 'DROP TABLE ?'"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("calls =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSchemaBuilderDropAllViewsSqlServer(t *testing.T) {
	conn, st := newFakeDB(t)
	conn.driver = "sqlserver"
	s := conn.Schema()
	g := grammar.NewSqlServerGrammar()
	st.On(g.CompileDropAllViews(), &fakeResult{})

	if err := s.DropAllViews(); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, g.CompileDropAllViews())
}

func TestSchemaBuilderForeignKeyConstraintsSqlServer(t *testing.T) {
	conn, st := newFakeDB(t)
	conn.driver = "sqlserver"
	enableSQL := `EXEC sp_msforeachtable @command1="print '?'", @command2="ALTER TABLE ? WITH CHECK CHECK CONSTRAINT all";`
	disableSQL := `EXEC sp_msforeachtable "ALTER TABLE ? NOCHECK CONSTRAINT all";`
	st.On(enableSQL, &fakeResult{})
	st.On(disableSQL, &fakeResult{})

	s := conn.Schema()
	if err := s.DisableForeignKeyConstraints(); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, disableSQL)
	if err := s.EnableForeignKeyConstraints(); err != nil {
		t.Fatal(err)
	}
	st.LastCall(t, enableSQL)
}

func TestSchemaBuilderDropColumnsSqlServer(t *testing.T) {
	conn, st := newFakeDB(t)
	conn.driver = "sqlserver"
	g := grammar.NewSqlServerGrammar()
	b := grammar.NewBlueprint("users")
	sql := g.CompileDropColumn(b, []string{"a", "b"})[0]
	st.On(sql, &fakeResult{})

	if err := conn.Schema().DropColumns("users", "a", "b"); err != nil {
		t.Fatal(err)
	}
	// dropColumns 经 Blueprint → compileDropColumn（含默认约束前置块）
	st.LastCall(t, sql)
}
