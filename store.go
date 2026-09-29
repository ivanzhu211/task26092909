package checkout

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// CartTTL 购物车最后一次修改后的过期时长。
const CartTTL = 72 * time.Hour

// Engine 结账引擎。上层店铺挂件后端持有并调用。
// 所有方法可并发调用；内部通过单连接序列化写事务，保证不超卖、不超用折扣。
type Engine struct {
	db  *sql.DB
	now func() time.Time
}

// Option 引擎可选配置。
type Option func(*Engine)

// WithClock 注入时钟（测试用），默认为 time.Now。
func WithClock(now func() time.Time) Option {
	return func(e *Engine) { e.now = now }
}

// Open 打开（必要时创建）SQLite 数据库并初始化 schema。
// path 传 ":memory:" 可得纯内存库。
func Open(path string, opts ...Option) (*Engine, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// 单连接序列化所有事务，避免 SQLITE_BUSY 技术性错误外泄。
	db.SetMaxOpenConns(1)
	e := &Engine{db: db, now: time.Now}
	for _, opt := range opts {
		opt(e)
	}
	if err := e.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return e, nil
}

// Close 关闭引擎。
func (e *Engine) Close() error {
	return e.db.Close()
}

func (e *Engine) migrate() error {
	_, err := e.db.Exec(schema)
	return err
}

const schema = `
CREATE TABLE IF NOT EXISTS products (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  merchant_id TEXT    NOT NULL,
  name        TEXT    NOT NULL,
  price_cents INTEGER NOT NULL,
  stock       INTEGER NOT NULL,
  on_shelf    INTEGER NOT NULL DEFAULT 1,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_products_merchant ON products(merchant_id);

CREATE TABLE IF NOT EXISTS discount_codes (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  merchant_id     TEXT    NOT NULL,
  code            TEXT    NOT NULL,
  kind            INTEGER NOT NULL,
  percent         INTEGER NOT NULL DEFAULT 0,
  amount_cents    INTEGER NOT NULL DEFAULT 0,
  min_spend_cents INTEGER NOT NULL DEFAULT 0,
  max_uses        INTEGER NOT NULL DEFAULT 0,
  created_at      INTEGER NOT NULL,
  UNIQUE(merchant_id, code)
);

CREATE TABLE IF NOT EXISTS carts (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  merchant_id      TEXT    NOT NULL,
  token            TEXT    NOT NULL,
  discount_code_id INTEGER NOT NULL DEFAULT 0,
  checked_out      INTEGER NOT NULL DEFAULT 0,
  last_modified_at INTEGER NOT NULL,
  created_at       INTEGER NOT NULL,
  UNIQUE(merchant_id, token)
);

CREATE TABLE IF NOT EXISTS cart_items (
  cart_id    INTEGER NOT NULL,
  product_id INTEGER NOT NULL,
  quantity   INTEGER NOT NULL,
  PRIMARY KEY (cart_id, product_id)
);

CREATE TABLE IF NOT EXISTS orders (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  merchant_id     TEXT    NOT NULL,
  cart_id         INTEGER NOT NULL,
  idempotency_key TEXT    NOT NULL,
  discount_code_id INTEGER NOT NULL DEFAULT 0,
  discount_code   TEXT    NOT NULL DEFAULT '',
  subtotal_cents  INTEGER NOT NULL,
  discount_cents  INTEGER NOT NULL,
  total_cents     INTEGER NOT NULL,
  cancelled       INTEGER NOT NULL DEFAULT 0,
  created_at      INTEGER NOT NULL,
  UNIQUE(merchant_id, idempotency_key)
);

CREATE TABLE IF NOT EXISTS order_lines (
  order_id          INTEGER NOT NULL,
  product_id        INTEGER NOT NULL,
  product_name      TEXT    NOT NULL,
  quantity          INTEGER NOT NULL,
  unit_price_cents  INTEGER NOT NULL,
  line_total_cents  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_order_lines_order ON order_lines(order_id);
`

// nowNano 当前时刻的 Unix 纳秒。
func (e *Engine) nowNano() int64 {
	return e.now().UnixNano()
}
