package checkout

const schema = `
CREATE TABLE IF NOT EXISTS products (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    merchant_id TEXT NOT NULL,
    name        TEXT NOT NULL,
    price_cents INTEGER NOT NULL CHECK (price_cents >= 0),
    stock       INTEGER NOT NULL CHECK (stock >= 0),
    status      TEXT NOT NULL DEFAULT 'on_shelf'
);
CREATE INDEX IF NOT EXISTS idx_products_merchant ON products (merchant_id);

CREATE TABLE IF NOT EXISTS discount_codes (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    merchant_id        TEXT NOT NULL,
    code               TEXT NOT NULL,
    kind               TEXT NOT NULL,
    percent            INTEGER NOT NULL DEFAULT 0,
    amount_cents       INTEGER NOT NULL DEFAULT 0,
    min_subtotal_cents INTEGER NOT NULL DEFAULT 0,
    usage_limit        INTEGER NOT NULL DEFAULT 0,
    UNIQUE (merchant_id, code)
);

CREATE TABLE IF NOT EXISTS carts (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    merchant_id      TEXT NOT NULL,
    token            TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'active',
    discount_code_id INTEGER NOT NULL DEFAULT 0,
    updated_at       INTEGER NOT NULL,
    created_at       INTEGER NOT NULL,
    UNIQUE (merchant_id, token)
);

CREATE TABLE IF NOT EXISTS cart_items (
    cart_id    INTEGER NOT NULL REFERENCES carts (id),
    product_id INTEGER NOT NULL REFERENCES products (id),
    quantity   INTEGER NOT NULL CHECK (quantity >= 1 AND quantity <= 99),
    PRIMARY KEY (cart_id, product_id)
);

CREATE TABLE IF NOT EXISTS orders (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    merchant_id      TEXT NOT NULL,
    cart_id          INTEGER NOT NULL REFERENCES carts (id),
    status           TEXT NOT NULL DEFAULT 'paid',
    discount_code_id INTEGER NOT NULL DEFAULT 0,
    subtotal_cents   INTEGER NOT NULL,
    discount_cents   INTEGER NOT NULL,
    total_cents      INTEGER NOT NULL,
    idempotency_key  TEXT NOT NULL,
    created_at       INTEGER NOT NULL,
    canceled_at      INTEGER NOT NULL DEFAULT 0
);
-- 幂等键只在商户内唯一；只有成功的结账才会落订单，失败的结账不占键。
CREATE UNIQUE INDEX IF NOT EXISTS idx_orders_idem ON orders (merchant_id, idempotency_key);
CREATE INDEX IF NOT EXISTS idx_orders_discount ON orders (discount_code_id, status);

CREATE TABLE IF NOT EXISTS order_items (
    order_id         INTEGER NOT NULL REFERENCES orders (id),
    product_id       INTEGER NOT NULL,
    quantity         INTEGER NOT NULL,
    unit_price_cents INTEGER NOT NULL,
    PRIMARY KEY (order_id, product_id)
);
`
