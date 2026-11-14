package field

import "time"

type IDAttr struct {
	ID uint `torm:"primary_key" json:"id"`
}

type CreatedAtAttr struct {
	CreatedAt time.Time `torm:"created_at" json:"created_at"`
}

type UpdatedAtAttr struct {
	UpdatedAt time.Time `torm:"updated_at" json:"updated_at"`
}

type DeletedAtAttr struct {
	DeletedAt time.Time `torm:"deleted_at" json:"deleted_at"`
}

// ModelStateAttr 模型状态挂载点：内嵌进 torm.Model 后，original/changes/exists
// 状态直接随模型实例存储（代替全局状态表对模型指针的强引用），长驻进程
// 不再需要手动 ForgetModel。State 的具体类型由 torm 包持有（any），
// 内嵌字段不参与数据库列解析（tormstate tag）。
type ModelStateAttr struct {
	State any `tormstate:"1"`
}

// TormState 供 torm 包反射定位的状态指针读取入口（字段级访问辅助）。
func (m *ModelStateAttr) TormState() any { return m.State }

// SetTormState 供 torm 包写入状态指针。
func (m *ModelStateAttr) SetTormState(v any) { m.State = v }
