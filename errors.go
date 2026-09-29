package checkout

import "fmt"

// ErrNotFound 资源不存在或不属于该商户。
var ErrNotFound = fmt.Errorf("checkout: not found")

// ErrCartExpired 购物车最后一次修改已满 72 小时。
var ErrCartExpired = fmt.Errorf("checkout: cart expired")

// ErrCartFrozen 购物车已结账冻结，不能再修改或再次结账。
var ErrCartFrozen = fmt.Errorf("checkout: cart already checked out")

// ErrCartEmpty 空购物车不能结账。
var ErrCartEmpty = fmt.Errorf("checkout: cart is empty")

// ErrProductOffShelf 商品已下架。
var ErrProductOffShelf = fmt.Errorf("checkout: product off shelf")

// ErrCartFull 购物车商品种类已达上限。
var ErrCartFull = fmt.Errorf("checkout: cart item kind limit exceeded")

// ErrIdempotencyConflict 同一幂等键被用于不同的购物车。
var ErrIdempotencyConflict = fmt.Errorf("checkout: idempotency key used for another cart")

// ErrDiscountMinSubtotal 未达到折扣码最低消费门槛。
var ErrDiscountMinSubtotal = fmt.Errorf("checkout: discount minimum subtotal not met")

// ErrDiscountExhausted 折扣码使用次数已用完。
var ErrDiscountExhausted = fmt.Errorf("checkout: discount usage limit reached")

// ErrInvalidArgument 参数非法。
var ErrInvalidArgument = fmt.Errorf("checkout: invalid argument")

// StockShortage 描述一件库存不足的商品。
type StockShortage struct {
	ProductID int64
	Requested int64
	Available int64
}

// InsufficientStockError 库存不足，列出所有不够的商品。
type InsufficientStockError struct {
	Shortages []StockShortage
}

func (e *InsufficientStockError) Error() string {
	return fmt.Sprintf("checkout: insufficient stock for %d product(s)", len(e.Shortages))
}
