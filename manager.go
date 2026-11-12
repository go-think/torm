package torm

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/go-think/torm/events"
)

// Config 连接配置（对应 Laravel database.connections[*]）。
type Config struct {
	// Database 数据库名（对应 Laravel config['database']，内省与
	// CreateDatabase 使用）
	Database string

	Driver  string
	Prefix  string
	Dsn     string
	ReadDsn string // 兼容：单个只读 DSN（读写分离，select 类查询路由到此）
	// ReadDsns 只读 DSN 池：建连时随机选一（对应配置 read 数组 + Arr::random）。
	ReadDsns []string
	// Sticky sticky 模式：本连接发生写操作后，后续读强制走主连接
	// （对应配置 sticky，配合 recordsModified 跟踪）。
	Sticky bool

	// MySQL 连接参数（MySqlConnector 自动附加到 DSN，已有参数不覆盖）
	Charset   string // NAMES charset
	Collation string // COLLATE collation
	SqlMode   string // SESSION sql_mode
	Timezone  string // time_zone
	// Modes sql_mode 数组（对应 config['modes']，join 后优先于 SqlMode 单串）
	Modes []string

	// Postgres / SQLServer
	IsolationLevel string // 会话事务隔离级别（建连后以 SET 语句生效）
	SslMode        string // Postgres sslmode
	SearchPath     string // Postgres search_path
	SslCert        string // Postgres sslcert DSN 参数
	SslKey         string // Postgres sslkey DSN 参数
	SslRootCert    string // Postgres sslrootcert DSN 参数
	// SynchronousCommit Postgres synchronous_commit（建连后 set synchronous_commit to）
	SynchronousCommit string
	// ClientEncoding / ApplicationName Postgres DSN 参数
	ClientEncoding  string
	ApplicationName string

	// SQLite pragmas
	ForeignKeys *bool  // PRAGMA foreign_keys（nil 表示不设置）
	BusyTimeout int    // PRAGMA busy_timeout（毫秒，0 表示不设置）
	JournalMode string // PRAGMA journal_mode
	Synchronous string // PRAGMA synchronous
	// Pragmas 自定义 pragma 集合（对应 config['pragmas']，建连后逐条执行）
	Pragmas map[string]string
}

// DatabaseManager 具名连接管理器（连接复用 + 并发安全）。
type Manager struct {
	mu          sync.Mutex
	configs     map[string]Config
	connections map[string]*Connection
	defaultName string
	extensions  map[string]func(Config) (*Connection, error)
	dispatcher  events.Dispatcher
	reconnector func(*Connection) error
}

// NewManager 创建管理器并可预置配置。
func NewManager(configs ...map[string]Config) *Manager {
	m := &Manager{
		configs:     map[string]Config{},
		connections: map[string]*Connection{},
		extensions:  map[string]func(Config) (*Connection, error){},
	}
	for _, group := range configs {
		for name, cfg := range group {
			m.configs[name] = cfg
		}
	}
	return m
}

// AddConfig 注册一个具名配置。
func (m *Manager) AddConfig(name string, config Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.configs[name] = config
}

// SetDefaultConnection 设置默认连接名（Connect("") 时使用）。
func (m *Manager) SetDefaultConnection(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaultName = name
}

// GetDefaultConnection 返回默认连接名。
func (m *Manager) GetDefaultConnection() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.defaultName
}

// SetEventDispatcher 挂载管理器级事件分发器：每次成功建立新连接后分发
// ConnectionEstablished 事件（对应 DatabaseManager::dispatchConnectionEstablishedEvent）。
func (m *Manager) SetEventDispatcher(d events.Dispatcher) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dispatcher = d
}

// parseConnectionName 解析 name 的 ::read/::write 路由后缀
// （对应 DatabaseManager::parseConnectionName）。
func parseConnectionName(name string) (base, rwType string) {
	if strings.HasSuffix(name, "::read") || strings.HasSuffix(name, "::write") {
		parts := strings.SplitN(name, "::", 2)
		return parts[0], parts[1]
	}
	return name, ""
}

// Connect 获取具名连接实例；同一名称复用同一连接（*sql.DB 自带连接池）。
// name 为空时使用默认连接；支持 "name::read" / "name::write" 读写路由。
func (m *Manager) Connect(name string) (*Connection, error) {
	if name == "" {
		name = m.GetDefaultConnection()
	}
	base, rwType := parseConnectionName(name)

	m.mu.Lock()
	if conn, ok := m.connections[name]; ok {
		m.mu.Unlock()
		return conn, nil
	}

	config, ok := m.configs[base]
	if !ok {
		m.mu.Unlock()
		return nil, ErrInvalid{Msg: fmt.Sprintf("Database [%s] not configured.", base)}
	}

	// 扩展解析器优先按连接名，其次按驱动名（对应 makeConnection 的扩展查找）
	conn, err := m.resolveConnectionLocked(base, config)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	// 完整配置存入连接（重连时保留方言参数）
	conn.setConfig(config)
	applyReadWriteRouting(conn, rwType)
	m.connections[name] = conn
	dispatcher := m.dispatcher
	reconnector := m.reconnector
	m.mu.Unlock()

	// 管理器重连回调挂载到连接（对应 configure() 的 setReconnector 接线；
	// 默认回调走 Manager.Reconnect 保留实例重建句柄）
	if reconnector != nil {
		conn.SetReconnector(reconnector)
	} else {
		conn.SetReconnector(func(c *Connection) error {
			_, rerr := m.Reconnect(c.nameWithReadWriteType())
			return rerr
		})
	}

	// 管理器分发器挂载到连接（对应 configure() 的事件接线），随后分发建连事件
	if dispatcher != nil {
		conn.SetEventDispatcher(dispatcher)
		dispatcher.Dispatch(events.ConnectionEstablishedEvent{ConnectionName: name})
	}
	return conn, nil
}

// resolveConnectionLocked 依扩展/内建 Open 建立连接（须持 m.mu）。
func (m *Manager) resolveConnectionLocked(base string, config Config) (*Connection, error) {
	if resolver, ok := m.extensions[base]; ok {
		return resolver(config)
	}
	if resolver, ok := m.extensions[config.Driver]; ok {
		return resolver(config)
	}
	return openNamed(base, "", config)
}

// applyReadWriteRouting ::read/::write 固定路由（对应 setPdoForType）：
// read → 读写都走只读连接；write → 读写都走主连接。
func applyReadWriteRouting(conn *Connection, rwType string) {
	if rwType == "" {
		return
	}
	conn.setPinnedType(rwType)
	conn.setReadWriteType(rwType)
}

// ConnectUsing 以显式配置立即建立具名连接（对应 DatabaseManager::connectUsing）。
// force 为 true 时先断开并替换同名既有连接；否则同名连接已存在时报错。
func (m *Manager) ConnectUsing(name string, config Config, force ...bool) (*Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(force) > 0 && force[0] {
		if conn, ok := m.connections[name]; ok {
			delete(m.connections, name)
			if conn != nil {
				m.closeConnLocked(conn)
			}
		}
	}
	if _, ok := m.connections[name]; ok {
		return nil, ErrInvalid{Msg: fmt.Sprintf(
			"Cannot establish connection [%s] because another connection with that name already exists.", name)}
	}

	m.configs[name] = config
	conn, err := openNamed(name, "", config)
	if err != nil {
		return nil, err
	}
	conn.setConfig(config)
	m.connections[name] = conn
	if m.dispatcher != nil {
		conn.SetEventDispatcher(m.dispatcher)
		m.dispatcher.Dispatch(events.ConnectionEstablishedEvent{ConnectionName: name})
	}
	// 重连回调挂载（对应 configure() 的 setReconnector 接线；默认回调走
	// Manager.Reconnect 保留实例重建句柄）
	if m.reconnector != nil {
		conn.SetReconnector(m.reconnector)
	} else {
		conn.SetReconnector(func(c *Connection) error {
			_, rerr := m.Reconnect(c.nameWithReadWriteType())
			return rerr
		})
	}
	return conn, nil
}

// UsingConnection 在回调期间把默认连接临时切换为 name
// （对应 DatabaseManager::usingConnection）。
func (m *Manager) UsingConnection(name string, callback func() error) error {
	previous := m.GetDefaultConnection()
	m.SetDefaultConnection(name)
	defer m.SetDefaultConnection(previous)
	return callback()
}

// Extend 注册连接解析扩展（对应 DatabaseManager::extend）：优先按连接名命中，
// 其次按驱动名。
func (m *Manager) Extend(driver string, resolver func(Config) (*Connection, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.extensions[driver] = resolver
}

// ForgetExtension 移除连接解析扩展。
func (m *Manager) ForgetExtension(driver string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.extensions, driver)
}

// Transaction 在具名连接上执行事务回调（转发 Connection::Transaction）。
func (m *Manager) Transaction(name string, fn func(*Connection) error, attempts ...int) error {
	conn, err := m.Connect(name)
	if err != nil {
		return err
	}
	return conn.Transaction(fn, attempts...)
}

// closeConnLocked 关闭连接的底层句柄（须持 m.mu 或已从缓存移除）。
func (m *Manager) closeConnLocked(conn *Connection) {
	conn.connMu.Lock()
	defer conn.connMu.Unlock()
	if conn.DB != nil {
		_ = conn.DB.Close()
	}
	if conn.readDB != nil {
		_ = conn.readDB.Close()
	}
}

// Purge 断开并移除具名连接（name 为空时使用默认连接）。
func (m *Manager) Purge(name string) error {
	if name == "" {
		name = m.GetDefaultConnection()
	}
	m.mu.Lock()
	conn, ok := m.connections[name]
	delete(m.connections, name)
	m.mu.Unlock()

	if ok && conn != nil {
		m.closeConnLocked(conn)
	}
	return nil
}

// Disconnect 关闭具名连接并移出缓存，使后续 Connect 重新建立连接
// （对应 DatabaseManager::disconnect；Go 侧 *sql.DB 关闭后不可复用，
// 故移出缓存以实现重连）。
func (m *Manager) Disconnect(name string) error {
	return m.Purge(name)
}

// SetReconnector 设置重连回调：之后建立的连接将挂载该回调
// （对应 DatabaseManager::setReconnector + configure 的接线；未设置时使用
// 默认回调，即 Manager.Reconnect 保留实例重建句柄）。
func (m *Manager) SetReconnector(fn func(*Connection) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reconnector = fn
}

// Reconnect 重连具名连接（对应 DatabaseManager::reconnect）：与 Disconnect+
// Connect 不同，保留同一 Connection 实例（连接名/事件/事务状态不丢），仅用
// 其配置重建底层句柄并重发 ConnectionEstablished；连接尚未建立时等价 Connect。
func (m *Manager) Reconnect(name string) (*Connection, error) {
	if name == "" {
		name = m.GetDefaultConnection()
	}
	base, _ := parseConnectionName(name)

	m.mu.Lock()
	conn, ok := m.connections[name]
	if !ok {
		m.mu.Unlock()
		return m.Connect(name)
	}
	// 用连接自身的配置重建（方言参数在重连后照常生效）
	config := conn.GetConfig()
	if config.Driver == "" && config.Dsn == "" {
		config = m.configs[base]
	}
	// 与 makeConnection 一致：优先走扩展解析器
	fresh, err := m.resolveConnectionLocked(base, config)
	dispatcher := m.dispatcher
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}

	conn.adoptHandles(fresh)

	if dispatcher != nil {
		conn.SetEventDispatcher(dispatcher)
		dispatcher.Dispatch(events.ConnectionEstablishedEvent{ConnectionName: name})
	}
	return conn, nil
}

// GetConnections 返回已创建连接的名称列表（对应 DatabaseManager::getConnections）。
func (m *Manager) GetConnections() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.connections))
	for name := range m.connections {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

var errNotConfigured = errors.New("database not configured")
