package grammar

import "strings"

// ColumnDefinition 对应 Laravel Schema\ColumnDefinition。
type ColumnDefinition struct {
	Name          string
	Type          string // int / bigint / varchar / char / text / tinyint / decimal / timestamp / datetime / date / json / jsonb / serial / bigserial / boolean
	Length        int    // varchar / char
	Precision     int    // decimal
	Scale         int
	Unsigned      bool
	IsNullable    bool
	NotNull       bool
	DefaultValue  any // nil 未设置；数值/字符串字面量
	DefaultRaw    string
	AutoIncrement bool
	PrimaryKey    bool
	Comment       string
	From          int64 // 自增起始值（MySQL）
}

// IndexDefinition 对应 Laravel 的索引定义。
type IndexDefinition struct {
	Type    string // index / unique / primary / foreign
	Name    string
	Columns []string
	// 外键
	ReferenceColumn string
	OnTable         string
	OnDelete        string
	OnUpdate        string
}

// Blueprint 对应 Laravel Schema\Blueprint。
type Blueprint struct {
	Table     string
	Create    bool
	Temporary bool
	Columns   []*ColumnDefinition
	Indexes   []*IndexDefinition
	Commands  []string // drop / dropIfExists 等
	// MySQL 专有
	Engine    string
	Charset   string
	Collation string
}

func NewBlueprint(table string) *Blueprint {
	return &Blueprint{Table: table}
}

// ---- 流式列定义 ----

func (b *Blueprint) addColumn(def *ColumnDefinition) *ColumnDefinition {
	b.Columns = append(b.Columns, def)
	return def
}

// ID 自增主键（bigIncrements，Laravel 10+ 默认）。
func (b *Blueprint) ID(name ...string) *ColumnDefinition {
	n := "id"
	if len(name) > 0 {
		n = name[0]
	}
	return b.BigIncrements(n)
}

// BigIncrements bigint 自增主键。
func (b *Blueprint) BigIncrements(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "bigint", Unsigned: true, IsNullable: false, AutoIncrement: true, PrimaryKey: true})
}

// Increments int 自增主键。
func (b *Blueprint) Increments(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "int", Unsigned: true, IsNullable: false, AutoIncrement: true, PrimaryKey: true})
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

// Text 长文本。
func (b *Blueprint) Text(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "text"})
}

// Integer int。
func (b *Blueprint) Integer(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "int"})
}

// BigInteger bigint。
func (b *Blueprint) BigInteger(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "bigint"})
}

// Boolean 布尔。
func (b *Blueprint) Boolean(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "boolean"})
}

// Date 日期。
func (b *Blueprint) Date(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "date"})
}

// DateTime 日期时间。
func (b *Blueprint) DateTime(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "datetime"})
}

// Timestamp 时间戳。
func (b *Blueprint) Timestamp(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "timestamp"})
}

// Decimal 定点数。
func (b *Blueprint) Decimal(name string, precision, scale int) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "decimal", Precision: precision, Scale: scale})
}

// JSON JSON。
func (b *Blueprint) JSON(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "json"})
}

// UUID 通用唯一标识。
func (b *Blueprint) UUID(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "uuid"})
}

// ForeignID 无符号整数外键列。
func (b *Blueprint) ForeignID(name string) *ColumnDefinition {
	return b.addColumn(&ColumnDefinition{Name: name, Type: "bigint", Unsigned: true})
}

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

// ---- 惯用组合 ----

// Timestamps created_at + updated_at（nullable）。
func (b *Blueprint) Timestamps() {
	b.addColumn(&ColumnDefinition{Name: "created_at", Type: "timestamp"}).Nullable()
	b.addColumn(&ColumnDefinition{Name: "updated_at", Type: "timestamp"}).Nullable()
}

// SoftDeletes deleted_at 可空时间戳。
func (b *Blueprint) SoftDeletes(name ...string) {
	n := "deleted_at"
	if len(name) > 0 {
		n = name[0]
	}
	b.addColumn(&ColumnDefinition{Name: n, Type: "timestamp"}).Nullable()
}

// RememberToken remember_token varchar(100)。
func (b *Blueprint) RememberToken() {
	b.String("remember_token", 100).Nullable()
}

// ---- 索引 ----

// Index 常规索引（name 缺省按 table_cols_index 命名）。
func (b *Blueprint) Index(columns []string, name ...string) *IndexDefinition {
	idx := &IndexDefinition{Type: "index", Name: indexName(b.Table, "index", columns), Columns: columns}
	if len(name) > 0 {
		idx.Name = name[0]
	}
	b.Indexes = append(b.Indexes, idx)
	return idx
}

// Unique 唯一索引。
func (b *Blueprint) Unique(columns []string, name ...string) *IndexDefinition {
	idx := &IndexDefinition{Type: "unique", Name: indexName(b.Table, "unique", columns), Columns: columns}
	if len(name) > 0 {
		idx.Name = name[0]
	}
	b.Indexes = append(b.Indexes, idx)
	return idx
}

// Primary 联合主键。
func (b *Blueprint) Primary(columns []string) *IndexDefinition {
	idx := &IndexDefinition{Type: "primary", Columns: columns}
	b.Indexes = append(b.Indexes, idx)
	return idx
}

// Foreign 外键链起点。
func (b *Blueprint) Foreign(columns ...string) *IndexDefinition {
	idx := &IndexDefinition{Type: "foreign", Name: indexName(b.Table, "foreign", columns), Columns: columns}
	b.Indexes = append(b.Indexes, idx)
	return idx
}

// References 外键目标列。
func (i *IndexDefinition) References(cols ...string) *IndexDefinition {
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

// ---- 命令 ----

// Drop 标记删表。
func (b *Blueprint) Drop() { b.Commands = append(b.Commands, "drop") }

// DropIfExists 标记条件删表。
func (b *Blueprint) DropIfExists() { b.Commands = append(b.Commands, "dropIfExists") }

// ---- 编译 ----

// ToSql 产出语句列表（方言经由 SchemaGrammar）。
func (b *Blueprint) ToSql(g SchemaGrammar) []string {
	if len(b.Commands) > 0 {
		var out []string
		for _, cmd := range b.Commands {
			switch cmd {
			case "drop":
				out = append(out, g.CompileDrop(b.Table))
			case "dropIfExists":
				out = append(out, g.CompileDropIfExists(b.Table))
			}
		}
		return out
	}

	if b.Create {
		return g.CompileCreate(b)
	}

	// alter：MySQL 每列一条 add；索引逐条
	return g.CompileAdd(b)
}

func indexName(table, kind string, columns []string) string {
	return table + "_" + strings.Join(columns, "_") + "_" + kind
}

// SchemaGrammar DDL 编译接口。
type SchemaGrammar interface {
	SetTablePrefix(prefix string)
	CompileCreate(b *Blueprint) []string
	CompileAdd(b *Blueprint) []string
	CompileDrop(table string) string
	CompileDropIfExists(table string) string
	CompileIndex(b *Blueprint, idx *IndexDefinition) string
	CompileUnique(b *Blueprint, idx *IndexDefinition) string
	CompilePrimary(b *Blueprint, idx *IndexDefinition) string
	CompileForeign(b *Blueprint, idx *IndexDefinition) string
	// 内省
	CompileTableExists(schema, table string) (string, []any)
	CompileColumnListing(schema, table string) (string, []any)
	WrapTableForSchema(v any) string
}
