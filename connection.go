package torm

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log"
	"reflect"
	"sync"
	"time"

	"github.com/thinkoner/torm/events"
	"github.com/thinkoner/torm/grammar"
)

type Connection struct {
	DB          *sql.DB
	tablePrefix string
	driver      string
	dsn         string
	readDB      *sql.DB // 读写分离的只读连接（Config.ReadDsn）

	// 事务状态（Laravel transactions 计数语义）
	txMu             sync.Mutex
	tx               *sql.Tx
	level            int
	afterCommitHooks []txHook
	afterRollbackHooks []txHook

	// 查询事件钩子（对应 Laravel QueryExecuted/DB::listen）
	onQuery func(QueryExecuted)

	// 模型事件分发器（对应 Laravel Dispatcher）
	dispatcher events.Dispatcher
}

// OnQuery 注册查询事件回调（对应 Laravel DB::listen；替代旧的全局 log.Println）。
func (c *Connection) OnQuery(fn func(QueryExecuted)) {
	c.onQuery = fn
}

// logQuery 触发查询事件；未注册回调时退化为标准日志。
func (c *Connection) logQuery(sql string, bindings []interface{}, d time.Duration) {
	if c.onQuery != nil {
		c.onQuery(QueryExecuted{Sql: sql, Bindings: bindings, TimeNs: int64(d)})
		return
	}
	log.Println(sql)
}

// Table Begin a fluent query against a database table.
func (c *Connection) Table(table string) *Builder {
	return c.Query().From(table)
}

// Query Get a new query builder instance.
func (c *Connection) Query() *Builder {
	return NewBuilder(c, c.GetQueryGrammar())
}

// SelectOne Run a select statement and return a single result.
func (c *Connection) SelectOne(query string, bindings []interface{}, dest interface{}) error {
	return c.Select(query, bindings, dest)
}

// Select Run a select statement against the database.
func (c *Connection) Select(query string, bindings []interface{}, dest interface{}) error {
	return c.selectImpl(query, bindings, dest)
}

func (c *Connection) selectImpl(query string, bindings []interface{}, dest interface{}) error {
	return c.selectImplRoute(query, bindings, dest, false)
}

// SelectFromWrite 强制走写连接的 select（对应 Laravel useWritePdo）。
func (c *Connection) SelectFromWrite(query string, bindings []interface{}, dest interface{}) error {
	return c.selectImplRoute(query, bindings, dest, true)
}

func (c *Connection) selectImplRoute(query string, bindings []interface{}, dest interface{}, forceWrite bool) error {
	start := time.Now()
	q := c.readQueryer(forceWrite)
	stmt, err := q.Prepare(query)
	if err != nil {
		return wrapQueryError(err, query, bindings)
	}
	defer stmt.Close()

	rows, err := stmt.Query(c.prepareBindings(bindings)...)
	if err != nil {
		return wrapQueryError(err, query, bindings)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	c.logQuery(query, bindings, time.Since(start))

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
	}

	return rows.Err()
}

// Scan Run a query and scan columns into given destinations.
func (c *Connection) Scan(query string, bindings []interface{}, dest ...interface{}) error {
	start := time.Now()
	q := c.readQueryer(false)
	stmt, err := q.Prepare(query)
	if err != nil {
		return wrapQueryError(err, query, bindings)
	}
	defer stmt.Close()

	rows, err := stmt.Query(c.prepareBindings(bindings)...)
	if err != nil {
		return wrapQueryError(err, query, bindings)
	}
	defer rows.Close()

	rows.Next()
	c.logQuery(query, bindings, time.Since(start))

	return rows.Scan(c.prepareBindings(dest)...)
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
	var err error

	start := time.Now()
	q := c.queryer()
	stmt, err := q.Prepare(query)
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(c.prepareBindings(args)...)
	c.logQuery(query, args, time.Since(start))

	return err
}

// SelectMaps 执行查询并把行结果输出为 map 切片（explain/内省用）。
func (c *Connection) SelectMaps(query string, bindings []interface{}) ([]map[string]any, error) {
	start := time.Now()
	q := c.readQueryer(false)
	stmt, err := q.Prepare(query)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()

	rows, err := stmt.Query(c.prepareBindings(bindings)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	c.logQuery(query, bindings, time.Since(start))

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
func (c *Connection) SetEventDispatcher(d events.Dispatcher) {
	c.dispatcher = d
}

// dispatchEvent 分发模型事件（未挂载分发器时为 no-op）。
func (c *Connection) dispatchEvent(name string, model any) {
	if c.dispatcher == nil {
		return
	}
	c.dispatcher.Dispatch(events.ModelEvent{Name: name, Model: model})
}

// GetQueryGrammar 按 driver 构造方言 grammar，并接线上表前缀。
func (c *Connection) GetQueryGrammar() grammar.Grammar {
	g := grammar.NewMySqlGrammar()
	g.SetTablePrefix(c.tablePrefix)
	return g
}

// prepareBindings 移除 Expression 绑定（对应 Laravel Connection::prepareBindings）。
func (c *Connection) prepareBindings(args []interface{}) []interface{} {
	out := make([]interface{}, 0, len(args))
	for _, arg := range args {
		if _, ok := arg.(grammar.Expression); ok {
			continue
		}
		out = append(out, arg)
	}
	return out
}

// AffectingStatement Run an SQL statement and get the number of rows affected.
func (c *Connection) affectingStatement(query string, args ...interface{}) (int64, int64, error) {
	var err error

	start := time.Now()
	q := c.queryer()
	stmt, err := q.Prepare(query)

	if err != nil {
		return 0, 0, err
	}
	defer stmt.Close()

	result, err := stmt.Exec(c.prepareBindings(args)...)

	if err != nil {
		return 0, 0, wrapQueryError(err, query, args)
	}
	c.logQuery(query, args, time.Since(start))

	affected, err := result.RowsAffected()
	if err != nil {
		return 0, affected, err
	}

	insertId, err := result.LastInsertId()
	return insertId, affected, err
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

// ---- 流式读取与读写分离 ----

// initReadDB 按 Config.ReadDsn 打开只读连接（读写分离最小集）。
func (c *Connection) initReadDB(dsn string) error {
	db, err := sql.Open(c.driver, dsn)
	if err != nil {
		return err
	}
	c.readDB = db
	return nil
}

// readQueryer 读路由：事务内走事务；配置了 ReadDsn 且非强制写时走只读连接。
func (c *Connection) readQueryer(forceWrite bool) queryer {
	c.txMu.Lock()
	tx := c.tx
	c.txMu.Unlock()
	if tx != nil {
		return tx
	}
	if !forceWrite && c.readDB != nil {
		return c.readDB
	}
	return c.DB
}

// CursorQuery 打开流式查询；返回的迭代器逐行产出 map，迭代结束或提前终止时自动关闭。
// 扫描/驱动错误通过 iter.Seq2 的 error 通道传出。
func (c *Connection) CursorQuery(query string, bindings []interface{}, forceWrite bool) (iter.Seq2[map[string]any, error], error) {
	start := time.Now()
	q := c.readQueryer(forceWrite)
	stmt, err := q.Prepare(query)
	if err != nil {
		return nil, wrapQueryError(err, query, bindings)
	}
	rows, err := stmt.Query(c.prepareBindings(bindings)...)
	if err != nil {
		stmt.Close()
		return nil, wrapQueryError(err, query, bindings)
	}
	columns, err := rows.Columns()
	if err != nil {
		rows.Close()
		stmt.Close()
		return nil, err
	}
	c.logQuery(query, bindings, time.Since(start))

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
