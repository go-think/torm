package torm

import (
	"fmt"
)

// ---- Factory / Seeder（对应 Laravel 的工厂与种子）----

// Factory 模型工厂：Definition 按 1 起始的序号生成模型。
// T 为模型指针类型（如 *Post）以支持 Create 走完整生命周期。
type Factory[T any] struct {
	Definition func(seq int) T
	Table      string // 可选：批量插入的表名（Create 用 Create 生命周期则不需要）
}

// Make 生成 count 个模型实例（不落库，对应 Laravel make）。
func (f Factory[T]) Make(count int) []T {
	out := make([]T, 0, count)
	for i := 1; i <= count; i++ {
		out = append(out, f.Definition(i))
	}
	return out
}

// Create 生成并落库：逐条走 Create 生命周期（时间戳/观察者/主键回填）。
func (f Factory[T]) Create(conn *Connection, count int) ([]T, error) {
	out := make([]T, 0, count)
	for i := 1; i <= count; i++ {
		item := f.Definition(i)
		if err := conn.Create(item); err != nil {
			return out, err
		}
		out = append(out, item)
	}
	return out, nil
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
