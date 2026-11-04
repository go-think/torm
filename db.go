package torm

import (
	"database/sql"
)

func Open(config Config) (*Connection, error) {
	db, err := sql.Open(config.Driver, config.Dsn)

	if err != nil {
		return nil, err
	}

	c := &Connection{
		DB:          db,
		tablePrefix: config.Prefix,
		driver:      config.Driver,
		dsn:         config.Dsn,
	}

	// 读写分离最小集：配置了 ReadDsn 时，select 类查询走只读连接
	if config.ReadDsn != "" {
		if err := c.initReadDB(config.ReadDsn); err != nil {
			db.Close()
			return nil, err
		}
	}

	return c, nil
}
