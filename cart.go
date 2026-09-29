package checkout

import (
	"database/sql"
	"errors"
	"time"
)

// 购物车限制。
const (
	MaxCartItems   = 50 // 最多 50 种商品
	MaxItemPerKind = 99 // 每种最多 99 件
)

type querier interface {
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
	Exec(query string, args ...any) (sql.Result, error)
}

// GetCart 按会话令牌取车（不存在则创建空车），返回含明细的视图。
func (e *Engine) GetCart(merchantID, token string) (*CartView, error) {
	if merchantID == "" || token == "" {
		return nil, validationErr("merchant id and token must not be empty")
	}
	cart, err := e.getOrCreateCart(e.db, merchantID, token)
	if err != nil {
		return nil, err
	}
	return e.cartView(e.db, cart)
}

// SetItemQuantity 加购或改数量；quantity 为 0 表示移除该商品。
// 按当时库存校验（加购不占库存）。
func (e *Engine) SetItemQuantity(merchantID, token string, productID, quantity int64) error {
	if quantity < 0 || quantity > MaxItemPerKind {
		return validationErr("quantity must be in [0,%d], got %d", MaxItemPerKind, quantity)
	}
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	cart, err := e.getOrCreateCart(tx, merchantID, token)
	if err != nil {
		return err
	}
	if err := e.assertCartMutable(cart); err != nil {
		return err
	}

	if quantity == 0 {
		if _, err := tx.Exec(`DELETE FROM cart_items WHERE cart_id = ? AND product_id = ?`, cart.ID, productID); err != nil {
			return err
		}
		return e.touchCart(tx, cart.ID)
	}

	// 商品必须属于本商户且在上架。
	p, err := getProductTx(tx, merchantID, productID)
	if err != nil {
		return err
	}
	if !p.OnShelf {
		return ErrProductOffShelf
	}
	if p.Stock < quantity {
		return &InsufficientStockError{Items: []StockShortage{{
			ProductID: p.ID, Name: p.Name, Requested: quantity, Available: p.Stock,
		}}}
	}

	// 新品加购时校验 50 种上限。
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM cart_items WHERE cart_id = ? AND product_id = ?`, cart.ID, productID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		var kinds int
		if err := tx.QueryRow(`SELECT COUNT(1) FROM cart_items WHERE cart_id = ?`, cart.ID).Scan(&kinds); err != nil {
			return err
		}
		if kinds >= MaxCartItems {
			return validationErr("cart holds at most %d kinds of products", MaxCartItems)
		}
	}

	if _, err := tx.Exec(
		`INSERT INTO cart_items (cart_id, product_id, quantity) VALUES (?,?,?)
		 ON CONFLICT(cart_id, product_id) DO UPDATE SET quantity = excluded.quantity`,
		cart.ID, productID, quantity); err != nil {
		return err
	}
	return e.touchCart(tx, cart.ID)
}

// ApplyDiscount 给车挂折扣码；code 为空串表示摘掉。
// 门槛与剩余次数在结账时才校验。
func (e *Engine) ApplyDiscount(merchantID, token, code string) error {
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	cart, err := e.getOrCreateCart(tx, merchantID, token)
	if err != nil {
		return err
	}
	if err := e.assertCartMutable(cart); err != nil {
		return err
	}

	var discountID int64
	if code != "" {
		d, err := getDiscountByCodeTx(tx, merchantID, code)
		if err != nil {
			return err
		}
		discountID = d.ID
	}
	if _, err := tx.Exec(`UPDATE carts SET discount_code_id = ? WHERE id = ?`, discountID, cart.ID); err != nil {
		return err
	}
	return e.touchCart(tx, cart.ID)
}

// getOrCreateCart 取车，不存在则创建。
func (e *Engine) getOrCreateCart(q querier, merchantID, token string) (*Cart, error) {
	cart, err := getCartByToken(q, merchantID, token)
	if err == nil {
		return cart, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	now := e.nowNano()
	if _, err := q.Exec(
		`INSERT INTO carts (merchant_id, token, discount_code_id, checked_out, last_modified_at, created_at)
		 VALUES (?,?,0,0,?,?) ON CONFLICT(merchant_id, token) DO NOTHING`,
		merchantID, token, now, now); err != nil {
		return nil, err
	}
	return getCartByToken(q, merchantID, token)
}

func getCartByToken(q querier, merchantID, token string) (*Cart, error) {
	row := q.QueryRow(
		`SELECT c.id, c.merchant_id, c.token, c.discount_code_id, IFNULL(d.code,''), c.checked_out, c.last_modified_at, c.created_at
		 FROM carts c LEFT JOIN discount_codes d ON d.id = c.discount_code_id
		 WHERE c.merchant_id = ? AND c.token = ?`, merchantID, token)
	return scanCart(row)
}

func scanCart(row rowScanner) (*Cart, error) {
	var c Cart
	var checkedOut int64
	var lastMod, created int64
	err := row.Scan(&c.ID, &c.MerchantID, &c.Token, &c.DiscountCodeID, &c.DiscountCode, &checkedOut, &lastMod, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.CheckedOut = checkedOut != 0
	c.LastModifiedAt = time.Unix(0, lastMod)
	c.CreatedAt = time.Unix(0, created)
	return &c, nil
}

// assertCartMutable 冻结或已过期的车不能再修改。
func (e *Engine) assertCartMutable(c *Cart) error {
	if c.CheckedOut {
		return ErrCartFrozen
	}
	if e.isExpired(c) {
		return ErrCartExpired
	}
	return nil
}

// isExpired 最后一次修改满 72 小时即过期（含恰好满 72 小时这一时刻）。
func (e *Engine) isExpired(c *Cart) bool {
	return !e.now().Before(c.LastModifiedAt.Add(CartTTL))
}

// touchCart 更新最后修改时间并提交事务。
func (e *Engine) touchCart(tx *sql.Tx, cartID int64) error {
	if _, err := tx.Exec(`UPDATE carts SET last_modified_at = ? WHERE id = ?`, e.nowNano(), cartID); err != nil {
		return err
	}
	return tx.Commit()
}

func (e *Engine) cartView(q querier, cart *Cart) (*CartView, error) {
	rows, err := q.Query(
		`SELECT ci.product_id, p.name, ci.quantity
		 FROM cart_items ci JOIN products p ON p.id = ci.product_id
		 WHERE ci.cart_id = ? ORDER BY ci.product_id`, cart.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	view := &CartView{Cart: *cart}
	for rows.Next() {
		var item CartItem
		if err := rows.Scan(&item.ProductID, &item.Name, &item.Quantity); err != nil {
			return nil, err
		}
		view.Items = append(view.Items, item)
	}
	return view, rows.Err()
}

func getProductTx(q querier, merchantID string, productID int64) (*Product, error) {
	row := q.QueryRow(
		`SELECT id, merchant_id, name, price_cents, stock, on_shelf, created_at, updated_at
		 FROM products WHERE id = ? AND merchant_id = ?`, productID, merchantID)
	return scanProduct(row)
}

func getDiscountByCodeTx(q querier, merchantID, code string) (*DiscountCode, error) {
	row := q.QueryRow(
		`SELECT id, merchant_id, code, kind, percent, amount_cents, min_spend_cents, max_uses, created_at
		 FROM discount_codes WHERE merchant_id = ? AND code = ?`, merchantID, code)
	return scanDiscountCode(row)
}
