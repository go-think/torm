package torm

import (
	"reflect"
	"sync"
)

// ---- Morph Map（对应 Laravel Relation::enforceMorphMap）----
// 把多态类型串映射到模型零值：类型列取别名而非表名/FQCN。

var (
	morphMapMu sync.RWMutex
	morphMap   = map[string]any{}
)

// SetMorphMap 注册别名→模型映射（覆盖同名别名；对齐 Laravel Relation::enforceMorphMap 的全局语义）。
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

// morphKeyForParent 多态类型列取值：MorphKey 显式 > morph map 别名 > 父表名。
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
	return parentTable
}
