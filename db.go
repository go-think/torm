package torm

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
	}

	// 驱动侧 Connector：按方言增强/校验 DSN 并打开主（写）连接
	connector := connectorFor(config.Driver, config)
	db, err := connector.Connect(config.Dsn)
	if err != nil {
		return nil, err
	}
	c.DB = db

	// 读写分离：配置了只读 DSN（单个 ReadDsn 或 ReadDsns 池）时，select 类
	// 查询路由到随机选中的只读连接
	readDsns := config.ReadDsns
	if config.ReadDsn != "" {
		readDsns = append(append([]string(nil), readDsns...), config.ReadDsn)
	}
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
