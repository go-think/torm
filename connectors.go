package torm

import (
	"database/sql"
	"sort"
	"strconv"
	"strings"

	gomysql "github.com/go-sql-driver/mysql"
)

// Connector 对应 Laravel 的 Connectors\ConnectorInterface：建立数据库连接。
type Connector interface {
	// Connect 打开数据库连接（实现可在连接前对 DSN 做方言化增强/校验）。
	Connect(dsn string) (*sql.DB, error)
}

// connectorFor 按驱动路由到对应 Connector（对应
// ConnectionFactory::createConnector 的驱动分支）。
func connectorFor(driver string, config Config) Connector {
	switch driver {
	case "mysql", "mariadb":
		return &MySqlConnector{config: config}
	case "postgres", "pgsql", "postgresql":
		return &PostgresConnector{config: config}
	case "sqlite", "sqlite3":
		return &SQLiteConnector{config: config}
	case "sqlserver", "mssql":
		return &SqlServerConnector{config: config}
	default:
		return &defaultConnector{driver: driver}
	}
}

// defaultConnector 未注册方言的驱动：原样 sql.Open（连接池惰性建连）。
type defaultConnector struct{ driver string }

// Connect 打开连接。
func (c *defaultConnector) Connect(dsn string) (*sql.DB, error) {
	return openWithLostConnectionRetry(c.driver, dsn)
}

// openWithLostConnectionRetry 打开连接；断连类错误重试一次（对应
// Connector::createConnection → tryAgainIfCausedByLostConnection）。
func openWithLostConnectionRetry(driver, dsn string) (*sql.DB, error) {
	db, err := sql.Open(driver, dsn)
	if err != nil && classifyError(err) == KindLostConnection {
		db, err = sql.Open(driver, dsn)
	}
	return db, err
}

// ---- MySQL ----

// MySqlConnector MySQL 连接器（对应 Connectors\MySqlConnector）：校验 DSN 并
// 自动附加 charset/collation/sql_mode/timezone 参数（来自 Config，已有参数不覆盖）。
type MySqlConnector struct {
	config Config
}

// Connect 打开 MySQL 连接。
func (c *MySqlConnector) Connect(dsn string) (*sql.DB, error) {
	db, err := openWithLostConnectionRetry("mysql", c.BuildDsn(dsn))
	if err != nil {
		return nil, err
	}
	if err := c.configureSession(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// configureSession 建连后的会话配置（对应 MySqlConnector::configureConnection）：
// isolation_level 以 SET SESSION TRANSACTION ISOLATION LEVEL 生效（charset/
// sql_mode/timezone 经 DSN 参数由驱动处理）。
func (c *MySqlConnector) configureSession(db *sql.DB) error {
	if c.config.IsolationLevel != "" {
		if _, err := db.Exec("set session transaction isolation level " + c.config.IsolationLevel); err != nil {
			return err
		}
	}
	return nil
}

// BuildDsn 校验（非标准 DSN 原样返回，交给驱动在连接时报错）并增强 DSN。
func (c *MySqlConnector) BuildDsn(dsn string) string {
	if c.config.Charset == "" && c.config.Collation == "" &&
		c.config.SqlMode == "" && len(c.config.Modes) == 0 && c.config.Timezone == "" {
		return dsn
	}
	parsed, err := gomysql.ParseDSN(dsn)
	if err != nil {
		return dsn
	}
	if parsed.Params == nil {
		parsed.Params = map[string]string{}
	}
	setParam := func(key, value string) {
		if value == "" {
			return
		}
		if _, exists := parsed.Params[key]; !exists {
			parsed.Params[key] = value
		}
	}
	// modes 数组优先于 sql_mode 单串（对应 getSqlMode 的 config['modes'] 分支）
	sqlMode := c.config.SqlMode
	if len(c.config.Modes) > 0 {
		sqlMode = strings.Join(c.config.Modes, ",")
	}
	setParam("sql_mode", sqlMode)
	setParam("time_zone", c.config.Timezone)
	// charset 在 go-sql-driver 中是私有 charsets 字段（非普通 Params），
	// collation 是专用字段：已有值（原始 DSN 中出现）时不覆盖
	if c.config.Charset != "" && !hasDsnParam(dsn, "charset") {
		setParam("charset", c.config.Charset)
	}
	if c.config.Collation != "" && parsed.Collation == "" && !hasDsnParam(dsn, "collation") {
		setParam("collation", c.config.Collation)
	}
	return parsed.FormatDSN()
}

// ---- Postgres ----

// PostgresConnector Postgres 连接器（对应 Connectors\PostgresConnector）：
// 自动附加 sslmode/search_path/timezone DSN 参数；isolation_level 在建连后
// 以会话语句设置。
type PostgresConnector struct {
	config Config
}

// Connect 打开 Postgres 连接。
func (c *PostgresConnector) Connect(dsn string) (*sql.DB, error) {
	db, err := openWithLostConnectionRetry(c.config.Driver, c.BuildDsn(dsn))
	if err != nil {
		return nil, err
	}
	if c.config.IsolationLevel != "" {
		if _, err := db.Exec("set session characteristics as transaction isolation level " + c.config.IsolationLevel); err != nil {
			db.Close()
			return nil, err
		}
	}
	if c.config.SynchronousCommit != "" {
		if _, err := db.Exec("set synchronous_commit to '" + c.config.SynchronousCommit + "'"); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

// BuildDsn 附加 sslmode/sslcert/sslkey/sslrootcert/search_path/client_encoding/
// application_name/TimeZone 参数（已有参数不覆盖）。
func (c *PostgresConnector) BuildDsn(dsn string) string {
	var adds []string
	if c.config.SslMode != "" && !hasDsnParam(dsn, "sslmode") {
		adds = append(adds, "sslmode="+c.config.SslMode)
	}
	if c.config.SslCert != "" && !hasDsnParam(dsn, "sslcert") {
		adds = append(adds, "sslcert="+c.config.SslCert)
	}
	if c.config.SslKey != "" && !hasDsnParam(dsn, "sslkey") {
		adds = append(adds, "sslkey="+c.config.SslKey)
	}
	if c.config.SslRootCert != "" && !hasDsnParam(dsn, "sslrootcert") {
		adds = append(adds, "sslrootcert="+c.config.SslRootCert)
	}
	if c.config.SearchPath != "" && !hasDsnParam(dsn, "search_path") {
		adds = append(adds, "search_path="+c.config.SearchPath)
	}
	if c.config.ClientEncoding != "" && !hasDsnParam(dsn, "client_encoding") {
		adds = append(adds, "client_encoding="+c.config.ClientEncoding)
	}
	if c.config.ApplicationName != "" && !hasDsnParam(dsn, "application_name") {
		adds = append(adds, "application_name="+c.config.ApplicationName)
	}
	if c.config.Timezone != "" && !hasDsnParam(dsn, "timezone") {
		adds = append(adds, "TimeZone="+c.config.Timezone)
	}
	return appendDsnParams(dsn, adds)
}

// ---- SQLite ----

// SQLiteConnector SQLite 连接器（对应 Connectors\SQLiteConnector）：经驱动
// 参数附加 pragmas（foreign_keys/busy_timeout/journal_mode/synchronous）。
type SQLiteConnector struct {
	config Config
}

// Connect 打开 SQLite 连接。
func (c *SQLiteConnector) Connect(dsn string) (*sql.DB, error) {
	db, err := openWithLostConnectionRetry(c.config.Driver, c.BuildDsn(dsn))
	if err != nil {
		return nil, err
	}
	// 自定义 pragma（对应 SQLiteConnector::configurePragmas）：按键名排序，
	// 逐条以会话语句执行
	if len(c.config.Pragmas) > 0 {
		keys := make([]string, 0, len(c.config.Pragmas))
		for k := range c.config.Pragmas {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if _, err := db.Exec("pragma " + k + " = " + c.config.Pragmas[k]); err != nil {
				db.Close()
				return nil, err
			}
		}
	}
	return db, nil
}

// BuildDsn 附加 sqlite pragma 参数。
func (c *SQLiteConnector) BuildDsn(dsn string) string {
	var adds []string
	if c.config.ForeignKeys != nil {
		v := "off"
		if *c.config.ForeignKeys {
			v = "on"
		}
		adds = append(adds, "_foreign_keys="+v)
	}
	if c.config.BusyTimeout > 0 {
		adds = append(adds, "_busy_timeout="+strconv.Itoa(c.config.BusyTimeout))
	}
	if c.config.JournalMode != "" {
		adds = append(adds, "_journal_mode="+c.config.JournalMode)
	}
	if c.config.Synchronous != "" {
		adds = append(adds, "_synchronous="+c.config.Synchronous)
	}
	return appendDsnParams(dsn, adds)
}

// ---- SQLServer ----

// SqlServerConnector SQLServer 连接器（对应 Connectors\SqlServerConnector）：
// isolation_level 在建连后以会话语句设置。
type SqlServerConnector struct {
	config Config
}

// Connect 打开 SQLServer 连接。
func (c *SqlServerConnector) Connect(dsn string) (*sql.DB, error) {
	db, err := openWithLostConnectionRetry(c.config.Driver, dsn)
	if err != nil {
		return nil, err
	}
	if c.config.IsolationLevel != "" {
		if _, err := db.Exec("set transaction isolation level " + c.config.IsolationLevel); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

// ---- DSN 工具 ----

// hasDsnParam 报告 key=value 形式的 DSN 是否已包含指定参数（? 之后的查询串）。
func hasDsnParam(dsn, key string) bool {
	i := strings.IndexByte(dsn, '?')
	if i < 0 {
		return false
	}
	for _, pair := range strings.Split(dsn[i+1:], "&") {
		if k := strings.SplitN(pair, "=", 2)[0]; strings.EqualFold(k, key) {
			return true
		}
	}
	return false
}

// appendDsnParams 把 key=value 参数追加到 DSN（自动选择 ? / & 连接符）。
func appendDsnParams(dsn string, params []string) string {
	if len(params) == 0 {
		return dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + strings.Join(params, "&")
}
