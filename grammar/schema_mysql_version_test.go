package grammar

import (
	"reflect"
	"testing"
)

// D7 版本分支 golden 测试（对齐 MySqlGrammar.php:338-380 compileRenameColumn、
// MariaDbGrammar.php:26-56 typeUuid/typeGeometry、MySqlGrammar.php:965-1094
// typeDate/typeYear 的 useCurrent 版本分支）。

func TestSchemaMySQLRenameColumnModern(t *testing.T) {
	g := NewMySqlGrammar()
	g.SetServerVersion("8.0.36")
	b := NewBlueprint("users")
	b.String("name")
	b.RenameColumn("name", "username")
	got := b.ToSql(g)
	want := []string{
		"alter table `users` add `name` varchar(255) not null",
		"alter table `users` rename column `name` to `username`",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("modern rename = %v", got)
	}
}

func TestSchemaMySQLRenameColumnLegacy(t *testing.T) {
	g := NewMySqlGrammar()
	g.SetServerVersion("5.7.42")
	b := NewBlueprint("users")
	b.String("name", 100)
	b.RenameColumn("name", "username")
	got := b.ToSql(g)
	// legacy：alter table change <from> <to> <完整定义>（定义取自 Blueprint 已有列）
	want := []string{
		"alter table `users` add `name` varchar(100) not null",
		"alter table `users` change `name` `username` `name` varchar(100) not null",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("legacy rename = %v", got)
	}
}

func TestSchemaMySQLRenameColumnLegacyNoDefinition(t *testing.T) {
	// Blueprint 无该列定义时保守回退现代 rename（需列内省，TODO 接线点）
	g := NewMySqlGrammar()
	g.SetServerVersion("5.7.42")
	b := NewBlueprint("users")
	b.RenameColumn("name", "username")
	got := b.ToSql(g)
	want := []string{"alter table `users` rename column `name` to `username`"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("legacy rename fallback = %v", got)
	}
}

func TestSchemaMariaDbRenameColumnLegacy(t *testing.T) {
	g := NewMariaDbGrammar()
	g.SetIsMaria(true)
	g.SetServerVersion("10.4.32-MariaDB")
	b := NewBlueprint("users")
	b.Integer("id")
	b.RenameColumn("id", "oid")
	got := b.ToSql(g)
	want := []string{
		"alter table `users` add `id` int not null",
		"alter table `users` change `id` `oid` `id` int not null",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mariadb legacy rename = %v", got)
	}

	// MariaDB >= 10.5.2 → 现代 rename
	g2 := NewMariaDbGrammar()
	g2.SetIsMaria(true)
	g2.SetServerVersion("10.5.2")
	b2 := NewBlueprint("users")
	b2.RenameColumn("id", "oid")
	if got := b2.ToSql(g2); got[0] != "alter table `users` rename column `id` to `oid`" {
		t.Errorf("mariadb modern rename = %v", got)
	}
}

func TestSchemaMariaDbUuidVersionBranch(t *testing.T) {
	// MariaDB < 10.7 → char(36)
	g := NewMariaDbGrammar()
	g.SetIsMaria(true)
	g.SetServerVersion("10.6.7-MariaDB")
	b := NewBlueprint("users")
	b.Create = true
	b.UUID("u")
	if got := b.ToSql(g); got[0] != "create table `users` (`u` char(36) not null)" {
		t.Errorf("mariadb uuid < 10.7 = %v", got)
	}

	// MariaDB >= 10.7 → uuid
	g2 := NewMariaDbGrammar()
	g2.SetIsMaria(true)
	g2.SetServerVersion("10.7.3-MariaDB")
	b2 := NewBlueprint("users")
	b2.Create = true
	b2.UUID("u")
	if got := b2.ToSql(g2); got[0] != "create table `users` (`u` uuid not null)" {
		t.Errorf("mariadb uuid >= 10.7 = %v", got)
	}

	// 版本未知 → 保守 char(36)
	g3 := NewMariaDbGrammar()
	g3.SetIsMaria(true)
	b3 := NewBlueprint("users")
	b3.Create = true
	b3.UUID("u")
	if got := b3.ToSql(g3); got[0] != "create table `users` (`u` char(36) not null)" {
		t.Errorf("mariadb uuid unknown version = %v", got)
	}
}

func TestSchemaMariaDbGeometryRefSystemId(t *testing.T) {
	// MariaDB：srid → ref_system_id=N（MariaDbGrammar.php:41-56）
	g := NewMariaDbGrammar()
	g.SetIsMaria(true)
	b := NewBlueprint("geo")
	b.Create = true
	b.Geometry("loc", "point").SetSrid(4326)
	if got := b.ToSql(g); got[0] != "create table `geo` (`loc` point ref_system_id=4326 not null)" {
		t.Errorf("mariadb geometry = %v", got)
	}

	// MySQL：srid → srid N
	g2 := NewMySqlGrammar()
	b2 := NewBlueprint("geo")
	b2.Create = true
	b2.Geometry("loc", "point").SetSrid(4326)
	if got := b2.ToSql(g2); got[0] != "create table `geo` (`loc` point srid 4326 not null)" {
		t.Errorf("mysql geometry = %v", got)
	}
}

func TestSchemaMySQLDateYearUseCurrentVersionBranch(t *testing.T) {
	// MySQL < 8.0.13：date/year 的 useCurrent 不注入默认值
	g := NewMySqlGrammar()
	g.SetServerVersion("5.7.42")
	b := NewBlueprint("logs")
	b.Date("d").SetUseCurrent()
	b.Year("y").SetUseCurrent()
	got := b.ToSql(g)
	want := []string{
		"alter table `logs` add `d` date not null",
		"alter table `logs` add `y` year not null",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mysql < 8.0.13 date/year useCurrent = %v", got)
	}

	// MySQL >= 8.0.13：注入 (CURDATE()) / (YEAR(CURDATE()))
	g2 := NewMySqlGrammar()
	g2.SetServerVersion("8.0.36")
	b2 := NewBlueprint("logs")
	b2.Date("d").SetUseCurrent()
	b2.Year("y").SetUseCurrent()
	got2 := b2.ToSql(g2)
	want2 := []string{
		"alter table `logs` add `d` date not null default (CURDATE())",
		"alter table `logs` add `y` year not null default (YEAR(CURDATE()))",
	}
	if !reflect.DeepEqual(got2, want2) {
		t.Errorf("mysql >= 8.0.13 date/year useCurrent = %v", got2)
	}

	// MariaDB：无条件注入
	g3 := NewMariaDbGrammar()
	g3.SetIsMaria(true)
	g3.SetServerVersion("10.4.32-MariaDB")
	b3 := NewBlueprint("logs")
	b3.Date("d").SetUseCurrent()
	got3 := b3.ToSql(g3)
	if got3[0] != "alter table `logs` add `d` date not null default (CURDATE())" {
		t.Errorf("mariadb date useCurrent = %v", got3)
	}
}
