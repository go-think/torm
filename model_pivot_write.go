package torm

import (
	"strings"

	"github.com/go-think/torm/grammar"
)

// ---- pivot 写侧公共抽象（BelongsToMany / MorphToMany 共用）----
//
// MorphToMany 此前只有读侧（attach/detach/sync 族仅收 BelongsToMany），
// 标签打点/多态收藏等写场景无 API。本文件给出两种关系的 pivot 写侧规格，
// 写函数（Attach/Detach/Sync/Toggle/UpdateExistingPivot/SyncWithPivotValues）
// 经 PivotRelation 接口接受两者。

// PivotSpec pivot 写侧归一规格。
type PivotSpec struct {
	Pivot      string // pivot 表名
	FPK        string // 父侧键列
	RPK        string // 目标侧键列
	TypeCol    string // 多态类型列（BTM 为空）
	MorphKey   string // 类型列取值（HasType 时有效）
	HasType    bool
	Timestamps bool // attach/sync 更新时自动补 created_at/updated_at
	// Attrs WithPivotValue 固定值（attach 属性打底；同时参与 sync/detach 范围）
	Attrs map[string]any
	// ScopeConds wherePivot 族条件（参与读取与 sync/detach 范围）
	ScopeConds []PivotScopeCond
}

// PivotScopeCond wherePivot 族的单条条件（basic/in/notIn/between/notBetween/null/notNull）。
type PivotScopeCond struct {
	Col    string
	Op     string
	Value  any
	Values []any
	Kind   string
}

// PivotRelation belongsToMany / morphToMany 的 pivot 写侧关系接口。
type PivotRelation interface {
	Relation
	pivotWriteSpec(parent any) PivotSpec
}

// pivotWriteSpec BelongsToMany 写侧规格。
func (r BelongsToMany) pivotWriteSpec(parent any) PivotSpec {
	pivot, fpk, rpk, _ := btmResolvedKeys(r, modelTableName(parent))
	spec := PivotSpec{
		Pivot:      pivot,
		FPK:        fpk,
		RPK:        rpk,
		Timestamps: r.PivotTimestamps,
		Attrs:      r.withPivotValueAttrs(nil),
	}
	for _, w := range r.pivotWheres {
		kind := w.kind
		if kind == "" && w.isIn {
			kind = "in"
		}
		spec.ScopeConds = append(spec.ScopeConds, PivotScopeCond{Col: w.column, Op: w.operator, Value: w.value, Values: w.values, Kind: kind})
	}
	return spec
}

// pivotWriteSpec MorphToMany 写侧规格（多态 pivot：morph_id/morph_type 指向父）。
// 类型列并入 Attrs：attach 必须写入类型值，否则行为永久孤儿行。
func (r MorphToMany) pivotWriteSpec(parent any) PivotSpec {
	pivot, fpk, rpk, _, typeCol, morphKey := mtmResolvedKeys(r, modelTableName(parent), r.MorphName)
	key := morphKeyForTable(morphKey, modelTableName(parent))
	return PivotSpec{
		Pivot:    pivot,
		FPK:      fpk,
		RPK:      rpk,
		TypeCol:  typeCol,
		MorphKey: key,
		HasType:  true,
		Attrs:    map[string]any{typeCol: key},
	}
}

// pivotScopeSQL 编译 pivot 写侧范围条件（类型过滤 + wherePivot 族 + 固定值），
// 追加到 where 子句后返回 SQL 片段（以 " and " 前缀连接）与绑定。
func pivotScopeSQL(g grammar.Grammar, spec PivotSpec) (string, []any) {
	var conds []string
	var binds []any
	if spec.HasType {
		conds = append(conds, g.Wrap(spec.TypeCol)+" = ?")
		binds = append(binds, spec.MorphKey)
	}
	for _, c := range spec.ScopeConds {
		col := g.Wrap(c.Col)
		switch c.Kind {
		case "in", "notIn":
			op := "in"
			if c.Kind == "notIn" {
				op = "not in"
			}
			if len(c.Values) == 0 {
				conds = append(conds, map[bool]string{true: "1 = 0", false: "1 = 1"}[c.Kind == "in"])
				continue
			}
			conds = append(conds, col+" "+op+" ("+strings.Repeat("?, ", len(c.Values)-1)+"?)")
			binds = append(binds, c.Values...)
		case "between", "notBetween":
			op := "between"
			if c.Kind == "notBetween" {
				op = "not between"
			}
			conds = append(conds, col+" "+op+" ? and ?")
			binds = append(binds, c.Values...)
		case "null":
			conds = append(conds, col+" is null")
		case "notNull":
			conds = append(conds, col+" is not null")
		default:
			op := c.Op
			if op == "" {
				op = "="
			}
			conds = append(conds, col+" "+op+" ?")
			binds = append(binds, c.Value)
		}
	}
	// WithPivotValue 固定值参与 sync/detach 范围
	for _, col := range sortedColumnNames(spec.Attrs) {
		conds = append(conds, g.Wrap(col)+" = ?")
		binds = append(binds, spec.Attrs[col])
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " and " + strings.Join(conds, " and "), binds
}

// attachRowsSQL 单条多值 INSERT（批量 attach 形态；每行新分配
// 绑定切片——连接层可能保留引用）。
func attachRowsSQL(g grammar.Grammar, spec PivotSpec, cols []string, rows [][]any) (string, []any) {
	colSQL := joinWrappedCols(g, cols)
	rowSQL := make([]string, 0, len(rows))
	var binds []any
	for _, row := range rows {
		rowSQL = append(rowSQL, "("+strings.Repeat("?, ", len(row)-1)+"?)")
		binds = append(binds, row...)
	}
	sql := "insert into " + g.WrapTable(spec.Pivot) + " (" + colSQL + ") values " + strings.Join(rowSQL, ", ")
	return sql, binds
}
