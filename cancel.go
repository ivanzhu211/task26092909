package checkout

// CancelOrder 取消订单：归还库存，并归还该订单占用的折扣次数。
// 重复取消是幂等空操作，不会重复归还。
func (e *Engine) CancelOrder(merchantID string, orderID int64) error {
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	row := tx.QueryRow(
		`SELECT id, merchant_id, cart_id, idempotency_key, discount_code, subtotal_cents, discount_cents, total_cents, cancelled, created_at
		 FROM orders WHERE id = ? AND merchant_id = ?`, orderID, merchantID)
	order, err := scanOrder(row)
	if err != nil {
		return err
	}
	if order.Cancelled {
		return nil // 幂等：已取消，不再归还
	}

	// 条件更新保证并发下只流转一次。
	res, err := tx.Exec(`UPDATE orders SET cancelled = 1 WHERE id = ? AND cancelled = 0`, orderID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return nil // 已被并发取消
	}

	// 归还库存。
	rows, err := tx.Query(`SELECT product_id, quantity FROM order_lines WHERE order_id = ?`, orderID)
	if err != nil {
		return err
	}
	type restore struct {
		productID int64
		qty       int64
	}
	var restores []restore
	for rows.Next() {
		var r restore
		if err := rows.Scan(&r.productID, &r.qty); err != nil {
			rows.Close()
			return err
		}
		restores = append(restores, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, r := range restores {
		if _, err := tx.Exec(`UPDATE products SET stock = stock + ? WHERE id = ?`, r.qty, r.productID); err != nil {
			return err
		}
	}
	// 折扣次数由 orders.cancelled 派生，标记取消即归还。
	return tx.Commit()
}
