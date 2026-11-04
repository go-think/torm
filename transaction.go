package torm

import (
	"database/sql"
	"strconv"
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

// txHook 是带注册层级的 afterCommit/afterRollBack 回调。层级用于在部分回滚时
// 精确丢弃/触发对应事务注册的回调（对齐 DatabaseTransactionsManager 的记录树）。
type txHook struct {
	level int
	fn    func()
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

// Commit 提交当前事务层；仅最外层提交到数据库。afterCommit 回调在释放事务
// 锁之后执行：回调内允许再次调用 TransactionLevel/AfterCommit/Commit 而不会
// 自死锁（对齐 Laravel commit() 在锁外通知 manager）。
func (c *Connection) Commit() error {
	var runHooks []func()

	c.txMu.Lock()
	if c.level == 1 && c.tx != nil {
		if err := c.tx.Commit(); err != nil {
			c.txMu.Unlock()
			return err
		}
		c.tx = nil
	}
	if c.level > 0 {
		// 内层提交：回调上浮到父事务层；最外层提交：全部就绪执行。
		var kept []txHook
		for _, h := range c.afterCommitHooks {
			if c.level == 1 {
				runHooks = append(runHooks, h.fn)
			} else {
				kept = append(kept, txHook{level: c.level - 1, fn: h.fn})
			}
		}
		c.afterCommitHooks = kept
		c.level--
	}
	c.txMu.Unlock()

	for _, fn := range runHooks {
		fn()
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
	if toLevel < 0 {
		c.txMu.Lock()
		toLevel = c.level - 1
		c.txMu.Unlock()
	}

	var runRollbackHooks []func()

	c.txMu.Lock()
	if toLevel < 0 || toLevel >= c.level {
		c.txMu.Unlock()
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
		c.txMu.Unlock()
		return err
	}

	// 被回滚层级注册的 afterCommit 回调必须丢弃（否则会在后续无关提交时被
	// 误执行），afterRollBack 回调此刻触发（对齐 DatabaseTransactionRecord
	// 的 addCallbackForRollback / rollback 语义）。
	var kept []txHook
	for _, h := range c.afterCommitHooks {
		if h.level > toLevel {
			continue
		}
		kept = append(kept, h)
	}
	c.afterCommitHooks = kept
	for _, h := range c.afterRollbackHooks {
		if h.level > toLevel {
			runRollbackHooks = append(runRollbackHooks, h.fn)
		}
	}
	var keptRollback []txHook
	for _, h := range c.afterRollbackHooks {
		if h.level <= toLevel {
			keptRollback = append(keptRollback, h)
		}
	}
	c.afterRollbackHooks = keptRollback
	c.level = toLevel
	c.txMu.Unlock()

	for _, fn := range runRollbackHooks {
		fn()
	}
	return nil
}

// TransactionLevel 返回当前事务层数。
func (c *Connection) TransactionLevel() int {
	c.txMu.Lock()
	defer c.txMu.Unlock()
	return c.level
}

// Transaction 在事务中执行回调。最外层遇到死锁/锁等待时回滚并重试（最多
// attempts 次，默认 1）；嵌套调用降级为 savepoint，任何错误都回滚到进入前的
// 层级并把错误交还外层处理——嵌套层级不自行重试（对齐 Laravel
// handleTransactionException：仅最外层事务参与重试）。
func (c *Connection) Transaction(fn func(*Connection) error, attempts ...int) error {
	maxAttempts := 1
	if len(attempts) > 0 && attempts[0] > 0 {
		maxAttempts = attempts[0]
	}

	startLevel := c.TransactionLevel()

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := c.BeginTransaction(); err != nil {
			return err
		}

		if err := fn(c); err != nil {
			if rerr := c.rollBackToLevel(startLevel); rerr != nil {
				return rerr
			}
			// 仅最外层事务（进入前 level==0）参与死锁/锁等待重试。
			if startLevel == 0 && isConcurrencyError(err) && attempt < maxAttempts {
				continue
			}
			return err
		}

		if err := c.Commit(); err != nil {
			_ = c.rollBackToLevel(startLevel)
			if startLevel == 0 && isConcurrencyError(err) && attempt < maxAttempts {
				continue
			}
			return err
		}
	}
	return nil
}

// AfterCommit 注册事务提交后的回调；注册时无活动事务则立即执行，嵌套事务中
// 注册的回调在其所在层级提交时上浮，最外层提交后统一执行；所在事务回滚时被
// 丢弃（对齐 DatabaseTransactionsManager::addCallback）。
func (c *Connection) AfterCommit(fn func()) {
	c.txMu.Lock()
	if c.level == 0 {
		c.txMu.Unlock()
		fn()
		return
	}
	c.afterCommitHooks = append(c.afterCommitHooks, txHook{level: c.level, fn: fn})
	c.txMu.Unlock()
}

// AfterRollBack 注册回滚回调：所在事务（或其内层）发生回滚时执行；无活动
// 事务时立即执行（对齐 ManagesTransactions::afterRollBack）。
func (c *Connection) AfterRollBack(fn func()) {
	c.txMu.Lock()
	if c.level == 0 {
		c.txMu.Unlock()
		fn()
		return
	}
	c.afterRollbackHooks = append(c.afterRollbackHooks, txHook{level: c.level, fn: fn})
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

// isConcurrencyError 判断死锁/锁等待错误，统一走 classifyError 的
// KindDeadlock 分类（文本 + SQLSTATE 双通道），避免两套判定不一致。
func isConcurrencyError(err error) bool {
	return classifyError(err) == KindDeadlock
}
