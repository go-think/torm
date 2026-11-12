package torm

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log"
	"math/rand"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/thinkoner/torm/events"
	"github.com/thinkoner/torm/grammar"
)

// ErrMultipleColumnsSelected 对应 Laravel 的 MultipleColumnsSelectedException
// （Scalar 命中多列时抛出）。
var ErrMultipleColumnsSelected = errors.New("torm: multiple columns selected")

type Connection struct {
	DB          *sql.DB
	tablePrefix string
	driver      string
	dsn         string
	config      Config // 完整连接配置（重连时保留方言参数）
	readDB      *sql.DB // 读写分离的只读连接（Config.ReadDsn / ReadDsns）
	readDsn     string  // 实际选中的只读 DSN
	readDsns    []string

	// 连接标识（对应 Laravel $name / $readWriteType）
	name          string
	readWriteType string
	// pinnedType 管理层 ::read/::write 路由固定（对应 setPdoForType）
	pinnedType string

	// 事务状态（Laravel transactions 计数语义）
	txMu               sync.Mutex
	tx                 *sql.Tx
	level              int
	afterCommitHooks   []txHook
	afterRollbackHooks []txHook

	// 执行前回调（对应 Laravel beforeStartingTransaction / beforeExecuting）
	beforeStartingTransaction []func()
	beforeExecutingMu        sync.Mutex
	beforeExecuting          []func(query string, bindings []any)

	// connMu 保护 DB / readDB 的热替换（断连重连时切换句柄）
	connMu sync.Mutex

	// 查询事件钩子（对应 Laravel QueryExecuted/DB::listen）
	onQuery   func(QueryExecuted) // 兼容：单回调（OnQuery）
	listeners []func(QueryExecuted)

	// 内存查询日志与 dry-run（对应 loggingQueries/queryLog/pretending）
	loggingQueries bool
	queryLog       []QueryExecuted
	pretending     bool

	// 慢查询监听（对应 whenQueryingForLongerThan/totalQueryDuration）
	totalQueryDuration   time.Duration
	queryDurationHandler []queryDurationHandler

	// 读写分离 sticky 状态（对应 recordsModified/readOnWriteConnection/sticky）
	sticky                bool
	readOnWriteConnection bool
	recordsModified       bool

	// 状态锁：保护上述查询日志/监听器/recordsModified 等可变状态
	stateMu sync.Mutex

	// 模型事件分发器（对应 Laravel Dispatcher）
	dispatcher events.Dispatcher

	// 断连重连回调（对应 Laravel reconnector；reconnect() 优先调用，
	// 未设置时走内建重连）
	reconnector func(*Connection) error

	// 服务器版本缓存（对应 getServerVersion 的 PDO::ATTR_SERVER_VERSION，
	// 惰性求值 + 加锁缓存）
	serverVersionMu     sync.Mutex
	serverVersion       string
	serverVersionCached bool
}

type queryDurationHandler struct {
	hasRun bool
	fn     func(QueryExecuted)
}

// ---- 连接标识（对应 Connection::getName / setConnectionName） ----

// SetConnectionName 设置连接名（错误消息与事件将携带该名称）。
func (c *Connection) SetConnectionName(name string) { c.name = name }

// GetName 返回连接名。
func (c *Connection) GetName() string { return c.name }

// nameWithReadWriteType 返回 name[::read|::write]（对应 getNameWithReadWriteType）。
func (c *Connection) nameWithReadWriteType() string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.nameWithReadWriteTypeLocked()
}

func (c *Connection) nameWithReadWriteTypeLocked() string {
	if c.readWriteType != "" {
		return c.name + "::" + c.readWriteType
	}
	return c.name
}

// setReadWriteType 设置读写类型标识（"read"/"write"）。
func (c *Connection) setReadWriteType(t string) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.readWriteType = t
}

// setPinnedType 管理层 ::read/::write 固定路由（对应 setPdoForType）。
func (c *Connection) setPinnedType(t string) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.pinnedType = t
}

// ---- 查询事件监听（对应 DB::listen） ----

// OnQuery 注册查询事件回调（兼容接口：单回调，重复设置时覆盖）。
func (c *Connection) OnQuery(fn func(QueryExecuted)) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.onQuery = fn
}

// Listen 注册查询事件监听器（对应 Connection::listen，支持多监听器）。
func (c *Connection) Listen(fn func(QueryExecuted)) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.listeners = append(c.listeners, fn)
}

// ---- 查询日志（对应 enableQueryLog/getQueryLog/flushQueryLog） ----

// EnableQueryLog 开启内存查询日志。
func (c *Connection) EnableQueryLog() {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.loggingQueries = true
}

// DisableQueryLog 关闭内存查询日志（不清空已记录内容）。
func (c *Connection) DisableQueryLog() {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.loggingQueries = false
}

// GetQueryLog 返回已记录的查询日志快照。
func (c *Connection) GetQueryLog() []QueryExecuted {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return append([]QueryExecuted(nil), c.queryLog...)
}

// FlushQueryLog 清空查询日志。
func (c *Connection) FlushQueryLog() {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.queryLog = nil
}

// Pretend 以 dry-run 方式执行回调（对应 Connection::pretend）：查询被记录
// 但不真正发往数据库；返回期间记录的查询日志。
func (c *Connection) Pretend(fn func(*Connection)) []QueryExecuted {
	c.stateMu.Lock()
	prevLogging, prevLog, prevPretending := c.loggingQueries, c.queryLog, c.pretending
	c.loggingQueries = true
	c.queryLog = nil
	c.pretending = true
	c.stateMu.Unlock()

	defer func() {
		c.stateMu.Lock()
		c.loggingQueries, c.pretending = prevLogging, prevPretending
		c.stateMu.Unlock()
	}()

	fn(c)

	c.stateMu.Lock()
	out := c.queryLog
	c.queryLog = prevLog
	c.stateMu.Unlock()
	return out
}

// ---- 慢查询监听（对应 whenQueryingForLongerThan） ----

// WhenQueryingForLongerThan 当累计查询耗时超过 thresholdMs 毫秒时触发一次
// handler（随附触发时的 QueryExecuted）。
func (c *Connection) WhenQueryingForLongerThan(thresholdMs int64, handler func(QueryExecuted)) {
	c.stateMu.Lock()
	c.queryDurationHandler = append(c.queryDurationHandler, queryDurationHandler{fn: handler})
	key := len(c.queryDurationHandler) - 1
	c.stateMu.Unlock()

	c.Listen(func(e QueryExecuted) {
		c.stateMu.Lock()
		h := &c.queryDurationHandler[key]
		fire := !h.hasRun && c.totalQueryDuration > time.Duration(thresholdMs)*time.Millisecond
		if fire {
			h.hasRun = true
		}
		c.stateMu.Unlock()
		if fire {
			handler(e)
		}
	})
}

// TotalQueryDuration 返回累计查询耗时（毫秒）。
func (c *Connection) TotalQueryDuration() float64 {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return float64(c.totalQueryDuration) / float64(time.Millisecond)
}

// ResetTotalQueryDuration 清零累计查询耗时。
func (c *Connection) ResetTotalQueryDuration() {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.totalQueryDuration = 0
}

// logQuery 记录/分发查询事件（对应 Connection::logQuery）。
func (c *Connection) logQuery(sqlStr string, bindings []any, d time.Duration, rw string) {
	c.stateMu.Lock()
	c.totalQueryDuration += d
	e := QueryExecuted{
		Sql:            sqlStr,
		Bindings:       bindings,
		TimeNs:         int64(d),
		RawSql:         substituteBindingsIntoRawSql(sqlStr, bindings),
		ConnectionName: c.nameWithReadWriteTypeLocked(),
		ReadWriteType:  rw,
	}
	if c.pretending {
		// dry-run 时记录插值后的原始 SQL（对应 Laravel pretending 分支）
		e.Sql = e.RawSql
	}
	hasListener := c.onQuery != nil || len(c.listeners) > 0
	var listeners []func(QueryExecuted)
	if c.onQuery != nil {
		listeners = append(listeners, c.onQuery)
	}
	listeners = append(listeners, c.listeners...)
	if c.loggingQueries {
		c.queryLog = append(c.queryLog, e)
	}
	c.stateMu.Unlock()

	if !hasListener {
		log.Println(sqlStr)
		return
	}
	for _, fn := range listeners {
		fn(e)
	}
}

// readWriteTypeFor 返回本次读查询将使用的连接类型标识（"read"/"write"）。
func (c *Connection) readWriteTypeFor(forceWrite bool) string {
	c.txMu.Lock()
	tx := c.tx
	c.txMu.Unlock()
	if tx == nil {
		c.stateMu.Lock()
		pinned := c.pinnedType
		readOnWrite := c.readOnWriteConnection
		stickyHit := c.recordsModified && c.sticky
		c.stateMu.Unlock()
		if c.hasReadDB() && (pinned == "read" ||
			(!forceWrite && pinned != "write" && !readOnWrite && !stickyHit)) {
			return "read"
		}
	}
	return "write"
}

// hasReadDB 报告是否配置了只读连接。
func (c *Connection) hasReadDB() bool {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	return c.readDB != nil
}

// ---- run() 语义统一（对应 Connection::run / runQueryCallback /
// handleQueryException / tryAgainIfCausedByLostConnection） ----

// run 所有执行路径的统一入口：计时、错误包装为 *Error、断连重试、查询日志。
// read 标记读路径（走 readQueryer 路由，含事务/只读/sticky 语义），写路径走
// queryer()。
func (c *Connection) run(query string, bindings []any, rw string, read, forceWrite bool, fn func(queryer) error) error {
	// Laravel：beforeExecuting 在 pretending 检查之前触发（Connection.php:799-803）
	c.fireBeforeExecuting(query, bindings)

	if c.isPretending() {
		c.logQuery(query, bindings, 0, rw)
		return nil
	}

	start := time.Now()
	err := c.runQueryCallback(query, bindings, read, forceWrite, fn)
	if err != nil {
		err = c.handleQueryException(err, query, bindings, read, forceWrite, fn)
	}
	c.logQuery(query, bindings, time.Since(start), rw)
	return err
}

// runQueryCallback 执行回调，错误统一包装为携带连接名的 *Error。
func (c *Connection) runQueryCallback(query string, bindings []any, read, forceWrite bool, fn func(queryer) error) error {
	if err := fn(c.routeQueryer(read, forceWrite)); err != nil {
		return c.wrapQueryError(err, query, bindings)
	}
	return nil
}

// routeQueryer 按路径解析执行器：读路径走 readQueryer（事务 > 只读路由 > 主
// 连接），写路径走 queryer()（事务 > 主连接）。
func (c *Connection) routeQueryer(read, forceWrite bool) queryer {
	if read {
		return c.readQueryer(forceWrite)
	}
	return c.queryer()
}

// handleQueryException 无事务时断连类错误：重连并重跑一次。
func (c *Connection) handleQueryException(err error, query string, bindings []any, read, forceWrite bool, fn func(queryer) error) error {
	if c.TransactionLevel() >= 1 {
		return err
	}
	var qe *Error
	if errors.As(err, &qe) && qe.Kind == KindLostConnection {
		if rerr := c.reconnect(); rerr == nil {
			return c.runQueryCallback(query, bindings, read, forceWrite, fn)
		}
	}
	return err
}

// reconnect 重建底层连接（对应 Connection::reconnect）：设置了 reconnector
// 时优先交由回调重建（Manager 挂载的回调保留同一实例换句柄）；否则走内建
// 重连（重新 sql.Open 并换入新句柄，只读连接一并重建）。
func (c *Connection) reconnect() error {
	c.stateMu.Lock()
	fn := c.reconnector
	c.stateMu.Unlock()
	if fn != nil {
		return fn(c)
	}

	c.connMu.Lock()
	defer c.connMu.Unlock()

	connector := connectorFor(c.driver, c.configForReconnect())
	db, err := connector.Connect(c.dsn)
	if err != nil {
		return err
	}
	old := c.DB
	c.DB = db
	if old != nil {
		_ = old.Close()
	}
	if c.readDsn != "" {
		if rdb, rerr := connector.Connect(c.readDsn); rerr == nil {
			if c.readDB != nil {
				_ = c.readDB.Close()
			}
			c.readDB = rdb
		}
	}
	return nil
}

// configForReconnect 重连所需的连接配置：持有完整 config（openNamed/Manager
// 存入）时原样返回，方言参数（charset/isolation/pragmas 等）在重连后照常
// 生效；否则按字段合成最小配置（兼容 Open 直建的连接）。
func (c *Connection) configForReconnect() Config {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	cfg := c.config
	if cfg.Driver == "" {
		cfg.Driver = c.driver
	}
	if cfg.Dsn == "" {
		cfg.Dsn = c.dsn
	}
	if cfg.Prefix == "" {
		cfg.Prefix = c.tablePrefix
	}
	return cfg
}

// setConfig 存储完整连接配置（Manager 建连后调用；重连/内省需要方言参数）。
func (c *Connection) setConfig(cfg Config) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.config = cfg
}

// GetConfig 返回连接配置（对应 Connection::getConfig）。
func (c *Connection) GetConfig() Config {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.config
}

// GetDatabaseName 返回数据库名（对应 Connection::getDatabaseName）。
func (c *Connection) GetDatabaseName() string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.config.Database
}

// SetDatabaseName 设置数据库名（对应 Connection::setDatabaseName）。
func (c *Connection) SetDatabaseName(name string) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.config.Database = name
}

// wrapQueryError 把驱动错误包装为携带连接名的 *Error。
func (c *Connection) wrapQueryError(err error, query string, bindings []any) error {
	return newQueryError(c.nameWithReadWriteType(), err, query, bindings)
}

// prepareStmt 预处理语句并在成功后分发 StatementPrepared 事件。
func (c *Connection) prepareStmt(q queryer, query string) (*sql.Stmt, error) {
	stmt, err := q.Prepare(query)
	if err != nil {
		return nil, err
	}
	c.stateMu.Lock()
	d := c.dispatcher
	c.stateMu.Unlock()
	if d != nil {
		d.Dispatch(events.StatementPreparedEvent{Sql: query, ConnectionName: c.nameWithReadWriteType()})
	}
	return stmt, nil
}

// Table Begin a fluent query against a database table.
func (c *Connection) Table(table string) *Builder {
	return c.Query().From(table)
}

// Query Get a new query builder instance.
func (c *Connection) Query() *Builder {
	return NewBuilder(c, c.GetQueryGrammar())
}

// SelectOne Run a select statement and return a single result（只取第一行，
// 对应 Laravel selectOne 的 array_shift 语义）。
func (c *Connection) SelectOne(query string, bindings []interface{}, dest interface{}) error {
	return c.runSelect(query, bindings, dest, false, true)
}

// Select Run a select statement against the database.
func (c *Connection) Select(query string, bindings []interface{}, dest interface{}) error {
	return c.runSelect(query, bindings, dest, false, false)
}

// SelectFromWrite 强制走写连接的 select（对应 Laravel useWritePdo）。
func (c *Connection) SelectFromWrite(query string, bindings []interface{}, dest interface{}) error {
	return c.runSelect(query, bindings, dest, true, false)
}

func (c *Connection) runSelect(query string, bindings []interface{}, dest interface{}, forceWrite, limitOne bool) error {
	rw := c.readWriteTypeFor(forceWrite)
	return c.run(query, bindings, rw, true, forceWrite, func(q queryer) error {
		stmt, err := c.prepareStmt(q, query)
		if err != nil {
			return err
		}
		defer stmt.Close()

		rows, err := stmt.Query(c.prepareBindings(bindings)...)
		if err != nil {
			return err
		}
		defer rows.Close()

		columns, err := rows.Columns()
		if err != nil {
			return err
		}

		return c.collectRows(rows, columns, dest, limitOne)
	})
}

// collectRows 把行结果扫描进 dest（struct / struct 切片 / 标量切片）。
func (c *Connection) collectRows(rows *sql.Rows, columns []string, dest interface{}, limitOne bool) error {
	results := reflect.Indirect(reflect.ValueOf(dest))

	kind := results.Kind()
	if kind != reflect.Slice && kind != reflect.Struct {
		return errors.New("unsupported destination, should be slice or struct")
	}

	// struct 目标：直接扫进 dest 自身（dest 为指针，可寻址）
	isStructDest := kind == reflect.Struct
	if isStructDest {
		structType := results.Type()
		fields := make([]*Field, 0, structType.NumField())
		for i := 0; i < structType.NumField(); i++ {
			fields = append(fields, NewField(results.Field(i), structType.Field(i)))
		}
		if rows.Next() {
			if err := c.scan(rows, columns, fields); err != nil {
				return err
			}
		}
		return rows.Err()
	}

	elemType := results.Type().Elem()
	isPtrElem := false
	if elemType.Kind() == reflect.Ptr {
		elemType = elemType.Elem()
		isPtrElem = true
	}

	// 标量元素（pluck 场景）：每行取首列值
	if elemType.Kind() != reflect.Struct {
		for rows.Next() {
			raw := make([]any, len(columns))
			ptrs := make([]any, len(columns))
			for i := range columns {
				ptrs[i] = &raw[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				return err
			}
			elem := reflect.New(elemType)
			var val reflect.Value
			if len(raw) > 0 {
				val = reflect.ValueOf(raw[0])
			}
			if !val.IsValid() {
				val = reflect.Zero(elemType)
			}
			if !val.Type().AssignableTo(elemType) {
				if !val.Type().ConvertibleTo(elemType) {
					return fmt.Errorf("cannot convert %s to %s", val.Type(), elemType)
				}
				val = val.Convert(elemType)
			}
			elem.Elem().Set(val)
			if isPtrElem {
				results.Set(reflect.Append(results, elem))
			} else {
				results.Set(reflect.Append(results, elem.Elem()))
			}
			if limitOne {
				return rows.Err()
			}
		}
		return rows.Err()
	}

	for rows.Next() {
		// 每行分配全新的元素，避免多行共享同一块底层内存
		newElem := reflect.New(elemType)
		elemValue := newElem.Elem()

		fields := make([]*Field, 0, elemType.NumField())
		for i := 0; i < elemType.NumField(); i++ {
			fields = append(fields, NewField(elemValue.Field(i), elemType.Field(i)))
		}

		if err := c.scan(rows, columns, fields); err != nil {
			return err
		}

		if isPtrElem {
			results.Set(reflect.Append(results, newElem))
		} else {
			results.Set(reflect.Append(results, elemValue))
		}
		if limitOne {
			return rows.Err()
		}
	}

	return rows.Err()
}

// Scalar Run a select statement and return the first column of the first row
// （对应 Connection::scalar）；多列时返回 ErrMultipleColumnsSelected。
func (c *Connection) Scalar(query string, bindings []interface{}) (any, error) {
	var value any
	rw := c.readWriteTypeFor(false)
	err := c.run(query, bindings, rw, true, false, func(q queryer) error {
		stmt, err := c.prepareStmt(q, query)
		if err != nil {
			return err
		}
		defer stmt.Close()

		rows, err := stmt.Query(c.prepareBindings(bindings)...)
		if err != nil {
			return err
		}
		defer rows.Close()

		columns, err := rows.Columns()
		if err != nil {
			return err
		}

		if rows.Next() {
			if len(columns) > 1 {
				return ErrMultipleColumnsSelected
			}
			var raw any
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			value = raw
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return value, nil
}

// SelectResultSets 执行查询并返回全部结果集（对应 selectResultSets）。
// Go database/sql 单条语句只产出单个结果集，故返回包含一个结果集的切片。
func (c *Connection) SelectResultSets(query string, bindings []interface{}) ([][]map[string]any, error) {
	set, err := c.SelectMaps(query, bindings)
	if err != nil {
		return nil, err
	}
	return [][]map[string]any{set}, nil
}

// Scan Run a query and scan columns into given destinations.
func (c *Connection) Scan(query string, bindings []interface{}, dest ...interface{}) error {
	rw := c.readWriteTypeFor(false)
	return c.run(query, bindings, rw, true, false, func(q queryer) error {
		stmt, err := c.prepareStmt(q, query)
		if err != nil {
			return err
		}
		defer stmt.Close()

		rows, err := stmt.Query(c.prepareBindings(bindings)...)
		if err != nil {
			return err
		}
		defer rows.Close()

		rows.Next()
		return rows.Scan(c.prepareBindings(dest)...)
	})
}

// Insert Run an insert statement against the database.
func (c *Connection) Insert(query string, args ...interface{}) (int64, int64, error) {
	return c.affectingStatement(query, args...)
}

// InsertGetId 执行 insert 并返回自增 id。
func (c *Connection) InsertGetId(query string, args ...interface{}) (int64, error) {
	insertId, _, err := c.affectingStatement(query, args...)
	return insertId, err
}

// Update Run an update statement against the database.
func (c *Connection) Update(query string, args ...interface{}) (int64, error) {
	_, affected, err := c.affectingStatement(query, args...)
	return affected, err
}

// Delete Run a delete statement against the database.
func (c *Connection) Delete(query string, args ...interface{}) (int64, error) {
	_, affected, err := c.affectingStatement(query, args...)
	return affected, err
}

// Statement Execute an SQL statement and return the boolean result.
func (c *Connection) Statement(query string, args ...interface{}) error {
	err := c.run(query, args, "write", false, false, func(q queryer) error {
		stmt, err := c.prepareStmt(q, query)
		if err != nil {
			return err
		}
		defer stmt.Close()

		_, err = stmt.Exec(c.prepareBindings(args)...)
		return err
	})
	if err == nil {
		c.recordsHaveBeenModified(true)
	}
	return err
}

// SelectMaps 执行查询并把行结果输出为 map 切片（explain/内省用）。
func (c *Connection) SelectMaps(query string, bindings []interface{}) ([]map[string]any, error) {
	var out []map[string]any
	rw := c.readWriteTypeFor(false)
	err := c.run(query, bindings, rw, true, false, func(q queryer) error {
		stmt, err := c.prepareStmt(q, query)
		if err != nil {
			return err
		}
		defer stmt.Close()

		rows, err := stmt.Query(c.prepareBindings(bindings)...)
		if err != nil {
			return err
		}
		defer rows.Close()

		columns, err := rows.Columns()
		if err != nil {
			return err
		}

		out, err = collectMaps(rows, columns)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// collectMaps 把行结果扫描为 map 切片。
func collectMaps(rows *sql.Rows, columns []string) ([]map[string]any, error) {
	out := make([]map[string]any, 0, 8)
	for rows.Next() {
		raw := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range columns {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(columns))
		for i, col := range columns {
			row[col] = raw[i]
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// SetEventDispatcher 挂载事件分发器；生命周期点将同步分发事件对象。
// ConnectionEstablished 由 Open/Manager 在建连时经已挂载的分发器分发
// （Manager.SetEventDispatcher 提供管理器级挂载点）。
func (c *Connection) SetEventDispatcher(d events.Dispatcher) {
	c.stateMu.Lock()
	c.dispatcher = d
	c.stateMu.Unlock()
}

// dispatchEstablishedEvent 分发 ConnectionEstablished（对应
// ConnectionEstablished 事件；无分发器时跳过）。
func (c *Connection) dispatchEstablishedEvent() {
	c.stateMu.Lock()
	d := c.dispatcher
	name := c.name
	c.stateMu.Unlock()
	if d != nil {
		d.Dispatch(events.ConnectionEstablishedEvent{ConnectionName: name})
	}
}

// dispatchEvent 分发模型事件（未挂载分发器时为 no-op）。
func (c *Connection) dispatchEvent(name string, model any) {
	c.stateMu.Lock()
	d := c.dispatcher
	c.stateMu.Unlock()
	if d != nil {
		d.Dispatch(events.ModelEvent{Name: name, Model: model})
	}
}

// BeforeStartingTransaction 注册事务开始前的回调（对应 Laravel 同名 API）。
func (c *Connection) BeforeStartingTransaction(fn func()) {
	c.txMu.Lock()
	c.beforeStartingTransaction = append(c.beforeStartingTransaction, fn)
	c.txMu.Unlock()
}

// BeforeExecuting 注册每条语句执行前的回调（对应 Laravel 同名 API）。
func (c *Connection) BeforeExecuting(fn func(query string, bindings []any)) {
	c.beforeExecutingMu.Lock()
	c.beforeExecuting = append(c.beforeExecuting, fn)
	c.beforeExecutingMu.Unlock()
}

// fireBeforeExecuting 触发语句执行前回调。
func (c *Connection) fireBeforeExecuting(query string, bindings []any) {
	c.beforeExecutingMu.Lock()
	fns := append([]func(string, []any){}, c.beforeExecuting...)
	c.beforeExecutingMu.Unlock()
	for _, fn := range fns {
		fn(query, bindings)
	}
}

// Driver 返回连接驱动名（对应 Laravel Connection::getDriverName）。
func (c *Connection) Driver() string {
	return c.driver
}

// GetQueryGrammar 按 driver 构造方言 grammar，并接线上表前缀。
// 对应 Laravel 各连接子类的 getDefaultQueryGrammar 方言分发：PG/SQLite/
// SqlServer/MariaDb 连接必须路由到各自的语法编译器，否则运行时全部按 MySQL
// 语法编译。
func (c *Connection) GetQueryGrammar() grammar.Grammar {
	var g grammar.Grammar
	switch strings.ToLower(c.driver) {
	case "postgres", "postgresql", "pgsql":
		g = grammar.NewPostgresGrammar()
	case "sqlite":
		g = grammar.NewSqliteQueryGrammar()
	case "sqlserver":
		g = grammar.NewSqlServerGrammar()
	case "mariadb":
		g = grammar.NewMariaDbGrammar()
	default:
		g = grammar.NewMySqlGrammar()
	}
	g.SetTablePrefix(c.tablePrefix)
	return g
}

// prepareBindings 绑定值预处理（对应 Connection::prepareBindings）：移除
// Expression；MySQL 系驱动将 time.Time 按方言日期格式化、bool 转 int（MySQL
// 语义）。其余驱动按 database/sql 原生绑定原样透传。
func (c *Connection) prepareBindings(args []interface{}) []interface{} {
	convert := c.driver == "mysql" || c.driver == "mariadb"
	var dateFormat string
	if convert {
		dateFormat = phpDateToGoLayout(c.GetQueryGrammar().GetDateFormat())
	}
	out := make([]interface{}, 0, len(args))
	for _, arg := range args {
		if _, ok := arg.(grammar.Expression); ok {
			continue
		}
		if convert {
			switch v := arg.(type) {
			case time.Time:
				out = append(out, v.Format(dateFormat))
				continue
			case bool:
				if v {
					out = append(out, 1)
				} else {
					out = append(out, 0)
				}
				continue
			}
		}
		out = append(out, arg)
	}
	return out
}

// phpDateToGoLayout 把 PHP date 风格的方言日期格式（grammar.GetDateFormat，
// 如 "Y-m-d H:i:s"）转换为 Go time.Format 布局。
func phpDateToGoLayout(format string) string {
	repl := map[byte]string{
		'Y': "2006", 'y': "06",
		'm': "01", 'n': "1",
		'd': "02", 'j': "2",
		'H': "15", 'G': "3", 'h': "03", 'g': "3",
		'i': "04", 's': "05", 'v': "000", 'u': "000000",
	}
	var b strings.Builder
	for i := 0; i < len(format); i++ {
		if r, ok := repl[format[i]]; ok {
			b.WriteString(r)
		} else {
			b.WriteByte(format[i])
		}
	}
	return b.String()
}

// AffectingStatement Run an SQL statement and get the number of rows affected.
func (c *Connection) affectingStatement(query string, args ...interface{}) (int64, int64, error) {
	var insertId, affected int64
	err := c.run(query, args, "write", false, false, func(q queryer) error {
		stmt, err := c.prepareStmt(q, query)
		if err != nil {
			return err
		}
		defer stmt.Close()

		result, err := stmt.Exec(c.prepareBindings(args)...)
		if err != nil {
			return err
		}
		affected, err = result.RowsAffected()
		if err != nil {
			return err
		}
		insertId, err = result.LastInsertId()
		return err
	})
	if err == nil {
		c.recordsHaveBeenModified(true)
	}
	if err != nil {
		return 0, 0, err
	}
	return insertId, affected, nil
}

func (c *Connection) scan(rows *sql.Rows, columns []string, fields []*Field) error {
	count := len(columns)
	resets := make(map[int]*Field)
	values := make([]interface{}, count)
	args := make([]interface{}, count)

	castHolders := map[int]*Field{}

	for i, column := range columns {
		args[i] = &values[i]
		for _, field := range fields {
			if field.Ignored {
				continue
			}
			if field.Name != column {
				continue
			}

			// cast:json：原始列是字符串，先扫入占位符再反序列化
			if field.Cast == "json" && (field.Value.Kind() == reflect.Struct ||
				field.Value.Kind() == reflect.Slice || field.Value.Kind() == reflect.Map) {
				holder := new(string)
				args[i] = holder
				castHolders[i] = field
				continue
			}

			if field.Value.Kind() == reflect.Ptr {
				args[i] = field.Value.Addr().Interface()
			} else {
				reflectValue := reflect.New(reflect.PtrTo(field.StructField.Type))
				reflectValue.Elem().Set(field.Value.Addr())
				args[i] = reflectValue.Interface()
				resets[i] = field
			}
		}
	}

	err := rows.Scan(args...)

	if err != nil {
		return err
	}

	for index, field := range resets {
		if v := reflect.ValueOf(args[index]).Elem().Elem(); v.IsValid() {
			field.Value.Set(v)
		}
	}
	for index, field := range castHolders {
		holder, ok := args[index].(*string)
		if !ok || holder == nil || *holder == "" {
			continue
		}
		if err := json.Unmarshal([]byte(*holder), field.Value.Addr().Interface()); err != nil {
			return err
		}
	}

	return nil
}

// ---- 读写分离 ----

// initReadDB 按 Config.ReadDsn / ReadDsns 打开只读连接（随机选一，对应
// ConnectionFactory::getReadWriteConfig 的 Arr::random）。
func (c *Connection) initReadDB(dsns []string) error {
	if len(dsns) == 0 {
		return nil
	}
	dsn := dsns[rand.Intn(len(dsns))]
	connector := connectorFor(c.driver, c.configForReconnect())
	db, err := connector.Connect(dsn)
	if err != nil {
		return err
	}
	c.readDB = db
	c.readDsn = dsn
	return nil
}

// readQueryer 读路由（对应 getReadPdo）：事务内走事务；::read 固定走只读；
// 强制写、::write 固定、useWriteConnectionWhenReading、sticky+已发生写时走
// 主连接；否则走只读连接（未配置时为主连接）。
func (c *Connection) readQueryer(forceWrite bool) queryer {
	return c.readQueryerUnlocked(forceWrite)
}

func (c *Connection) readQueryerUnlocked(forceWrite bool) queryer {
	c.txMu.Lock()
	tx := c.tx
	c.txMu.Unlock()
	if tx != nil {
		return tx
	}
	c.stateMu.Lock()
	pinned := c.pinnedType
	readOnWrite := c.readOnWriteConnection
	stickyHit := c.recordsModified && c.sticky
	c.stateMu.Unlock()
	c.connMu.Lock()
	readDB, mainDB := c.readDB, c.DB
	c.connMu.Unlock()

	if pinned == "read" {
		if readDB != nil {
			return readDB
		}
		return mainDB
	}
	if forceWrite || pinned == "write" || readOnWrite || stickyHit {
		return mainDB
	}
	if readDB != nil {
		return readDB
	}
	return mainDB
}

// UseWriteConnectionWhenReading 强制读语句走写连接（对应
// useWriteConnectionWhenReading）。
func (c *Connection) UseWriteConnectionWhenReading(value ...bool) {
	v := true
	if len(value) > 0 {
		v = value[0]
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.readOnWriteConnection = v
}

// ---- recordsModified 跟踪（对应 hasModifiedRecords 等） ----

// recordsHaveBeenModified 标记已发生写操作（仅首次为 true 时生效，对应
// recordsHaveBeenModified 的幂等语义）。
func (c *Connection) recordsHaveBeenModified(value bool) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if !c.recordsModified {
		c.recordsModified = value
	}
}

// HasModifiedRecords 报告自上次重置以来是否发生过写操作。
func (c *Connection) HasModifiedRecords() bool {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.recordsModified
}

// RecordsHaveBeenModified 显式设置写标记。
func (c *Connection) RecordsHaveBeenModified(value bool) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if !c.recordsModified {
		c.recordsModified = value
	}
}

// SetRecordModificationState 无条件设置写标记。
func (c *Connection) SetRecordModificationState(value bool) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.recordsModified = value
}

// ForgetRecordModificationState 重置写标记（对应 forgetRecordModificationState）。
func (c *Connection) ForgetRecordModificationState() {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.recordsModified = false
}

// isPretending 报告连接是否处于 dry-run 状态。
func (c *Connection) isPretending() bool {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.pretending
}

// ---- 流式读取 ----

// CursorQuery 打开流式查询；返回的迭代器逐行产出 map，迭代结束或提前终止时自动关闭。
// 扫描/驱动错误通过 iter.Seq2 的 error 通道传出。
func (c *Connection) CursorQuery(query string, bindings []interface{}, forceWrite bool) (iter.Seq2[map[string]any, error], error) {
	if c.isPretending() {
		c.logQuery(query, bindings, 0, c.readWriteTypeFor(forceWrite))
		return func(yield func(map[string]any, error) bool) {}, nil
	}

	rw := c.readWriteTypeFor(forceWrite)
	var stmt *sql.Stmt
	var rows *sql.Rows
	var columns []string
	err := c.run(query, bindings, rw, true, false, func(q queryer) error {
		var err error
		stmt, err = c.prepareStmt(q, query)
		if err != nil {
			return err
		}
		rows, err = stmt.Query(c.prepareBindings(bindings)...)
		if err != nil {
			return err
		}
		columns, err = rows.Columns()
		return err
	})
	if err != nil {
		return nil, err
	}

	return func(yield func(map[string]any, error) bool) {
		defer rows.Close()
		defer stmt.Close()
		for rows.Next() {
			raw := make([]any, len(columns))
			ptrs := make([]any, len(columns))
			for i := range columns {
				ptrs[i] = &raw[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				yield(nil, err)
				return
			}
			row := make(map[string]any, len(columns))
			for i, col := range columns {
				row[col] = raw[i]
			}
			if !yield(row, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(nil, err)
		}
	}, nil
}

// ---- D1 服务器版本（对应 Connection::getServerVersion） ----

// ServerVersion 返回数据库服务器版本（对应 PDO::ATTR_SERVER_VERSION）。
// 惰性求值并按驱动选择版本查询：mysql/mariadb/postgres 走 select version()，
// sqlite 走 select sqlite_version()，sqlserver 走
// select serverproperty('ProductVersion')；仅首次真正查询，结果加锁缓存。
func (c *Connection) ServerVersion() (string, error) {
	c.serverVersionMu.Lock()
	defer c.serverVersionMu.Unlock()
	if c.serverVersionCached {
		return c.serverVersion, nil
	}

	query := "select version()"
	switch strings.ToLower(c.driver) {
	case "sqlite", "sqlite3":
		query = "select sqlite_version()"
	case "sqlserver", "mssql":
		query = "select serverproperty('ProductVersion')"
	}

	v, err := c.Scalar(query, nil)
	if err != nil {
		return "", err
	}
	s, _ := v.(string)
	c.serverVersion, c.serverVersionCached = s, true
	return s, nil
}

// IsMaria 报告服务器是否为 MariaDB：仅 mysql/mariadb 驱动按版本串判断
// （含 "MariaDB"），其余驱动恒为 false。
func (c *Connection) IsMaria() bool {
	switch strings.ToLower(c.driver) {
	case "mysql", "mariadb":
		version, err := c.ServerVersion()
		return err == nil && strings.Contains(version, "MariaDB")
	}
	return false
}

// ---- D10 便利 API（对应 Connection.php 同名方法） ----

// Unprepared 直发一条未预处理 SQL（对应 Connection::unprepared）：绕过 run
// 包装（不重试、不计时不写查询日志），成功后标记已发生写操作；
// pretending 时返回 (true, nil)。
func (c *Connection) Unprepared(query string) (bool, error) {
	if c.isPretending() {
		return true, nil
	}
	c.connMu.Lock()
	db := c.DB
	c.connMu.Unlock()
	if _, err := db.Exec(query); err != nil {
		return false, err
	}
	c.recordsHaveBeenModified(true)
	return true, nil
}

// WithoutPretending 在临时关闭 dry-run 的状态下执行回调（对应
// Connection::withoutPretending）：回调结束后恢复原 pretending 状态。
func (c *Connection) WithoutPretending(fn func() error) error {
	c.stateMu.Lock()
	wasPretending := c.pretending
	if wasPretending {
		c.pretending = false
	}
	c.stateMu.Unlock()

	if wasPretending {
		defer func() {
			c.stateMu.Lock()
			c.pretending = true
			c.stateMu.Unlock()
		}()
	}
	return fn()
}

// RawQueryLog 绑定插值后的查询日志条目（对应 getRawQueryLog 的
// raw_query/time）。
type RawQueryLog struct {
	RawSql string
	TimeNs int64
}

// GetRawQueryLog 返回绑定插值后的查询日志（对应 Connection::getRawQueryLog：
// substituteBindingsIntoRawSql 套用在现有 QueryLog 上）。
func (c *Connection) GetRawQueryLog() []RawQueryLog {
	log := c.GetQueryLog()
	out := make([]RawQueryLog, 0, len(log))
	for _, entry := range log {
		out = append(out, RawQueryLog{
			RawSql: substituteBindingsIntoRawSql(entry.Sql, c.prepareBindings(entry.Bindings)),
			TimeNs: entry.TimeNs,
		})
	}
	return out
}

// Raw 返回原始 SQL 表达式（对应 Connection::raw）。
func (c *Connection) Raw(value string) grammar.Expression {
	return grammar.NewExpression(value)
}

// threadCountCompiler 预留：方言 grammar 若实现 CompileThreadCount 则优先采用。
type threadCountCompiler interface {
	CompileThreadCount() string
}

// compileThreadCount 返回线程/连接数查询（对齐 Laravel 各方言 grammar 的
// compileThreadCount；未支持的驱动返回空串）。
func (c *Connection) compileThreadCount() string {
	if tc, ok := c.GetQueryGrammar().(threadCountCompiler); ok {
		return tc.CompileThreadCount()
	}
	switch strings.ToLower(c.driver) {
	case "mysql":
		return "select variable_value as `Value` from performance_schema.session_status where variable_name = 'threads_connected'"
	case "mariadb":
		return "select variable_value as `Value` from information_schema.global_status where variable_name = 'THREADS_CONNECTED'"
	case "postgres", "postgresql", "pgsql":
		return `select count(*) as "Value" from pg_stat_activity`
	case "sqlserver", "mssql":
		return "select count(*) Value from sys.dm_exec_sessions where status = N'running'"
	}
	return ""
}

// ThreadCount 返回数据库打开的连接/线程数（对应 Connection::threadCount）：
// 驱动不支持时返回 (-1, nil)。
func (c *Connection) ThreadCount() (int, error) {
	query := c.compileThreadCount()
	if query == "" {
		return -1, nil
	}
	v, err := c.Scalar(query, nil)
	if err != nil {
		return -1, err
	}
	return anyToInt(v), nil
}

// anyToInt 把 Scalar 的标量结果转为 int（nil → 0）。
func anyToInt(v any) int {
	switch n := v.(type) {
	case nil:
		return 0
	case int:
		return n
	case int8:
		return int(n)
	case int16:
		return int(n)
	case int32:
		return int(n)
	case int64:
		return int(n)
	case uint:
		return int(n)
	case uint8:
		return int(n)
	case uint16:
		return int(n)
	case uint32:
		return int(n)
	case uint64:
		return int(n)
	case float32:
		return int(n)
	case float64:
		return int(n)
	case string:
		i := 0
		for _, r := range strings.TrimSpace(n) {
			if r < '0' || r > '9' {
				break
			}
			i = i*10 + int(r-'0')
		}
		return i
	default:
		return 0
	}
}

// Escape 为安全 SQL 内嵌转义值（对应 Connection::escape）：null → 'null'、
// bool 按 escapeBool、整数/浮点原样、字符串单引号双写（含 NUL 字节或非法
// UTF-8 时报错）；binary=true 时按方言转义二进制。
func (c *Connection) Escape(value any, binary ...bool) (string, error) {
	if value == nil {
		return "null", nil
	}
	if len(binary) > 0 && binary[0] {
		return c.escapeBinary(value)
	}
	switch v := value.(type) {
	case bool:
		return c.escapeBoolFor(v), nil
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", v), nil
	case float32, float64:
		return fmt.Sprintf("%v", v), nil
	case string:
		return c.escapeString(v)
	case []byte:
		return c.escapeString(string(v))
	}
	return "", errors.New("The database connection does not support escaping arrays.")
}

// escapeString 字符串转义（对应 Connection::escapeString / PDO::quote）。
func (c *Connection) escapeString(value string) (string, error) {
	if strings.ContainsRune(value, '\x00') {
		return "", errors.New("Strings with null bytes cannot be escaped. Use the binary escape option.")
	}
	if !utf8.ValidString(value) {
		return "", errors.New("Strings with invalid UTF-8 byte sequences cannot be escaped.")
	}
	return "'" + strings.ReplaceAll(value, "'", "''") + "'", nil
}

// escapeBoolFor 布尔转义（对应 Connection::escapeBool；Postgres 子类覆写为
// true/false，见 PostgresConnection.php:67-70）。
func (c *Connection) escapeBoolFor(value bool) string {
	switch strings.ToLower(c.driver) {
	case "postgres", "postgresql", "pgsql":
		if value {
			return "true"
		}
		return "false"
	}
	if value {
		return "1"
	}
	return "0"
}

// escapeBinary 二进制转义：方言子类覆写（MySqlConnection.php:67、
// PostgresConnection.php:54、SQLiteConnection.php:47、SqlServerConnection.php:73）；
// 未支持的驱动报错（Connection.php:1224）。
func (c *Connection) escapeBinary(value any) (string, error) {
	var raw []byte
	switch v := value.(type) {
	case string:
		raw = []byte(v)
	case []byte:
		raw = v
	default:
		raw = []byte(fmt.Sprintf("%v", v))
	}
	h := hex.EncodeToString(raw)
	switch strings.ToLower(c.driver) {
	case "mysql", "mariadb", "sqlite", "sqlite3":
		return "x'" + h + "'", nil
	case "postgres", "postgresql", "pgsql":
		return "'\\x" + h + "'::bytea", nil
	case "sqlserver", "mssql":
		return "0x" + h, nil
	}
	return "", errors.New("The database connection does not support escaping binary values.")
}

// SetReconnector 设置断连重连回调（对应 Connection::setReconnector）：
// reconnect() 优先调用该回调。
func (c *Connection) SetReconnector(fn func(*Connection) error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.reconnector = fn
}

// UnsetEventDispatcher 卸载事件分发器（对应 Connection::unsetEventDispatcher）。
func (c *Connection) UnsetEventDispatcher() {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.dispatcher = nil
}

// adoptHandles 用 fresh 连接的底层句柄替换当前句柄（Manager.Reconnect 使用：
// 保留同一 Connection 实例，仅重建 *sql.DB；旧句柄关闭）。
func (c *Connection) adoptHandles(fresh *Connection) {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	oldDB, oldReadDB := c.DB, c.readDB
	c.DB = fresh.DB
	c.readDB, c.readDsn, c.readDsns = fresh.readDB, fresh.readDsn, fresh.readDsns
	if oldDB != nil {
		_ = oldDB.Close()
	}
	if oldReadDB != nil {
		_ = oldReadDB.Close()
	}
}
