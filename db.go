package torm

import (
	"database/sql"
	"time"
)

// Open 按配置建立连接（连接池惰性建连；驱动路由到对应 Connector）。
func Open(config Config) (*Connection, error) {
	return openNamed("", "", config)
}

// openNamed 建立带连接名与读写类型标识的连接（Manager 内部使用）。
// name 为逻辑连接名，rwType 为 ""/"read"/"write"。
func openNamed(name, rwType string, config Config) (*Connection, error) {
	c := &Connection{
		tablePrefix:   config.Prefix,
		driver:        config.Driver,
		dsn:           config.Dsn,
		name:          name,
		readWriteType: rwType,
		sticky:        config.Sticky,
		config:        config,
		readDsns:      append([]string(nil), config.ReadDsns...),
	}
	// 兼容单 DSN 字段：仅非空时入池（旧实现的条件追加语义；
	// 空串入池会让每个连接凭空获得一个空 DSN 的只读连接）
	if config.ReadDsn != "" {
		c.readDsns = append(c.readDsns, config.ReadDsn)
	}

	// 驱动侧 Connector：按方言增强/校验 DSN 并打开主（写）连接
	connector := connectorFor(config.Driver, config)
	db, err := connector.Connect(config.Dsn)
	if err != nil {
		return nil, err
	}
	applyPoolParams(db, config)
	c.DB = db

	// 读写分离：配置了只读 DSN（单个 ReadDsn 或 ReadDsns 池）时，select 类
	// 查询路由到只读连接
	readDsns := c.readDsns
	if len(readDsns) > 0 {
		if err := c.initReadDB(readDsns); err != nil {
			db.Close()
			return nil, err
		}
	}

	// 首次建连成功：分发 ConnectionEstablished（已挂载分发器时）
	c.dispatchEstablishedEvent()

	return c, nil
}

// applyPoolParams 应用连接池调优参数（对应 database/sql 的 Set* API；
// 0 值保留驱动默认——Go 默认 MaxOpenConns 无上限）。
func applyPoolParams(db *sql.DB, config Config) {
	if config.MaxOpenConns > 0 {
		db.SetMaxOpenConns(config.MaxOpenConns)
	}
	if config.MaxIdleConns > 0 {
		db.SetMaxIdleConns(config.MaxIdleConns)
	}
	if config.ConnMaxLifetimeSec > 0 {
		db.SetConnMaxLifetime(time.Duration(config.ConnMaxLifetimeSec) * time.Second)
	}
	if config.ConnMaxIdleTimeSec > 0 {
		db.SetConnMaxIdleTime(time.Duration(config.ConnMaxIdleTimeSec) * time.Second)
	}
}
