package grammar

import (
	"fmt"
	"strconv"
	"strings"
)

// ---- MySQL Schema 编译 ----

// getColumnType 方言类型映射入口（子方言可经由 columnDefinitionWith /
// compileAddWith 注入自定义映射，见 schema_mariadb.go）。
func (g *MySqlGrammar) getColumnType(c *ColumnDefinition) string {
	return g.baseColumnType(c)
}

// baseColumnType 对应 MySqlGrammar 的 type* 方法族。
func (g *MySqlGrammar) baseColumnType(c *ColumnDefinition) string {
	switch c.Type {
	case "boolean":
		return "tinyint(1)"
	case "json", "jsonb":
		// 对齐 typeJson/typeJsonb：MySQL 两者均为 json
		return "json"
	case "decimal":
		return fmt.Sprintf("decimal(%d, %d)", c.Precision, c.Scale)
	case "float":
		if c.Precision > 0 {
			return fmt.Sprintf("float(%d)", c.Precision)
		}
		return "float"
	case "double":
		return "double"
	case "enum":
		return fmt.Sprintf("enum(%s)", g.QuoteString(c.Allowed))
	case "set":
		return fmt.Sprintf("set(%s)", g.QuoteString(c.Allowed))
	case "uuid":
		// 对齐 MySqlGrammar::typeUuid（char(36)）与 MariaDbGrammar::typeUuid
		// （MariaDbGrammar.php:26-33：>= 10.7 → uuid）；版本未知时保守 char(36)。
		if g.isMaria && g.serverVersion != "" && !mysqlVersionLessThan(g.serverVersion, "10.7.0") {
			return "uuid"
		}
		return "char(36)"
	case "ulid":
		return fmt.Sprintf("char(%d)", c.Length)
	case "ipaddress":
		return "varchar(45)"
	case "macaddress":
		return "varchar(17)"
	case "varchar", "char":
		return fmt.Sprintf("%s(%d)", c.Type, c.Length)
	case "binary":
		if c.Length > 0 {
			if c.Fixed {
				return fmt.Sprintf("binary(%d)", c.Length)
			}
			return fmt.Sprintf("varbinary(%d)", c.Length)
		}
		return "blob"
	case "datetime":
		if c.Precision > 0 {
			return fmt.Sprintf("datetime(%d)", c.Precision)
		}
		return "datetime"
	case "datetimetz":
		// MySQL 无时区类型，datetimetz 回落为 datetime 类型
		if c.Precision > 0 {
			return fmt.Sprintf("datetime(%d)", c.Precision)
		}
		return "datetime"
	case "timestamp":
		if c.Precision > 0 {
			return fmt.Sprintf("timestamp(%d)", c.Precision)
		}
		return "timestamp"
	case "timestamptz":
		// MySQL 无时区类型，timestamptz 回落为 timestamp 类型
		if c.Precision > 0 {
			return fmt.Sprintf("timestamp(%d)", c.Precision)
		}
		return "timestamp"
	case "time":
		if c.Precision > 0 {
			return fmt.Sprintf("time(%d)", c.Precision)
		}
		return "time"
	case "timetz":
		// timetz 回落为 time 类型（MySQL 无时区时间类型）
		if c.Precision > 0 {
			return fmt.Sprintf("time(%d)", c.Precision)
		}
		return "time"
	case "geometry", "geography":
		// geometry：子类型白名单 + srid
		sub := strings.ToLower(c.Subtype)
		switch sub {
		case "point", "linestring", "polygon", "geometrycollection", "multipoint", "multilinestring", "multipolygon":
		default:
			sub = "geometry"
		}
		sql := sub
		if c.Srid > 0 {
			// 对齐 MySqlGrammar::typeGeometry（:1168-1181）：MariaDB 走
			// MariaDbGrammar::typeGeometry（:41-56）ref_system_id，MySQL 走 srid
			if g.isMaria {
				sql += fmt.Sprintf(" ref_system_id=%d", c.Srid)
			} else {
				sql += fmt.Sprintf(" srid %d", c.Srid)
			}
		}
		return sql
	case "vector":
		if c.Dimensions > 0 {
			return fmt.Sprintf("vector(%d)", c.Dimensions)
		}
		return "vector"
	case "computed":
		panic(ErrUnsupported{"This database driver requires a type, see the virtualAs / storedAs modifiers."})
	case "tsvector":
		panic(ErrUnsupported{"This database driver does not support tsvector columns."})
	default:
		return c.Type
	}
}

// currentExpr CURRENT_TIMESTAMP（按精度）。
func currentExpr(precision int) string {
	if precision > 0 {
		return fmt.Sprintf("CURRENT_TIMESTAMP(%d)", precision)
	}
	return "CURRENT_TIMESTAMP"
}

// mysqlVersionLessThan 数值版本段比较（对应 version_compare 的数值子集，
// 忽略 "-MariaDB" 等后缀）；空版本视为未知，一律返回 false（保守走现代分支）。
func mysqlVersionLessThan(version, target string) bool {
	if version == "" {
		return false
	}
	if i := strings.IndexByte(version, '-'); i >= 0 {
		version = version[:i]
	}
	vp := strings.Split(version, ".")
	tp := strings.Split(target, ".")
	for i := range tp {
		v, t := 0, 0
		if i < len(vp) {
			v, _ = strconv.Atoi(vp[i])
		}
		t, _ = strconv.Atoi(tp[i])
		if v != t {
			return v < t
		}
	}
	return false
}

// supportsCurrentDateDefault date/year 的 useCurrent 表达式默认值支持范围
// （typeDate/typeYear：isMaria 或 MySQL >= 8.0.13）。
func (g *MySqlGrammar) supportsCurrentDateDefault() bool {
	if g.isMaria {
		return true
	}
	return g.serverVersion != "" && !mysqlVersionLessThan(g.serverVersion, "8.0.13")
}

func (g *MySqlGrammar) columnDefinition(c *ColumnDefinition) string {
	return g.columnDefinitionWith(c, g.getColumnType)
}

// columnDefinitionWith：类型映射函数可被子方言覆盖（MariaDB json 等）。
func (g *MySqlGrammar) columnDefinitionWith(c *ColumnDefinition, ct func(*ColumnDefinition) string) string {
	// useCurrent / useCurrentOnUpdate 在 timestamp/datetime 家族上注入默认值
	// （default current_timestamp / on update current_timestamp 注入语义）
	if c.UseCurrent && isDatetimeFamily(c.Type) {
		c = cloneColumn(c)
		if c.DefaultRaw == "" && c.DefaultValue == nil {
			c.DefaultRaw = currentExpr(c.Precision)
		}
	}
	// date/year 的 useCurrent 表达式默认值版本分支（对齐 MySqlGrammar::typeDate
	// :965-986 / typeYear :1078-1094：isMaria 或 MySQL >= 8.0.13 才注入）
	if c.UseCurrent && (c.Type == "date" || c.Type == "year") && g.supportsCurrentDateDefault() {
		c = cloneColumn(c)
		if c.DefaultRaw == "" && c.DefaultValue == nil {
			if c.Type == "date" {
				c.DefaultRaw = "(CURDATE())"
			} else {
				c.DefaultRaw = "(YEAR(CURDATE()))"
			}
		}
	}

	parts := []string{g.Wrap(c.Name), ct(c)}

	// 生成列（对齐 modifyVirtualAs / modifyStoredAs；生成列不带其他修饰符）
	if c.VirtualAs != "" {
		parts = append(parts, "as ("+c.VirtualAs+")")
		return strings.Join(parts, " ")
	}
	if c.StoredAs != "" {
		parts = append(parts, "as ("+c.StoredAs+") stored")
		return strings.Join(parts, " ")
	}

	if c.IsUnsigned {
		parts = append(parts, "unsigned")
	}
	if c.Charset != "" {
		parts = append(parts, "character set "+c.Charset)
	}
	if c.Collation != "" {
		parts = append(parts, "collate '"+c.Collation+"'")
	}
	if c.NotNull || !c.IsNullable {
		parts = append(parts, "not null")
	} else {
		parts = append(parts, "null")
	}
	if c.Invisible {
		parts = append(parts, "invisible")
	}
	if c.DefaultRaw != "" {
		parts = append(parts, "default "+c.DefaultRaw)
	} else if c.DefaultValue != nil {
		parts = append(parts, "default "+g.QuoteString(c.DefaultValue))
	}
	// on update（对齐 modifyOnUpdate）
	if c.OnUpdate != "" {
		parts = append(parts, "on update "+c.OnUpdate)
	} else if c.UseCurrentOnUpdate && isDatetimeFamily(c.Type) {
		parts = append(parts, "on update "+currentExpr(c.Precision))
	}
	if c.IsAutoIncrement {
		parts = append(parts, "auto_increment")
		if c.PrimaryKey {
			parts = append(parts, "primary key")
		}
	}
	if c.First {
		parts = append(parts, "first")
	}
	if c.After != "" {
		parts = append(parts, "after "+g.Wrap(c.After))
	}
	if c.CommentText != "" {
		parts = append(parts, "comment '"+strings.ReplaceAll(c.CommentText, "'", "\\'")+"'")
	}
	return strings.Join(parts, " ")
}

func isDatetimeFamily(t string) bool {
	switch t {
	case "timestamp", "timestamptz", "datetime", "datetimetz":
		return true
	}
	return false
}

// cloneColumn 浅拷贝（避免编译期改写共享定义）。
func cloneColumn(c *ColumnDefinition) *ColumnDefinition {
	cp := *c
	return &cp
}

// CompileCreate：create table ... （含 charset/collation/engine 与尾部 primary key 段）
func (g *MySqlGrammar) CompileCreate(b *Blueprint) []string {
	return g.compileCreateWith(b, g.getColumnType)
}

func (g *MySqlGrammar) compileCreateWith(b *Blueprint, ct func(*ColumnDefinition) string) []string {
	cols := make([]string, 0, len(b.Columns)+1)
	var pkCols []string
	for _, c := range b.Columns {
		cols = append(cols, g.columnDefinitionWith(c, ct))
		if !c.IsAutoIncrement && c.PrimaryKey {
			pkCols = append(pkCols, g.Wrap(c.Name))
		}
	}
	for _, idx := range b.Indexes {
		if idx.Type == "primary" {
			for _, col := range idx.Columns {
				pkCols = append(pkCols, g.Wrap(col))
			}
		} else if idx.Type != "index" && idx.Type != "unique" && idx.Type != "foreign" &&
			idx.Type != "fulltext" && idx.Type != "spatialindex" && idx.Type != "vectorindex" {
			cols = append(cols, g.columnDefinitionWith(&ColumnDefinition{Name: idx.Name}, ct))
		}
	}
	if len(pkCols) > 0 {
		cols = append(cols, "primary key ("+strings.Join(pkCols, ", ")+")")
	}

	verb := "create"
	if b.Temporary {
		verb = "create temporary"
	}
	sql := verb + " table " + g.WrapTable(b.Table) + " (" + strings.Join(cols, ", ") + ")"
	if b.Charset != "" {
		sql += " default character set " + b.Charset
	}
	if b.Collation != "" {
		sql += " collate '" + b.Collation + "'"
	}
	if b.Engine != "" {
		sql += " engine = " + b.Engine
	}

	// MySQL：index/unique/foreign 在 create 场景下也是独立 alter 语句
	out := []string{sql}
	out = append(out, g.compileSchemaIndexesNonPrimary(b)...)
	return out
}

// CompileAdd：alter 逐列 add；带 Change 标记的列走 modify；索引逐条
func (g *MySqlGrammar) CompileAdd(b *Blueprint) []string {
	return g.compileAddWith(b, g.getColumnType)
}

func (g *MySqlGrammar) compileAddWith(b *Blueprint, ct func(*ColumnDefinition) string) []string {
	table := g.WrapTable(b.Table)
	out := make([]string, 0, len(b.Columns)+len(b.Indexes)+1)
	for _, c := range b.Columns {
		if c.IsChange {
			out = append(out, g.compileChangeWith(b, c, ct)...)
			continue
		}
		out = append(out, "alter table "+table+" add "+g.columnDefinitionWith(c, ct))
	}
	out = append(out, g.compileSchemaIndexes(b)...)
	for _, c := range b.Columns {
		if c.From > 0 {
			out = append(out, "alter table "+table+" auto_increment = "+fmt.Sprint(c.From))
		}
	}
	return out
}

// CompileChange 对应 MySqlGrammar::compileChange：alter table t modify `col` ...
func (g *MySqlGrammar) CompileChange(b *Blueprint, c *ColumnDefinition) []string {
	return g.compileChangeWith(b, c, g.getColumnType)
}

func (g *MySqlGrammar) compileChangeWith(b *Blueprint, c *ColumnDefinition, ct func(*ColumnDefinition) string) []string {
	sql := "alter table " + g.WrapTable(b.Table) + " modify " + g.columnDefinitionWith(c, ct)
	return []string{sql}
}

func (g *MySqlGrammar) compileSchemaIndexes(b *Blueprint) []string {
	return g.compileSchemaIndexesFiltered(b, "index", "unique", "primary", "foreign", "fulltext", "spatialindex", "vectorindex")
}

func (g *MySqlGrammar) compileSchemaIndexesNonPrimary(b *Blueprint) []string {
	return g.compileSchemaIndexesFiltered(b, "index", "unique", "foreign", "fulltext", "spatialindex", "vectorindex")
}

func (g *MySqlGrammar) compileSchemaIndexesFiltered(b *Blueprint, kinds ...string) []string {
	out := make([]string, 0, len(b.Indexes))
	table := g.WrapTable(b.Table)
	for _, idx := range b.Indexes {
		kind := idx.Type
		allowed := false
		for _, k := range kinds {
			if kind == k {
				allowed = true
				break
			}
		}
		if !allowed {
			continue
		}
		switch kind {
		case "index":
			out = append(out, "alter table "+table+" add index "+g.Wrap(idx.Name)+g.indexColumnList(idx))
		case "unique":
			out = append(out, "alter table "+table+" add unique "+g.Wrap(idx.Name)+g.indexColumnList(idx))
		case "fulltext":
			out = append(out, "alter table "+table+" add fulltext "+g.Wrap(idx.Name)+g.indexColumnList(idx))
		case "spatialindex":
			out = append(out, "alter table "+table+" add spatial index "+g.Wrap(idx.Name)+g.indexColumnList(idx))
		case "primary":
			out = append(out, "alter table "+table+" add primary key ("+g.columnizeNames(idx.Columns)+")")
		case "vectorindex":
			panic(ErrUnsupported{"The database driver in use does not support vector indexes."})
		case "foreign":
			out = append(out, g.foreignKeySQL(table, idx))
		}
	}
	return out
}

// indexColumnList 列清单或原始表达式（rawIndex）。
func (g *MySqlGrammar) indexColumnList(idx *IndexDefinition) string {
	if idx.Expression != "" {
		return "(" + idx.Expression + ")"
	}
	return "(" + g.columnizeNames(idx.Columns) + ")"
}

// foreignKeySQL 编译外键（含复合外键引用列与 on delete/on update）。
func (g *MySqlGrammar) foreignKeySQL(table string, idx *IndexDefinition) string {
	sql := "alter table " + table + " add constraint " + g.Wrap(idx.Name) +
		" foreign key (" + g.columnizeNames(idx.Columns) + ") references " +
		g.WrapTable(idx.OnTable) + " (" + g.columnizeNames(idx.foreignRefColumns()) + ")"
	if idx.OnDelete != "" {
		sql += " on delete " + idx.OnDelete
	}
	if idx.OnUpdate != "" {
		sql += " on update " + idx.OnUpdate
	}
	return sql
}

func (g *MySqlGrammar) columnizeNames(cols []string) string {
	wrapped := make([]string, len(cols))
	for i, c := range cols {
		wrapped[i] = g.Wrap(c)
	}
	return strings.Join(wrapped, ", ")
}

func (g *MySqlGrammar) CompileDrop(table string) string {
	return "drop table " + g.WrapTable(table)
}

func (g *MySqlGrammar) CompileDropIfExists(table string) string {
	return "drop table if exists " + g.WrapTable(table)
}

// CompileDropColumn 对应 MySqlGrammar::compileDropColumn：drop `a`, drop `b`。
func (g *MySqlGrammar) CompileDropColumn(b *Blueprint, columns []string) []string {
	parts := make([]string, len(columns))
	for i, c := range columns {
		parts[i] = "drop " + g.Wrap(c)
	}
	return []string{"alter table " + g.WrapTable(b.Table) + " " + strings.Join(parts, ", ")}
}

// CompileRenameColumn 对齐 MySqlGrammar::compileRenameColumn（:338-350）：
// MariaDB < 10.5.2 或 MySQL < 8.0.3 回退 legacy change 形式，否则现代
// rename column（版本未知时保守走现代分支）。
func (g *MySqlGrammar) CompileRenameColumn(b *Blueprint, from, to string) string {
	if (g.isMaria && mysqlVersionLessThan(g.serverVersion, "10.5.2")) ||
		(!g.isMaria && mysqlVersionLessThan(g.serverVersion, "8.0.3")) {
		return g.compileLegacyRenameColumn(b, from, to)
	}
	return "alter table " + g.WrapTable(b.Table) + " rename column " + g.Wrap(from) + " to " + g.Wrap(to)
}

// compileLegacyRenameColumn：老版本 MySQL 的列重命名走
// alter table t change from to <完整列定义>，需先取得列定义；Go 侧优先从
// Blueprint 已有列定义取（同迁移内重命名场景），取不到时
// 保守回退现代 rename column。TODO 接线点：接入列内省后可完整支持。
func (g *MySqlGrammar) compileLegacyRenameColumn(b *Blueprint, from, to string) string {
	table := g.WrapTable(b.Table)
	for _, c := range b.Columns {
		if c.Name == from {
			return "alter table " + table + " change " + g.Wrap(from) + " " + g.Wrap(to) + " " + g.columnDefinition(c)
		}
	}
	return "alter table " + table + " rename column " + g.Wrap(from) + " to " + g.Wrap(to)
}

// CompileRename 对应 compileRename：rename table x to y。
func (g *MySqlGrammar) CompileRename(b *Blueprint, to string) string {
	return "rename table " + g.WrapTable(b.Table) + " to " + g.WrapTable(to)
}

// CompileRenameIndex 对应 compileRenameIndex。
func (g *MySqlGrammar) CompileRenameIndex(b *Blueprint, from, to string) string {
	return "alter table " + g.WrapTable(b.Table) + " rename index " + g.Wrap(from) + " to " + g.Wrap(to)
}

func (g *MySqlGrammar) CompileDropPrimary(b *Blueprint, index string) string {
	return "alter table " + g.WrapTable(b.Table) + " drop primary key"
}

func (g *MySqlGrammar) CompileDropUnique(b *Blueprint, index string) string {
	return "alter table " + g.WrapTable(b.Table) + " drop index " + g.Wrap(index)
}

func (g *MySqlGrammar) CompileDropIndex(b *Blueprint, index string) string {
	return "alter table " + g.WrapTable(b.Table) + " drop index " + g.Wrap(index)
}

func (g *MySqlGrammar) CompileDropFullText(b *Blueprint, index string) string {
	return g.CompileDropIndex(b, index)
}

func (g *MySqlGrammar) CompileDropSpatialIndex(b *Blueprint, index string) string {
	return g.CompileDropIndex(b, index)
}

func (g *MySqlGrammar) CompileDropVectorIndex(b *Blueprint, index string) string {
	return g.CompileDropIndex(b, index)
}

func (g *MySqlGrammar) CompileDropForeign(b *Blueprint, index string) string {
	return "alter table " + g.WrapTable(b.Table) + " drop foreign key " + g.Wrap(index)
}

func (g *MySqlGrammar) CompileIndex(b *Blueprint, idx *IndexDefinition) string {
	return g.compileSchemaIndexes(b)[0]
}

func (g *MySqlGrammar) CompileUnique(b *Blueprint, idx *IndexDefinition) string {
	return g.compileSchemaIndexes(b)[0]
}

func (g *MySqlGrammar) CompilePrimary(b *Blueprint, idx *IndexDefinition) string {
	return g.compileSchemaIndexes(b)[0]
}

func (g *MySqlGrammar) CompileForeign(b *Blueprint, idx *IndexDefinition) string {
	return g.compileSchemaIndexes(b)[0]
}

func (g *MySqlGrammar) CompileFullText(b *Blueprint, idx *IndexDefinition) string {
	return g.compileSchemaIndexes(b)[0]
}

func (g *MySqlGrammar) CompileSpatialIndex(b *Blueprint, idx *IndexDefinition) string {
	return g.compileSchemaIndexes(b)[0]
}

// CompileVectorIndex MySQL 不支持向量索引（抛出运行时错误）。
func (g *MySqlGrammar) CompileVectorIndex(b *Blueprint, idx *IndexDefinition) string {
	panic(ErrUnsupported{"The database driver in use does not support vector indexes."})
}

func (g *MySqlGrammar) CompileTableExists(schema, table string) (string, []any) {
	return "select * from information_schema.tables where table_schema = ? and table_name = ? and table_type = 'BASE TABLE'",
		[]any{schema, table}
}

func (g *MySqlGrammar) CompileColumnListing(schema, table string) (string, []any) {
	return "select `column_name` as `column_name` from information_schema.columns where `table_schema` = ? and `table_name` = ?",
		[]any{schema, table}
}

func (g *MySqlGrammar) WrapTableForSchema(v any) string {
	return g.WrapTable(v)
}

// 编译期断言：实现 SchemaGrammar
var _ SchemaGrammar = (*MySqlGrammar)(nil)
var _ SchemaGrammar = (*PostgresGrammar)(nil)
var _ SchemaGrammar = (*SQLiteGrammar)(nil)
var _ SchemaGrammar = (*SqlServerGrammar)(nil)
