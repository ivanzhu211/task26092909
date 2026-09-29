package checkout

import (
	"database/sql"
	"errors"
	"time"
)

// MaxStock 库存上限。
const MaxStock = 999999

// CreateProduct 新建商品，返回商品 ID。
func (e *Engine) CreateProduct(merchantID, name string, priceCents, stock int64) (int64, error) {
	if merchantID == "" {
		return 0, validationErr("merchant id is empty")
	}
	if name == "" {
		return 0, validationErr("product name is empty")
	}
	if priceCents < 0 {
		return 0, validationErr("price must be >= 0, got %d", priceCents)
	}
	if stock < 0 || stock > MaxStock {
		return 0, validationErr("stock must be in [0,%d], got %d", MaxStock, stock)
	}
	now := e.nowNano()
	res, err := e.db.Exec(
		`INSERT INTO products (merchant_id, name, price_cents, stock, on_shelf, created_at, updated_at)
		 VALUES (?,?,?,?,1,?,?)`,
		merchantID, name, priceCents, stock, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// AdjustStock 增减库存（delta 可正可负），结果必须落在 [0, MaxStock]。
func (e *Engine) AdjustStock(merchantID string, productID, delta int64) error {
	p, err := e.GetProduct(merchantID, productID)
	if err != nil {
		return err
	}
	next := p.Stock + delta
	if next < 0 {
		return validationErr("stock would become negative: current %d, delta %d", p.Stock, delta)
	}
	if next > MaxStock {
		return validationErr("stock would exceed max %d: current %d, delta %d", MaxStock, p.Stock, delta)
	}
	_, err = e.db.Exec(
		`UPDATE products SET stock = ?, updated_at = ? WHERE id = ? AND merchant_id = ?`,
		next, e.nowNano(), productID, merchantID)
	return err
}

// SetPrice 改价（不影响已生成订单）。
func (e *Engine) SetPrice(merchantID string, productID, priceCents int64) error {
	if priceCents < 0 {
		return validationErr("price must be >= 0, got %d", priceCents)
	}
	res, err := e.db.Exec(
		`UPDATE products SET price_cents = ?, updated_at = ? WHERE id = ? AND merchant_id = ?`,
		priceCents, e.nowNano(), productID, merchantID)
	if err != nil {
		return err
	}
	return requireAffected(res, ErrNotFound)
}

// SetShelf 上下架。
func (e *Engine) SetShelf(merchantID string, productID int64, onShelf bool) error {
	v := int64(0)
	if onShelf {
		v = 1
	}
	res, err := e.db.Exec(
		`UPDATE products SET on_shelf = ?, updated_at = ? WHERE id = ? AND merchant_id = ?`,
		v, e.nowNano(), productID, merchantID)
	if err != nil {
		return err
	}
	return requireAffected(res, ErrNotFound)
}

// GetProduct 查询商品。
func (e *Engine) GetProduct(merchantID string, productID int64) (*Product, error) {
	row := e.db.QueryRow(
		`SELECT id, merchant_id, name, price_cents, stock, on_shelf, created_at, updated_at
		 FROM products WHERE id = ? AND merchant_id = ?`, productID, merchantID)
	return scanProduct(row)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanProduct(row rowScanner) (*Product, error) {
	var p Product
	var onShelf int64
	var created, updated int64
	err := row.Scan(&p.ID, &p.MerchantID, &p.Name, &p.PriceCents, &p.Stock, &onShelf, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.OnShelf = onShelf != 0
	p.CreatedAt = time.Unix(0, created)
	p.UpdatedAt = time.Unix(0, updated)
	return &p, nil
}

func requireAffected(res sql.Result, err error) error {
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return err
	}
	return nil
}
