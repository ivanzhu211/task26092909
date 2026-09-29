package checkout

import (
	"database/sql"
	"errors"
	"fmt"
)

const maxStock = 999999

// CreateProduct 创建商品。initialStock 范围 0-999999。
func (e *Engine) CreateProduct(merchantID, name string, priceCents, initialStock int64) (*Product, error) {
	if merchantID == "" || name == "" || priceCents < 0 || initialStock < 0 || initialStock > maxStock {
		return nil, ErrInvalidArgument
	}
	res, err := e.db.Exec(
		`INSERT INTO products (merchant_id, name, price_cents, stock, status) VALUES (?, ?, ?, ?, ?)`,
		merchantID, name, priceCents, initialStock, ProductStatusOnShelf,
	)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return e.GetProduct(merchantID, id)
}

// AdjustStock 增减库存（可正可负），结果不能为负或超过 999999。
func (e *Engine) AdjustStock(merchantID string, productID, delta int64) (*Product, error) {
	res, err := e.db.Exec(
		`UPDATE products SET stock = stock + ? WHERE id = ? AND merchant_id = ? AND stock + ? BETWEEN 0 AND ?`,
		delta, productID, merchantID, delta, maxStock,
	)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		if _, err := e.GetProduct(merchantID, productID); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: stock adjustment out of range", ErrInvalidArgument)
	}
	return e.GetProduct(merchantID, productID)
}

// UpdatePrice 改价，不影响已生成的订单。
func (e *Engine) UpdatePrice(merchantID string, productID, priceCents int64) (*Product, error) {
	if priceCents < 0 {
		return nil, ErrInvalidArgument
	}
	res, err := e.db.Exec(
		`UPDATE products SET price_cents = ? WHERE id = ? AND merchant_id = ?`,
		priceCents, productID, merchantID,
	)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return e.GetProduct(merchantID, productID)
}

// SetProductStatus 上下架。
func (e *Engine) SetProductStatus(merchantID string, productID int64, status ProductStatus) (*Product, error) {
	if status != ProductStatusOnShelf && status != ProductStatusOffShelf {
		return nil, ErrInvalidArgument
	}
	res, err := e.db.Exec(
		`UPDATE products SET status = ? WHERE id = ? AND merchant_id = ?`,
		status, productID, merchantID,
	)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return e.GetProduct(merchantID, productID)
}

// GetProduct 读取商品。
func (e *Engine) GetProduct(merchantID string, productID int64) (*Product, error) {
	var p Product
	err := e.db.QueryRow(
		`SELECT id, merchant_id, name, price_cents, stock, status FROM products WHERE id = ? AND merchant_id = ?`,
		productID, merchantID,
	).Scan(&p.ID, &p.MerchantID, &p.Name, &p.PriceCents, &p.Stock, &p.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}
