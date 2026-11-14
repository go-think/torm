package torm

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/go-think/torm/grammar"
)

// 本文件实现 Schema 构建器（表/列 DDL 与内省，含各方言子类语义）。
//
// 待 grammar 侧提供（schema_builder 暂以本地编译函数顶替，见 compile*SQL 系列）：
//   - SchemaGrammar 新增编译方法：CompileColumns / CompileIndexes /
//     CompileForeignKeys / CompileTables / CompileViews / CompileTypes /
//     CompileCreateDatabase / CompileDropDatabaseIfExists /
//     CompileEnableForeignKeyConstraints / CompileDisableForeignKeyConstraints /
//     CompileDropAllTables / CompileDropAllViews / CompileDropAllTypes /
//     CompileRename / CompileDropColumn
//   - 方言 SchemaGrammar 类型：SQLite / SQL Server / MariaDB（MariaDB 可由
//     QueryGrammar 包裹形态提供，SQLite 与 SQL Server 的 QueryGrammar 尚未实现
//     SchemaGrammar 接口）
//   - Connection 保留原始 Config（当前 Config.SearchPath 经连接器注入 DSN 后
//     丢失，schema_builder 只能从 DSN 参数解析 search_path 默认 schema）

// SchemaColumn 内省结果：表列元数据（getColumns 返回的行结构）。
type SchemaColumn struct {
	Name          string
	TypeName      string // 基础类型名（如 varchar / int4）
	Type          string // 完整类型（如 varchar(255)）
	Nullable      bool
	Default       any
	AutoIncrement bool
	Comment       string
}

// SchemaIndex 内省结果：索引元数据（对应 getIndexes 行结构）。
type SchemaIndex struct {
	Name    string
	Columns []string
	Type    string // btree / hash 等（方言相关）
	Unique  bool
	Primary bool
}

// SchemaForeignKey 内省结果：外键元数据（对应 getForeignKeys 行结构）。
type SchemaForeignKey struct {
	Name           string
	Columns        []string
	ForeignSchema  string
	ForeignTable   string
	ForeignColumns []string
	OnUpdate       string
	OnDelete       string
}

// SchemaTable 内省结果：表元数据（对应 getTables 行结构）。
type SchemaTable struct {
	Name      string
	Schema    string
	Size      int64
	Comment   string
	Engine    string
	Collation string
}

// SchemaView 内省结果：视图元数据（对应 getViews 行结构）。
type SchemaView struct {
	Name       string
	Schema     string
	Definition string
}

// SchemaType 内省结果：用户自定义类型元数据（对应 getTypes 行结构，仅 Postgres）。
type SchemaType struct {
	Name     string
	Schema   string
	Type     string // typtype：c(composite)/e(enum)/d(domain)/m(range) 等
	Category string // typcategory
	Implicit bool
}

// SchemaBuilder 为 Schema 构建器门面（挂在具名连接上）。
type SchemaBuilder struct {
	conn   *Connection
	prefix string
}

// Schema 返回连接的 Schema 构建器。
func (c *Connection) Schema() *SchemaBuilder {
	return &SchemaBuilder{conn: c, prefix: c.tablePrefix}
}

// GetSchemaGrammar 按驱动返回 DDL 编译器。
// 对应各驱动的 getConnection → getSchemaGrammar 方言分发。
func (c *Connection) GetSchemaGrammar() grammar.SchemaGrammar {
	g := c.newSchemaGrammar()
	// D7 版本分支：仅 MySQL/MariaDB 消费版本信息（rename column 回退、
	// uuid/geometry/date useCurrent 分支），避免每次 GetSchemaGrammar 都发
	// 版本探测查询。版本未注入时各方言保持保守默认（现代语法）。
	switch strings.ToLower(c.driver) {
	case "mysql", "mariadb":
		if sv, ok := g.(interface {
			SetServerVersion(string)
			SetIsMaria(bool)
		}); ok {
			if v, err := c.ServerVersion(); err == nil && v != "" {
				sv.SetServerVersion(v)
			}
			sv.SetIsMaria(strings.ToLower(c.driver) == "mariadb")
		}
	}
	return g
}

// newSchemaGrammar 方言 → SchemaGrammar 实例。
func (c *Connection) newSchemaGrammar() grammar.SchemaGrammar {
	switch strings.ToLower(c.driver) {
	case "postgres", "postgresql", "pgsql":
		return grammar.NewPostgresGrammar()
	case "mariadb":
		// MariaDbGrammar 内嵌 MySqlGrammar（方法提升），天然满足 SchemaGrammar；
		// isMaria 标记按 driver 传入（版本由 SetServerVersion 注入，见 TODO）。
		mg := grammar.NewMariaDbGrammar()
		mg.SetIsMaria(true)
		return mg
	case "sqlite", "sqlite3":
		return grammar.NewSqliteQueryGrammar()
	case "sqlserver":
		return grammar.NewSqlServerGrammar()
	default:
		return grammar.NewMySqlGrammar()
	}
}

// ---- 表构建（Table / Create：alter / create 两种模式） ----

// Table 修改表（Blueprint 处于 alter 模式）。
func (s *SchemaBuilder) Table(table string, fn func(t *grammar.Blueprint)) error {
	g := s.conn.GetSchemaGrammar()
	g.SetTablePrefix(s.prefix)
	b := grammar.NewBlueprint(table)
	fn(b)
	for _, stmt := range b.ToSql(g) {
		if err := s.conn.Statement(stmt); err != nil {
			return err
		}
	}
	return nil
}

// Create 新建表（Blueprint 处于 create 模式）。
func (s *SchemaBuilder) Create(table string, fn func(t *grammar.Blueprint)) error {
	g := s.conn.GetSchemaGrammar()
	g.SetTablePrefix(s.prefix)
	b := grammar.NewBlueprint(table)
	b.Create = true
	fn(b)
	for _, stmt := range b.ToSql(g) {
		if err := s.conn.Statement(stmt); err != nil {
			return err
		}
	}
	return nil
}

// CreateTable 兼容别名（= Create，新建表）。
func (s *SchemaBuilder) CreateTable(table string, fn func(t *grammar.Blueprint)) error {
	return s.Create(table, fn)
}

// AlterTable 兼容别名（= Table，修改表）。
func (s *SchemaBuilder) AlterTable(table string, fn func(t *grammar.Blueprint)) error {
	return s.Table(table, fn)
}

// Drop 删表。
func (s *SchemaBuilder) Drop(table string) error {
	g := s.conn.GetSchemaGrammar()
	g.SetTablePrefix(s.prefix)
	return s.conn.Statement(g.CompileDrop(table))
}

// DropIfExists 条件删表。
func (s *SchemaBuilder) DropIfExists(table string) error {
	g := s.conn.GetSchemaGrammar()
	g.SetTablePrefix(s.prefix)
	return s.conn.Statement(g.CompileDropIfExists(table))
}

// Statement 在连接上执行原生 SQL（Builder::statement 语义）。
// 供 SQL 文件迁移等无 Blueprint 场景使用；多语句文件要求驱动支持单次
// Exec 执行多语句（MySQL 需 DSN multiStatements=true，SQLite 原生支持）。
// Statement 执行原生 SQL（SQL 文件迁移的执行通道）。
// **无参数时绕过预编译、直走连接级 Exec**：SQLite（mattn）与 MySQL
// （multiStatements=true）只在连接级 Execer 路径处理多语句脚本，
// 预编译路径只执行首条且余下语句被静默丢弃（已实证）。
func (s *SchemaBuilder) Statement(query string, args ...any) error {
	if len(args) == 0 {
		_, err := s.conn.Unprepared(query)
		return err
	}
	return s.conn.Statement(query, args...)
}

// HasTable 表是否存在（引用支持 schema.table 拆分）。
func (s *SchemaBuilder) HasTable(table string) (bool, error) {
	schema, tbl, err := s.parseSchemaAndTable(table)
	if err != nil {
		return false, err
	}
	g := s.conn.GetSchemaGrammar()
	g.SetTablePrefix(s.prefix)
	sql, bindings := g.CompileTableExists(s.resolveSchema(schema), s.prefix+tbl)
	rows, err := s.conn.SelectMaps(sql, bindings)
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

// ---- 数据库级 DDL（Builder.php:131/:144） ----

// CreateDatabase 创建数据库。
func (s *SchemaBuilder) CreateDatabase(name string) error {
	if s.dialect() == dialectSqlite {
		// SQLiteBuilder::createDatabase（SQLiteBuilder.php:17-24）：
		// SQLite 的"建库"即创建空数据库文件（os.Create 截断语义一致）。
		f, err := os.Create(name)
		if err != nil {
			return err
		}
		return f.Close()
	}
	d := s.dialect()
	sql, args := compileCreateDatabaseSQL(d, name)
	return s.conn.Statement(sql, args...)
}

// DropDatabaseIfExists 条件删除数据库。
func (s *SchemaBuilder) DropDatabaseIfExists(name string) error {
	if s.dialect() == dialectSqlite {
		// SQLiteBuilder::dropDatabaseIfExists（SQLiteBuilder.php:26-33）：
		// ! File::exists($name) || File::delete($name) —— 文件不存在视为成功。
		if _, err := os.Stat(name); err != nil {
			return nil
		}
		return os.Remove(name)
	}
	d := s.dialect()
	sql, args := compileDropDatabaseIfExistsSQL(d, name)
	return s.conn.Statement(sql, args...)
}

// ---- 列 / 表级 DDL ----

// DropColumns 删除列（对应 Builder.php:560 dropColumns）。统一走各方言
// grammar 的 CompileDropColumn（Blueprint 编译路径）：
// SQLite 产出合法的 alter table x drop column y
// （>= 3.35，避免缺 COLUMN 关键字的方言非法 SQL）；SQL Server 含默认约束
// 前置块；方言不支持时返回明确错误（RuntimeException 对应物）。
func (s *SchemaBuilder) DropColumns(table string, columns ...string) error {
	if len(columns) == 0 {
		return nil
	}
	g := s.conn.GetSchemaGrammar()
	g.SetTablePrefix(s.prefix)
	stmts, err := compileSchemaSQL(func() []string {
		return g.CompileDropColumn(grammar.NewBlueprint(table), columns)
	})
	if err != nil {
		return err
	}
	for _, stmt := range stmts {
		if err := s.conn.Statement(stmt); err != nil {
			return err
		}
	}
	return nil
}

// Rename 重命名表（对应 Builder.php:610 rename）。走各方言 grammar 的
// CompileRename（MySQL rename table ... to ...、SQL Server sp_rename、
// PG/SQLite alter table ... rename to ...），方言不支持时返回明确错误。
func (s *SchemaBuilder) Rename(from, to string) error {
	g := s.conn.GetSchemaGrammar()
	g.SetTablePrefix(s.prefix)
	stmts, err := compileSchemaSQL(func() []string {
		return []string{g.CompileRename(grammar.NewBlueprint(from), to)}
	})
	if err != nil {
		return err
	}
	for _, stmt := range stmts {
		if err := s.conn.Statement(stmt); err != nil {
			return err
		}
	}
	return nil
}

// compileSchemaSQL 执行方言编译，把 ErrUnsupported panic 转为明确错误
// （方言不支持时的 RuntimeException 语义，Go 侧以 error 返回）。
func compileSchemaSQL(fn func() []string) (stmts []string, err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(grammar.ErrUnsupported); ok {
				stmts, err = nil, fmt.Errorf("schema: %s", e.Msg)
				return
			}
			panic(r)
		}
	}()
	return fn(), nil
}

// DropAllTables 删除当前 schema 下全部表。
// MySQL/PG：外键开关包裹 + 逐表 drop（对应 MySqlBuilder/PostgresBuilder）；
// SQLite：writable_schema + sqlite_master delete + vacuum（SQLiteBuilder::dropAllTables，
// 统一走 writable_schema 路径保证连接内一致；文件级清空 TODO 接线点：
// 按 DSN 判定文件库后 os.Truncate）；
// SQL Server：先 drop 全部外键，再 sp_msforeachtable（SqlServerBuilder::dropAllTables）。
func (s *SchemaBuilder) DropAllTables() error {
	switch s.dialect() {
	case dialectSqlite:
		return s.dropAllTablesSqlite()
	case dialectSqlserver:
		return s.dropAllTablesSqlserver()
	}
	tables, err := s.GetTableListing(s.currentSchemaName())
	if err != nil {
		return err
	}
	if len(tables) == 0 {
		return nil
	}
	d := s.dialect()
	if err := s.DisableForeignKeyConstraints(); err != nil {
		return err
	}
	defer func() { _ = s.EnableForeignKeyConstraints() }()
	return s.conn.Statement(compileDropAllTablesSQL(d, tables))
}

// dropAllTablesSqlite 对应 SQLiteBuilder::dropAllTables（SQLiteBuilder.php:143-169）
// 的 writable_schema 分支：pragma writable_schema = 1 → 删 sqlite_master 行 →
// pragma writable_schema = 0 → vacuum 重建（compileRebuild）。
func (s *SchemaBuilder) dropAllTablesSqlite() error {
	g := s.conn.GetSchemaGrammar().(*grammar.SQLiteGrammar)
	g.SetTablePrefix(s.prefix)
	// getCurrentSchemaListing 对 SQLite 固定返回 ['main']
	schema := "main"
	if err := s.conn.Statement("PRAGMA writable_schema = 1"); err != nil {
		return err
	}
	if err := s.conn.Statement(g.CompileDropAllTables(schema)); err != nil {
		return err
	}
	if err := s.conn.Statement("PRAGMA writable_schema = 0"); err != nil {
		return err
	}
	return s.conn.Statement(g.CompileRebuild(schema))
}

// dropAllTablesSqlserver 对应 SqlServerBuilder::dropAllTables（SqlServerBuilder.php:14-20）：
// 先 compileDropAllForeignKeys 再 compileDropAllTables（无外键开关、无 listing）。
func (s *SchemaBuilder) dropAllTablesSqlserver() error {
	g := s.conn.GetSchemaGrammar().(*grammar.SqlServerGrammar)
	g.SetTablePrefix(s.prefix)
	if err := s.conn.Statement(g.CompileDropAllForeignKeys()); err != nil {
		return err
	}
	return s.conn.Statement(g.CompileDropAllTables())
}

// DropAllViews 删除当前 schema 下全部视图。
func (s *SchemaBuilder) DropAllViews() error {
	switch s.dialect() {
	case dialectSqlite:
		return s.dropAllViewsSqlite()
	case dialectSqlserver:
		g := s.conn.GetSchemaGrammar().(*grammar.SqlServerGrammar)
		g.SetTablePrefix(s.prefix)
		return s.conn.Statement(g.CompileDropAllViews())
	}
	views, err := s.GetViews(s.currentSchemaName())
	if err != nil {
		return err
	}
	if len(views) == 0 {
		return nil
	}
	names := make([]string, 0, len(views))
	for _, v := range views {
		names = append(names, v.Name)
	}
	return s.conn.Statement(compileDropAllViewsSQL(s.dialect(), names))
}

// dropAllViewsSqlite 对应 SQLiteBuilder::dropAllViews（SQLiteBuilder.php:174-187）：
// writable_schema + sqlite_master delete + vacuum。
func (s *SchemaBuilder) dropAllViewsSqlite() error {
	g := s.conn.GetSchemaGrammar().(*grammar.SQLiteGrammar)
	g.SetTablePrefix(s.prefix)
	schema := "main"
	if err := s.conn.Statement("PRAGMA writable_schema = 1"); err != nil {
		return err
	}
	if err := s.conn.Statement(g.CompileDropAllViews(schema)); err != nil {
		return err
	}
	if err := s.conn.Statement("PRAGMA writable_schema = 0"); err != nil {
		return err
	}
	return s.conn.Statement(g.CompileRebuild(schema))
}

// DropAllTypes 删除当前 schema 下全部用户自定义类型（仅 Postgres 支持，
// 其余驱动对应基类的 LogicException 语义）。
func (s *SchemaBuilder) DropAllTypes() error {
	d := s.dialect()
	if d != dialectPostgres {
		return fmt.Errorf("this database driver does not support dropping all types")
	}
	types, err := s.GetTypes()
	if err != nil {
		return err
	}
	if len(types) == 0 {
		return nil
	}
	names := make([]string, 0, len(types))
	for _, ty := range types {
		names = append(names, ty.Name)
	}
	return s.conn.Statement(compileDropAllTypesSQL(d, names))
}

// ---- 外键约束开关（Builder.php:622-649） ----

// EnableForeignKeyConstraints 启用外键约束。
func (s *SchemaBuilder) EnableForeignKeyConstraints() error {
	sql := compileForeignKeyConstraintsSQL(s.dialect(), true)
	return s.conn.Statement(sql)
}

// DisableForeignKeyConstraints 禁用外键约束。
func (s *SchemaBuilder) DisableForeignKeyConstraints() error {
	sql := compileForeignKeyConstraintsSQL(s.dialect(), false)
	return s.conn.Statement(sql)
}

// WithoutForeignKeyConstraints 在回调执行期间禁用外键约束（无论成败都恢复）。
func (s *SchemaBuilder) WithoutForeignKeyConstraints(fn func() error) error {
	if err := s.DisableForeignKeyConstraints(); err != nil {
		return err
	}
	defer func() { _ = s.EnableForeignKeyConstraints() }()
	return fn()
}

// ---- 列内省（Builder.php:270/:284/:363/:382/:393） ----

// HasColumn 列是否存在（大小写不敏感）。
func (s *SchemaBuilder) HasColumn(table, column string) (bool, error) {
	listing, err := s.GetColumnListing(table)
	if err != nil {
		return false, err
	}
	return containsFold(listing, column), nil
}

// HasColumns 全部列都存在才返回 true（对应 hasColumns 的 array_all 语义）。
func (s *SchemaBuilder) HasColumns(table string, columns []string) (bool, error) {
	listing, err := s.GetColumnListing(table)
	if err != nil {
		return false, err
	}
	for _, column := range columns {
		if !containsFold(listing, column) {
			return false, nil
		}
	}
	return true, nil
}

// GetColumnType 返回列的完整类型（对应 getColumnType 的 fullDefinition 分支）；
// 列不存在时报错（InvalidArgumentException 语义）。
func (s *SchemaBuilder) GetColumnType(table, column string) (string, error) {
	cols, err := s.GetColumns(table)
	if err != nil {
		return "", err
	}
	for _, col := range cols {
		if strings.EqualFold(col.Name, column) {
			return col.Type, nil
		}
	}
	return "", fmt.Errorf("there is no column with name '%s' on table '%s'", column, table)
}

// GetColumns 返回表列元数据（name/type_name/type/nullable/default/
// autoincrement/comment）。
func (s *SchemaBuilder) GetColumns(table string) ([]SchemaColumn, error) {
	schema, tbl, err := s.parseSchemaAndTable(table)
	if err != nil {
		return nil, err
	}
	d := s.dialect()
	sql, args := compileColumnsSQL(d, s.resolveSchema(schema), s.prefix+tbl)
	rows, err := s.conn.SelectMaps(sql, args)
	if err != nil {
		return nil, err
	}
	out := make([]SchemaColumn, 0, len(rows))
	switch d {
	case dialectSqlite:
		// 对应 SQLiteProcessor::processColumns（SQLiteProcessor.php:9-50）：
		// hasPrimaryKey = sum(primary) == 1（复合主键剔除自增判断）
		pkCount := 0
		for _, row := range rows {
			if schemaBool(row["primary"]) {
				pkCount++
			}
		}
		for _, row := range rows {
			typ := strings.ToLower(schemaStr(row["type"]))
			typeName := typ
			if i := strings.IndexByte(typ, '('); i >= 0 {
				typeName = typ[:i]
			}
			out = append(out, SchemaColumn{
				Name:          schemaStr(row["name"]),
				TypeName:      typeName,
				Type:          typ,
				Nullable:      schemaBool(row["nullable"]),
				Default:       row["default"],
				Comment:       "",
				AutoIncrement: pkCount == 1 && schemaBool(row["primary"]) && typeName == "integer",
			})
			// hidden（extra）2/3 为生成列、collation 提取依赖建表 SQL 正则
			// （compileSqlCreateStatement）——TODO 接线点（SchemaColumn 暂无对应字段）。
		}
		return out, nil
	case dialectSqlserver:
		// 对应 SqlServerProcessor::processColumns（SqlServerProcessor.php:56-84）：
		// type 由 type_name + length/precision/places 组装
		for _, row := range rows {
			typeName := schemaStr(row["type_name"])
			length := schemaInt64(row["length"])
			precision := schemaInt64(row["precision"])
			places := schemaInt64(row["places"])
			typ := typeName
			switch typeName {
			case "binary", "varbinary", "char", "varchar", "nchar", "nvarchar":
				if length == -1 {
					typ = typeName + "(max)"
				} else {
					typ = fmt.Sprintf("%s(%d)", typeName, length)
				}
			case "decimal", "numeric":
				typ = fmt.Sprintf("%s(%d,%d)", typeName, precision, places)
			case "float", "datetime2", "datetimeoffset", "time":
				typ = fmt.Sprintf("%s(%d)", typeName, precision)
			}
			out = append(out, SchemaColumn{
				Name:          schemaStr(row["name"]),
				TypeName:      typeName,
				Type:          typ,
				Nullable:      schemaBool(row["nullable"]),
				Default:       row["default"],
				Comment:       schemaStr(row["comment"]),
				AutoIncrement: schemaBool(row["autoincrement"]),
			})
		}
		return out, nil
	}
	for _, row := range rows {
		col := SchemaColumn{
			Name:          schemaStr(row["name"]),
			TypeName:      schemaStr(row["type_name"]),
			Type:          schemaStr(row["type"]),
			Nullable:      schemaNullableValue(row["nullable"]),
			Default:       row["default"],
			Comment:       schemaStr(row["comment"]),
			AutoIncrement: false,
		}
		switch d {
		case dialectPostgres:
			// 对应 PostgresProcessor::processColumns：default 以 nextval( 开头即自增
			def := schemaStr(row["default"])
			col.AutoIncrement = def != "" && strings.HasPrefix(def, "nextval(")
			if col.Default != nil && schemaStr(row["generated"]) != "" {
				col.Default = nil
			}
		default:
			// 对应 MySqlProcessor::processColumns：nullable === 'YES'、
			// extra === 'auto_increment'
			col.Nullable = strings.EqualFold(schemaStr(row["nullable"]), "YES")
			col.AutoIncrement = strings.EqualFold(schemaStr(row["extra"]), "auto_increment")
		}
		out = append(out, col)
	}
	return out, nil
}

// GetColumnListing 列名列表。
func (s *SchemaBuilder) GetColumnListing(table string) ([]string, error) {
	schema, tbl, err := s.parseSchemaAndTable(table)
	if err != nil {
		return nil, err
	}
	g := s.conn.GetSchemaGrammar()
	sql, bindings := g.CompileColumnListing(s.resolveSchema(schema), s.prefix+tbl)
	rows, err := s.conn.SelectMaps(sql, bindings)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if v, ok := row["column_name"]; ok {
			if str, ok := v.(string); ok {
				out = append(out, str)
			}
		}
	}
	return out, nil
}

// ---- 索引内省（Builder.php:412-460） ----

// GetIndexes 返回表索引元数据。
func (s *SchemaBuilder) GetIndexes(table string) ([]SchemaIndex, error) {
	schema, tbl, err := s.parseSchemaAndTable(table)
	if err != nil {
		return nil, err
	}
	d := s.dialect()
	sql, args := compileIndexesSQL(d, s.resolveSchema(schema), s.prefix+tbl)
	rows, err := s.conn.SelectMaps(sql, args)
	if err != nil {
		return nil, err
	}
	out := make([]SchemaIndex, 0, len(rows))
	switch d {
	case dialectSqlite:
		// 对应 SQLiteProcessor::processIndexes（SQLiteProcessor.php:52-75）：
		// primary 行计数 > 1 时剔除合成 'primary' 行（复合主键表会同时出现
		// 合成主键段与 origin='pk' 索引段）；type 为 null。
		primaryCount := 0
		for _, row := range rows {
			if schemaBool(row["primary"]) {
				primaryCount++
			}
		}
		for _, row := range rows {
			name := strings.ToLower(schemaStr(row["name"]))
			if primaryCount > 1 && name == "primary" {
				continue
			}
			out = append(out, SchemaIndex{
				Name:    name,
				Columns: splitCommaList(schemaStr(row["columns"])),
				Unique:  schemaBool(row["unique"]),
				Primary: schemaBool(row["primary"]),
			})
		}
		return out, nil
	case dialectSqlserver:
		// 对应 SqlServerProcessor::processIndexes：name/type 小写化
		for _, row := range rows {
			out = append(out, SchemaIndex{
				Name:    strings.ToLower(schemaStr(row["name"])),
				Columns: splitCommaList(schemaStr(row["columns"])),
				Type:    strings.ToLower(schemaStr(row["type"])),
				Unique:  schemaBool(row["unique"]),
				Primary: schemaBool(row["primary"]),
			})
		}
		return out, nil
	}
	for _, row := range rows {
		idx := SchemaIndex{
			Name:    schemaStr(row["name"]),
			Columns: splitCommaList(schemaStr(row["columns"])),
			Type:    schemaStr(row["type"]),
			Unique:  schemaBool(row["unique"]),
			Primary: schemaBool(row["primary"]),
		}
		if d == dialectMysql {
			// MySQL statistics 无主键标记：主键索引名固定为 PRIMARY
			// （对应 MySqlProcessor::processIndexes）
			idx.Primary = strings.EqualFold(idx.Name, "primary")
		}
		out = append(out, idx)
	}
	return out, nil
}

// GetIndexListing 返回索引名列表。
func (s *SchemaBuilder) GetIndexListing(table string) ([]string, error) {
	indexes, err := s.GetIndexes(table)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(indexes))
	for _, idx := range indexes {
		out = append(out, idx.Name)
	}
	return out, nil
}

// HasIndex 表是否拥有指定索引：index 可传索引名（string）或列集（[]string）；
// typ 可选 "primary"/"unique"/方言类型名（对应 hasIndex 的 $type 过滤）。
func (s *SchemaBuilder) HasIndex(table string, index any, typ ...string) (bool, error) {
	var typeFilter string
	if len(typ) > 0 {
		typeFilter = strings.ToLower(typ[0])
	}
	indexes, err := s.GetIndexes(table)
	if err != nil {
		return false, err
	}
	for _, idx := range indexes {
		typeMatches := typeFilter == "" ||
			(typeFilter == "primary" && idx.Primary) ||
			(typeFilter == "unique" && idx.Unique) ||
			typeFilter == strings.ToLower(idx.Type)
		if !typeMatches {
			continue
		}
		switch v := index.(type) {
		case string:
			if v == idx.Name {
				return true, nil
			}
		case []string:
			if equalStringSlice(v, idx.Columns) {
				return true, nil
			}
		default:
			return false, fmt.Errorf("hasIndex: index must be a string name or a []string column set")
		}
	}
	return false, nil
}

// ---- 外键内省（Builder.php:469/:486） ----

// GetForeignKeys 返回表外键元数据。
func (s *SchemaBuilder) GetForeignKeys(table string) ([]SchemaForeignKey, error) {
	schema, tbl, err := s.parseSchemaAndTable(table)
	if err != nil {
		return nil, err
	}
	d := s.dialect()
	sql, args := compileForeignKeysSQL(d, s.resolveSchema(schema), s.prefix+tbl)
	rows, err := s.conn.SelectMaps(sql, args)
	if err != nil {
		return nil, err
	}
	out := make([]SchemaForeignKey, 0, len(rows))
	for _, row := range rows {
		onUpdate := pgForeignKeyAction(row["on_update"], d)
		onDelete := pgForeignKeyAction(row["on_delete"], d)
		switch d {
		case dialectSqlite:
			// 对应 SQLiteProcessor::processForeignKeys：动作值小写化
			onUpdate = strings.ToLower(onUpdate)
			onDelete = strings.ToLower(onDelete)
		case dialectSqlserver:
			// 对应 SqlServerProcessor::processForeignKeys：NO_ACTION → no action
			// （下划线转空格 + 小写；不做额外语义映射）
			onUpdate = strings.ToLower(strings.ReplaceAll(onUpdate, "_", " "))
			onDelete = strings.ToLower(strings.ReplaceAll(onDelete, "_", " "))
		}
		out = append(out, SchemaForeignKey{
			Name:           schemaStr(row["name"]),
			Columns:        splitCommaList(schemaStr(row["columns"])),
			ForeignSchema:  schemaStr(row["foreign_schema"]),
			ForeignTable:   schemaStr(row["foreign_table"]),
			ForeignColumns: splitCommaList(schemaStr(row["foreign_columns"])),
			OnUpdate:       onUpdate,
			OnDelete:       onDelete,
		})
	}
	return out, nil
}

// HasForeignKey 表是否拥有指定外键：foreignKey 可传外键名（string）或列集
// （[]string）。
func (s *SchemaBuilder) HasForeignKey(table string, foreignKey any) (bool, error) {
	keys, err := s.GetForeignKeys(table)
	if err != nil {
		return false, err
	}
	for _, fk := range keys {
		switch v := foreignKey.(type) {
		case string:
			if v == fk.Name {
				return true, nil
			}
		case []string:
			if equalStringSlice(v, fk.Columns) {
				return true, nil
			}
		default:
			return false, fmt.Errorf("hasForeignKey: foreignKey must be a string name or a []string column set")
		}
	}
	return false, nil
}

// ---- 表 / 视图 / 类型内省（Builder.php:194-261） ----

// GetTables 返回当前连接的表元数据（结果过滤视图；可传 schema 收窄范围）。
func (s *SchemaBuilder) GetTables(schema ...string) ([]SchemaTable, error) {
	d := s.dialect()
	sql, args := compileTablesSQL(d, firstString(schema))
	rows, err := s.conn.SelectMaps(sql, args)
	if err != nil {
		return nil, err
	}
	out := make([]SchemaTable, 0, len(rows))
	for _, row := range rows {
		out = append(out, SchemaTable{
			Name:      schemaStr(row["name"]),
			Schema:    schemaStr(row["schema"]),
			Size:      schemaInt64(row["size"]),
			Comment:   schemaStr(row["comment"]),
			Engine:    schemaStr(row["engine"]),
			Collation: schemaStr(row["collation"]),
		})
	}
	return out, nil
}

// GetTableListing 返回当前连接的表名列表（不含视图）。
func (s *SchemaBuilder) GetTableListing(schema ...string) ([]string, error) {
	tables, err := s.GetTables(schema...)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(tables))
	for _, table := range tables {
		out = append(out, table.Name)
	}
	return out, nil
}

// GetViews 返回视图元数据（可传 schema 收窄范围）。
func (s *SchemaBuilder) GetViews(schema ...string) ([]SchemaView, error) {
	d := s.dialect()
	sql, args := compileViewsSQL(d, firstString(schema))
	rows, err := s.conn.SelectMaps(sql, args)
	if err != nil {
		return nil, err
	}
	out := make([]SchemaView, 0, len(rows))
	for _, row := range rows {
		out = append(out, SchemaView{
			Name:       schemaStr(row["name"]),
			Schema:     schemaStr(row["schema"]),
			Definition: schemaStr(row["definition"]),
		})
	}
	return out, nil
}

// HasView 视图是否存在（大小写不敏感，引用支持 schema.table 拆分）。
func (s *SchemaBuilder) HasView(view string) (bool, error) {
	schema, name, err := s.parseSchemaAndTable(view)
	if err != nil {
		return false, err
	}
	views, err := s.GetViews()
	if err != nil {
		return false, err
	}
	target := s.prefix + name
	for _, v := range views {
		if schema != "" && !strings.EqualFold(v.Schema, schema) {
			continue
		}
		if strings.EqualFold(v.Name, target) {
			return true, nil
		}
	}
	return false, nil
}

// GetTypes 返回用户自定义类型元数据（仅 Postgres；其余驱动不支持）。
func (s *SchemaBuilder) GetTypes(schema ...string) ([]SchemaType, error) {
	d := s.dialect()
	if d != dialectPostgres {
		return nil, fmt.Errorf("this database driver does not support user-defined types")
	}
	schemaClause, extra := schemaWhereClause(d, schemaTableName(d), firstString(schema))
	sql, args := compileTypesSQL(d, schemaClause, extra)
	rows, err := s.conn.SelectMaps(sql, args)
	if err != nil {
		return nil, err
	}
	out := make([]SchemaType, 0, len(rows))
	for _, row := range rows {
		out = append(out, SchemaType{
			Name:     schemaStr(row["name"]),
			Schema:   schemaStr(row["schema"]),
			Type:     schemaStr(row["type"]),
			Category: schemaStr(row["category"]),
			Implicit: schemaBool(row["implicit"]),
		})
	}
	return out, nil
}

// ---- 条件表构建（Builder.php:299-351） ----

// WhenTableHasColumn 表拥有指定列时执行表构建回调。
func (s *SchemaBuilder) WhenTableHasColumn(table, column string, fn func(t *grammar.Blueprint)) error {
	ok, err := s.HasColumn(table, column)
	if err != nil || !ok {
		return err
	}
	return s.Table(table, fn)
}

// WhenTableDoesntHaveColumn 表缺少指定列时执行表构建回调。
func (s *SchemaBuilder) WhenTableDoesntHaveColumn(table, column string, fn func(t *grammar.Blueprint)) error {
	ok, err := s.HasColumn(table, column)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	return s.Table(table, fn)
}

// WhenTableHasIndex 表拥有指定索引（名称或列集，可选类型过滤）时执行表构建回调。
func (s *SchemaBuilder) WhenTableHasIndex(table string, index any, fn func(t *grammar.Blueprint), typ ...string) error {
	ok, err := s.HasIndex(table, index, typ...)
	if err != nil || !ok {
		return err
	}
	return s.Table(table, fn)
}

// WhenTableDoesntHaveIndex 表缺少指定索引时执行表构建回调。
func (s *SchemaBuilder) WhenTableDoesntHaveIndex(table string, index any, fn func(t *grammar.Blueprint), typ ...string) error {
	ok, err := s.HasIndex(table, index, typ...)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	return s.Table(table, fn)
}

// ---- 引用解析（Builder.php:752 parseSchemaAndTable） ----

// parseSchemaAndTable 拆分 "schema.table" 引用；超过两段报错
// （InvalidArgumentException 语义）。无显式 schema 时返回空串，由调用方用
// resolveSchema 回退到默认 schema。
func (s *SchemaBuilder) parseSchemaAndTable(reference string) (schema, table string, err error) {
	segments := strings.Split(reference, ".")
	if len(segments) > 2 {
		return "", "", fmt.Errorf(
			"using three-part references is not supported, you may use `Schema::connection('%s')` instead",
			segments[0])
	}
	table = segments[0]
	if len(segments) == 2 {
		schema = segments[0]
		table = segments[1]
	}
	return schema, table, nil
}

// resolveSchema 返回显式 schema 或连接默认 schema。
func (s *SchemaBuilder) resolveSchema(schema string) string {
	if schema != "" {
		return schema
	}
	return s.currentSchemaName()
}

// currentSchemaName 默认 schema（对应 getCurrentSchemaName）：Postgres 取
// search_path 首段（Config.SearchPath 经连接器注入 DSN；连接未保留原始 Config，
// 故从 DSN 解析），SQLite 取附加库名 main（无显式 schema 时缺省 'main'），
// 其余方言取库名。
func (s *SchemaBuilder) currentSchemaName() string {
	if s.dialect() == dialectPostgres {
		if sp := dsnSearchPath(s.conn.Dsn()); len(sp) > 0 {
			return sp[0]
		}
		return "public"
	}
	if s.dialect() == dialectSqlite {
		return "main"
	}
	return s.databaseName()
}

// databaseName 从 DSN 解析库名：libpq 关键字形式（host=... dbname=...）解析
// dbname=；URL / MySQL 形式按 '/' 硬切。
func (s *SchemaBuilder) databaseName() string {
	dsn := s.conn.Dsn()
	if v := dsnKeywordParam(dsn, "dbname"); v != "" {
		return v
	}
	if i := strings.Index(dsn, "?"); i >= 0 {
		dsn = dsn[:i]
	}
	if i := strings.LastIndex(dsn, "/"); i >= 0 {
		dsn = dsn[i+1:]
	}
	if dsn == "" {
		return "public"
	}
	return dsn
}

// SetDsn 内部接线（Open 后由 Connection 注入）。
func (c *Connection) Dsn() string { return c.dsn }

// ---- 方言判定与 DSN 解析 ----

type schemaDialect int

const (
	dialectMysql schemaDialect = iota
	dialectPostgres
	dialectSqlite
	dialectSqlserver
)

// dialect 连接方言（fake 等未注册驱动按 MySQL 语义处理）。
func (s *SchemaBuilder) dialect() schemaDialect {
	switch strings.ToLower(s.conn.driver) {
	case "postgres", "postgresql", "pgsql":
		return dialectPostgres
	case "sqlite", "sqlite3":
		return dialectSqlite
	case "sqlserver", "mssql":
		return dialectSqlserver
	default:
		return dialectMysql
	}
}

// dsnKeywordParam 从 libpq 关键字形式 DSN（空格分隔的 k=v 对）取参数值。
func dsnKeywordParam(dsn, key string) string {
	if !strings.Contains(dsn, " ") {
		return ""
	}
	for _, field := range strings.Fields(dsn) {
		if k, v, ok := strings.Cut(field, "="); ok && strings.EqualFold(k, key) {
			return strings.Trim(v, "'\"")
		}
	}
	return ""
}

// dsnSearchPath 解析 DSN 中的 search_path（URL 查询串 ?search_path= 或关键字
// 形式），剔除空段与 $user（$user→username 映射待 Connection 保留 Config 后接入）。
func dsnSearchPath(dsn string) []string {
	var raw string
	if i := strings.IndexByte(dsn, '?'); i >= 0 {
		for _, pair := range strings.Split(dsn[i+1:], "&") {
			if k, v, ok := strings.Cut(pair, "="); ok && strings.EqualFold(k, "search_path") {
				raw = v
				break
			}
		}
	}
	if raw == "" {
		raw = dsnKeywordParam(dsn, "search_path")
	}
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.Trim(strings.TrimSpace(part), "\"")
		if part == "" || part == "$user" {
			continue
		}
		out = append(out, part)
	}
	return out
}

// ---- 内省 SQL 编译（本地顶替实现，待 grammar 侧迁移） ----

// schemaTableName schema 过滤子句用到的表/列名字（按方言限定）。
func schemaTableName(d schemaDialect) string {
	if d == dialectPostgres {
		return "n.nspname"
	}
	return "table_schema"
}

// schemaWhereClause 对应 compileSchemaWhereClause：显式 schema 绑定
// 参数；未指定时过滤系统 schema。
func schemaWhereClause(d schemaDialect, column, schema string) (clause string, args []any) {
	if schema != "" {
		return column + " = ?", []any{schema}
	}
	switch d {
	case dialectPostgres:
		return column + " <> 'information_schema' and " + column + " not like 'pg\\_%'", nil
	default:
		return column + " not in ('information_schema', 'mysql', 'ndbinfo', 'performance_schema', 'sys')", nil
	}
}

// compileCreateDatabaseSQL 对应 Grammar::compileCreateDatabase（charset/collation
// 附加段依赖 Config，待 grammar/Connection 接线后补齐）。
func compileCreateDatabaseSQL(d schemaDialect, name string) (string, []any) {
	return "create database " + quoteIdentifier(d, name), nil
}

// compileDropDatabaseIfExistsSQL 对应 Grammar::compileDropDatabaseIfExists。
func compileDropDatabaseIfExistsSQL(d schemaDialect, name string) (string, []any) {
	return "drop database if exists " + quoteIdentifier(d, name), nil
}

// compileForeignKeyConstraintsSQL 对应各方言
// compileEnable/DisableForeignKeyConstraints。
func compileForeignKeyConstraintsSQL(d schemaDialect, enable bool) string {
	switch d {
	case dialectPostgres:
		if enable {
			return "SET CONSTRAINTS ALL IMMEDIATE;"
		}
		return "SET CONSTRAINTS ALL DEFERRED;"
	case dialectSqlite:
		if enable {
			return "PRAGMA foreign_keys = ON;"
		}
		return "PRAGMA foreign_keys = OFF;"
	case dialectSqlserver:
		// 对齐 SqlServerGrammar.php:515-530：enable 带 @command1="print '?'"，
		// disable 的命令串用双引号包裹
		if enable {
			return `EXEC sp_msforeachtable @command1="print '?'", @command2="ALTER TABLE ? WITH CHECK CHECK CONSTRAINT all";`
		}
		return `EXEC sp_msforeachtable "ALTER TABLE ? NOCHECK CONSTRAINT all";`
	default:
		if enable {
			return "SET FOREIGN_KEY_CHECKS=1;"
		}
		return "SET FOREIGN_KEY_CHECKS=0;"
	}
}

// compileDropAllTablesSQL 对应 compileDropAllTables（PG 追加 cascade）。
func compileDropAllTablesSQL(d schemaDialect, tables []string) string {
	return joinWrapped(d, "drop table", tables, d == dialectPostgres)
}

// compileDropAllViewsSQL 对应 compileDropAllViews（PG 追加 cascade）。
func compileDropAllViewsSQL(d schemaDialect, views []string) string {
	return joinWrapped(d, "drop view", views, d == dialectPostgres)
}

// compileDropAllTypesSQL 对应 compileDropAllTypes（仅 Postgres）。
func compileDropAllTypesSQL(d schemaDialect, types []string) string {
	return joinWrapped(d, "drop type", types, true)
}

func joinWrapped(d schemaDialect, prefix string, names []string, cascade bool) string {
	wrapped := make([]string, 0, len(names))
	for _, name := range names {
		wrapped = append(wrapped, quoteIdentifier(d, name))
	}
	sql := prefix + " " + strings.Join(wrapped, ", ")
	if cascade {
		sql += " cascade"
	}
	return sql
}

// quoteIdentifier 标识符按方言引号包裹（待 grammar 提供通用 wrap 后迁移）。
func quoteIdentifier(d schemaDialect, name string) string {
	switch d {
	case dialectMysql:
		return "`" + name + "`"
	case dialectSqlserver:
		return "[" + name + "]"
	default:
		return `"` + name + `"`
	}
}

// compileColumnsSQL 对应 MySqlGrammar / PostgresGrammar::compileColumns；
// SQLite / SQL Server 委托 grammar 侧方言方法（schema_sqlite_introspect.go /
// schema_sqlserver_introspect.go）。
func compileColumnsSQL(d schemaDialect, schema, table string) (string, []any) {
	switch d {
	case dialectSqlite:
		return grammar.NewSqliteQueryGrammar().CompileColumns(schema, table)
	case dialectSqlserver:
		return grammar.NewSqlServerGrammar().CompileColumns(schema, table)
	case dialectPostgres:
		return `select a.attname as "name", t.typname as "type_name", ` +
			`format_type(a.atttypid, a.atttypmod) as "type", not a.attnotnull as "nullable", ` +
			`(select pg_get_expr(adbin, adrelid) from pg_attrdef where c.oid = pg_attrdef.adrelid and pg_attrdef.adnum = a.attnum) as "default", ` +
			`'' as "generated", col_description(c.oid, a.attnum) as "comment" ` +
			`from pg_attribute a, pg_class c, pg_type t, pg_namespace n ` +
			`where n.nspname = ? and c.relname = ? and a.attnum > 0 and a.attrelid = c.oid and a.atttypid = t.oid and n.oid = c.relnamespace ` +
			`order by a.attnum`, []any{schema, table}
	default:
		return "select column_name as `name`, data_type as `type_name`, column_type as `type`, " +
			"is_nullable as `nullable`, column_default as `default`, column_comment as `comment`, extra as `extra` " +
			"from information_schema.columns where table_schema = ? and table_name = ? " +
			"order by ordinal_position asc", []any{schema, table}
	}
}

// compileIndexesSQL 对应 MySqlGrammar / PostgresGrammar::compileIndexes；
// SQLite / SQL Server 委托 grammar 侧方言方法。
func compileIndexesSQL(d schemaDialect, schema, table string) (string, []any) {
	switch d {
	case dialectSqlite:
		return grammar.NewSqliteQueryGrammar().CompileIndexes(schema, table)
	case dialectSqlserver:
		return grammar.NewSqlServerGrammar().CompileIndexes(schema, table)
	case dialectPostgres:
		return `select ic.relname as "name", string_agg(a.attname, ',' order by indseq.ord) as "columns", ` +
			`am.amname as "type", i.indisunique as "unique", i.indisprimary as "primary" ` +
			`from pg_index i ` +
			`join pg_class tc on tc.oid = i.indrelid ` +
			`join pg_namespace tn on tn.oid = tc.relnamespace ` +
			`join pg_class ic on ic.oid = i.indexrelid ` +
			`join pg_am am on am.oid = ic.relam ` +
			`join lateral unnest(i.indkey) with ordinality as indseq(num, ord) on true ` +
			`left join pg_attribute a on a.attrelid = i.indrelid and a.attnum = indseq.num ` +
			`where tn.nspname = ? and tc.relname = ? ` +
			`group by ic.relname, am.amname, i.indisunique, i.indisprimary`, []any{schema, table}
	default:
		return "select index_name as `name`, group_concat(column_name order by seq_in_index) as `columns`, " +
			"index_type as `type`, not non_unique as `unique` " +
			"from information_schema.statistics where table_schema = ? and table_name = ? " +
			"group by index_name, index_type, non_unique", []any{schema, table}
	}
}

// compileForeignKeysSQL 对应 MySqlGrammar / PostgresGrammar::compileForeignKeys；
// SQLite / SQL Server 委托 grammar 侧方言方法。
func compileForeignKeysSQL(d schemaDialect, schema, table string) (string, []any) {
	switch d {
	case dialectSqlite:
		return grammar.NewSqliteQueryGrammar().CompileForeignKeys(schema, table)
	case dialectSqlserver:
		return grammar.NewSqlServerGrammar().CompileForeignKeys(schema, table)
	case dialectPostgres:
		return `select c.conname as "name", ` +
			`string_agg(la.attname, ',' order by conseq.ord) as "columns", ` +
			`fn.nspname as "foreign_schema", fc.relname as "foreign_table", ` +
			`string_agg(fa.attname, ',' order by conseq.ord) as "foreign_columns", ` +
			`c.confupdtype as "on_update", c.confdeltype as "on_delete" ` +
			`from pg_constraint c ` +
			`join pg_class tc on c.conrelid = tc.oid ` +
			`join pg_namespace tn on tn.oid = tc.relnamespace ` +
			`join pg_class fc on c.confrelid = fc.oid ` +
			`join pg_namespace fn on fn.oid = fc.relnamespace ` +
			`join lateral unnest(c.conkey) with ordinality as conseq(num, ord) on true ` +
			`join pg_attribute la on la.attrelid = c.conrelid and la.attnum = conseq.num ` +
			`join pg_attribute fa on fa.attrelid = c.confrelid and fa.attnum = c.confkey[conseq.ord] ` +
			`where c.contype = 'f' and tn.nspname = ? and tc.relname = ? ` +
			`group by c.conname, fn.nspname, fc.relname, c.confupdtype, c.confdeltype`, []any{schema, table}
	default:
		return "select kc.constraint_name as `name`, " +
				"group_concat(kc.column_name order by kc.ordinal_position) as `columns`, " +
				"kc.referenced_table_schema as `foreign_schema`, " +
				"kc.referenced_table_name as `foreign_table`, " +
				"group_concat(kc.referenced_column_name order by kc.ordinal_position) as `foreign_columns`, " +
				"rc.update_rule as `on_update`, rc.delete_rule as `on_delete` " +
				"from information_schema.key_column_usage kc join information_schema.referential_constraints rc " +
				"on kc.constraint_schema = rc.constraint_schema and kc.constraint_name = rc.constraint_name " +
				"where kc.table_schema = ? and kc.table_name = ? and kc.referenced_table_name is not null " +
				"group by kc.constraint_name, kc.referenced_table_schema, kc.referenced_table_name, rc.update_rule, rc.delete_rule",
			[]any{schema, table}
	}
}

// compileTablesSQL 对应 MySqlGrammar / PostgresGrammar::compileTables；
// SQLite / SQL Server 委托 grammar 侧方言方法（schema 为空表示不过滤 schema）。
func compileTablesSQL(d schemaDialect, schema string) (string, []any) {
	switch d {
	case dialectSqlite:
		return grammar.NewSqliteQueryGrammar().CompileTables(schema)
	case dialectSqlserver:
		return grammar.NewSqlServerGrammar().CompileTables(schema)
	case dialectPostgres:
		clause, args := schemaWhereClause(d, "n.nspname", schema)
		return "select c.relname as \"name\", n.nspname as \"schema\", pg_total_relation_size(c.oid) as \"size\", " +
			"obj_description(c.oid, 'pg_class') as \"comment\" from pg_class c, pg_namespace n " +
			"where c.relkind in ('r', 'p') and n.oid = c.relnamespace and " + clause +
			" order by n.nspname, c.relname", args
	default:
		clause, args := schemaWhereClause(d, "table_schema", schema)
		return "select table_name as `name`, table_schema as `schema`, (data_length + index_length) as `size`, " +
			"table_comment as `comment`, engine as `engine`, table_collation as `collation` " +
			"from information_schema.tables where table_type in ('BASE TABLE', 'SYSTEM VERSIONED') and " +
			clause + " order by table_schema, table_name", args
	}
}

// compileViewsSQL 对应 MySqlGrammar / PostgresGrammar::compileViews；
// SQLite / SQL Server 委托 grammar 侧方言方法。
func compileViewsSQL(d schemaDialect, schema string) (string, []any) {
	switch d {
	case dialectSqlite:
		return grammar.NewSqliteQueryGrammar().CompileViews(schema)
	case dialectSqlserver:
		return grammar.NewSqlServerGrammar().CompileViews(schema)
	case dialectPostgres:
		clause, args := schemaWhereClause(d, "schemaname", schema)
		return "select viewname as \"name\", schemaname as \"schema\", definition from pg_views where " +
			clause + " order by schemaname, viewname", args
	default:
		clause, args := schemaWhereClause(d, "table_schema", schema)
		return "select table_name as `name`, table_schema as `schema`, view_definition as `definition` " +
			"from information_schema.views where " + clause +
			" order by table_schema, table_name", args
	}
}

// compileTypesSQL 对应 PostgresGrammar::compileTypes（仅 Postgres 调用）。
func compileTypesSQL(d schemaDialect, schemaClause string, args []any) (string, []any) {
	return "select t.typname as \"name\", n.nspname as \"schema\", t.typtype as \"type\", t.typcategory as \"category\", " +
		"((t.typinput = 'array_in'::regproc and t.typoutput = 'array_out'::regproc) or t.typtype = 'm') as \"implicit\" " +
		"from pg_type t join pg_namespace n on n.oid = t.typnamespace " +
		"left join pg_class c on c.oid = t.typrelid " +
		"left join pg_type el on el.oid = t.typelem " +
		"left join pg_class ce on ce.oid = el.typrelid " +
		"where ((t.typrelid = 0 and (ce.relkind = 'c' or ce.relkind is null)) or c.relkind = 'c') " +
		"and not exists (select 1 from pg_depend d where d.objid in (t.oid, t.typelem) and d.deptype = 'e') and " +
		schemaClause, args
}

// ---- 行值处理辅助 ----

// pgForeignKeyAction 对应 PostgresProcessor 的约束动作码映射；非 PG 原样返回。
func pgForeignKeyAction(v any, d schemaDialect) string {
	raw := schemaStr(v)
	if d != dialectPostgres {
		return raw
	}
	switch strings.ToLower(raw) {
	case "a":
		return "no action"
	case "r":
		return "restrict"
	case "c":
		return "cascade"
	case "n":
		return "set null"
	case "d":
		return "set default"
	default:
		return raw
	}
}

// schemaNullableValue nullable 列值归一化：MySQL 'YES'/'NO'、PG bool/t-f 文本、
// 驱动 bool / 0-1 整数。
func schemaNullableValue(v any) bool {
	return schemaBool(v) || strings.EqualFold(schemaStr(v), "YES")
}

func schemaStr(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	default:
		return fmt.Sprint(x)
	}
}

func schemaBool(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case int64:
		return x != 0
	case int:
		return x != 0
	case []byte:
		return parseBoolText(string(x))
	case string:
		return parseBoolText(x)
	default:
		return false
	}
}

func parseBoolText(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "t", "true", "yes", "1":
		return true
	}
	return false
}

func schemaInt64(v any) int64 {
	switch x := v.(type) {
	case nil:
		return 0
	case int64:
		return x
	case int:
		return int64(x)
	case []byte:
		n, _ := strconv.ParseInt(string(x), 10, 64)
		return n
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	default:
		return 0
	}
}

// splitCommaList 还原 group_concat / string_agg 聚合出的列集。
func splitCommaList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsFold(list []string, target string) bool {
	for _, item := range list {
		if strings.EqualFold(item, target) {
			return true
		}
	}
	return false
}

func firstString(list []string) string {
	if len(list) > 0 {
		return list[0]
	}
	return ""
}
