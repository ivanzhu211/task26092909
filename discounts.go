package checkout

import (
	"database/sql"
	"errors"
	"time"
)

// CreateDiscountCode 新建折扣码，返回折扣码 ID。
// 百分比折扣：Kind=DiscountPercent 且 Percent 取 1~50；
// 固定金额折扣：Kind=DiscountFixed 且 AmountCents>0。
// MinSpendCents 为折前小计门槛（0 表示无门槛），MaxUses 为总使用次数上限（0 表示不限）。
func (e *Engine) CreateDiscountCode(c DiscountCode) (int64, error) {
	if c.MerchantID == "" {
		return 0, validationErr("merchant id is empty")
	}
	if c.Code == "" {
		return 0, validationErr("discount code is empty")
	}
	switch c.Kind {
	case DiscountPercent:
		if c.Percent < 1 || c.Percent > 50 {
			return 0, validationErr("percent must be in [1,50], got %d", c.Percent)
		}
	case DiscountFixed:
		if c.AmountCents <= 0 {
			return 0, validationErr("fixed discount amount must be > 0, got %d", c.AmountCents)
		}
	default:
		return 0, validationErr("unknown discount kind %d", c.Kind)
	}
	if c.MinSpendCents < 0 {
		return 0, validationErr("min spend must be >= 0, got %d", c.MinSpendCents)
	}
	if c.MaxUses < 0 {
		return 0, validationErr("max uses must be >= 0, got %d", c.MaxUses)
	}
	res, err := e.db.Exec(
		`INSERT INTO discount_codes (merchant_id, code, kind, percent, amount_cents, min_spend_cents, max_uses, created_at)
		 VALUES (?,?,?,?,?,?,?,?)`,
		c.MerchantID, c.Code, int(c.Kind), c.Percent, c.AmountCents, c.MinSpendCents, c.MaxUses, e.nowNano())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetDiscountCode 按码查询（仅本商户）。
func (e *Engine) GetDiscountCode(merchantID, code string) (*DiscountCode, error) {
	row := e.db.QueryRow(
		`SELECT id, merchant_id, code, kind, percent, amount_cents, min_spend_cents, max_uses, created_at
		 FROM discount_codes WHERE merchant_id = ? AND code = ?`, merchantID, code)
	return scanDiscountCode(row)
}

func scanDiscountCode(row rowScanner) (*DiscountCode, error) {
	var d DiscountCode
	var kind int
	var created int64
	err := row.Scan(&d.ID, &d.MerchantID, &d.Code, &kind, &d.Percent, &d.AmountCents, &d.MinSpendCents, &d.MaxUses, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d.Kind = DiscountKind(kind)
	d.CreatedAt = time.Unix(0, created)
	return &d, nil
}
