package torm

import (
	"database/sql"
	"errors"
	"log"
	"strconv"

	"github.com/go-think/torm/events"
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
	// 管理层 ::read 固定路由：写语句也走只读连接（对应 setPdoForType 'read'）
	c.stateMu.Lock()
	pinned := c.pinnedType
	c.stateMu.Unlock()
	c.connMu.Lock()
	readDB, mainDB := c.readDB, c.DB
	c.connMu.Unlock()
	if pinned == "read" && readDB != nil {
		return readDB
	}
	return mainDB
}

// txHook 是带注册层级的 afterCommit/afterRollBack 回调。层级用于在部分回滚时
// 精确丢弃/触发对应事务注册的回调（对齐 DatabaseTransactionsManager 的记录树）。
type txHook struct {
	level int
	fn    func()
}

// dispatchTxEvent 分发事务生命周期事件（无 dispatcher 时为 no-op）。
func (c *Connection) dispatchTxEvent(name string) {
	c.txMu.Lock()
	level := c.level
	c.txMu.Unlock()
	c.dispatchTxEventWithLevel(name, level)
}

// dispatchTxEventWithLevel 以显式层级分发事务事件（锁外执行）。
func (c *Connection) dispatchTxEventWithLevel(name string, level int) {
	c.stateMu.Lock()
	d := c.dispatcher
	name2 := c.name
	c.stateMu.Unlock()
	if d != nil {
		d.Dispatch(events.TransactionEvent{Name: name, TransactionLevel: level, ConnectionName: name2})
	}
}

// dispatchTxEventLocked 在已持 txMu 锁的情况下分发事务事件（事件监听器不得
// 调用事务 API）。dispatcher/name 由 stateMu 保护：先在 stateMu 下拷贝快照，
// 锁外分发（旧实现持 txMu 裸读 stateMu 保护的字段，构成数据竞争）。
func (c *Connection) dispatchTxEventLocked(name string, level int) {
	c.stateMu.Lock()
	d := c.dispatcher
	name2 := c.name
	c.stateMu.Unlock()
	if d != nil {
		d.Dispatch(events.TransactionEvent{
			Name:             name,
			TransactionLevel: level,
			ConnectionName:   name2,
		})
	}
}

// BeginTransaction 开启事务；嵌套时创建 savepoint（嵌套事务管理）。
// level 0 的 BEGIN 遇断连错误重连后重试一次
// （handleBeginTransactionException）。
func (c *Connection) BeginTransaction() error {
	c.txMu.Lock()
	fns := make([]func(), len(c.beforeStartingTransaction))
	copy(fns, c.beforeStartingTransaction)
	c.txMu.Unlock()
	for _, fn := range fns {
		fn()
	}
	c.txMu.Lock()
	defer c.txMu.Unlock()

	if c.level == 0 {
		// c.DB 由 connMu 保护（重连热替换）；事务恒开在主（写）连接上
		c.connMu.Lock()
		db := c.DB
		c.connMu.Unlock()
		tx, err := db.Begin()
		if err != nil && classifyError(err) == KindLostConnection {
			// 断连重连必须先释放 txMu：reconnect 经 Manager.Reconnect 会
			// 调 TransactionLevel()（取 txMu），持锁调用构成自死锁
			c.txMu.Unlock()
			rerr := c.reconnect()
			c.txMu.Lock()
			if rerr != nil {
				return err
			}
			// 重入后复验：并发路径可能在解锁窗口内已开事务
			if c.level != 0 || c.tx != nil {
				return errors.New("torm: transaction state changed during reconnect")
			}
			c.connMu.Lock()
			db = c.DB
			c.connMu.Unlock()
			tx, err = db.Begin()
			if err != nil {
				return err
			}
		}
		if err != nil {
			return err
		}
		c.tx = tx
	} else if c.queryGrammarForTxSupportsSavepoints() {
		// SAVEPOINT trans{level+1}（supportsSavepoints 守卫，C1③）
		if _, err := c.tx.Exec(c.grammarForTx().CompileSavepoint("trans" + strconv.Itoa(c.level+1))); err != nil {
			return err
		}
	}
	// beganTransaction 事件在 begin 成功后、计数递增后分发
	// （紧跟 level++ 之后）
	c.level++
	c.dispatchTxEventLocked(events.TransactionBeginning, c.level)
	return nil
}

// queryGrammarForTxSupportsSavepoints 对应 Query Grammar::supportsSavepoints
// （基类恒 true，方言可覆写）。
func (c *Connection) queryGrammarForTxSupportsSavepoints() bool {
	type sp interface{ SupportsSavepoints() bool }
	if g, ok := c.grammarForTx().(sp); ok {
		return g.SupportsSavepoints()
	}
	return true
}

// Commit 提交当前事务层；仅最外层提交到数据库。afterCommit 回调在释放事务
// 锁之后执行：回调内允许再次调用 TransactionLevel/AfterCommit/Commit 而不会
// 自死锁（commit() 在锁外通知 manager）。
func (c *Connection) Commit() error {
	var runHooks []func()
	var runRollbackHooks []func()

	c.txMu.Lock()
	committedLevel := c.level
	// committing 事件仅最外层（transactionLevel===1）时、pdo commit 之前
	// 分发
	if c.level == 1 {
		c.dispatchTxEventLocked(events.TransactionCommitting, c.level)
	}
	if c.level == 1 && c.tx != nil {
		if err := c.tx.Commit(); err != nil {
			// handleCommitTransactionException：先无条件 transactions--
			// 状态清理全部在锁内完成（解锁后写 txMu 保护字段构成数据竞争）
			if classifyError(err) == KindLostConnection {
				// 断连：事务已随连接丢失，计数清零（不重试 commit）
				c.tx = nil
				c.level = 0
				c.afterCommitHooks = nil
			} else if classifyError(err) == KindDeadlock {
				// 并发错误：事务已死（tx 置 nil）后由外层 Transaction 重试循环
				// 重新 BEGIN（:232-251）。该层注册的 afterCommit 回调必须丢弃
				// （否则会在重试成功后的提交、乃至之后任何无关提交时被误执行），
				// afterRollBack 回调此刻触发——与 rollBackToLevel 的断连分支对称
				c.tx = nil
				c.level = 0
				c.afterCommitHooks = nil
				for _, h := range c.afterRollbackHooks {
					runRollbackHooks = append(runRollbackHooks, h.fn)
				}
				c.afterRollbackHooks = nil
			} else {
				c.txMu.Unlock()
				return err
			}
			c.txMu.Unlock()
			for _, fn := range runRollbackHooks {
				fn()
			}
			return err
		}
		c.tx = nil
		// 最外层提交：全部 afterCommit 回调就绪执行（解锁后运行）
		runHooks = make([]func(), 0, len(c.afterCommitHooks))
		for _, h := range c.afterCommitHooks {
			runHooks = append(runHooks, h.fn)
		}
		c.afterCommitHooks = nil
		c.level = 0
	} else if c.level > 0 {
		// 内层提交：回调上浮到父事务层
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

	// manager.commit 先执行 afterCommit 回调，再发 committed 事件
	for _, fn := range runHooks {
		fn()
	}
	// committed 事件携带提交前的事务层（提交完成后 level 已归零，
	// 事件载荷若读 c.level 恒为 0——名存实亡）
	c.dispatchTxEventWithLevel(events.TransactionCommitted, committedLevel)
	return nil
}

// RollBack 回滚到上一层事务；toLevel<0 或越界时为 no-op。
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
	// rolledBack 事件携带回滚前的事务层
	rolledBackLevel := c.level

	var err error
	if toLevel == 0 {
		if c.tx != nil {
			err = c.tx.Rollback()
			c.tx = nil
		}
	} else if c.tx != nil && c.queryGrammarForTxSupportsSavepoints() {
		// ROLLBACK TO SAVEPOINT trans{toLevel+1}（supportsSavepoints 守卫）
		_, err = c.tx.Exec(c.grammarForTx().CompileSavepointRollBack("trans" + strconv.Itoa(toLevel+1)))
	}
	if err != nil {
		// handleRollBackException：断连时 transactions=0、按层级降序
		// 执行全部 afterRollBack 回调、丢弃 afterCommit 回调，然后重抛
		if classifyError(err) == KindLostConnection {
			c.tx = nil
			c.level = 0
			for _, h := range c.afterRollbackHooks {
				runRollbackHooks = append(runRollbackHooks, h.fn)
			}
			c.afterRollbackHooks = nil
			c.afterCommitHooks = nil
		}
		c.txMu.Unlock()
		for _, fn := range runRollbackHooks {
			fn()
		}
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

	// manager.rollback 先执行 afterRollBack 回调，再发 rollingBack
	for _, fn := range runRollbackHooks {
		fn()
	}
	c.dispatchTxEventWithLevel(events.TransactionRolledBack, rolledBackLevel)
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
// 层级并把错误交还外层处理——嵌套层级不自行重试
// （handleTransactionException：仅最外层事务参与重试）。
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

		if err := c.runTxCallback(fn, startLevel); err != nil {
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
			// 并发/断连错误时 Commit 内部已将事务打散（level 归零/tx 丢弃），
			// 无需再回滚；仅嵌套降级路径需要显式回滚到入口 savepoint
			if isConcurrencyError(err) || classifyError(err) == KindLostConnection {
				if startLevel == 0 && isConcurrencyError(err) && attempt < maxAttempts {
					continue
				}
				return err
			}
			_ = c.rollBackToLevel(startLevel)
			return err
		}
	}
	return nil
}

// runTxCallback 执行事务回调；panic 时先回滚到入口层级再重抛（对齐
// try/catch(Throwable) 的回滚语义——旧实现 panic 直接逃逸，事务不回滚、
// 连接被 *sql.Tx 永久占用、hook 泄漏、level 卡死）。
func (c *Connection) runTxCallback(fn func(*Connection) error, startLevel int) (err error) {
	defer func() {
		if r := recover(); r != nil {
			// 回滚失败不吞：记日志后重抛原始 panic（level/tx 卡留可由
			// 连接关闭兜底，原始 panic 栈优先保留）
			if rerr := c.rollBackToLevel(startLevel); rerr != nil {
				log.Printf("torm: rollback after panic failed: %v", rerr)
			}
			panic(r)
		}
	}()
	return fn(c)
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

// AfterRollBack 注册回滚回调：所在事务（或其内层）发生回滚时执行。无活动
// 事务时回调被静默丢弃（对齐 DatabaseTransactionsManager::addCallbackForRollback）。
func (c *Connection) AfterRollBack(fn func()) {
	c.txMu.Lock()
	if c.level == 0 {
		c.txMu.Unlock()
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
