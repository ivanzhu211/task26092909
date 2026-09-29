package checkout

import (
	"errors"
	"fmt"
)

// 业务错误。调用方可用 errors.Is / errors.As 判断。
var (
	ErrNotFound            = errors.New("checkout: not found")
	ErrValidation          = errors.New("checkout: validation failed")
	ErrCartExpired         = errors.New("checkout: cart expired")
	ErrCartFrozen          = errors.New("checkout: cart already checked out")
	ErrProductOffShelf     = errors.New("checkout: product off shelf")
	ErrBelowMinSpend       = errors.New("checkout: below discount minimum spend")
	ErrDiscountExhausted   = errors.New("checkout: discount code usage exhausted")
	ErrIdempotencyConflict = errors.New("checkout: idempotency key used for a different cart")
	ErrOrderCancelled      = errors.New("checkout: order already cancelled")
)

// InsufficientStockError 库存不足，Items 列出所有不够的商品。
type InsufficientStockError struct {
	Items []StockShortage
}

func (e *InsufficientStockError) Error() string {
	return fmt.Sprintf("checkout: insufficient stock for %d product(s)", len(e.Items))
}

// IsInsufficientStock 判断 err 是否为库存不足错误。
func IsInsufficientStock(err error) bool {
	var e *InsufficientStockError
	return errors.As(err, &e)
}

func validationErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrValidation, fmt.Sprintf(format, args...))
}
