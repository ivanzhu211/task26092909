package checkout

import (
	"database/sql"
	"time"

	_ "modernc.org/sqlite"
)

// Engine 结账引擎，由店铺挂件后端持有并调用。
// 所有写操作在单连接上以 IMMEDIATE 事务串行执行，保证不超卖、不超用。
type Engine struct {
	db  *sql.DB
	now func() time.Time
}

// Option 引擎可选配置。
type Option func(*Engine)

// WithClock 注入时钟，主要用于测试过期边界。
func WithClock(now func() time.Time) Option {
	return func(e *Engine) { e.now = now }
}

// Open 打开（必要时创建）SQLite 数据库并初始化 schema。
// path 传 ":memory:" 可得到纯内存库。
func Open(path string, opts ...Option) (*Engine, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// 单连接串行化所有事务，配合 BEGIN IMMEDIATE 杜绝写冲突。
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	e := &Engine{db: db, now: time.Now}
	for _, opt := range opts {
		opt(e)
	}
	return e, nil
}

// Close 关闭底层数据库。
func (e *Engine) Close() error { return e.db.Close() }

// cartTTL 购物车最后一次修改后的有效期。
const cartTTL = 72 * time.Hour

// cartExpired 判断 updatedAt 时刻最后修改的车在 now 时刻是否已过期：
// 满 72 小时即过期。
func cartExpired(now, updatedAt time.Time) bool {
	return !now.Before(updatedAt.Add(cartTTL))
}

// withTx 在事务里执行 fn；fn 返回错误则整体回滚，保证要么全改要么不改。
// 引擎使用单连接，所有事务天然串行，不会出现锁冲突。
func (e *Engine) withTx(fn func(tx *sql.Tx) error) error {
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
