package checkout

import (
	"database/sql"
	"errors"
	"time"
)

const (
	maxCartItemKinds = 50
	maxItemQuantity  = 99
)

// GetOrCreateCart 凭会话令牌取车，不存在则创建。
func (e *Engine) GetOrCreateCart(merchantID, token string) (*Cart, error) {
	if merchantID == "" || token == "" {
		return nil, ErrInvalidArgument
	}
	now := e.now().Unix()
	// 已存在则忽略插入，保持原车的 updated_at。
	if _, err := e.db.Exec(
		`INSERT OR IGNORE INTO carts (merchant_id, token, status, updated_at, created_at) VALUES (?, ?, ?, ?, ?)`,
		merchantID, token, CartStatusActive, now, now,
	); err != nil {
		return nil, err
	}
	return e.GetCart(merchantID, token)
}

// GetCart 读取购物车（含行项目与折扣码）。
func (e *Engine) GetCart(merchantID, token string) (*Cart, error) {
	var c Cart
	var updatedAt, createdAt int64
	err := e.db.QueryRow(
		`SELECT c.id, c.merchant_id, c.token, c.status, c.discount_code_id, c.updated_at, c.created_at,
		        COALESCE(d.code, '')
		 FROM carts c LEFT JOIN discount_codes d ON d.id = c.discount_code_id
		 WHERE c.merchant_id = ? AND c.token = ?`,
		merchantID, token,
	).Scan(&c.ID, &c.MerchantID, &c.Token, &c.Status, &c.DiscountCodeID, &updatedAt, &createdAt, &c.DiscountCode)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.UpdatedAt = time.Unix(updatedAt, 0)
	c.CreatedAt = time.Unix(createdAt, 0)
	items, err := e.loadCartItems(c.ID)
	if err != nil {
		return nil, err
	}
	c.Items = items
	return &c, nil
}

func (e *Engine) loadCartItems(cartID int64) ([]CartItem, error) {
	rows, err := e.db.Query(
		`SELECT product_id, quantity FROM cart_items WHERE cart_id = ? ORDER BY product_id`, cartID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []CartItem{}
	for rows.Next() {
		var it CartItem
		if err := rows.Scan(&it.ProductID, &it.Quantity); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// loadMutableCart 在事务内取出可修改的车（不存在则创建）：已冻结、已过期都报错。
func loadMutableCart(tx *sql.Tx, now time.Time, merchantID, token string) (int64, error) {
	if _, err := tx.Exec(
		`INSERT OR IGNORE INTO carts (merchant_id, token, status, updated_at, created_at) VALUES (?, ?, ?, ?, ?)`,
		merchantID, token, CartStatusActive, now.Unix(), now.Unix(),
	); err != nil {
		return 0, err
	}
	var cartID int64
	var status CartStatus
	var updatedAt int64
	err := tx.QueryRow(
		`SELECT id, status, updated_at FROM carts WHERE merchant_id = ? AND token = ?`,
		merchantID, token,
	).Scan(&cartID, &status, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if status != CartStatusActive {
		return 0, ErrCartFrozen
	}
	if cartExpired(now, time.Unix(updatedAt, 0)) {
		return 0, ErrCartExpired
	}
	return cartID, nil
}

func touchCart(tx *sql.Tx, now time.Time, cartID int64) error {
	_, err := tx.Exec(`UPDATE carts SET updated_at = ? WHERE id = ?`, now.Unix(), cartID)
	return err
}

// SetItemQuantity 加购或改数量；quantity 为 0 表示移除。
func (e *Engine) SetItemQuantity(merchantID, token string, productID, quantity int64) (*Cart, error) {
	if quantity < 0 || quantity > maxItemQuantity {
		return nil, ErrInvalidArgument
	}
	now := e.now()
	err := e.withTx(func(tx *sql.Tx) error {
		cartID, err := loadMutableCart(tx, now, merchantID, token)
		if err != nil {
			return err
		}
		if quantity == 0 {
			if _, err := tx.Exec(`DELETE FROM cart_items WHERE cart_id = ? AND product_id = ?`, cartID, productID); err != nil {
				return err
			}
			return touchCart(tx, now, cartID)
		}
		// 商品必须属于本商户且在架上。
		var stock int64
		var status ProductStatus
		err = tx.QueryRow(
			`SELECT stock, status FROM products WHERE id = ? AND merchant_id = ?`, productID, merchantID,
		).Scan(&stock, &status)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if status != ProductStatusOnShelf {
			return ErrProductOffShelf
		}
		if stock < quantity {
			return &InsufficientStockError{Shortages: []StockShortage{{
				ProductID: productID, Requested: quantity, Available: stock,
			}}}
		}
		// 新品种要检查 50 种上限。
		var existing int64
		if err := tx.QueryRow(
			`SELECT COUNT(1) FROM cart_items WHERE cart_id = ? AND product_id = ?`, cartID, productID,
		).Scan(&existing); err != nil {
			return err
		}
		if existing == 0 {
			var kinds int64
			if err := tx.QueryRow(`SELECT COUNT(1) FROM cart_items WHERE cart_id = ?`, cartID).Scan(&kinds); err != nil {
				return err
			}
			if kinds >= maxCartItemKinds {
				return ErrCartFull
			}
		}
		if _, err := tx.Exec(
			`INSERT INTO cart_items (cart_id, product_id, quantity) VALUES (?, ?, ?)
			 ON CONFLICT (cart_id, product_id) DO UPDATE SET quantity = excluded.quantity`,
			cartID, productID, quantity,
		); err != nil {
			return err
		}
		return touchCart(tx, now, cartID)
	})
	if err != nil {
		return nil, err
	}
	return e.GetCart(merchantID, token)
}

// ApplyDiscount 挂折扣码；code 为空字符串表示摘除。
func (e *Engine) ApplyDiscount(merchantID, token, code string) (*Cart, error) {
	now := e.now()
	err := e.withTx(func(tx *sql.Tx) error {
		cartID, err := loadMutableCart(tx, now, merchantID, token)
		if err != nil {
			return err
		}
		var codeID int64
		if code != "" {
			err = tx.QueryRow(
				`SELECT id FROM discount_codes WHERE merchant_id = ? AND code = ?`, merchantID, code,
			).Scan(&codeID)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`UPDATE carts SET discount_code_id = ? WHERE id = ?`, codeID, cartID); err != nil {
			return err
		}
		return touchCart(tx, now, cartID)
	})
	if err != nil {
		return nil, err
	}
	return e.GetCart(merchantID, token)
}
