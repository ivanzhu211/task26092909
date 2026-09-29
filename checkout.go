package checkout

import (
	"database/sql"
	"errors"
	"time"
)

// Checkout 结账：把车转成订单，同事务内扣库存、占用一次折扣次数。
// idempotencyKey 在商户内唯一标识本次结账：
//   - 同键同车重复提交：返回同一张订单；
//   - 同键不同车：ErrIdempotencyConflict；
//   - 失败的结账不占用键，可原样重试。
func (e *Engine) Checkout(merchantID, token, idempotencyKey string) (*Order, error) {
	if merchantID == "" || token == "" {
		return nil, validationErr("merchant id and token must not be empty")
	}
	if idempotencyKey == "" {
		return nil, validationErr("idempotency key must not be empty")
	}

	tx, err := e.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// 幂等：该键是否已结过账。
	existing, err := getOrderByKeyTx(tx, merchantID, idempotencyKey)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if existing != nil {
		cartOfOrder, err := getCartByIDTx(tx, merchantID, existing.CartID)
		if err != nil {
			return nil, err
		}
		if cartOfOrder.Token != token {
			return nil, ErrIdempotencyConflict
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return existing, nil
	}

	cart, err := getCartByToken(tx, merchantID, token)
	if err != nil {
		return nil, err
	}
	if cart.CheckedOut {
		return nil, ErrCartFrozen
	}
	if e.isExpired(cart) {
		return nil, ErrCartExpired
	}

	// 读明细并逐行校验商品状态与库存，收集所有缺货商品。
	rows, err := tx.Query(
		`SELECT ci.product_id, p.name, ci.quantity, p.price_cents, p.stock, p.on_shelf
		 FROM cart_items ci JOIN products p ON p.id = ci.product_id
		 WHERE ci.cart_id = ? ORDER BY ci.product_id`, cart.ID)
	if err != nil {
		return nil, err
	}
	type line struct {
		productID int64
		name      string
		qty       int64
		price     int64
	}
	var lines []line
	var shortages []StockShortage
	for rows.Next() {
		var l line
		var stock, onShelf int64
		if err := rows.Scan(&l.productID, &l.name, &l.qty, &l.price, &stock, &onShelf); err != nil {
			rows.Close()
			return nil, err
		}
		if onShelf == 0 {
			rows.Close()
			return nil, ErrProductOffShelf
		}
		if stock < l.qty {
			shortages = append(shortages, StockShortage{
				ProductID: l.productID, Name: l.name, Requested: l.qty, Available: stock,
			})
		}
		lines = append(lines, l)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	if len(lines) == 0 {
		return nil, validationErr("cart is empty")
	}
	if len(shortages) > 0 {
		return nil, &InsufficientStockError{Items: shortages}
	}

	// 同一事务内取价，保证所有行单价来自同一时刻。
	var subtotal int64
	for _, l := range lines {
		subtotal += l.price * l.qty
	}

	// 折扣：门槛与剩余次数按结账这一刻计算。
	var discount int64
	var discountID int64
	var discountCode string
	if cart.DiscountCodeID != 0 {
		d, err := getDiscountByIDTx(tx, merchantID, cart.DiscountCodeID)
		if err != nil {
			return nil, err
		}
		if subtotal < d.MinSpendCents {
			return nil, ErrBelowMinSpend
		}
		if d.MaxUses > 0 {
			used, err := countActiveUsesTx(tx, d.ID)
			if err != nil {
				return nil, err
			}
			if used >= d.MaxUses {
				return nil, ErrDiscountExhausted
			}
		}
		discount = computeDiscount(d, subtotal)
		discountID = d.ID
		discountCode = d.Code
	}

	// 扣库存（同一事务内已校验充足）。
	for _, l := range lines {
		if _, err := tx.Exec(`UPDATE products SET stock = stock - ? WHERE id = ?`, l.qty, l.productID); err != nil {
			return nil, err
		}
	}

	// 写订单与明细，冻结购物车。
	now := e.nowNano()
	res, err := tx.Exec(
		`INSERT INTO orders (merchant_id, cart_id, idempotency_key, discount_code_id, discount_code,
			subtotal_cents, discount_cents, total_cents, cancelled, created_at)
		 VALUES (?,?,?,?,?,?,?,?,0,?)`,
		merchantID, cart.ID, idempotencyKey, discountID, discountCode,
		subtotal, discount, subtotal-discount, now)
	if err != nil {
		return nil, err
	}
	orderID, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	order := &Order{
		ID:             orderID,
		MerchantID:     merchantID,
		CartID:         cart.ID,
		IdempotencyKey: idempotencyKey,
		DiscountCode:   discountCode,
		SubtotalCents:  subtotal,
		DiscountCents:  discount,
		TotalCents:     subtotal - discount,
		CreatedAt:      time.Unix(0, now),
	}
	for _, l := range lines {
		lineTotal := l.price * l.qty
		if _, err := tx.Exec(
			`INSERT INTO order_lines (order_id, product_id, product_name, quantity, unit_price_cents, line_total_cents)
			 VALUES (?,?,?,?,?,?)`,
			orderID, l.productID, l.name, l.qty, l.price, lineTotal); err != nil {
			return nil, err
		}
		order.Lines = append(order.Lines, OrderLine{
			ProductID:      l.productID,
			ProductName:    l.name,
			Quantity:       l.qty,
			UnitPriceCents: l.price,
			LineTotalCents: lineTotal,
		})
	}
	if _, err := tx.Exec(`UPDATE carts SET checked_out = 1 WHERE id = ?`, cart.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return order, nil
}

// computeDiscount 计算折扣金额：百分比四舍五入到分；固定金额最多减到 0。
func computeDiscount(d *DiscountCode, subtotal int64) int64 {
	switch d.Kind {
	case DiscountPercent:
		return (subtotal*int64(d.Percent) + 50) / 100
	case DiscountFixed:
		if d.AmountCents > subtotal {
			return subtotal
		}
		return d.AmountCents
	}
	return 0
}

// countActiveUsesTx 未取消订单占用的折扣次数。
func countActiveUsesTx(q querier, discountCodeID int64) (int64, error) {
	var n int64
	err := q.QueryRow(
		`SELECT COUNT(1) FROM orders WHERE discount_code_id = ? AND cancelled = 0`,
		discountCodeID).Scan(&n)
	return n, err
}

// GetOrder 查询订单（含明细）。
func (e *Engine) GetOrder(merchantID string, orderID int64) (*Order, error) {
	row := e.db.QueryRow(
		`SELECT id, merchant_id, cart_id, idempotency_key, discount_code, subtotal_cents, discount_cents, total_cents, cancelled, created_at
		 FROM orders WHERE id = ? AND merchant_id = ?`, orderID, merchantID)
	order, err := scanOrder(row)
	if err != nil {
		return nil, err
	}
	if err := e.loadOrderLines(e.db, order); err != nil {
		return nil, err
	}
	return order, nil
}

func getOrderByKeyTx(q querier, merchantID, key string) (*Order, error) {
	row := q.QueryRow(
		`SELECT id, merchant_id, cart_id, idempotency_key, discount_code, subtotal_cents, discount_cents, total_cents, cancelled, created_at
		 FROM orders WHERE merchant_id = ? AND idempotency_key = ?`, merchantID, key)
	order, err := scanOrder(row)
	if err != nil {
		return nil, err
	}
	if err := loadOrderLinesQ(q, order); err != nil {
		return nil, err
	}
	return order, nil
}

func getCartByIDTx(q querier, merchantID string, cartID int64) (*Cart, error) {
	row := q.QueryRow(
		`SELECT c.id, c.merchant_id, c.token, c.discount_code_id, IFNULL(d.code,''), c.checked_out, c.last_modified_at, c.created_at
		 FROM carts c LEFT JOIN discount_codes d ON d.id = c.discount_code_id
		 WHERE c.merchant_id = ? AND c.id = ?`, merchantID, cartID)
	return scanCart(row)
}

func getDiscountByIDTx(q querier, merchantID string, id int64) (*DiscountCode, error) {
	row := q.QueryRow(
		`SELECT id, merchant_id, code, kind, percent, amount_cents, min_spend_cents, max_uses, created_at
		 FROM discount_codes WHERE merchant_id = ? AND id = ?`, merchantID, id)
	return scanDiscountCode(row)
}

func scanOrder(row rowScanner) (*Order, error) {
	var o Order
	var cancelled int64
	var created int64
	err := row.Scan(&o.ID, &o.MerchantID, &o.CartID, &o.IdempotencyKey, &o.DiscountCode,
		&o.SubtotalCents, &o.DiscountCents, &o.TotalCents, &cancelled, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	o.Cancelled = cancelled != 0
	o.CreatedAt = time.Unix(0, created)
	return &o, nil
}

func (e *Engine) loadOrderLines(q querier, o *Order) error {
	return loadOrderLinesQ(q, o)
}

func loadOrderLinesQ(q querier, o *Order) error {
	rows, err := q.Query(
		`SELECT product_id, product_name, quantity, unit_price_cents, line_total_cents
		 FROM order_lines WHERE order_id = ? ORDER BY product_id`, o.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var l OrderLine
		if err := rows.Scan(&l.ProductID, &l.ProductName, &l.Quantity, &l.UnitPriceCents, &l.LineTotalCents); err != nil {
			return err
		}
		o.Lines = append(o.Lines, l)
	}
	return rows.Err()
}
