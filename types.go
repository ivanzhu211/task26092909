package checkout

import "time"

// ProductStatus 商品上下架状态。
type ProductStatus string

const (
	ProductStatusOnShelf  ProductStatus = "on_shelf"
	ProductStatusOffShelf ProductStatus = "off_shelf"
)

// DiscountKind 折扣码类型。
type DiscountKind string

const (
	DiscountKindPercent DiscountKind = "percent" // 百分比折扣，1-50
	DiscountKindFixed   DiscountKind = "fixed"   // 固定金额折扣（分）
)

// CartStatus 购物车状态。
type CartStatus string

const (
	CartStatusActive     CartStatus = "active"
	CartStatusCheckedOut CartStatus = "checked_out" // 已结账，冻结
)

// OrderStatus 订单状态。
type OrderStatus string

const (
	OrderStatusPaid     OrderStatus = "paid"
	OrderStatusCanceled OrderStatus = "canceled"
)

// Product 商户商品。价格以分为单位。
type Product struct {
	ID         int64
	MerchantID string
	Name       string
	PriceCents int64
	Stock      int64
	Status     ProductStatus
}

// DiscountCode 折扣码。Percent 或 AmountCents 依 Kind 生效。
// MinSubtotalCents 为最低消费门槛（折前小计，分），0 表示无门槛。
// UsageLimit 为总使用次数上限，0 表示不限。
type DiscountCode struct {
	ID               int64
	MerchantID       string
	Code             string
	Kind             DiscountKind
	Percent          int64
	AmountCents      int64
	MinSubtotalCents int64
	UsageLimit       int64
}

// CartItem 购物车内一行商品。
type CartItem struct {
	ProductID int64
	Quantity  int64
}

// Cart 买家购物车。
type Cart struct {
	ID             int64
	MerchantID     string
	Token          string
	Status         CartStatus
	Items          []CartItem
	DiscountCodeID int64 // 0 表示未挂折扣码
	DiscountCode   string
	UpdatedAt      time.Time
	CreatedAt      time.Time
}

// OrderItem 订单行，记录结账时刻的单价。
type OrderItem struct {
	ProductID      int64
	Quantity       int64
	UnitPriceCents int64
}

// Order 结账生成的订单。
type Order struct {
	ID             int64
	MerchantID     string
	CartID         int64
	Status         OrderStatus
	Items          []OrderItem
	DiscountCodeID int64
	DiscountCode   string
	SubtotalCents  int64
	DiscountCents  int64
	TotalCents     int64
	IdempotencyKey string
	CreatedAt      time.Time
	CanceledAt     time.Time // 零值表示未取消
}
