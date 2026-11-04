package torm

import (
	"errors"
	"fmt"
	"sync"
)

type Config struct {
	Driver  string
	Prefix  string
	Dsn     string
	ReadDsn string // 可选：只读连接（读写分离最小集，select 类查询路由到此）
}

// DatabaseManager 具名连接管理器（连接复用 + 并发安全）。
type Manager struct {
	mu          sync.Mutex
	configs     map[string]Config
	connections map[string]*Connection
}

// NewManager 创建管理器并可预置配置。
func NewManager(configs ...map[string]Config) *Manager {
	m := &Manager{
		configs:     map[string]Config{},
		connections: map[string]*Connection{},
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

// Connect 获取具名连接实例；同一名称复用同一连接（*sql.DB 自带连接池）。
func (m *Manager) Connect(name string) (*Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if conn, ok := m.connections[name]; ok {
		return conn, nil
	}

	config, ok := m.configs[name]
	if !ok {
		return nil, ErrInvalid{Msg: fmt.Sprintf("Database [%s] not configured.", name)}
	}

	conn, err := Open(config)
	if err != nil {
		return nil, err
	}
	m.connections[name] = conn
	return conn, nil
}

// Purge 断开并移除具名连接。
func (m *Manager) Purge(name string) error {
	m.mu.Lock()
	conn, ok := m.connections[name]
	delete(m.connections, name)
	m.mu.Unlock()

	if ok && conn != nil && conn.DB != nil {
		return conn.DB.Close()
	}
	return nil
}

// Disconnect 关闭具名连接。
func (m *Manager) Disconnect(name string) error {
	m.mu.Lock()
	conn, ok := m.connections[name]
	m.mu.Unlock()

	if ok && conn != nil && conn.DB != nil {
		return conn.DB.Close()
	}
	return nil
}

var errNotConfigured = errors.New("database not configured")
