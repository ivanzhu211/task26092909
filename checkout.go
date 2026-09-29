package checkout

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Checkout 把车转成订单：校验、扣库存、占用折扣次数，全部在一个事务里。
// idempotencyKey 必填；同键同车返回同一张订单，同键不同车报错；
// 失败的结账不落任何数据，可用原键重试。
func (e *Engine) Checkout(merchantID, token, idempotencyKey string) (*Order, error) {
	if merchantID == "" || token == "" || idempotencyKey == "" {
		return nil, ErrInvalidArgument
	}
	now := e.now()
	var order *Order
	err := e.withTx(func(tx *sql.Tx) error {
		var cartID int64
		var status CartStatus
		var updatedAt int64
		err := tx.QueryRow(
			`SELECT id, status, updated_at FROM carts WHERE merchant_id = ? AND token = ?`,
			merchantID, token,
		).Scan(&cartID, &status, &updatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}

		// 幂等重放：同键同车直接返回已生成的订单；同键不同车报错。
		existing, err := findOrderByIdempotencyKey(tx, merchantID, idempotencyKey)
		if err != nil {
			return err
		}
		if existing != nil {
			if existing.CartID != cartID {
				return ErrIdempotencyConflict
			}
			order = existing
			return nil
		}

		if status != CartStatusActive {
			return ErrCartFrozen
		}
		if cartExpired(now, time.Unix(updatedAt, 0)) {
			return ErrCartExpired
		}

		// 取车明细并联商品，所有单价都取自本事务这一刻。
		rows, err := tx.Query(
			`SELECT ci.product_id, ci.quantity, p.price_cents, p.stock, p.status
			 FROM cart_items ci JOIN products p ON p.id = ci.product_id AND p.merchant_id = ?
			 WHERE ci.cart_id = ? ORDER BY ci.product_id`,
			merchantID, cartID,
		)
		if err != nil {
			return err
		}
		type line struct {
			productID int64
			quantity  int64
			price     int64
		}
		var lines []line
		var shortages []StockShortage
		var subtotal int64
		for rows.Next() {
			var l line
			var stock int64
			var pstatus ProductStatus
			if err := rows.Scan(&l.productID, &l.quantity, &l.price, &stock, &pstatus); err != nil {
				rows.Close()
				return err
			}
			if pstatus != ProductStatusOnShelf {
				rows.Close()
				return fmt.Errorf("%w: product %d", ErrProductOffShelf, l.productID)
			}
			if stock < l.quantity {
				shortages = append(shortages, StockShortage{
					ProductID: l.productID, Requested: l.quantity, Available: stock,
				})
			}
			subtotal += l.price * l.quantity
			lines = append(lines, l)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(lines) == 0 {
			return ErrCartEmpty
		}
		if len(shortages) > 0 {
			return &InsufficientStockError{Shortages: shortages}
		}

		// 折扣：门槛按折前小计、剩余次数按此刻未取消订单数计算。
		var codeID int64
		var discount int64
		var kind string
		var percent, amount, minSubtotal, usageLimit int64
		err = tx.QueryRow(
			`SELECT d.id, d.kind, d.percent, d.amount_cents, d.min_subtotal_cents, d.usage_limit, d.code
			 FROM carts c JOIN discount_codes d ON d.id = c.discount_code_id
			 WHERE c.id = ? AND c.discount_code_id != 0`,
			cartID,
		).Scan(&codeID, &kind, &percent, &amount, &minSubtotal, &usageLimit, new(string))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if codeID != 0 {
			if subtotal < minSubtotal {
				return ErrDiscountMinSubtotal
			}
			if usageLimit > 0 {
				var used int64
				if err := tx.QueryRow(
					`SELECT COUNT(1) FROM orders WHERE discount_code_id = ? AND status = ?`,
					codeID, OrderStatusPaid,
				).Scan(&used); err != nil {
					return err
				}
				if used >= usageLimit {
					return ErrDiscountExhausted
				}
			}
			switch DiscountKind(kind) {
			case DiscountKindPercent:
				// 四舍五入到分。
				discount = (subtotal*percent + 50) / 100
			case DiscountKindFixed:
				discount = amount
				if discount > subtotal {
					discount = subtotal
				}
			}
		}

		// 落订单与明细。
		res, err := tx.Exec(
			`INSERT INTO orders (merchant_id, cart_id, status, discount_code_id, subtotal_cents, discount_cents, total_cents, idempotency_key, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			merchantID, cartID, OrderStatusPaid, codeID, subtotal, discount, subtotal-discount, idempotencyKey, now.Unix(),
		)
		if err != nil {
			return err
		}
		orderID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		for _, l := range lines {
			if _, err := tx.Exec(
				`INSERT INTO order_items (order_id, product_id, quantity, unit_price_cents) VALUES (?, ?, ?, ?)`,
				orderID, l.productID, l.quantity, l.price,
			); err != nil {
				return err
			}
			// 同事务内已校验过库存，这里的守卫是双保险。
			r, err := tx.Exec(
				`UPDATE products SET stock = stock - ? WHERE id = ? AND stock >= ?`,
				l.quantity, l.productID, l.quantity,
			)
			if err != nil {
				return err
			}
			if n, _ := r.RowsAffected(); n == 0 {
				return &InsufficientStockError{Shortages: []StockShortage{{ProductID: l.productID, Requested: l.quantity}}}
			}
		}
		// 冻结购物车。
		if _, err := tx.Exec(`UPDATE carts SET status = ? WHERE id = ?`, CartStatusCheckedOut, cartID); err != nil {
			return err
		}

		order, err = loadOrder(tx, merchantID, orderID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return order, nil
}

// CancelOrder 取消订单：归还库存，订单状态翻转后折扣次数自动释放。
// 重复取消直接返回订单，不会重复归还。
func (e *Engine) CancelOrder(merchantID string, orderID int64) (*Order, error) {
	var order *Order
	err := e.withTx(func(tx *sql.Tx) error {
		o, err := loadOrder(tx, merchantID, orderID)
		if errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if o.Status == OrderStatusCanceled {
			order = o
			return nil
		}
		for _, it := range o.Items {
			if _, err := tx.Exec(
				`UPDATE products SET stock = stock + ? WHERE id = ? AND merchant_id = ?`,
				it.Quantity, it.ProductID, merchantID,
			); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(
			`UPDATE orders SET status = ?, canceled_at = ? WHERE id = ? AND status = ?`,
			OrderStatusCanceled, e.now().Unix(), orderID, OrderStatusPaid,
		); err != nil {
			return err
		}
		order, err = loadOrder(tx, merchantID, orderID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return order, nil
}

// GetOrder 读取订单。
func (e *Engine) GetOrder(merchantID string, orderID int64) (*Order, error) {
	var order *Order
	err := e.withTx(func(tx *sql.Tx) error {
		o, err := loadOrder(tx, merchantID, orderID)
		if err != nil {
			return err
		}
		order = o
		return nil
	})
	if err != nil {
		return nil, err
	}
	return order, nil
}

func findOrderByIdempotencyKey(tx *sql.Tx, merchantID, key string) (*Order, error) {
	var id int64
	err := tx.QueryRow(
		`SELECT id FROM orders WHERE merchant_id = ? AND idempotency_key = ?`, merchantID, key,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return loadOrder(tx, merchantID, id)
}

func loadOrder(tx *sql.Tx, merchantID string, orderID int64) (*Order, error) {
	var o Order
	var createdAt, canceledAt int64
	err := tx.QueryRow(
		`SELECT o.id, o.merchant_id, o.cart_id, o.status, o.discount_code_id, o.subtotal_cents,
		        o.discount_cents, o.total_cents, o.idempotency_key, o.created_at, o.canceled_at,
		        COALESCE(d.code, '')
		 FROM orders o LEFT JOIN discount_codes d ON d.id = o.discount_code_id
		 WHERE o.id = ? AND o.merchant_id = ?`,
		orderID, merchantID,
	).Scan(&o.ID, &o.MerchantID, &o.CartID, &o.Status, &o.DiscountCodeID, &o.SubtotalCents,
		&o.DiscountCents, &o.TotalCents, &o.IdempotencyKey, &createdAt, &canceledAt, &o.DiscountCode)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	o.CreatedAt = time.Unix(createdAt, 0)
	if canceledAt != 0 {
		o.CanceledAt = time.Unix(canceledAt, 0)
	}
	rows, err := tx.Query(
		`SELECT product_id, quantity, unit_price_cents FROM order_items WHERE order_id = ? ORDER BY product_id`,
		orderID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	o.Items = []OrderItem{}
	for rows.Next() {
		var it OrderItem
		if err := rows.Scan(&it.ProductID, &it.Quantity, &it.UnitPriceCents); err != nil {
			return nil, err
		}
		o.Items = append(o.Items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &o, nil
}
