package torm

import (
	"database/sql"
	"strconv"
	"strings"
)

// queryer 统一 *sql.DB 与 *sql.Tx 的执行接口，事务内的查询路由到当前 tx。
type queryer interface {
	Prepare(query string) (*sql.Stmt, error)
	Query(query string, args ...any) (*sql.Rows, error)
	Exec(query string, args ...any) (sql.Result, error)
}

func (c *Connection) queryer() queryer {
	c.txMu.Lock()
	tx := c.tx
	c.txMu.Unlock()
	if tx != nil {
		return tx
	}
	return c.DB
}

// BeginTransaction 开启事务；嵌套时创建 savepoint（对齐 Laravel ManagesTransactions）。
func (c *Connection) BeginTransaction() error {
	c.txMu.Lock()
	defer c.txMu.Unlock()

	if c.level == 0 {
		tx, err := c.DB.Begin()
		if err != nil {
			return err
		}
		c.tx = tx
	} else {
		// SAVEPOINT trans{level+1}
		if _, err := c.tx.Exec(c.grammarForTx().CompileSavepoint("trans" + strconv.Itoa(c.level+1))); err != nil {
			return err
		}
	}
	c.level++
	return nil
}

// Commit 提交当前事务层；仅最外层提交到数据库。
func (c *Connection) Commit() error {
	c.txMu.Lock()
	defer c.txMu.Unlock()

	if c.level == 1 && c.tx != nil {
		if err := c.tx.Commit(); err != nil {
			return err
		}
		c.tx = nil
	}
	if c.level > 0 {
		c.level--
	}

	// 最外层提交后触发 afterCommit 队列
	if c.level == 0 && len(c.afterCommitHooks) > 0 {
		hooks := c.afterCommitHooks
		c.afterCommitHooks = nil
		for _, fn := range hooks {
			fn()
		}
	}
	return nil
}

// RollBack 回滚到上一层事务；toLevel<0 或越界时为 no-op（对齐 Laravel）。
func (c *Connection) RollBack() error {
	return c.rollBackToLevel(-1)
}

// RollBackToLevel 回滚到指定事务层。
func (c *Connection) RollBackToLevel(toLevel int) error {
	return c.rollBackToLevel(toLevel)
}

func (c *Connection) rollBackToLevel(toLevel int) error {
	c.txMu.Lock()
	defer c.txMu.Unlock()

	if toLevel < 0 {
		toLevel = c.level - 1
	}
	if toLevel < 0 || toLevel >= c.level {
		return nil
	}

	var err error
	if toLevel == 0 {
		if c.tx != nil {
			err = c.tx.Rollback()
			c.tx = nil
		}
	} else if c.tx != nil {
		// ROLLBACK TO SAVEPOINT trans{toLevel+1}
		_, err = c.tx.Exec(c.grammarForTx().CompileSavepointRollBack("trans" + strconv.Itoa(toLevel+1)))
	}
	if err != nil {
		return err
	}

	c.level = toLevel
	return nil
}

// TransactionLevel 返回当前事务层数。
func (c *Connection) TransactionLevel() int {
	c.txMu.Lock()
	defer c.txMu.Unlock()
	return c.level
}

// Transaction 在事务中执行回调；出错回滚并重试（最多 attempts 次，默认 1）。
// 嵌套调用自动降级为 savepoint。
func (c *Connection) Transaction(fn func(*Connection) error, attempts ...int) error {
	maxAttempts := 1
	if len(attempts) > 0 && attempts[0] > 0 {
		maxAttempts = attempts[0]
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := c.BeginTransaction(); err != nil {
			return err
		}

		if err := fn(c); err != nil {
			if rerr := c.RollBack(); rerr != nil {
				return rerr
			}
			// 死锁/锁等待：重试
			if isConcurrencyError(err) && attempt < maxAttempts {
				continue
			}
			return err
		}

		levelBefore := c.TransactionLevel()
		if err := c.Commit(); err != nil {
			_ = c.rollBackToLevel(0)
			// 提交失败且并发错误 → 重试
			if isConcurrencyError(err) && attempt < maxAttempts {
				continue
			}
			return err
		}
		_ = levelBefore
	}
	return nil
}

// AfterCommit 注册事务最外层提交后的回调；无活动事务时立即执行
// （对齐 Laravel TransactionsManager::addCallback）。
func (c *Connection) AfterCommit(fn func()) {
	c.txMu.Lock()
	if c.level == 0 {
		c.txMu.Unlock()
		fn()
		return
	}
	c.afterCommitHooks = append(c.afterCommitHooks, fn)
	c.txMu.Unlock()
}

// grammarForTx 事务内部 DDL/savepoint 编译。
func (c *Connection) grammarForTx() grammarLike {
	return c.GetQueryGrammar()
}

// grammarLike 事务语句编译所需的最小接口。
type grammarLike interface {
	CompileSavepoint(name string) string
	CompileSavepointRollBack(name string) string
}

// isConcurrencyError 判断死锁/锁等待错误（MySQL 1213/1205，SQLSTATE 40001）。
func isConcurrencyError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	for _, sub := range []string{"deadlock", "lock wait timeout", "40001"} {
		if strings.Contains(lower, sub) {
			return true
		}
	}
	return false
}
