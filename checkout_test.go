package checkout

import (
	"errors"
	"testing"
)

func openTest(t *testing.T, opts ...Option) *Engine {
	t.Helper()
	e, err := Open(":memory:", opts...)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func mustProduct(t *testing.T, e *Engine, merchant, name string, price, stock int64) int64 {
	t.Helper()
	id, err := e.CreateProduct(merchant, name, price, stock)
	if err != nil {
		t.Fatalf("create product: %v", err)
	}
	return id
}

func TestBasicCheckoutFlow(t *testing.T) {
	e := openTest(t)
	pid := mustProduct(t, e, "m1", "apple", 1000, 10)

	if err := e.SetItemQuantity("m1", "tok1", pid, 2); err != nil {
		t.Fatalf("add item: %v", err)
	}
	order, err := e.Checkout("m1", "tok1", "key-1")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if order.TotalCents != 2000 || order.SubtotalCents != 2000 || order.DiscountCents != 0 {
		t.Fatalf("bad totals: %+v", order)
	}
	if len(order.Lines) != 1 || order.Lines[0].UnitPriceCents != 1000 || order.Lines[0].Quantity != 2 {
		t.Fatalf("bad lines: %+v", order.Lines)
	}
	p, _ := e.GetProduct("m1", pid)
	if p.Stock != 8 {
		t.Fatalf("stock = %d, want 8", p.Stock)
	}

	// 冻结后不能再修改。
	if err := e.SetItemQuantity("m1", "tok1", pid, 1); !errors.Is(err, ErrCartFrozen) {
		t.Fatalf("want ErrCartFrozen, got %v", err)
	}
	// 换键再结账也报错。
	if _, err := e.Checkout("m1", "tok1", "key-2"); !errors.Is(err, ErrCartFrozen) {
		t.Fatalf("want ErrCartFrozen, got %v", err)
	}
}

func TestIdempotency(t *testing.T) {
	e := openTest(t)
	pid := mustProduct(t, e, "m1", "apple", 500, 10)
	e.SetItemQuantity("m1", "tok1", pid, 1)
	e.SetItemQuantity("m1", "tok2", pid, 1)

	o1, err := e.Checkout("m1", "tok1", "key-x")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	// 同键同车：返回同一张订单。
	o2, err := e.Checkout("m1", "tok1", "key-x")
	if err != nil {
		t.Fatalf("repeat checkout: %v", err)
	}
	if o2.ID != o1.ID {
		t.Fatalf("id mismatch: %d vs %d", o2.ID, o1.ID)
	}
	// 同键不同车：报错。
	if _, err := e.Checkout("m1", "tok2", "key-x"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("want ErrIdempotencyConflict, got %v", err)
	}
	// 失败的结账不占键：空车结账失败后，装货再用同键可成功。
	if _, err := e.Checkout("m1", "tok3", "key-y"); err == nil {
		t.Fatal("empty cart checkout should fail")
	}
	e.SetItemQuantity("m1", "tok3", pid, 1)
	if _, err := e.Checkout("m1", "tok3", "key-y"); err != nil {
		t.Fatalf("retry with same key after failure: %v", err)
	}
	// 幂等键只在本商户内有效：另一商户可用同键。
	e.SetItemQuantity("m2", "tok9", mustProduct(t, e, "m2", "pear", 100, 5), 1)
	if _, err := e.Checkout("m2", "tok9", "key-x"); err != nil {
		t.Fatalf("other merchant same key: %v", err)
	}
}

func TestStockShortageListsAll(t *testing.T) {
	e := openTest(t)
	p1 := mustProduct(t, e, "m1", "a", 100, 1)
	p2 := mustProduct(t, e, "m1", "b", 100, 0)
	p3 := mustProduct(t, e, "m1", "c", 100, 5)
	e.SetItemQuantity("m1", "t", p1, 1)
	e.SetItemQuantity("m1", "t", p3, 2)
	// 加购时按当时库存校验：p2 库存 0 加不进去。
	var addErr *InsufficientStockError
	if err := e.SetItemQuantity("m1", "t", p2, 1); !errors.As(err, &addErr) {
		t.Fatalf("want shortage on add, got %v", err)
	}
	if addErr.Items[0].ProductID != p2 || addErr.Items[0].Requested != 1 || addErr.Items[0].Available != 0 {
		t.Fatalf("bad shortage detail: %+v", addErr.Items[0])
	}
	// 加购后别人买走库存，结账时报缺货并列出所有不够的商品。
	e.AdjustStock("m1", p1, -1)
	e.AdjustStock("m1", p3, -4)
	_, err := e.Checkout("m1", "t", "k")
	var shortage *InsufficientStockError
	if !errors.As(err, &shortage) {
		t.Fatalf("want shortage, got %v", err)
	}
	if len(shortage.Items) != 2 {
		t.Fatalf("want 2 shortages, got %+v", shortage.Items)
	}
	// 失败的结账不留改动：车未冻结，可改数量后重试成功。
	if err := e.SetItemQuantity("m1", "t", p1, 0); err != nil {
		t.Fatalf("cart should still be mutable: %v", err)
	}
	if err := e.SetItemQuantity("m1", "t", p3, 1); err != nil {
		t.Fatalf("adjust qty: %v", err)
	}
	if _, err := e.Checkout("m1", "t", "k"); err != nil {
		t.Fatalf("retry checkout: %v", err)
	}
}

func TestOffShelf(t *testing.T) {
	e := openTest(t)
	pid := mustProduct(t, e, "m1", "a", 100, 5)
	e.SetShelf("m1", pid, false)
	if err := e.SetItemQuantity("m1", "t", pid, 1); !errors.Is(err, ErrProductOffShelf) {
		t.Fatalf("want ErrProductOffShelf on add, got %v", err)
	}
	// 在车里之后下架：结账失败。
	e.SetShelf("m1", pid, true)
	e.SetItemQuantity("m1", "t", pid, 1)
	e.SetShelf("m1", pid, false)
	if _, err := e.Checkout("m1", "t", "k"); !errors.Is(err, ErrProductOffShelf) {
		t.Fatalf("want ErrProductOffShelf on checkout, got %v", err)
	}
}

func TestCartLimits(t *testing.T) {
	e := openTest(t)
	if err := e.SetItemQuantity("m1", "t", 999, 100); !errors.Is(err, ErrValidation) {
		t.Fatalf("qty 100 should fail: %v", err)
	}
	// 50 种上限。
	for i := 0; i < MaxCartItems; i++ {
		pid := mustProduct(t, e, "m1", "p", 10, 100)
		if err := e.SetItemQuantity("m1", "t", pid, 1); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	extra := mustProduct(t, e, "m1", "extra", 10, 100)
	if err := e.SetItemQuantity("m1", "t", extra, 1); !errors.Is(err, ErrValidation) {
		t.Fatalf("51st kind should fail: %v", err)
	}
	// 数量改 0 即移除。
	view, _ := e.GetCart("m1", "t")
	if err := e.SetItemQuantity("m1", "t", view.Items[0].ProductID, 0); err != nil {
		t.Fatalf("remove: %v", err)
	}
	view, _ = e.GetCart("m1", "t")
	if len(view.Items) != MaxCartItems-1 {
		t.Fatalf("items = %d", len(view.Items))
	}
}

func TestMerchantIsolation(t *testing.T) {
	e := openTest(t)
	pid := mustProduct(t, e, "m1", "a", 100, 5)
	// 别的商户的商品放不进本商户的车。
	if err := e.SetItemQuantity("m2", "t", pid, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-merchant add should fail: %v", err)
	}
	// 折扣码只在本商户内有效。
	e.CreateDiscountCode(DiscountCode{MerchantID: "m1", Code: "VIP", Kind: DiscountPercent, Percent: 10})
	if err := e.ApplyDiscount("m2", "t", "VIP"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-merchant discount should fail: %v", err)
	}
}

func TestDiscountRules(t *testing.T) {
	e := openTest(t)
	// 百分比：四舍五入到分。199 * 10% = 19.9 -> 20。
	pid := mustProduct(t, e, "m1", "a", 199, 100)
	e.CreateDiscountCode(DiscountCode{MerchantID: "m1", Code: "P10", Kind: DiscountPercent, Percent: 10})
	e.SetItemQuantity("m1", "t1", pid, 1)
	e.ApplyDiscount("m1", "t1", "P10")
	o, err := e.Checkout("m1", "t1", "k1")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if o.DiscountCents != 20 || o.TotalCents != 179 {
		t.Fatalf("discount = %d total = %d, want 20/179", o.DiscountCents, o.TotalCents)
	}

	// 固定金额最多减到 0。
	e.CreateDiscountCode(DiscountCode{MerchantID: "m1", Code: "F500", Kind: DiscountFixed, AmountCents: 500})
	e.SetItemQuantity("m1", "t2", pid, 2) // 小计 398
	e.ApplyDiscount("m1", "t2", "F500")
	o, err = e.Checkout("m1", "t2", "k2")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if o.DiscountCents != 398 || o.TotalCents != 0 {
		t.Fatalf("discount = %d total = %d, want 398/0", o.DiscountCents, o.TotalCents)
	}

	// 最低消费门槛按折前小计、按结账时刻判断。
	e.CreateDiscountCode(DiscountCode{MerchantID: "m1", Code: "MIN", Kind: DiscountPercent, Percent: 10, MinSpendCents: 1000})
	e.SetItemQuantity("m1", "t3", pid, 1)
	e.ApplyDiscount("m1", "t3", "MIN")
	if _, err := e.Checkout("m1", "t3", "k3"); !errors.Is(err, ErrBelowMinSpend) {
		t.Fatalf("want ErrBelowMinSpend, got %v", err)
	}

	// 一辆车最多挂一个码：后挂的覆盖先挂的。
	e.SetItemQuantity("m1", "t4", pid, 1)
	e.ApplyDiscount("m1", "t4", "P10")
	e.ApplyDiscount("m1", "t4", "F500")
	o, _ = e.Checkout("m1", "t4", "k4")
	if o.DiscountCode != "F500" {
		t.Fatalf("discount code = %q, want F500", o.DiscountCode)
	}

	// 非法折扣码参数。
	if _, err := e.CreateDiscountCode(DiscountCode{MerchantID: "m1", Code: "X", Kind: DiscountPercent, Percent: 60}); !errors.Is(err, ErrValidation) {
		t.Fatalf("percent 60 should fail: %v", err)
	}
}

func TestDiscountUsageLimit(t *testing.T) {
	e := openTest(t)
	pid := mustProduct(t, e, "m1", "a", 100, 100)
	e.CreateDiscountCode(DiscountCode{MerchantID: "m1", Code: "ONE", Kind: DiscountPercent, Percent: 10, MaxUses: 1})
	e.SetItemQuantity("m1", "t1", pid, 1)
	e.ApplyDiscount("m1", "t1", "ONE")
	if _, err := e.Checkout("m1", "t1", "k1"); err != nil {
		t.Fatalf("first checkout: %v", err)
	}
	e.SetItemQuantity("m1", "t2", pid, 1)
	e.ApplyDiscount("m1", "t2", "ONE")
	if _, err := e.Checkout("m1", "t2", "k2"); !errors.Is(err, ErrDiscountExhausted) {
		t.Fatalf("want ErrDiscountExhausted, got %v", err)
	}
}

func TestPriceChangeAfterOrder(t *testing.T) {
	e := openTest(t)
	pid := mustProduct(t, e, "m1", "a", 100, 10)
	e.SetItemQuantity("m1", "t", pid, 2)
	o, _ := e.Checkout("m1", "t", "k")
	e.SetPrice("m1", pid, 999)
	got, _ := e.GetOrder("m1", o.ID)
	if got.TotalCents != 200 || got.Lines[0].UnitPriceCents != 100 {
		t.Fatalf("order changed after reprice: %+v", got)
	}
}

func TestCancelOrder(t *testing.T) {
	e := openTest(t)
	pid := mustProduct(t, e, "m1", "a", 100, 5)
	e.CreateDiscountCode(DiscountCode{MerchantID: "m1", Code: "ONE", Kind: DiscountPercent, Percent: 10, MaxUses: 1})
	e.SetItemQuantity("m1", "t1", pid, 2)
	e.ApplyDiscount("m1", "t1", "ONE")
	o, _ := e.Checkout("m1", "t1", "k1")

	if err := e.CancelOrder("m1", o.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	p, _ := e.GetProduct("m1", pid)
	if p.Stock != 5 {
		t.Fatalf("stock = %d, want 5 after cancel", p.Stock)
	}
	// 折扣次数归还：另一辆车可再用。
	e.SetItemQuantity("m1", "t2", pid, 1)
	e.ApplyDiscount("m1", "t2", "ONE")
	if _, err := e.Checkout("m1", "t2", "k2"); err != nil {
		t.Fatalf("discount should be reusable after cancel: %v", err)
	}
	// 重复取消不重复归还。
	e.CancelOrder("m1", o.ID)
	e.CancelOrder("m1", o.ID)
	p, _ = e.GetProduct("m1", pid)
	if p.Stock != 4 {
		t.Fatalf("stock = %d, want 4 (no double restore)", p.Stock)
	}
	// 取消后订单状态可查。
	got, _ := e.GetOrder("m1", o.ID)
	if !got.Cancelled {
		t.Fatal("order should be cancelled")
	}
}

func TestAdjustStockBounds(t *testing.T) {
	e := openTest(t)
	pid := mustProduct(t, e, "m1", "a", 100, 3)
	if err := e.AdjustStock("m1", pid, -4); !errors.Is(err, ErrValidation) {
		t.Fatalf("negative stock should fail: %v", err)
	}
	if err := e.AdjustStock("m1", pid, MaxStock); !errors.Is(err, ErrValidation) {
		t.Fatalf("overflow should fail: %v", err)
	}
	if err := e.AdjustStock("m1", pid, 10); err != nil {
		t.Fatalf("adjust: %v", err)
	}
	p, _ := e.GetProduct("m1", pid)
	if p.Stock != 13 {
		t.Fatalf("stock = %d, want 13", p.Stock)
	}
}
