package grammar

// MariaDbGrammar 对应 Illuminate\Database\Query\Grammars\MariaDbGrammar。
// MariaDB 10.x 与 MySQL 编译行为一致（本子集内无差异点），
// 独立类型以便方言注册表与后续差异扩展（如 REGEXP 运算符、sequence 特性）。
type MariaDbGrammar struct {
	MySqlGrammar
}

func NewMariaDbGrammar() *MariaDbGrammar {
	// MariaDB 编译行为与 MySQL 子集一致：复用 MySQL 的钩子配置。
	inner := NewMySqlGrammar()
	return &MariaDbGrammar{MySqlGrammar: *inner}
}
