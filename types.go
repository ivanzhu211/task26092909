// Package checkout 是一个嵌入式购物车挂件后端调用的结账引擎库。
// 数据持久化在 SQLite，不提供任何网络接口层。
package checkout

import "time"

// DiscountKind 折扣码类型。
type DiscountKind int

const (
	// DiscountPercent 百分比折扣，取值 1~50（表示 1%~50%）。
	DiscountPercent DiscountKind = iota + 1
	// DiscountFixed 固定金额折扣，单位为分。
	DiscountFixed
)

// Product 商品。
type Product struct {
	ID          int64
	MerchantID  string
	Name        string
	PriceCents  int64 // 单价，精确到分
	Stock       int64 // 库存，0~999999
	OnShelf     bool  // 是否上架
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// DiscountCode 折扣码。
type DiscountCode struct {
	ID             int64
	MerchantID     string
	Code           string
	Kind           DiscountKind
	Percent        int   // Kind 为 DiscountPercent 时有效，1~50
	AmountCents    int64 // Kind 为 DiscountFixed 时有效，单位分
	MinSpendCents  int64 // 最低消费门槛（按折前小计），0 表示无门槛
	MaxUses        int64 // 总使用次数上限，0 表示不限
	CreatedAt      time.Time
}

// Cart 购物车。
type Cart struct {
	ID             int64
	MerchantID     string
	Token          string // 会话令牌，商户内唯一
	DiscountCodeID int64  // 0 表示未挂折扣码
	DiscountCode   string
	CheckedOut     bool // 结账成功后冻结
	LastModifiedAt time.Time
	CreatedAt      time.Time
}

// CartItem 购物车内的一件商品。
type CartItem struct {
	ProductID int64
	Name      string
	Quantity  int64
}

// CartView 购物车视图（含明细）。
type CartView struct {
	Cart  Cart
	Items []CartItem
}

// OrderLine 订单行，记录结账那一刻的单价。
type OrderLine struct {
	ProductID       int64
	ProductName     string
	Quantity        int64
	UnitPriceCents  int64
	LineTotalCents  int64
}

// Order 订单。
type Order struct {
	ID               int64
	MerchantID       string
	CartID           int64
	IdempotencyKey   string
	DiscountCode     string
	SubtotalCents    int64 // 折前小计
	DiscountCents    int64 // 折扣金额
	TotalCents       int64 // 应付总价
	Cancelled        bool
	Lines            []OrderLine
	CreatedAt        time.Time
}

// StockShortage 结账时某件商品库存不足的明细。
type StockShortage struct {
	ProductID int64
	Name      string
	Requested int64
	Available int64
}
