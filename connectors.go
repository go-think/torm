package torm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"sort"
	"strconv"
	"strings"

	gomysql "github.com/go-sql-driver/mysql"
)

// Connector 数据库连接器接口：建立数据库连接。
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

// sessionConnector 包装 driver.Connector：在每个新物理连接上执行会话级
// 语句（隔离级别等）。*sql.DB 连接池下 db.Exec 只落在随机一个连接上
// （旧实现的缺陷），本包装保证每个物理连接建连即配置——对齐
// 单连接会话的 configureConnection 语义。
type sessionConnector struct {
	inner      driver.Connector
	statements []string
}

func (s *sessionConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := s.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	for _, stmt := range s.statements {
		if err := execOnRawConn(ctx, conn, stmt); err != nil {
			conn.Close()
			return nil, err
		}
	}
	return conn, nil
}

func (s *sessionConnector) Driver() driver.Driver { return s.inner.Driver() }

// execOnRawConn 在裸 driver.Conn 上执行语句（优先 ExecerContext/Execer，
// 兜底 Prepare+Exec——后者是 driver.Conn 的必需能力，任何驱动都可用）。
func execOnRawConn(ctx context.Context, conn driver.Conn, query string) error {
	if ec, ok := conn.(driver.ExecerContext); ok {
		_, err := ec.ExecContext(ctx, query, nil)
		return err
	}
	if ec, ok := conn.(driver.Execer); ok {
		_, err := ec.Exec(query, nil)
		return err
	}
	stmt, err := conn.Prepare(query)
	if err != nil {
		return err
	}
	defer stmt.Close()
	_, err = stmt.Exec(nil)
	return err
}

// ---- MySQL ----

// MySqlConnector MySQL 连接器（对应 Connectors\MySqlConnector）：校验 DSN 并
// 自动附加 charset/collation/sql_mode/timezone/parseTime 参数（来自 Config，
// 已有参数不覆盖）；隔离级别经 driver.Connector 包装在每个物理连接上生效。
type MySqlConnector struct {
	config Config
}

// Connect 打开 MySQL 连接。
func (c *MySqlConnector) Connect(dsn string) (*sql.DB, error) {
	built := c.BuildDsn(dsn)
	if c.config.IsolationLevel != "" {
		inner, err := gomysql.MySQLDriver{}.OpenConnector(built)
		if err == nil {
			sc := &sessionConnector{
				inner:      inner,
				statements: []string{"set session transaction isolation level " + c.config.IsolationLevel},
			}
			return sql.OpenDB(sc), nil
		}
		// OpenConnector 不可用时退回普通打开（隔离级别降级为建连 Exec，
		// 仅首连接生效——保持旧行为，见配置注释）
	}
	return openWithLostConnectionRetry("mysql", built)
}

// BuildDsn 校验（非标准 DSN 原样返回，交给驱动在连接时报错）并增强 DSN。
func (c *MySqlConnector) BuildDsn(dsn string) string {
	if c.config.Charset == "" && c.config.Collation == "" &&
		c.config.SqlMode == "" && len(c.config.Modes) == 0 && c.config.Timezone == "" &&
		!parseTimeDefault(c.config.ParseTime) {
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
	// parseTime 默认注入：time.Time 字段扫描必需（connection 层读侧依赖；
	// Config.ParseTime=false 或 DSN 已带参时不注入）
	if parseTimeDefault(c.config.ParseTime) && !hasDsnParam(dsn, "parseTime") {
		setParam("parseTime", "true")
	}
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

// parseTimeDefault ParseTime 开关缺省语义：nil/true → 注入。
func parseTimeDefault(v *bool) bool {
	return v == nil || *v
}

// ---- Postgres ----

// PostgresConnector Postgres 连接器（对应 Connectors\PostgresConnector）：
// 自动附加 sslmode/search_path/timezone DSN 参数；isolation_level/
// synchronous_commit 经 DSN options=-c 参数生效（连接池下每个物理连接
// 均有效——libpq 标准参数，lib/pq 支持）。
type PostgresConnector struct {
	config Config
}

// Connect 打开 Postgres 连接。
func (c *PostgresConnector) Connect(dsn string) (*sql.DB, error) {
	return openWithLostConnectionRetry(c.config.Driver, c.BuildDsn(dsn))
}

// BuildDsn 附加 sslmode/sslcert/sslkey/sslrootcert/search_path/client_encoding/
// application_name/TimeZone/options 参数（已有参数不覆盖）。
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
	// 会话级参数 → options=-c 形式（池安全；驱动不支持 options 时
	// 建连报错，比"只在随机一个连接上生效"更早暴露问题）
	var opts []string
	if c.config.IsolationLevel != "" && !hasDsnParam(dsn, "options") {
		opts = append(opts, "-c default_transaction_isolation="+c.config.IsolationLevel)
	}
	if c.config.SynchronousCommit != "" && !hasDsnParam(dsn, "options") {
		opts = append(opts, "-c synchronous_commit="+c.config.SynchronousCommit)
	}
	if len(opts) > 0 {
		// lib/pq 的 parseOpts 按空白切分，值必须单引号包裹（libpq 标准转义）
		adds = append(adds, "options='"+strings.Join(opts, " ")+"'")
	}
	return appendDsnParams(dsn, adds)
}

// ---- SQLite ----

// SQLiteConnector SQLite 连接器（对应 Connectors\SQLiteConnector）：全部
// pragmas 经驱动 DSN 参数生效（_foreign_keys/_busy_timeout/_journal_mode/
// _synchronous/_pragma）——连接池下每个物理连接均生效；旧实现自定义
// pragmas 用 db.Exec 只落在池内随机一个连接上。
type SQLiteConnector struct {
	config Config
}

// Connect 打开 SQLite 连接。
func (c *SQLiteConnector) Connect(dsn string) (*sql.DB, error) {
	return openWithLostConnectionRetry(c.config.Driver, c.BuildDsn(dsn))
}

// BuildDsn 附加 sqlite pragma 参数。foreign_keys 缺省开启
// （与 SQLite 默认 foreign_keys=true 对齐；显式 false 可关闭）。
func (c *SQLiteConnector) BuildDsn(dsn string) string {
	var adds []string
	if c.config.ForeignKeys != nil {
		v := "off"
		if *c.config.ForeignKeys {
			v = "on"
		}
		adds = append(adds, "_foreign_keys="+v)
	} else {
		adds = append(adds, "_foreign_keys=on")
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
	// 自定义 pragmas → mattn 的 _pragma=name(value) 形式（按键名排序保证确定性）
	if len(c.config.Pragmas) > 0 {
		keys := make([]string, 0, len(c.config.Pragmas))
		for k := range c.config.Pragmas {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			adds = append(adds, "_pragma="+k+"("+c.config.Pragmas[k]+")")
		}
	}
	return appendDsnParams(dsn, adds)
}

// ---- SQLServer ----

// SqlServerConnector SQLServer 连接器（对应 Connectors\SqlServerConnector）。
// 注意：go-mssqldb 的 DSN 不支持隔离级别参数，isolation_level 仍以建连后
// SET 执行——连接池下仅对执行时选中的连接生效（已知限制，需要精确控制时
// 建议在事务内显式 set transaction isolation level）。
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
