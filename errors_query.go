package torm

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	gomysql "github.com/go-sql-driver/mysql"
	sqlite3 "github.com/mattn/go-sqlite3"
)

// ErrorKind 错误分类（按驱动错误码/SQLSTATE 解析）。
type ErrorKind int

const (
	KindGeneric ErrorKind = iota
	KindUniqueConstraint
	KindDeadlock
	KindLostConnection
)

// Error 携带 SQL 与分类的查询错误（查询异常包装）。
type Error struct {
	Kind     ErrorKind
	Err      error
	Sql      string
	Bindings []any
	ConnName string
}

func (e *Error) Error() string {
	msg := "torm: " + e.Err.Error() + " (sql: " + e.Sql + ")"
	if e.ConnName != "" {
		msg = "torm: " + e.Err.Error() + " (connection: " + e.ConnName + ", sql: " + e.Sql + ")"
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// ConnectionName 返回发生错误的连接名。
func (e *Error) ConnectionName() string { return e.ConnName }

// GetSql 返回原始 SQL。
func (e *Error) GetSql() string { return e.Sql }

// GetBindings 返回绑定。
func (e *Error) GetBindings() []any { return e.Bindings }

// RawSql 返回绑定插值后的 SQL（getRawSql：字符串加引号转义、nil→NULL）。
func (e *Error) RawSql() string {
	return substituteBindingsIntoRawSql(e.Sql, e.Bindings)
}

// substituteBindingsIntoRawSql 把 ? 占位符替换为字面值；PG 的 $n 编号占位符
// 同样支持（lib/pq/pgx 风格 SQL 的 RawSql 插值，对应各方言 grammar
// 的 substituteBindings 实现）。
func substituteBindingsIntoRawSql(sql string, bindings []any) string {
	if strings.Contains(sql, "?") {
		var b strings.Builder
		bi := 0
		for i := 0; i < len(sql); i++ {
			ch := sql[i]
			if ch == '?' && bi < len(bindings) {
				b.WriteString(quoteBinding(bindings[bi]))
				bi++
				continue
			}
			b.WriteByte(ch)
		}
		return b.String()
	}
	if !numberedPlaceholderPattern.MatchString(sql) {
		return sql
	}
	return numberedPlaceholderPattern.ReplaceAllStringFunc(sql, func(m string) string {
		n, err := strconv.Atoi(m[1:])
		if err != nil || n < 1 || n > len(bindings) {
			return m
		}
		return quoteBinding(bindings[n-1])
	})
}

var numberedPlaceholderPattern = regexp.MustCompile(`\$([0-9]+)`)

// quoteBinding 字面值引用：nil→NULL、string/time→'..'（单引号双写）、bool→0/1、
// 数值原样、其余用 fmt（近似方言 grammar 的 getBindingRepresentation）。
func quoteBinding(v any) string {
	switch val := v.(type) {
	case nil:
		return "null"
	case bool:
		if val {
			return "1"
		}
		return "0"
	case string:
		return "'" + strings.ReplaceAll(val, "'", "''") + "'"
	case []byte:
		return "'" + strings.ReplaceAll(string(val), "'", "''") + "'"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return fmt.Sprintf("%v", val)
	default:
		return "'" + strings.ReplaceAll(fmt.Sprintf("%v", val), "'", "''") + "'"
	}
}

// sqlstate 探测 PG 的 SQLSTATE 码（lib/pq 等驱动实现接口）。
type sqlStateError interface {
	SQLState() string
}

// lostConnectionPatterns 断连判定的错误消息模式
// （约 70 条，小写化匹配）。
var lostConnectionPatterns = []string{
	"server has gone away",
	"no connection to the server",
	"lost connection",
	"is dead or not enabled",
	"error while sending",
	"decryption failed or bad record mac",
	"server closed the connection unexpectedly",
	"ssl connection has been closed unexpectedly",
	"error writing data to the connection",
	"resource deadlock avoided",
	"transaction() on null",
	"child connection forced to terminate due to client_idle_limit",
	"query_wait_timeout",
	"reset by peer",
	"physical connection is not usable",
	"tcp provider: error code 0x68",
	"ora-03114",
	"packets out of order. expected",
	"adaptive server connection failed",
	"communication link failure",
	"connection is no longer usable",
	"login timeout expired",
	"connection refused",
	"running with the --read-only option so it cannot execute this statement",
	"the connection is broken and recovery is not possible",
	"php_network_getaddresses: getaddrinfo failed",
	"php_network_getaddresses: getaddrinfo for",
	"ssl syscall error: eof detected",
	"ssl error: unexpected eof",
	"connection timed out",
	"the last transaction was aborted due to seamless scaling",
	"temporary failure in name resolution",
	"could not connect to server: connection refused",
	"ssl syscall error: no route to host",
	"the client was disconnected by the server because of inactivity",
	"could not translate host name",
	"tcp provider: error code 0x274c",
	"no such file or directory",
	"ssl: operation timed out",
	"reason: server is in script upgrade mode",
	"unknown $curl_error_code: 77",
	"ssl: handshake timed out",
	"ssl error: sslv3 alert unexpected message",
	"ssl error: ssl/tls alert unexpected message",
	"unrecognized ssl error code:",
	"access denied for user",
	"no connection could be made because the target machine actively refused it",
	"a connection attempt failed because the connected party did not properly respond",
	"network is unreachable",
	"the requested address is not valid in its context",
	"a socket operation was attempted to an unreachable network",
	"operation now in progress",
	"operation in progress",
	"general error: 3989",
	"went away",
	"bad connection",
	"broken pipe",
	"invalid connection",
	"connection reset",
}

// matchesAny 报告消息是否含任一模式（大小写不敏感）。
func matchesAny(lowerMsg string, patterns []string) bool {
	for _, p := range patterns {
		if strings.Contains(lowerMsg, p) {
			return true
		}
	}
	return false
}

// classifyError 依据驱动结构化错误码/SQLSTATE/错误文本分类。
// 结构化通道优先（按驱动错误码判定；纯文本匹配易被 PG 消息内嵌的
// 绑定值误判——如 "invalid input syntax ...: \"40001\""），文本匹配仅兜底。
func classifyError(err error) ErrorKind {
	if err == nil {
		return KindGeneric
	}

	// 1) MySQL 结构化错误码（go-sql-driver）
	var me *gomysql.MySQLError
	if errors.As(err, &me) {
		switch me.Number {
		case 1062:
			return KindUniqueConstraint
		case 1213, 1205:
			return KindDeadlock
		}
	}

	// 2) SQLite 结构化错误码（mattn/go-sqlite3）
	var se *sqlite3.Error
	if errors.As(err, &se) {
		switch se.Code {
		case sqlite3.ErrConstraint:
			// 19 下的唯一约束细类（1555/2067）按文本二次判定
			if strings.Contains(strings.ToLower(se.Error()), "unique") {
				return KindUniqueConstraint
			}
		case sqlite3.ErrBusy, sqlite3.ErrLocked:
			return KindDeadlock
		}
	}

	// 3) SQLSTATE（lib/pq、pgx、mssqldb 等实现 SQLState 接口的驱动）
	var ss sqlStateError
	if errors.As(err, &ss) {
		switch ss.SQLState() {
		case "23505":
			return KindUniqueConstraint
		case "40001", "40P01":
			return KindDeadlock
		case "08000", "08003", "08006", "08001", "08004", "08007":
			return KindLostConnection
		}
	}

	// 4) 文本兜底
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "1062") && strings.Contains(msg, "duplicate entry"),
		strings.Contains(msg, "23505"), strings.Contains(msg, "unique constraint"),
		// SQL Server（mssqldb 不实现 SQLState()，按消息文本判定）
		strings.Contains(msg, "violation of unique key constraint"),
		strings.Contains(msg, "cannot insert duplicate key row"):
		return KindUniqueConstraint
	case strings.Contains(msg, "1213") && strings.Contains(msg, "deadlock"),
		strings.Contains(msg, "1205") && strings.Contains(msg, "lock wait timeout"),
		strings.Contains(msg, "40001"), strings.Contains(msg, "40p01"),
		strings.Contains(msg, "deadlock detected"):
		return KindDeadlock
	case strings.Contains(msg, "a table in the database is locked"),
		strings.Contains(msg, "has been chosen as the deadlock victim"),
		strings.Contains(msg, "wsrep detected deadlock"),
		strings.Contains(msg, "record has changed since last read in table"),
		strings.Contains(msg, "deadlock found when trying to get lock"),
		strings.Contains(msg, "database is locked"),
		strings.Contains(msg, "the database file is locked"),
		strings.Contains(msg, "database table is locked"):
		return KindDeadlock
	case matchesAny(msg, lostConnectionPatterns):
		return KindLostConnection
	}
	return KindGeneric
}

// wrapQueryError 把驱动错误包装为 *Error（已分类的错误原样返回；无连接名场景）。
func wrapQueryError(err error, sql string, bindings []any) error {
	return newQueryError("", err, sql, bindings)
}

// newQueryError 把驱动错误包装为携带连接名的 *Error（构造时传入
// 连接名 $connectionName）；已是 *Error 的错误在缺失
// ConnName 时补填。
func newQueryError(connName string, err error, sql string, bindings []any) error {
	if err == nil {
		return nil
	}
	var qe *Error
	if errors.As(err, &qe) {
		if qe.ConnName == "" {
			qe.ConnName = connName
		}
		return err
	}
	return &Error{Kind: classifyError(err), Err: err, Sql: sql, Bindings: bindings, ConnName: connName}
}
