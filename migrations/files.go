package migrations

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	torm "github.com/go-think/torm"
)

// SQL 文件迁移加载器（目录扫描 + 文件约定，与常见文件式迁移方案一致）：
//
//	NNNN_name.up.sql    必需，正向迁移 SQL；
//	NNNN_name.down.sql  可选，回滚 SQL（缺失时 Down 为 nil，回滚跳过并保留记录）。
//
// NNNN 为数字序号或时间戳（如 0001、20260101000000），LoadDir 按 NNNN 升序、
// 同号按名称升序返回 Migration{Name, Up, Down}，Name 即 "NNNN_name"。

// LoadDir 扫描目录下的成对 SQL 文件迁移并生成 Migration 切片。
// Up/Down 闭包整段执行文件内容（经 SchemaBuilder.Statement）：多语句文件
// 要求驱动支持单次 Exec 多语句（MySQL 需 DSN multiStatements=true，
// SQLite 原生支持）。孤儿 .down.sql（无对应 .up.sql）警告并跳过；
// 无数字前缀的文件名报错（保证排序确定性）。
func LoadDir(dir string) ([]Migration, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	type sqlPair struct {
		seq      int64
		base     string
		upFile   string
		downFile string
	}
	pairs := map[string]*sqlPair{}
	names := make([]string, 0, len(entries)) // 发现序，保证结果稳定
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		var kind, base string
		switch {
		case strings.HasSuffix(name, ".up.sql"):
			base, kind = strings.TrimSuffix(name, ".up.sql"), "up"
		case strings.HasSuffix(name, ".down.sql"):
			base, kind = strings.TrimSuffix(name, ".down.sql"), "down"
		default:
			continue // 非迁移对文件忽略
		}
		seq, err := parseSeqPrefix(base)
		if err != nil {
			return nil, fmt.Errorf("migrations: %s: %w", name, err)
		}
		p, ok := pairs[base]
		if !ok {
			p = &sqlPair{seq: seq, base: base}
			pairs[base] = p
			names = append(names, base)
		}
		if kind == "up" {
			p.upFile = filepath.Join(dir, name)
		} else {
			p.downFile = filepath.Join(dir, name)
		}
	}

	list := make([]*sqlPair, 0, len(pairs))
	for _, base := range names {
		p := pairs[base]
		if p.upFile == "" {
			// 孤儿 .down.sql：无对应 .up.sql，警告而非静默跳过。
			log.Printf("migrations: %s has no matching .up.sql, skipped", p.downFile)
			continue
		}
		list = append(list, p)
	}
	// 按序号升序（时间戳或序号），同号按名称升序。
	sort.Slice(list, func(i, j int) bool {
		if list[i].seq != list[j].seq {
			return list[i].seq < list[j].seq
		}
		return list[i].base < list[j].base
	})

	out := make([]Migration, 0, len(list))
	for _, p := range list {
		upSQL, err := os.ReadFile(p.upFile)
		if err != nil {
			return nil, err
		}
		mig := Migration{Name: p.base, Up: sqlMigrationClosure(strings.TrimSpace(string(upSQL)))}
		if p.downFile != "" {
			downSQL, err := os.ReadFile(p.downFile)
			if err != nil {
				return nil, err
			}
			mig.Down = sqlMigrationClosure(strings.TrimSpace(string(downSQL)))
		}
		out = append(out, mig)
	}
	return out, nil
}

// sqlMigrationClosure 生成整段执行 SQL 的迁移闭包。
func sqlMigrationClosure(sql string) func(*torm.SchemaBuilder) error {
	return func(s *torm.SchemaBuilder) error {
		return s.Statement(sql)
	}
}

// parseSeqPrefix 解析 "NNNN_name" 的数字前缀（NNNN 为序号或时间戳，
// 后面必须紧跟下划线与名称段）。
func parseSeqPrefix(base string) (int64, error) {
	i := 0
	for i < len(base) && base[i] >= '0' && base[i] <= '9' {
		i++
	}
	if i == 0 || i >= len(base) || base[i] != '_' {
		return 0, fmt.Errorf("name must follow the \"NNNN_name\" convention (numeric prefix + underscore + name)")
	}
	seq, err := strconv.ParseInt(base[:i], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid numeric prefix %q", base[:i])
	}
	return seq, nil
}
