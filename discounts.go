package checkout

import (
	"database/sql"
	"errors"
)

// CreateDiscountCode 创建折扣码。percent 折扣传 1-50；fixed 折扣传 amountCents（分）。
// minSubtotalCents 为最低消费门槛（0 无门槛），usageLimit 为总次数上限（0 不限）。
func (e *Engine) CreateDiscountCode(merchantID, code string, kind DiscountKind, percent, amountCents, minSubtotalCents, usageLimit int64) (*DiscountCode, error) {
	if merchantID == "" || code == "" || minSubtotalCents < 0 || usageLimit < 0 {
		return nil, ErrInvalidArgument
	}
	switch kind {
	case DiscountKindPercent:
		if percent < 1 || percent > 50 {
			return nil, ErrInvalidArgument
		}
		amountCents = 0
	case DiscountKindFixed:
		if amountCents <= 0 {
			return nil, ErrInvalidArgument
		}
		percent = 0
	default:
		return nil, ErrInvalidArgument
	}
	res, err := e.db.Exec(
		`INSERT INTO discount_codes (merchant_id, code, kind, percent, amount_cents, min_subtotal_cents, usage_limit)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		merchantID, code, kind, percent, amountCents, minSubtotalCents, usageLimit,
	)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return e.getDiscountCodeByID(merchantID, id)
}

// GetDiscountCode 按码值读取折扣码。
func (e *Engine) GetDiscountCode(merchantID, code string) (*DiscountCode, error) {
	return e.scanDiscountCode(e.db.QueryRow(
		`SELECT id, merchant_id, code, kind, percent, amount_cents, min_subtotal_cents, usage_limit
		 FROM discount_codes WHERE merchant_id = ? AND code = ?`,
		merchantID, code,
	))
}

func (e *Engine) getDiscountCodeByID(merchantID string, id int64) (*DiscountCode, error) {
	return e.scanDiscountCode(e.db.QueryRow(
		`SELECT id, merchant_id, code, kind, percent, amount_cents, min_subtotal_cents, usage_limit
		 FROM discount_codes WHERE merchant_id = ? AND id = ?`,
		merchantID, id,
	))
}

func (e *Engine) scanDiscountCode(row *sql.Row) (*DiscountCode, error) {
	var d DiscountCode
	err := row.Scan(&d.ID, &d.MerchantID, &d.Code, &d.Kind, &d.Percent, &d.AmountCents, &d.MinSubtotalCents, &d.UsageLimit)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}
