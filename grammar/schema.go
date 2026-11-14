package grammar

import (
	"strings"
)

// ColumnDefinition 为单列的 Schema 定义。
// 字段为导出结构体字段；fluent 修饰符方法见下方（与字段同名的方法在 Go 中
// 不允许共存，故部分 setter 以 Set 前缀命名）。
type ColumnDefinition struct {
	Name            string
	Type            string // int / bigint / varchar / char / text / tinyint / decimal / timestamp / datetime / date / json / jsonb / serial / bigserial / boolean / enum / set / ...
	Length          int    // varchar / char / binary
	Precision       int    // decimal / float / time / timestamp / datetime
	Scale           int
	IsUnsigned      bool
	IsNullable      bool
	NotNull         bool
	DefaultValue    any // nil 未设置；数值/字符串字面量
	DefaultRaw      string
	IsAutoIncrement bool
	PrimaryKey      bool
	CommentText     string
	From            int64 // 自增起始值（MySQL）

	// ---- 修饰符（对齐 ColumnDefinition fluent 属性） ----
	Allowed            []string // enum / set 允许值
	UseCurrent         bool     // default CURRENT_TIMESTAMP（timestamp/datetime/date 家族）
	UseCurrentOnUpdate bool     // on update CURRENT_TIMESTAMP（MySQL）
	OnUpdate           string   // on update <expr>（MySQL，原始表达式，优先于 UseCurrentOnUpdate）
	StoredAs           string   // 生成列：as (expr) stored
	VirtualAs          string   // 生成列：as (expr)
	GeneratedAs        string   // PG generated always as (expr) stored
	Charset            string   // MySQL 列级 character set
	Collation          string   // 列级 collate
	First              bool     // MySQL first
	After              string   // MySQL after <col>
	Srid               int      // geometry srid
	Invisible          bool     // MySQL invisible
	Persisted          bool     // SQLServer/PG 生成列 persisted
	IsChange           bool     // alter 时标记为 change 命令
	Fixed              bool     // binary 定长（binary(n) vs varbinary(n)）
	Subtype            string   // geometry/geography 子类型
	Dimensions         int      // vector 维度
}

// IndexDefinition 为索引定义。
type IndexDefinition struct {
	Type    string // index / unique / primary / foreign / fulltext / spatialindex / vectorindex
	Name    string
	Columns []string
	// rawIndex：原始表达式列（编译时不再 wrap）
	Expression string
	// 索引算法 / 操作符类（PG using / using gin 等）
	Algorithm     string
	OperatorClass string
	// PG fulltext 语言（默认 english）
	Language string
	// 外键
	ReferenceColumn  string // 单列（兼容旧字段）
	ReferenceColumns []string
	OnTable          string
	OnDelete         string
	OnUpdate         string
}

// Reference 外键目标列（支持复合外键，取全部列）。
func (i *IndexDefinition) Reference(cols ...string) *IndexDefinition {
	i.ReferenceColumns = cols
	i.ReferenceColumn = cols[0]
	return i
}

// On 外键目标表。
func (i *IndexDefinition) On(table string) *IndexDefinition {
	i.OnTable = table
	return i
}

// CascadeOnDelete 级联删除。
func (i *IndexDefinition) CascadeOnDelete() *IndexDefinition {
	i.OnDelete = "cascade"
	return i
}

// RestrictOnDelete 限制删除。
func (i *IndexDefinition) RestrictOnDelete() *IndexDefinition {
	i.OnDelete = "restrict"
	return i
}

// SetNullOnDelete 删除时置空。
func (i *IndexDefinition) SetNullOnDelete() *IndexDefinition {
	i.OnDelete = "set null"
	return i
}

// NoActionOnDelete 无动作（NO ACTION）删除。
func (i *IndexDefinition) NoActionOnDelete() *IndexDefinition {
	i.OnDelete = "no action"
	return i
}

// CascadeOnUpdate 级联更新。
func (i *IndexDefinition) CascadeOnUpdate() *IndexDefinition {
	i.OnUpdate = "cascade"
	return i
}

// RestrictOnUpdate 限制更新。
func (i *IndexDefinition) RestrictOnUpdate() *IndexDefinition {
	i.OnUpdate = "restrict"
	return i
}

// NullOnUpdate 更新时置空。
func (i *IndexDefinition) NullOnUpdate() *IndexDefinition {
	i.OnUpdate = "set null"
	return i
}

// NoActionOnUpdate 无动作（NO ACTION）更新。
func (i *IndexDefinition) NoActionOnUpdate() *IndexDefinition {
	i.OnUpdate = "no action"
	return i
}

// Command 为一条 DDL 命令（dropColumn / renameColumn / dropIndex 等）。
type Command struct {
	Name    string // dropColumn / renameColumn / rename / dropPrimary / dropUnique / dropIndex / dropFullText / dropSpatialIndex / dropVectorIndex / dropForeign / renameIndex / drop / dropIfExists
	Columns []string
	From    string
	To      string
	Index   string
	Comment string
}

// Blueprint 为一张表的 Schema 定义（建表与变更命令的载体）。
type Blueprint struct {
	Table     string
	Create    bool
	Temporary bool
	Columns   []*ColumnDefinition
	Indexes   []*IndexDefinition
	Commands  []*Command // drop / dropIfExists / dropColumn / rename 等
	// MySQL 专有
	Engine    string
	Charset   string
	Collation string
}

func NewBlueprint(table string) *Blueprint {
	return &Blueprint{Table: table}
}

// CreateIndexName 对应 Blueprint::createIndexName（:1826-1839）：table_cols_type，
// 全小写，'-'/'.' 替换为 '_'，不做长度截断。
func (b *Blueprint) CreateIndexName(kind string, columns []string) string {
	index := strings.ToLower(b.Table + "_" + strings.Join(columns, "_") + "_" + kind)
	return strings.NewReplacer("-", "_", ".", "_").Replace(index)
}

// ---- 流式列定义 ----

func (b *Blueprint) addColumn(def *ColumnDefinition) *ColumnDefinition {
	b.Columns = append(b.Columns, def)
	return def
}

// ID 自增主键（bigIncrements）。
func (b *Blueprint) ID(name ...string) *ColumnDefinition {
	n := "id"
	if len(name) > 0 {
		n = name[0]
	}
	return b.BigIncrements(n)
}

// BigIncrements bigint 自增主键。
func (b *Blueprint) BigIncrements(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "bigint", IsUnsigned: true, IsNullable: false, IsAutoIncrement: true, PrimaryKey: true})
}

// Increments int 自增主键。
func (b *Blueprint) Increments(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "int", IsUnsigned: true, IsNullable: false, IsAutoIncrement: true, PrimaryKey: true})
}

// IntegerIncrements int 自增主键（同 Increments，即 integerIncrements）。
func (b *Blueprint) IntegerIncrements(name string) *ColumnDefinition {
	return b.Increments(name)
}

// TinyIncrements tinyint 自增主键。
func (b *Blueprint) TinyIncrements(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "tinyint", IsUnsigned: true, IsNullable: false, IsAutoIncrement: true, PrimaryKey: true})
}

// SmallIncrements smallint 自增主键。
func (b *Blueprint) SmallIncrements(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "smallint", IsUnsigned: true, IsNullable: false, IsAutoIncrement: true, PrimaryKey: true})
}

// MediumIncrements mediumint 自增主键。
func (b *Blueprint) MediumIncrements(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "mediumint", IsUnsigned: true, IsNullable: false, IsAutoIncrement: true, PrimaryKey: true})
}

// String varchar。
func (b *Blueprint) String(name string, length ...int) *ColumnDefinition {
	l := 255
	if len(length) > 0 {
		l = length[0]
	}
	return b.addColumn(&ColumnDefinition{Name: name, Type: "varchar", Length: l})
}

// Char 定长字符串。
func (b *Blueprint) Char(name string, length ...int) *ColumnDefinition {
	l := 255
	if len(length) > 0 {
		l = length[0]
	}
	return b.addColumn(&ColumnDefinition{Name: name, Type: "char", Length: l})
}

// TinyText tinytext。
func (b *Blueprint) TinyText(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "tinytext"})
}

// Text 长文本。
func (b *Blueprint) Text(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "text"})
}

// MediumText mediumtext。
func (b *Blueprint) MediumText(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "mediumtext"})
}

// LongText longtext。
func (b *Blueprint) LongText(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "longtext"})
}

// Integer int。
func (b *Blueprint) Integer(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "int"})
}

// TinyInteger tinyint。
func (b *Blueprint) TinyInteger(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "tinyint"})
}

// SmallInteger smallint。
func (b *Blueprint) SmallInteger(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "smallint"})
}

// MediumInteger mediumint。
func (b *Blueprint) MediumInteger(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "mediumint"})
}

// BigInteger bigint。
func (b *Blueprint) BigInteger(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "bigint"})
}

// UnsignedInteger int unsigned。
func (b *Blueprint) UnsignedInteger(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "int", IsUnsigned: true})
}

// UnsignedTinyInteger tinyint unsigned。
func (b *Blueprint) UnsignedTinyInteger(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "tinyint", IsUnsigned: true})
}

// UnsignedSmallInteger smallint unsigned。
func (b *Blueprint) UnsignedSmallInteger(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "smallint", IsUnsigned: true})
}

// UnsignedMediumInteger mediumint unsigned。
func (b *Blueprint) UnsignedMediumInteger(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "mediumint", IsUnsigned: true})
}

// UnsignedBigInteger bigint unsigned。
func (b *Blueprint) UnsignedBigInteger(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "bigint", IsUnsigned: true})
}

// Float 单精度浮点（precision 可选，缺省 53）。
func (b *Blueprint) Float(name string, precision ...int) *ColumnDefinition {
	p := 0
	if len(precision) > 0 {
		p = precision[0]
	}
	return b.addColumn(&ColumnDefinition{Name: name, Type: "float", Precision: p})
}

// Double 双精度浮点。
func (b *Blueprint) Double(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "double"})
}

// Decimal 定点数。
func (b *Blueprint) Decimal(name string, precision, scale int) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "decimal", Precision: precision, Scale: scale})
}

// Boolean 布尔。
func (b *Blueprint) Boolean(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "boolean"})
}

// Enum 枚举（PG/SQLite 编译为 varchar check，MySQL 编译为 enum）。
func (b *Blueprint) Enum(name string, allowed []string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "enum", Allowed: allowed})
}

// Set MySQL set 枚举。
func (b *Blueprint) Set(name string, allowed []string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "set", Allowed: allowed})
}

// Date 日期。
func (b *Blueprint) Date(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "date"})
}

// DateTime 日期时间（precision 可选）。
func (b *Blueprint) DateTime(name string, precision ...int) *ColumnDefinition {
	p := precisionArg(precision)
	return b.addColumn(&ColumnDefinition{Name: name, Type: "datetime", Precision: p})
}

// DateTimeTz 带时区日期时间（MySQL 同 datetime；PG 为 timestamptz）。
func (b *Blueprint) DateTimeTz(name string, precision ...int) *ColumnDefinition {
	p := precisionArg(precision)
	return b.addColumn(&ColumnDefinition{Name: name, Type: "datetimetz", Precision: p})
}

// Time 时间（precision 可选）。
func (b *Blueprint) Time(name string, precision ...int) *ColumnDefinition {
	p := precisionArg(precision)
	return b.addColumn(&ColumnDefinition{Name: name, Type: "time", Precision: p})
}

// TimeTz 带时区时间。
func (b *Blueprint) TimeTz(name string, precision ...int) *ColumnDefinition {
	p := precisionArg(precision)
	return b.addColumn(&ColumnDefinition{Name: name, Type: "timetz", Precision: p})
}

// Timestamp 时间戳（precision 可选；未指定时 PG 按 (0) 处理）。
func (b *Blueprint) Timestamp(name string, precision ...int) *ColumnDefinition {
	p := precisionArg(precision)
	return b.addColumn(&ColumnDefinition{Name: name, Type: "timestamp", Precision: p})
}

// TimestampTz 带时区时间戳。
func (b *Blueprint) TimestampTz(name string, precision ...int) *ColumnDefinition {
	p := precisionArg(precision)
	return b.addColumn(&ColumnDefinition{Name: name, Type: "timestamptz", Precision: p})
}

// Year 年份（MySQL year；PG/SQLite 为 int）。
func (b *Blueprint) Year(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "year"})
}

// Binary 二进制（length 可选；fixed 定长 binary(n)，否则 varbinary(n)/blob/bytea）。
func (b *Blueprint) Binary(name string, length ...int) *ColumnDefinition {
	l := 0
	if len(length) > 0 {
		l = length[0]
	}
	return b.addColumn(&ColumnDefinition{Name: name, Type: "binary", Length: l})
}

// JSON JSON。
func (b *Blueprint) JSON(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "json"})
}

// JSONB JSONB（MySQL/SQLite 同 json；PG 为 jsonb；区分于 JSON）。
func (b *Blueprint) JSONB(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "jsonb"})
}

// UUID 通用唯一标识。
func (b *Blueprint) UUID(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "uuid"})
}

// ForeignUuid uuid 外键列。
func (b *Blueprint) ForeignUuid(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "uuid"})
}

// ULID char(26)。
func (b *Blueprint) Ulid(name string, length ...int) *ColumnDefinition {
	l := 26
	if len(length) > 0 {
		l = length[0]
	}
	return b.addColumn(&ColumnDefinition{Name: name, Type: "ulid", Length: l})
}

// ForeignUlid char(26) 外键列。
func (b *Blueprint) ForeignUlid(name string) *ColumnDefinition {
	return b.Ulid(name)
}

// ForeignID 无符号整数外键列。
func (b *Blueprint) ForeignID(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "bigint", IsUnsigned: true})
}

// IpAddress varchar(45)/inet。
func (b *Blueprint) IpAddress(name ...string) *ColumnDefinition {
	n := "ip_address"
	if len(name) > 0 {
		n = name[0]
	}
	return b.addColumn(&ColumnDefinition{Name: n, Type: "ipaddress"})
}

// MacAddress varchar(17)/macaddr。
func (b *Blueprint) MacAddress(name ...string) *ColumnDefinition {
	n := "mac_address"
	if len(name) > 0 {
		n = name[0]
	}
	return b.addColumn(&ColumnDefinition{Name: n, Type: "macaddress"})
}

// Geometry 空间几何（subtype/srid 可选）。
func (b *Blueprint) Geometry(name string, subtype ...string) *ColumnDefinition {
	st := ""
	if len(subtype) > 0 {
		st = subtype[0]
	}
	return b.addColumn(&ColumnDefinition{Name: name, Type: "geometry", Subtype: st})
}

// Geography 空间地理（subtype/srid 可选，PG 默认 SRID 4326 由调用方传 Srid）。
func (b *Blueprint) Geography(name string, subtype ...string) *ColumnDefinition {
	st := ""
	if len(subtype) > 0 {
		st = subtype[0]
	}
	return b.addColumn(&ColumnDefinition{Name: name, Type: "geography", Subtype: st})
}

// Computed 生成列表达式（MySQL 需搭配 StoredAs/VirtualAs；PG 用 GeneratedAs；
// 直接对 MySQL/SQLite 编译会报 ErrUnsupported，对齐 typeComputed）。
func (b *Blueprint) Computed(name string, expression string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "computed", StoredAs: expression})
}

// Vector 向量列（pgvector；dimensions 可选）。
func (b *Blueprint) Vector(name string, dimensions ...int) *ColumnDefinition {
	d := 0
	if len(dimensions) > 0 {
		d = dimensions[0]
	}
	return b.addColumn(&ColumnDefinition{Name: name, Type: "vector", Dimensions: d})
}

// Tsvector 全文向量列（PG 专有）。
func (b *Blueprint) Tsvector(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "tsvector"})
}

// precisionArg：未传 precision 时返回 0（0 表示方言层默认精度）。
func precisionArg(p []int) int {
	if len(p) > 0 {
		return p[0]
	}
	return 0
}

// ---- ColumnDefinition 修饰符 ----

// Nullable 标记可空。
func (c *ColumnDefinition) Nullable(v ...bool) *ColumnDefinition {
	c.IsNullable = len(v) == 0 || v[0]
	c.NotNull = !c.IsNullable
	return c
}

// Default 设置默认值。
func (c *ColumnDefinition) Default(v any) *ColumnDefinition {
	c.DefaultValue = v
	return c
}

// DefaultExpression 设置原始默认表达式（与 DefaultRaw 字段对应）。
func (c *ColumnDefinition) DefaultExpression(expr string) *ColumnDefinition {
	c.DefaultRaw = expr
	return c
}

// Primary 标记主键。
func (c *ColumnDefinition) Primary() *ColumnDefinition {
	c.PrimaryKey = true
	return c
}

// StartingValue 自增起始值。
func (c *ColumnDefinition) StartingValue(v int64) *ColumnDefinition {
	c.From = v
	return c
}

// Unsigned 无符号 setter（字段名 IsUnsigned，即 unsigned 修饰符）。
func (c *ColumnDefinition) Unsigned(v ...bool) *ColumnDefinition {
	c.IsUnsigned = len(v) == 0 || v[0]
	return c
}

// AutoIncrement 自增 setter（字段名 IsAutoIncrement）。
func (c *ColumnDefinition) AutoIncrement(v ...bool) *ColumnDefinition {
	c.IsAutoIncrement = len(v) == 0 || v[0]
	return c
}

// Comment 列级注释（字段名 CommentText）。
func (c *ColumnDefinition) SetComment(text string) *ColumnDefinition {
	c.CommentText = text
	return c
}

// Change 标记该列为 alter change（->change() 语义）。
func (c *ColumnDefinition) Change(v ...bool) *ColumnDefinition {
	c.IsChange = len(v) == 0 || v[0]
	return c
}

// UseCurrentSetter default CURRENT_TIMESTAMP（字段名 UseCurrent）。
func (c *ColumnDefinition) SetUseCurrent(v ...bool) *ColumnDefinition {
	c.UseCurrent = len(v) == 0 || v[0]
	return c
}

// SetUseCurrentOnUpdate on update CURRENT_TIMESTAMP（字段名 UseCurrentOnUpdate）。
func (c *ColumnDefinition) SetUseCurrentOnUpdate(v ...bool) *ColumnDefinition {
	c.UseCurrentOnUpdate = len(v) == 0 || v[0]
	return c
}

// StoredAs 生成列 as (expr) stored。
func (c *ColumnDefinition) SetStoredAs(expr string) *ColumnDefinition {
	c.StoredAs = expr
	return c
}

// VirtualAs 生成列 as (expr)。
func (c *ColumnDefinition) SetVirtualAs(expr string) *ColumnDefinition {
	c.VirtualAs = expr
	return c
}

// GeneratedAs PG generated always as (expr) stored。
func (c *ColumnDefinition) SetGeneratedAs(expr string) *ColumnDefinition {
	c.GeneratedAs = expr
	return c
}

// SetCharset MySQL 列级字符集。
func (c *ColumnDefinition) SetCharset(cs string) *ColumnDefinition {
	c.Charset = cs
	return c
}

// SetCollation 列级 collation。
func (c *ColumnDefinition) SetCollation(col string) *ColumnDefinition {
	c.Collation = col
	return c
}

// First MySQL first 修饰符。
func (c *ColumnDefinition) SetFirst() *ColumnDefinition {
	c.First = true
	return c
}

// After MySQL after 修饰符。
func (c *ColumnDefinition) SetAfter(col string) *ColumnDefinition {
	c.After = col
	return c
}

// SetOnUpdate MySQL on update 原始表达式。
func (c *ColumnDefinition) SetOnUpdate(expr string) *ColumnDefinition {
	c.OnUpdate = expr
	return c
}

// SetSrid geometry SRID。
func (c *ColumnDefinition) SetSrid(srid int) *ColumnDefinition {
	c.Srid = srid
	return c
}

// SetFixed binary 定长标记。
func (c *ColumnDefinition) SetFixed(v ...bool) *ColumnDefinition {
	c.Fixed = len(v) == 0 || v[0]
	return c
}

// SetInvisible MySQL invisible。
func (c *ColumnDefinition) SetInvisible(v ...bool) *ColumnDefinition {
	c.Invisible = len(v) == 0 || v[0]
	return c
}

// SetPersisted 生成列 persisted（SQLServer/PG）。
func (c *ColumnDefinition) SetPersisted(v ...bool) *ColumnDefinition {
	c.Persisted = len(v) == 0 || v[0]
	return c
}

// ---- 惯用组合 ----

// Timestamps created_at + updated_at（nullable）。
func (b *Blueprint) Timestamps() {
	b.addColumn(&ColumnDefinition{Name: "created_at", Type: "timestamp"}).Nullable()
	b.addColumn(&ColumnDefinition{Name: "updated_at", Type: "timestamp"}).Nullable()
}

// TimestampsTz 带时区 timestamps。
func (b *Blueprint) TimestampsTz() {
	b.addColumn(&ColumnDefinition{Name: "created_at", Type: "timestamptz"}).Nullable()
	b.addColumn(&ColumnDefinition{Name: "updated_at", Type: "timestamptz"}).Nullable()
}

// SoftDeletes deleted_at 可空时间戳。
func (b *Blueprint) SoftDeletes(name ...string) {
	n := "deleted_at"
	if len(name) > 0 {
		n = name[0]
	}
	b.addColumn(&ColumnDefinition{Name: n, Type: "timestamp"}).Nullable()
}

// SoftDeletesTz 带时区软删除列。
func (b *Blueprint) SoftDeletesTz(name ...string) {
	n := "deleted_at"
	if len(name) > 0 {
		n = name[0]
	}
	b.addColumn(&ColumnDefinition{Name: n, Type: "timestamptz"}).Nullable()
}

// RememberToken remember_token varchar(100)。
func (b *Blueprint) RememberToken() {
	b.String("remember_token", 100).Nullable()
}

// ---- morphs 族（Blueprint.php:1590-1743，默认 numeric） ----

// Morphs {name}_type + {name}_id + 组合索引（默认 numeric morph key）。
func (b *Blueprint) Morphs(name string, indexName ...string) {
	b.NumericMorphs(name, indexName...)
}

// NullableMorphs 可空 morph 列（默认 numeric）。
func (b *Blueprint) NullableMorphs(name string, indexName ...string) {
	b.NullableNumericMorphs(name, indexName...)
}

// NumericMorphs 无符号 bigint 形态列。
func (b *Blueprint) NumericMorphs(name string, indexName ...string) {
	b.String(name + "_type")
	b.UnsignedBigInteger(name + "_id")
	b.Index([]string{name + "_type", name + "_id"}, indexName...)
}

// NullableNumericMorphs 可空无符号 bigint 形态列。
func (b *Blueprint) NullableNumericMorphs(name string, indexName ...string) {
	b.String(name + "_type").Nullable()
	b.UnsignedBigInteger(name + "_id").Nullable()
	b.Index([]string{name + "_type", name + "_id"}, indexName...)
}

// UuidMorphs uuid 形态列。
func (b *Blueprint) UuidMorphs(name string, indexName ...string) {
	b.String(name + "_type")
	b.UUID(name + "_id")
	b.Index([]string{name + "_type", name + "_id"}, indexName...)
}

// NullableUuidMorphs 可空 uuid 形态列。
func (b *Blueprint) NullableUuidMorphs(name string, indexName ...string) {
	b.String(name + "_type").Nullable()
	b.UUID(name + "_id").Nullable()
	b.Index([]string{name + "_type", name + "_id"}, indexName...)
}

// UlidMorphs ulid 形态列。
func (b *Blueprint) UlidMorphs(name string, indexName ...string) {
	b.String(name + "_type")
	b.Ulid(name + "_id")
	b.Index([]string{name + "_type", name + "_id"}, indexName...)
}

// NullableUlidMorphs 可空 ulid 形态列。
func (b *Blueprint) NullableUlidMorphs(name string, indexName ...string) {
	b.String(name + "_type").Nullable()
	b.Ulid(name + "_id").Nullable()
	b.Index([]string{name + "_type", name + "_id"}, indexName...)
}

// ForeignIdFor 模型外键列（model 为模型名，列名 snake(model)+"_id"，可显式覆盖）。
func (b *Blueprint) ForeignIdFor(model string, column ...string) *ColumnDefinition {
	n := modelColumn(model, column)
	return b.addColumn(&ColumnDefinition{Name: n, Type: "bigint", IsUnsigned: true})
}

// ForeignUuidFor 模型 uuid 外键列。
func (b *Blueprint) ForeignUuidFor(model string, column ...string) *ColumnDefinition {
	n := modelColumn(model, column)
	return b.addColumn(&ColumnDefinition{Name: n, Type: "uuid"})
}

// ForeignUlidFor 模型 ulid 外键列。
func (b *Blueprint) ForeignUlidFor(model string, column ...string) *ColumnDefinition {
	n := modelColumn(model, column)
	return b.Ulid(n)
}

// modelColumn 由模型名推导外键列名：App\Models\User → user_id。
func modelColumn(model string, column []string) string {
	if len(column) > 0 && column[0] != "" {
		return column[0]
	}
	parts := strings.Split(model, "\\")
	name := parts[len(parts)-1]
	return toSnake(name) + "_id"
}

func toSnake(s string) string {
	var out []rune
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				out = append(out, '_')
			}
			out = append(out, r-'A'+'a')
		} else {
			out = append(out, r)
		}
	}
	return string(out)
}

// ---- 索引 ----

// Index 常规索引（name 缺省按 table_cols_index 命名）。
func (b *Blueprint) Index(columns []string, name ...string) *IndexDefinition {
	idx := &IndexDefinition{Type: "index", Name: b.CreateIndexName("index", columns), Columns: columns}
	if len(name) > 0 && name[0] != "" {
		idx.Name = name[0]
	}
	b.Indexes = append(b.Indexes, idx)
	return idx
}

// Unique 唯一索引。
func (b *Blueprint) Unique(columns []string, name ...string) *IndexDefinition {
	idx := &IndexDefinition{Type: "unique", Name: b.CreateIndexName("unique", columns), Columns: columns}
	if len(name) > 0 && name[0] != "" {
		idx.Name = name[0]
	}
	b.Indexes = append(b.Indexes, idx)
	return idx
}

// Primary 联合主键。
func (b *Blueprint) Primary(columns []string, name ...string) *IndexDefinition {
	idx := &IndexDefinition{Type: "primary", Columns: columns}
	idx.Name = b.CreateIndexName("primary", columns)
	if len(name) > 0 && name[0] != "" {
		idx.Name = name[0]
	}
	b.Indexes = append(b.Indexes, idx)
	return idx
}

// FullText 全文索引（MySQL fulltext；PG gin + to_tsvector）。
func (b *Blueprint) FullText(columns []string, name ...string) *IndexDefinition {
	idx := &IndexDefinition{Type: "fulltext", Name: b.CreateIndexName("fulltext", columns), Columns: columns}
	if len(name) > 0 && name[0] != "" {
		idx.Name = name[0]
	}
	b.Indexes = append(b.Indexes, idx)
	return idx
}

// SpatialIndex 空间索引（MySQL spatial index；PG gist）。
func (b *Blueprint) SpatialIndex(columns []string, name ...string) *IndexDefinition {
	idx := &IndexDefinition{Type: "spatialindex", Name: b.CreateIndexName("spatialindex", columns), Columns: columns}
	if len(name) > 0 && name[0] != "" {
		idx.Name = name[0]
	}
	b.Indexes = append(b.Indexes, idx)
	return idx
}

// VectorIndex 向量索引（PG hnsw + vector_cosine_ops；MySQL 不支持）。
func (b *Blueprint) VectorIndex(column string, name ...string) *IndexDefinition {
	cols := []string{column}
	idx := &IndexDefinition{Type: "vectorindex", Name: b.CreateIndexName("vectorindex", cols), Columns: cols,
		Algorithm: "hnsw", OperatorClass: "vector_cosine_ops"}
	if len(name) > 0 && name[0] != "" {
		idx.Name = name[0]
	}
	b.Indexes = append(b.Indexes, idx)
	return idx
}

// RawIndex 原始表达式索引。
func (b *Blueprint) RawIndex(expression, name string) *IndexDefinition {
	idx := &IndexDefinition{Type: "index", Name: name, Expression: expression}
	b.Indexes = append(b.Indexes, idx)
	return idx
}

// Foreign 外键链起点。
func (b *Blueprint) Foreign(columns ...string) *IndexDefinition {
	idx := &IndexDefinition{Type: "foreign", Name: b.CreateIndexName("foreign", columns), Columns: columns}
	b.Indexes = append(b.Indexes, idx)
	return idx
}

// References 外键目标列（兼容旧签名：单列）。
func (i *IndexDefinition) References(cols ...string) *IndexDefinition {
	i.ReferenceColumns = cols
	i.ReferenceColumn = cols[0]
	return i
}

// foreignRefColumns 取外键引用列列表（兼容单列旧字段）。
func (i *IndexDefinition) foreignRefColumns() []string {
	if len(i.ReferenceColumns) > 0 {
		return i.ReferenceColumns
	}
	return []string{i.ReferenceColumn}
}

// ---- drop / rename 命令族（Blueprint.php:422-660） ----

// Drop 标记删表。
func (b *Blueprint) Drop() { b.Commands = append(b.Commands, &Command{Name: "drop"}) }

// DropIfExists 标记条件删表。
func (b *Blueprint) DropIfExists() { b.Commands = append(b.Commands, &Command{Name: "dropIfExists"}) }

// DropColumn 删除列（可传多列）。
func (b *Blueprint) DropColumn(columns ...string) {
	b.Commands = append(b.Commands, &Command{Name: "dropColumn", Columns: columns})
}

// RenameColumn 重命名列。
func (b *Blueprint) RenameColumn(from, to string) {
	b.Commands = append(b.Commands, &Command{Name: "renameColumn", From: from, To: to})
}

// Rename 重命名表。
func (b *Blueprint) Rename(to string) {
	b.Commands = append(b.Commands, &Command{Name: "rename", To: to})
}

// DropIndexCommand 家族：index 参数既可为显式索引名，也可为列切片（按惯例名推导）。
// index 缺省时的回退：以命令名作为 createIndexName
// 的 type（如 DropPrimary() → {table}__dropprimary）。
func (b *Blueprint) addDropIndexCommand(name, kind string, index any) *Command {
	cmd := &Command{Name: name}
	switch v := index.(type) {
	case string:
		cmd.Index = v
	case []string:
		cmd.Index = b.CreateIndexName(kind, v)
	default:
		cmd.Index = b.CreateIndexName(name, nil)
	}
	b.Commands = append(b.Commands, cmd)
	return cmd
}

// DropPrimary 删除主键（index 可为 nil / 索引名 / 列切片）。
func (b *Blueprint) DropPrimary(index ...any) {
	var idx any
	if len(index) > 0 {
		idx = index[0]
	}
	b.addDropIndexCommand("dropPrimary", "primary", idx)
}

// DropUnique 删除唯一索引。
func (b *Blueprint) DropUnique(index any) { b.addDropIndexCommand("dropUnique", "unique", index) }

// DropIndex 删除普通索引。
func (b *Blueprint) DropIndex(index any) { b.addDropIndexCommand("dropIndex", "index", index) }

// DropFullText 删除全文索引。
func (b *Blueprint) DropFullText(index any) {
	b.addDropIndexCommand("dropFullText", "fulltext", index)
}

// DropSpatialIndex 删除空间索引。
func (b *Blueprint) DropSpatialIndex(index any) {
	b.addDropIndexCommand("dropSpatialIndex", "spatialindex", index)
}

// DropVectorIndex 删除向量索引。
func (b *Blueprint) DropVectorIndex(index any) {
	b.addDropIndexCommand("dropVectorIndex", "vectorindex", index)
}

// DropForeign 删除外键。
func (b *Blueprint) DropForeign(index any) { b.addDropIndexCommand("dropForeign", "foreign", index) }

// DropConstrainedForeignId 删除外键 + 列（对齐 Blueprint::dropConstrainedForeignId）。
func (b *Blueprint) DropConstrainedForeignId(column string) {
	b.DropForeign([]string{column})
	b.DropColumn(column)
}

// RenameIndex 重命名索引。
func (b *Blueprint) RenameIndex(from, to string) {
	b.Commands = append(b.Commands, &Command{Name: "renameIndex", From: from, To: to})
}

// DropTimestamps 删除 created_at/updated_at。
func (b *Blueprint) DropTimestamps() { b.DropColumn("created_at", "updated_at") }

// DropTimestampsTz 同 DropTimestamps。
func (b *Blueprint) DropTimestampsTz() { b.DropTimestamps() }

// DropSoftDeletes 删除软删除列。
func (b *Blueprint) DropSoftDeletes(column ...string) {
	n := "deleted_at"
	if len(column) > 0 {
		n = column[0]
	}
	b.DropColumn(n)
}

// DropSoftDeletesTz 同 DropSoftDeletes。
func (b *Blueprint) DropSoftDeletesTz(column ...string) { b.DropSoftDeletes(column...) }

// DropRememberToken 删除 remember_token。
func (b *Blueprint) DropRememberToken() { b.DropColumn("remember_token") }

// DropMorphs 删除 morph 列与其索引。
func (b *Blueprint) DropMorphs(name string, indexName ...string) {
	in := ""
	if len(indexName) > 0 && indexName[0] != "" {
		in = indexName[0]
	} else {
		in = b.CreateIndexName("index", []string{name + "_type", name + "_id"})
	}
	b.DropIndex(in)
	b.DropColumn(name+"_type", name+"_id")
}

// ---- 编译 ----

// ToSql 产出语句列表（方言经由 SchemaGrammar）。
// create 场景：CompileCreate（索引等由方言在 create 内部处理）；
// alter 场景：先 CompileAdd（列 + 索引），再依序编译 Commands。
func (b *Blueprint) ToSql(g SchemaGrammar) []string {
	var out []string

	if b.Create {
		return g.CompileCreate(b)
	}

	if len(b.Columns) > 0 || len(b.Indexes) > 0 {
		out = append(out, g.CompileAdd(b)...)
	}

	for _, cmd := range b.Commands {
		switch cmd.Name {
		case "drop":
			out = append(out, g.CompileDrop(b.Table))
		case "dropIfExists":
			out = append(out, g.CompileDropIfExists(b.Table))
		case "dropColumn":
			out = append(out, g.CompileDropColumn(b, cmd.Columns)...)
		case "renameColumn":
			out = append(out, g.CompileRenameColumn(b, cmd.From, cmd.To))
		case "rename":
			out = append(out, g.CompileRename(b, cmd.To))
		case "renameIndex":
			out = append(out, g.CompileRenameIndex(b, cmd.From, cmd.To))
		case "dropPrimary":
			out = append(out, g.CompileDropPrimary(b, cmd.Index))
		case "dropUnique":
			out = append(out, g.CompileDropUnique(b, cmd.Index))
		case "dropIndex":
			out = append(out, g.CompileDropIndex(b, cmd.Index))
		case "dropFullText":
			out = append(out, g.CompileDropFullText(b, cmd.Index))
		case "dropSpatialIndex":
			out = append(out, g.CompileDropSpatialIndex(b, cmd.Index))
		case "dropVectorIndex":
			out = append(out, g.CompileDropVectorIndex(b, cmd.Index))
		case "dropForeign":
			out = append(out, g.CompileDropForeign(b, cmd.Index))
		}
	}
	// 方言对不支持/由重建承担的命令返回空串，这里统一过滤
	filtered := out[:0]
	for _, s := range out {
		if s != "" {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

// SchemaGrammar DDL 编译接口。
type SchemaGrammar interface {
	SetTablePrefix(prefix string)
	CompileCreate(b *Blueprint) []string
	CompileAdd(b *Blueprint) []string
	// CompileChange 编译列修改（alter 语义）
	CompileChange(b *Blueprint, c *ColumnDefinition) []string
	CompileDrop(table string) string
	CompileDropIfExists(table string) string
	CompileDropColumn(b *Blueprint, columns []string) []string
	CompileRenameColumn(b *Blueprint, from, to string) string
	CompileRename(b *Blueprint, to string) string
	CompileRenameIndex(b *Blueprint, from, to string) string
	CompileIndex(b *Blueprint, idx *IndexDefinition) string
	CompileUnique(b *Blueprint, idx *IndexDefinition) string
	CompilePrimary(b *Blueprint, idx *IndexDefinition) string
	CompileForeign(b *Blueprint, idx *IndexDefinition) string
	CompileFullText(b *Blueprint, idx *IndexDefinition) string
	CompileSpatialIndex(b *Blueprint, idx *IndexDefinition) string
	CompileVectorIndex(b *Blueprint, idx *IndexDefinition) string
	CompileDropPrimary(b *Blueprint, index string) string
	CompileDropUnique(b *Blueprint, index string) string
	CompileDropIndex(b *Blueprint, index string) string
	CompileDropFullText(b *Blueprint, index string) string
	CompileDropSpatialIndex(b *Blueprint, index string) string
	CompileDropVectorIndex(b *Blueprint, index string) string
	CompileDropForeign(b *Blueprint, index string) string
	// 内省
	CompileTableExists(schema, table string) (string, []any)
	CompileColumnListing(schema, table string) (string, []any)
	WrapTableForSchema(v any) string
}
