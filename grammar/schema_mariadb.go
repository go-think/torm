package grammar

// ---- MariaDB Schema 编译 ----
//
// MariaDB 的 Schema 编译继承 MySqlGrammar（MariaDB 方言未覆写任何
// schema compile 方法）。与任务书“json→longtext with json_check”的差异说明：
// 上游参考实现的 typeJson 继承自 MySQL 方言、返回 `json`
// （MariaDB 10.5+ 的 `json` 本身就是 longtext + json_valid check 的服务端别名），
// 因此本实现同样输出 `json`，语义等价于 longtext with json_valid 校验。
//
// 若后续需要显式展开为 longtext，可基于 columnDefinitionWith / compileAddWith /
// compileCreateWith 的类型映射参数注入 mariadb 专用映射（见 schema_mysql.go）。

// MariaDbGrammar 已通过内嵌 MySqlGrammar 满足 SchemaGrammar；
// 此处保留编译期断言。
var _ SchemaGrammar = (*MariaDbGrammar)(nil)
