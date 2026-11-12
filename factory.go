package torm

import (
	"fmt"
)

// ---- Factory / Seeder（对应 Laravel 的工厂与种子，Factories/Factory.php）----
//
// Factory 以链式值语义组合：State/Sequence/For/Count/AfterMaking/AfterCreating
// 各返回新工厂，原工厂不变（对齐 Laravel 工厂克隆语义）。

// Factory 模型工厂：Definition 按 1 起始的序号生成模型。
// T 为模型指针类型（如 *Post）以支持 Create 走完整生命周期。
type Factory[T any] struct {
	Definition func(seq int) T
	Table      string // 可选：批量插入的表名（Create 用 Create 生命周期则不需要）

	states        []func(T)            // State：按序应用的状态变更器（T 为模型指针类型）
	sequence      []func(seq int, m T) // Sequence：按 (seq-1) % len 轮换
	forParent     []func(T)            // For：belongsTo 外键填充
	afterMaking   []func(T)            // AfterMaking 钩子（make 生命周期）
	afterCreating []func(T)            // AfterCreating 钩子（落库成功后）
	count         int                  // Count：默认生成数量
}

// State 注册命名状态变更器（对应 Laravel state）；
// name 仅作标识，make/create 时按注册顺序应用。
func (f Factory[T]) State(name string, mutator func(T)) Factory[T] {
	f.states = append(append([]func(T){}, f.states...), mutator)
	return f
}

// Sequence 注册轮换序列（对应 Laravel sequence）：第 i 个模型应用
// sequence[(i-1) % len]。
func (f Factory[T]) Sequence(mutators ...func(seq int, m T)) Factory[T] {
	f.sequence = append(append([]func(seq int, m T){}, f.sequence...), mutators...)
	return f
}

// SequenceAttrs 属性 map 形态的轮换序列（对应 Laravel sequence(['a' => 1], ['a' => 2])）。
func (f Factory[T]) SequenceAttrs(attrs ...map[string]any) Factory[T] {
	maps := attrs
	mutators := make([]func(seq int, m T), 0, len(maps))
	for i := range maps {
		m := maps[i]
		mutators = append(mutators, func(seq int, model T) {
			_ = seq
			_ = setModelAttrs(model, m)
		})
	}
	f.sequence = append(f.sequence, mutators...)
	return f
}

// For 注册 belongsTo 父模型：make/create 时把 parent 主键写入外键列
// （外键缺省 = singular(parent 表名)_id，对应 Laravel for）。
func (f Factory[T]) For(parent any, foreignKey ...string) Factory[T] {
	fk := ""
	if len(foreignKey) > 0 && foreignKey[0] != "" {
		fk = foreignKey[0]
	} else {
		fk = stripPlural(modelTableName(parent)) + "_id"
	}
	f.forParent = append(append([]func(T){}, f.forParent...), func(m T) {
		_ = setModelAttribute(m, fk, modelKeyValue(parent))
	})
	return f
}

// AfterMaking 注册 make 生命周期钩子（对应 Laravel afterMaking）。
func (f Factory[T]) AfterMaking(fn func(T)) Factory[T] {
	f.afterMaking = append(append([]func(T){}, f.afterMaking...), fn)
	return f
}

// AfterCreating 注册落库成功后的钩子（对应 Laravel afterCreating；
// 此时主键已回填）。
func (f Factory[T]) AfterCreating(fn func(T)) Factory[T] {
	f.afterCreating = append(append([]func(T){}, f.afterCreating...), fn)
	return f
}

// Count 设置默认生成数量（对应 Laravel count；配合 MakeMany/CreateMany）。
func (f Factory[T]) Count(n int) Factory[T] {
	f.count = n
	return f
}

// resolveCount 兼容旧 Make(count)/Create(conn, count) 签名：
// count <= 0 且设置了 Count(n) 时用 n，否则用 count。
func (f Factory[T]) resolveCount(count int) int {
	if count <= 0 && f.count > 0 {
		return f.count
	}
	if count < 0 {
		return 0
	}
	return count
}

// makeOne 生成单个模型：Definition → states → forParent → sequence → afterMaking。
func (f Factory[T]) makeOne(seq int) T {
	item := f.Definition(seq)
	for _, state := range f.states {
		state(item)
	}
	for _, fp := range f.forParent {
		fp(item)
	}
	if len(f.sequence) > 0 {
		f.sequence[(seq-1)%len(f.sequence)](seq, item)
	}
	for _, hook := range f.afterMaking {
		hook(item)
	}
	return item
}

// Make 生成 count 个模型实例（不落库，对应 Laravel make）。
// 工厂带 Count(n) 时可用 MakeMany 按默认数量生成。
func (f Factory[T]) Make(count int) []T {
	n := f.resolveCount(count)
	out := make([]T, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, f.makeOne(i))
	}
	return out
}

// MakeMany 按 Count(n) 数量生成（未设置时 1 个）。
func (f Factory[T]) MakeMany() []T {
	n := f.count
	if n <= 0 {
		n = 1
	}
	return f.Make(n)
}

// MakeOne 生成单个模型实例（对应 Laravel makeOne）。
func (f Factory[T]) MakeOne() T {
	return f.makeOne(1)
}

// Create 生成并落库：逐条走 Create 生命周期（时间戳/观察者/主键回填）。
func (f Factory[T]) Create(conn *Connection, count int) ([]T, error) {
	n := f.resolveCount(count)
	out := make([]T, 0, n)
	for i := 1; i <= n; i++ {
		item := f.makeOne(i)
		if err := conn.Create(any(item).(any)); err != nil {
			return out, err
		}
		for _, hook := range f.afterCreating {
			hook(item)
		}
		out = append(out, item)
	}
	return out, nil
}

// CreateMany 按 Count(n) 数量生成并落库（未设置时 1 个）。
func (f Factory[T]) CreateMany(conn *Connection) ([]T, error) {
	n := f.count
	if n <= 0 {
		n = 1
	}
	return f.Create(conn, n)
}

// CreateOne 生成单个模型并落库（对应 Laravel createOne）。
func (f Factory[T]) CreateOne(conn *Connection) (T, error) {
	out, err := f.Create(conn, 1)
	if err != nil {
		var zero T
		return zero, err
	}
	return out[0], nil
}

// FactoryHas 最小可用的 has 关系工厂组合（对应 Laravel has）：
// 先用 parentFactory 创建父模型，再以 parent 主键填充 childFactory 模型的
// foreignKey 外键列创建 count 个子模型；返回 (父模型, 子模型集合)。
func FactoryHas[P any, C any](conn *Connection, parentFactory Factory[P], childFactory Factory[C], foreignKey string, count int) (P, []C, error) {
	parent, err := parentFactory.CreateOne(conn)
	if err != nil {
		var zero P
		return zero, nil, err
	}
	if foreignKey == "" {
		foreignKey = stripPlural(modelTableName(any(parent).(any))) + "_id"
	}
	parentKey := modelKeyValue(any(parent).(any))
	if count <= 0 {
		count = 1
	}
	children := make([]C, 0, count)
	for i := 1; i <= count; i++ {
		child := childFactory.makeOne(i)
		if err := setModelAttribute(any(child).(any), foreignKey, parentKey); err != nil {
			return parent, children, err
		}
		if err := conn.Create(any(child).(any)); err != nil {
			return parent, children, err
		}
		for _, hook := range childFactory.afterCreating {
			hook(child)
		}
		children = append(children, child)
	}
	return parent, children, nil
}

// SeederFunc 种子函数。
type SeederFunc func(*Connection) error

// Seed 依次执行种子；单个种子失败即中止（对应 Laravel db:seed）。
func Seed(conn *Connection, seeders ...SeederFunc) error {
	for i, seeder := range seeders {
		if err := seeder(conn); err != nil {
			return fmt.Errorf("seeder [%d] failed: %w", i+1, err)
		}
	}
	return nil
}
