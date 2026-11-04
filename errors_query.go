package torm

import (
	"errors"
	"strings"
)

// ErrorKind 错误分类（对应 Laravel QueryException 对错误码的解析）。
type ErrorKind int

const (
	KindGeneric ErrorKind = iota
	KindUniqueConstraint
	KindDeadlock
	KindLostConnection
)

// Error 携带 SQL 与分类的查询错误。
type Error struct {
	Kind     ErrorKind
	Err      error
	Sql      string
	Bindings []any
}

func (e *Error) Error() string {
	return "torm: " + e.Err.Error() + " (sql: " + e.Sql + ")"
}

func (e *Error) Unwrap() error { return e.Err }

// sqlstate 探测 PG 的 SQLSTATE 码（lib/pq 等驱动实现接口）。
type sqlStateError interface {
	SQLState() string
}

// classifyError 依据错误文本/错误码分类。
func classifyError(err error) ErrorKind {
	if err == nil {
		return KindGeneric
	}
	msg := strings.ToLower(err.Error())

	// MySQL 错误码（go-sql-driver 的文本形式 "Error 1062: ..."）
	switch {
	case strings.Contains(msg, "1062") && strings.Contains(msg, "duplicate entry"),
		strings.Contains(msg, "23505"), strings.Contains(msg, "unique constraint"):
		return KindUniqueConstraint
	case strings.Contains(msg, "1213") && strings.Contains(msg, "deadlock"),
		strings.Contains(msg, "1205") && strings.Contains(msg, "lock wait timeout"),
		strings.Contains(msg, "40k01"), strings.Contains(msg, "40001"):
		return KindDeadlock
	case strings.Contains(msg, "server has gone away"),
		strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "connection reset"),
		strings.Contains(msg, "bad connection"),
		strings.Contains(msg, "broken pipe"):
		return KindLostConnection
	}

	var ss sqlStateError
	if errors.As(err, &ss) {
		switch ss.SQLState() {
		case "23505":
			return KindUniqueConstraint
		case "40001", "40P01":
			return KindDeadlock
		case "08000", "08003", "08006":
			return KindLostConnection
		}
	}
	return KindGeneric
}

// wrapQueryError 把驱动错误包装为 *Error（已分类的错误原样返回）。
func wrapQueryError(err error, sql string, bindings []any) error {
	if err == nil {
		return nil
	}
	var qe *Error
	if errors.As(err, &qe) {
		return err
	}
	return &Error{Kind: classifyError(err), Err: err, Sql: sql, Bindings: bindings}
}
