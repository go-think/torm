package torm

import (
	"reflect"
	"sync"
)

// ---- Morph Map（多态类型别名映射）----
// 把多态类型串映射到模型零值：类型列取别名而非表名/FQCN。

var (
	morphMapMu sync.RWMutex
	morphMap   = map[string]any{}
)

// SetMorphMap 注册别名→模型映射（覆盖同名别名；全局生效）。
func SetMorphMap(m map[string]any) {
	morphMapMu.Lock()
	defer morphMapMu.Unlock()
	for alias, model := range m {
		morphMap[alias] = model
	}
}

// ResetMorphMap 清空映射（测试辅助）。
func ResetMorphMap() {
	morphMapMu.Lock()
	defer morphMapMu.Unlock()
	morphMap = map[string]any{}
}

// MorphModelForAlias 按别名解析模型零值（未知别名返回 nil）。
func MorphModelForAlias(alias string) any {
	morphMapMu.RLock()
	defer morphMapMu.RUnlock()
	return morphMap[alias]
}

// MorphAliasOf 反查：模型（或其指针）对应的多态别名。
func MorphAliasOf(model any) (string, bool) {
	t := reflect.TypeOf(model)
	for t != nil && (t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice) {
		t = t.Elem()
	}
	if t == nil {
		return "", false
	}
	morphMapMu.RLock()
	defer morphMapMu.RUnlock()
	for alias, m := range morphMap {
		mt := reflect.TypeOf(m)
		for mt != nil && (mt.Kind() == reflect.Ptr || mt.Kind() == reflect.Slice) {
			mt = mt.Elem()
		}
		if mt == t {
			return alias, true
		}
	}
	return "", false
}

// morphKeyForParent 多态类型列取值：MorphKey 显式 > morph map 别名 > 父表名
// （enforceMorphMap 模式下未注册别名直接 panic——严格模式的
// 语义，遵循本库编程期错误 panic ErrInvalid 约定）。
func morphKeyForParent(explicit, parentTable string, parents reflect.Value) string {
	if explicit != "" {
		return explicit
	}
	if parents.Len() > 0 {
		p0 := parents.Index(0)
		if p0.Kind() == reflect.Ptr {
			p0 = p0.Elem()
		}
		if p0.Kind() == reflect.Struct {
			if alias, ok := MorphAliasOf(p0.Addr().Interface()); ok {
				return alias
			}
		}
	}
	if morphEnforcedMode() {
		panic(ErrInvalid{Msg: "torm: morph map enforced but model table " + parentTable + " is not registered"})
	}
	return parentTable
}

// morphKeyForTable 按父表名解析多态键（WhereHas/WithCount 等无父实例的路径）：
// MorphKey 显式 > morph map 中表名匹配的别名 > 父表名。旧实现各路径直接回退
// 父表名，与写侧/eager 的别名取值不一致——注册 morph map 后 WhereHas 会与
// 写入值错位。
func morphKeyForTable(explicit, parentTable string) string {
	if explicit != "" {
		return explicit
	}
	morphMapMu.RLock()
	for alias, m := range morphMap {
		if m == nil {
			continue // 注册 nil 值跳过（inspectModel 对 nil panic）
		}
		if inspectModel(m).table == parentTable {
			morphMapMu.RUnlock()
			return alias
		}
	}
	morphMapMu.RUnlock()
	if morphEnforcedMode() {
		panic(ErrInvalid{Msg: "torm: morph map enforced but model table " + parentTable + " is not registered"})
	}
	return parentTable
}

// EnforceMorphMap 强制多态映射：注册映射后，未注册的模型在多态键解析时
// 返回错误而非静默回退表名（严格模式语义）。
var (
	morphEnforceMu sync.Mutex
	morphEnforced  bool
)

func EnforceMorphMap(m map[string]any) {
	SetMorphMap(m)
	morphEnforceMu.Lock()
	morphEnforced = true
	morphEnforceMu.Unlock()
}

// morphEnforcedMode 报告是否处于强制映射模式。
func morphEnforcedMode() bool {
	morphEnforceMu.Lock()
	defer morphEnforceMu.Unlock()
	return morphEnforced
}
